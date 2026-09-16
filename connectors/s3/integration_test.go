package s3

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/connectors/filesystemcache"
	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/filesystem"
)

func TestS3CompatibleIntegration(t *testing.T) {
	endpoint := os.Getenv("CMS_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set CMS_TEST_S3_ENDPOINT to run the S3 integration test")
	}
	region := os.Getenv("CMS_TEST_S3_REGION")
	if region == "" {
		region = "us-east-1"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	connector, err := New(ctx, Config{
		Code:            "integration",
		Visibility:      filesystem.VisibilityPrivate,
		Region:          region,
		Bucket:          os.Getenv("CMS_TEST_S3_BUCKET"),
		Prefix:          "cms-integration",
		Endpoint:        endpoint,
		UsePathStyle:    true,
		AccessKeyID:     os.Getenv("CMS_TEST_S3_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("CMS_TEST_S3_SECRET_ACCESS_KEY"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := connector.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	key := "objects/" + time.Now().UTC().Format("20060102150405.000000000")
	secondKey := key + "-second"
	t.Cleanup(func() {
		_ = connector.Delete(context.Background(), key)
		_ = connector.Delete(context.Background(), secondKey)
	})
	if err := connector.PutNew(
		ctx,
		key,
		io.MultiReader(
			strings.NewReader("integ"),
			strings.NewReader("ration"),
		),
		"text/plain",
	); err != nil {
		t.Fatal(err)
	}
	if err := connector.PutNew(ctx, key, strings.NewReader("duplicate"), "text/plain"); !errors.Is(err, filesystem.ErrConflict) {
		t.Fatalf("duplicate put error = %v", err)
	}
	if err := connector.PutNew(ctx, secondKey, strings.NewReader("second"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	body, err := connector.Open(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || string(content) != "integration" {
		t.Fatalf("content = %q, %v", content, err)
	}
	signedURL, err := connector.TemporaryURL(
		ctx,
		filesystem.Reference{ID: "1", Path: key},
		time.Now().Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, signedURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	signedContent, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK || string(signedContent) != "integration" {
		t.Fatalf("signed GET status=%d content=%q read=%v close=%v", response.StatusCode, signedContent, readErr, closeErr)
	}

	t.Run("rejects tampered and expired signed URLs", func(t *testing.T) {
		parsed, err := url.Parse(signedURL)
		if err != nil {
			t.Fatal(err)
		}
		query := parsed.Query()
		query.Set("X-Amz-Signature", strings.Repeat("0", 64))
		parsed.RawQuery = query.Encode()
		assertForbidden := func(rawURL string) {
			t.Helper()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusForbidden {
				t.Fatalf("invalid signed URL status = %d", response.StatusCode)
			}
		}
		assertForbidden(parsed.String())
		shortURL, err := connector.TemporaryURL(ctx, filesystem.Reference{ID: "1", Path: key}, time.Now().Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-timer.C:
		}
		assertForbidden(shortURL)
	})

	scan, err := connector.OpenPrefixScan(ctx, "objects")
	if err != nil {
		t.Fatal(err)
	}
	var scanned []string
	for {
		page, err := scan.Next(ctx, 1)
		if err != nil {
			_ = scan.Close()
			t.Fatal(err)
		}
		scanned = append(scanned, page.Keys...)
		if page.Done {
			break
		}
	}
	if err := scan.Close(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(scanned)
	want := []string{key, secondKey}
	sort.Strings(want)
	if len(scanned) != len(want) || scanned[0] != want[0] || scanned[1] != want[1] {
		t.Fatalf("scanned keys = %#v, want %#v", scanned, want)
	}
	var walked []string
	if err := connector.WalkPrefix(ctx, "objects", func(key string) error {
		walked = append(walked, key)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(walked)
	if len(walked) != len(want) || walked[0] != want[0] || walked[1] != want[1] {
		t.Fatalf("walked keys = %#v, want %#v", walked, want)
	}
	if err := connector.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := connector.Delete(ctx, secondKey); err != nil {
		t.Fatal(err)
	}
	if _, err := connector.Open(ctx, key); !errors.Is(err, filesystem.ErrNotFound) {
		t.Fatalf("open deleted object error = %v", err)
	}

	cachePrefix := "cache-integration/" + time.Now().UTC().Format("20060102150405.000000000")
	store, err := filesystemcache.New(ctx, filesystemcache.Config{
		Code:   "s3-integration",
		Disk:   connector.Code(),
		Layout: filesystemcache.LayoutAuto,
		Prefix: cachePrefix,
	}, connector)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Flush(context.Background()) })
	if err := store.Set(ctx, "fresh", []byte("cached"), cache.SetOptions{}); err != nil {
		t.Fatal(err)
	}
	if value, err := store.Get(ctx, "fresh"); err != nil || string(value) != "cached" {
		t.Fatalf("cached value = %q, %v", value, err)
	}
	if err := store.Set(ctx, "expired", []byte("stale"), cache.SetOptions{TTL: time.Nanosecond}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if err := store.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "expired"); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("expired cached value error = %v", err)
	}
	if err := store.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "fresh"); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("flushed cached value error = %v", err)
	}
}
