package localstorage

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/filesystem"
)

func TestConnectorStoresWithoutOverwriteAndDeletesIdempotently(t *testing.T) {
	connector, err := New(context.Background(), Config{
		Code:       "public",
		Visibility: filesystem.VisibilityPublic,
		Root:       t.TempDir(),
		BaseURL:    "https://files.example.test/base",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := connector.PutNew(
		context.Background(),
		"objects/2026/item",
		strings.NewReader("hello"),
		"text/plain",
	); err != nil {
		t.Fatal(err)
	}
	if err := connector.PutNew(
		context.Background(),
		"objects/2026/item",
		strings.NewReader("replacement"),
		"text/plain",
	); !errors.Is(err, filesystem.ErrConflict) {
		t.Fatalf("overwrite error = %v", err)
	}

	body, err := connector.Open(context.Background(), "objects/2026/item")
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || string(content) != "hello" {
		t.Fatalf("stored content = %q, %v", content, err)
	}
	if err := connector.Put(
		context.Background(),
		"objects/2026/item",
		strings.NewReader("replacement"),
		"text/plain",
	); err != nil {
		t.Fatal(err)
	}
	body, err = connector.Open(context.Background(), "objects/2026/item")
	if err != nil {
		t.Fatal(err)
	}
	content, err = io.ReadAll(body)
	_ = body.Close()
	if err != nil || string(content) != "replacement" {
		t.Fatalf("replaced content = %q, %v", content, err)
	}
	if _, err := connector.Open(
		context.Background(),
		"../escape",
	); err == nil {
		t.Fatal("path traversal was accepted")
	}
	outside := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(outside, "secret"),
		[]byte("secret"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		outside,
		filepath.Join(connector.root, "link"),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := connector.Open(
		context.Background(),
		"link/secret",
	); err == nil {
		t.Fatal("symbolic-link traversal was accepted")
	}

	publicURL, err := connector.URL(
		context.Background(),
		filesystem.Reference{ID: "42", Path: "objects/2026/item"},
	)
	if err != nil || publicURL != "https://files.example.test/base/_cms/files/42" {
		t.Fatalf("public URL = %q, %v", publicURL, err)
	}

	if err := connector.Delete(
		context.Background(),
		"objects/2026/item",
	); err != nil {
		t.Fatal(err)
	}
	if err := connector.Delete(
		context.Background(),
		"objects/2026/item",
	); err != nil {
		t.Fatal(err)
	}
}

func TestConnectorListsPrefixInBoundedCursorPages(t *testing.T) {
	connector, err := New(context.Background(), Config{Code: "private", Visibility: filesystem.VisibilityPrivate, Root: t.TempDir(), BaseURL: "https://files.example.test", SigningKey: "0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"spool/site/a", "spool/site/b", "spool/site/c", "spool/other/d"} {
		if err := connector.PutNew(context.Background(), key, strings.NewReader(key), "text/plain"); err != nil {
			t.Fatal(err)
		}
	}
	scanValue, err := connector.OpenPrefixScan(context.Background(), "spool/site/")
	if err != nil {
		t.Fatal(err)
	}
	scan := scanValue.(*prefixScan)
	first, err := scan.Next(context.Background(), 2)
	if err != nil || len(first.Keys) > 2 || first.Done {
		t.Fatalf("first page = %#v, %v", first, err)
	}
	visited := scan.visited
	var keys []string
	keys = append(keys, first.Keys...)
	for !first.Done {
		first, err = scan.Next(context.Background(), 2)
		if err != nil {
			t.Fatal(err)
		}
		if scan.visited-visited > 2 {
			t.Fatalf("later page physically traversed %d entries", scan.visited-visited)
		}
		visited = scan.visited
		keys = append(keys, first.Keys...)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"spool/site/a", "spool/site/b", "spool/site/c"}) || !scan.done || len(scan.stack) != 0 {
		t.Fatalf("scan keys/state = %#v done=%t open=%d", keys, scan.done, len(scan.stack))
	}
}

func TestPrefixScanClosesOnCancellationAndExplicitClose(t *testing.T) {
	connector, err := New(context.Background(), Config{Code: "private", Visibility: filesystem.VisibilityPrivate, Root: t.TempDir(), BaseURL: "https://files.example.test", SigningKey: "0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	if err := connector.PutNew(context.Background(), "spool/site/a", strings.NewReader("a"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	scanValue, err := connector.OpenPrefixScan(context.Background(), "spool/site/")
	if err != nil {
		t.Fatal(err)
	}
	scan := scanValue.(*prefixScan)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scan.Next(ctx, 1); !errors.Is(err, context.Canceled) || !scan.done || len(scan.stack) != 0 {
		t.Fatalf("cancelled scan = %v done=%t open=%d", err, scan.done, len(scan.stack))
	}
	scanValue, err = connector.OpenPrefixScan(context.Background(), "spool/site/")
	if err != nil {
		t.Fatal(err)
	}
	scan = scanValue.(*prefixScan)
	if err := scan.Close(); err != nil || !scan.done || len(scan.stack) != 0 {
		t.Fatalf("closed scan = %v done=%t open=%d", err, scan.done, len(scan.stack))
	}
}

func TestPrivateConnectorSignsAndVerifiesTemporaryURL(t *testing.T) {
	connector, err := New(context.Background(), Config{
		Code:       "private",
		Visibility: filesystem.VisibilityPrivate,
		Root:       t.TempDir(),
		BaseURL:    "https://files.example.test",
		SigningKey: strings.Repeat("secret", 8),
	})
	if err != nil {
		t.Fatal(err)
	}
	reference := filesystem.Reference{ID: "7", Path: "objects/item"}
	expiresAt := time.Now().Add(time.Hour).Truncate(time.Second)
	rawURL, err := connector.TemporaryURL(
		context.Background(),
		reference,
		expiresAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	signature := parsed.Query().Get("signature")
	if err := connector.VerifyTemporaryURL(
		reference,
		expiresAt,
		signature,
	); err != nil {
		t.Fatal(err)
	}
	if err := connector.VerifyTemporaryURL(
		filesystem.Reference{ID: "8", Path: reference.Path},
		expiresAt,
		signature,
	); !errors.Is(err, filesystem.ErrUnauthorized) {
		t.Fatalf("tampered signature error = %v", err)
	}
	if err := connector.VerifyTemporaryURL(
		reference,
		time.Now().Add(-time.Second),
		signature,
	); !errors.Is(err, filesystem.ErrUnauthorized) {
		t.Fatalf("expired signature error = %v", err)
	}
}
