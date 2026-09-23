package image

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/security"
	"golang.org/x/sync/singleflight"
)

type ThumbnailSpec struct {
	Width    int
	Height   int
	Fit      string
	Position string
}
type Thumbnails struct {
	flights   singleflight.Group
	files     file.Service
	processor Processor
	store     cache.Store
	limits    Limits
}

func NewThumbnails(files file.Service, processor Processor, store cache.Store, limits Limits) *Thumbnails {
	return &Thumbnails{files: files, processor: processor, store: store, limits: limits}
}
func (s *Thumbnails) Get(ctx context.Context, actor security.Actor, id file.ID, spec ThumbnailSpec) (Result, error) {
	options, err := NormalizeThumbnail(spec, s.limits)
	if err != nil {
		return Result{}, err
	}
	// Always authorize and resolve live source identity, including on cache hits.
	source, err := s.files.GetFile(ctx, actor, id)
	if err != nil {
		return Result{}, err
	}
	if !EditableMIME(source.MIMEType) {
		return Result{}, ErrUnsupportedFormat
	}
	flight := s.flights.DoChan(ThumbnailKey(source, options), func() (any, error) {
		return cache.RememberJSON(ctx, s.store, ThumbnailKey(source, options), cache.SetOptions{TTL: 24 * time.Hour}, func(ctx context.Context) (Result, error) {
			opened, err := s.files.Open(ctx, actor, id)
			if err != nil {
				return Result{}, err
			}
			defer opened.Body.Close()
			return s.processor.Transform(ctx, opened.Body, options)
		})
	})
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case result := <-flight:
		if result.Err != nil {
			return Result{}, result.Err
		}
		value := result.Val.(Result)
		value.Bytes = append([]byte(nil), value.Bytes...)
		return value, nil
	}
}
func NormalizeThumbnail(spec ThumbnailSpec, l Limits) (TransformOptions, error) {
	if spec.Width == 0 {
		spec.Width = 128
	}
	if spec.Height == 0 {
		spec.Height = 128
	}
	// Finite preset grid: at most 5x5x3x9 variants per immutable source.
	for _, n := range []int{spec.Width, spec.Height} {
		if n > l.ThumbnailDimension || (n != 64 && n != 128 && n != 256 && n != 512 && n != 1024) {
			return TransformOptions{}, ErrInvalidTransform
		}
	}
	return Normalize(TransformOptions{Width: spec.Width, Height: spec.Height, Fit: spec.Fit, Position: spec.Position}, l)
}
func ThumbnailKey(source file.File, o TransformOptions) string {
	raw, _ := json.Marshal(o)
	return fmt.Sprintf("images:thumb:v1:%d:%s:%x", source.ID, source.ChecksumSHA256, sha256.Sum256(raw))
}
