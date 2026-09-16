package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
)

func TestPostgresMediaSettingsAtomicity(t *testing.T) {
	_, db, ctx := openOutboxIntegrationDatabase(t)
	suffix := fmt.Sprint(time.Now().UnixNano())
	f, err := db.Files().CreateFile(ctx, file.File{Storage: "public", Name: "settings.png", Path: suffix, MIMEType: "image/png", Size: 1, ChecksumSHA256: fmt.Sprintf("%064d", 1)})
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.Media().Create(ctx, nil, media.Media{FileID: f.ID, Params: map[string]any{"image": map[string]any{"version": 1}}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.Media().Create(ctx, nil, media.Media{FileID: f.ID, Params: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Media().Delete(context.Background(), first.ID)
		db.Media().Delete(context.Background(), second.ID)
		db.Files().DeleteFile(context.Background(), f.ID, func(context.Context, []file.File) error { return nil })
	})
	writer := db.Media().(media.SettingsRepository)
	updated, err := writer.UpdateSettings(ctx, nil, first.ID, map[string]any{"alt": "first"}, first.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Params["image"] == nil {
		t.Fatal("lost image metadata")
	}
	other, _ := db.Media().ByID(ctx, second.ID)
	if other.Params["settings"] != nil {
		t.Fatal("changed second Media")
	}
	// Both settings and image writes participate in the same version check.
	imageWriter := db.Media().(media.ImageRepository)
	if _, err := imageWriter.UpdateImage(ctx, nil, first, first.UpdatedAt, func(context.Context, []media.Usage) error { return nil }); !errors.Is(err, media.ErrImageConflict) {
		t.Fatalf("stale image: %v", err)
	}
	next := media.Clone(updated)
	next.Params["image"] = map[string]any{"version": 1, "changed": true}
	next, err = imageWriter.UpdateImage(ctx, nil, next, updated.UpdatedAt, func(context.Context, []media.Usage) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.UpdateSettings(ctx, nil, first.ID, map[string]any{"alt": "stale"}, updated.UpdatedAt); !errors.Is(err, media.ErrSettingsConflict) {
		t.Fatalf("stale settings: %v", err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, alt := range []string{"left", "right"} {
		wg.Add(1)
		go func(alt string) {
			defer wg.Done()
			_, err := writer.UpdateSettings(ctx, nil, first.ID, map[string]any{"alt": alt}, next.UpdatedAt)
			results <- err
		}(alt)
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, media.ErrSettingsConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent writes: successes=%d conflicts=%d", successes, conflicts)
	}
	final, err := db.Media().ByID(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Params["image"].(map[string]any)["changed"] != true {
		t.Fatal("lost current image metadata")
	}
}
