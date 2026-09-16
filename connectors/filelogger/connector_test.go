package filelogger

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
}

func testFileConfig(path string) Config {
	return Config{
		Path:        path,
		Level:       slog.LevelInfo,
		ServiceName: "cms-test",
		Environment: "test",
		Location:    time.UTC,
		MaxSize:     1 << 20,
		MaxBackups:  14,
		MaxAge:      14 * 24 * time.Hour,
		Compress:    true,
	}
}

func TestConnectorWritesStructuredJSONAndCreatesSecureFile(
	t *testing.T,
) {
	path := filepath.Join(t.TempDir(), "nested", "cms.log")
	connector, err := New(context.Background(), testFileConfig(path))
	if err != nil {
		t.Fatal(err)
	}

	connector.Logger().Debug("hidden debug record")
	connector.Logger().Info("visible record", slog.String("component", "test"))
	if err := connector.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := connector.Close(); err != nil {
		t.Fatal(err)
	}
	if err := connector.Close(); err != nil {
		t.Fatalf("second close failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("log permissions = %o", permissions)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "hidden debug record") {
		t.Fatal("debug record passed the info level filter")
	}

	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("decode JSON log: %v; log = %q", err, data)
	}
	if record["level"] != "INFO" ||
		record["msg"] != "visible record" ||
		record["component"] != "test" ||
		record["service.name"] != "cms-test" ||
		record["deployment.environment.name"] != "test" {
		t.Fatalf("unexpected log record: %#v", record)
	}
	source, ok := record["source"].(map[string]any)
	if !ok || source["file"] == "" || source["function"] == "" {
		t.Fatalf("source location is missing: %#v", record["source"])
	}
}

func TestConnectorSecuresExistingLogFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cms.log")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	connector, err := New(context.Background(), testFileConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := connector.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("existing log permissions = %o", permissions)
	}
}

func TestConnectorRotatesBySizeAndCompressesBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cms.log")
	config := testFileConfig(path)
	config.MaxSize = 256
	connector, err := New(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}

	connector.Logger().Info("oversized", slog.String(
		"payload",
		strings.Repeat("x", 512),
	))
	if err := connector.Close(); err != nil {
		t.Fatal(err)
	}

	backups, err := filepath.Glob(filepath.Join(
		filepath.Dir(path),
		"cms-*.log.gz",
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("compressed backups = %v", backups)
	}
	file, err := os.Open(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"msg":"oversized"`) {
		t.Fatalf("rotated log = %q", data)
	}
}

func TestConnectorRotatesAtLocalCalendarBoundary(t *testing.T) {
	location := time.FixedZone("test-zone", 3*60*60)
	clock := &fakeClock{now: time.Date(
		2026,
		time.July,
		29,
		23,
		59,
		0,
		0,
		location,
	)}
	path := filepath.Join(t.TempDir(), "cms.log")
	config := testFileConfig(path)
	config.Location = location
	config.Clock = clock
	config.Compress = false
	connector, err := New(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}

	connector.Logger().Info("before midnight")
	clock.Advance(2 * time.Minute)
	connector.Logger().Info("after midnight")
	if err := connector.Close(); err != nil {
		t.Fatal(err)
	}

	backups, err := filepath.Glob(filepath.Join(
		filepath.Dir(path),
		"cms-*.log",
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 ||
		!strings.Contains(filepath.Base(backups[0]), "2026-07-30") {
		t.Fatalf("calendar backups = %v", backups)
	}
	active, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(active), `"msg":"after midnight"`) ||
		strings.Contains(string(active), `"msg":"before midnight"`) {
		t.Fatalf("active log = %q", active)
	}
}

func TestConnectorPrunesBackupsByCountAndAge(t *testing.T) {
	clock := &fakeClock{now: time.Date(
		2026,
		time.July,
		25,
		12,
		0,
		0,
		0,
		time.UTC,
	)}
	path := filepath.Join(t.TempDir(), "cms.log")
	config := testFileConfig(path)
	config.Clock = clock
	config.MaxBackups = 2
	config.MaxAge = 48 * time.Hour
	connector, err := New(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}

	for day := range 5 {
		connector.Logger().Info("daily record", slog.Int("day", day))
		clock.Advance(24 * time.Hour)
	}
	if err := connector.Close(); err != nil {
		t.Fatal(err)
	}

	backups, err := filepath.Glob(filepath.Join(
		filepath.Dir(path),
		"cms-*.log.gz",
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) > 2 {
		t.Fatalf("retained backups = %v", backups)
	}
}

func TestConnectorRejectsInvalidConfiguration(t *testing.T) {
	valid := testFileConfig(filepath.Join(t.TempDir(), "cms.log"))
	tests := []struct {
		name   string
		ctx    context.Context
		config Config
	}{
		{name: "nil context", config: valid},
		{
			name:   "empty path",
			ctx:    context.Background(),
			config: Config{ServiceName: "cms", Environment: "test"},
		},
		{
			name: "empty service",
			ctx:  context.Background(),
			config: Config{
				Path:        valid.Path,
				Environment: "test",
			},
		},
		{
			name: "empty environment",
			ctx:  context.Background(),
			config: Config{
				Path:        valid.Path,
				ServiceName: "cms",
			},
		},
		{
			name: "negative max size",
			ctx:  context.Background(),
			config: func() Config {
				config := valid
				config.MaxSize = -1
				return config
			}(),
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := New(testCase.ctx, testCase.config); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}
