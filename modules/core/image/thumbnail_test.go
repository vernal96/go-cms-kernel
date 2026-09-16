package image

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/security"
)

type thumbFiles struct {
	file.Service
	source    file.File
	forbidden bool
	opens     int
}

func (f *thumbFiles) GetFile(context.Context, security.Actor, file.ID) (file.File, error) {
	if f.forbidden {
		return file.File{}, security.ErrForbidden
	}
	return f.source, nil
}
func (f *thumbFiles) Open(context.Context, security.Actor, file.ID) (file.OpenedFile, error) {
	f.opens++
	return file.OpenedFile{File: f.source, Body: io.NopCloser(strings.NewReader("source"))}, nil
}

type thumbProcessor struct{ calls int }

func (p *thumbProcessor) Transform(context.Context, io.Reader, TransformOptions) (Result, error) {
	p.calls++
	return Result{Bytes: []byte("thumbnail"), MIMEType: "image/png"}, nil
}

type thumbCache struct {
	cache.Store
	items map[string][]byte
}

func (c *thumbCache) Code() cache.Code { return "test" }
func (c *thumbCache) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := c.items[key]
	if !ok {
		return nil, cache.ErrMiss
	}
	return v, nil
}
func (c *thumbCache) Set(_ context.Context, key string, v []byte, _ cache.SetOptions) error {
	c.items[key] = v
	return nil
}
func TestThumbnailsCacheIdentityAndAuthorization(t *testing.T) {
	f := &thumbFiles{source: file.File{ID: 1, Storage: "private", MIMEType: "image/png", ChecksumSHA256: "abc"}}
	p := &thumbProcessor{}
	c := &thumbCache{items: map[string][]byte{}}
	s := NewThumbnails(f, p, c, DefaultLimits())
	ctx := context.Background()
	a := security.User(1)
	for i := 0; i < 2; i++ {
		if _, err := s.Get(ctx, a, 1, ThumbnailSpec{Width: 128, Height: 128}); err != nil {
			t.Fatal(err)
		}
	}
	if p.calls != 1 || f.opens != 1 {
		t.Fatal("identical request missed cache")
	}
	if _, err := s.Get(ctx, a, 1, ThumbnailSpec{Width: 256, Height: 128}); err != nil {
		t.Fatal(err)
	}
	f.source.ChecksumSHA256 = "changed"
	if _, err := s.Get(ctx, a, 1, ThumbnailSpec{Width: 128, Height: 128}); err != nil {
		t.Fatal(err)
	}
	if p.calls != 3 || len(c.items) != 3 {
		t.Fatal("cache identity collision")
	}
	f.forbidden = true
	if _, err := s.Get(ctx, a, 1, ThumbnailSpec{Width: 128, Height: 128}); !errors.Is(err, security.ErrForbidden) {
		t.Fatal("private cached file leaked", err)
	}
	if p.calls != 3 {
		t.Fatal("unauthorized request processed source")
	}
}
func TestThumbnailNormalizationAndCardinality(t *testing.T) {
	a, err := NormalizeThumbnail(ThumbnailSpec{}, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NormalizeThumbnail(ThumbnailSpec{Width: 128, Height: 128, Fit: "contain", Position: "center"}, DefaultLimits())
	source := file.File{ID: 1, ChecksumSHA256: "x"}
	if ThumbnailKey(source, a) != ThumbnailKey(source, b) {
		t.Fatal("defaults not canonical")
	}
	for _, n := range []int{-1, 1, 127, 129, 1024, 999999} {
		if _, err := NormalizeThumbnail(ThumbnailSpec{Width: n}, DefaultLimits()); err == nil {
			t.Fatalf("unbounded spec %d", n)
		}
	}
}
