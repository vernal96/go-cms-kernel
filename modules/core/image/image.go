// Package image defines bounded raster transforms independent of storage and transport.
package image

import (
	"context"
	"errors"
	"io"
	"math"
)

var ErrInvalidTransform = errors.New("invalid image transform")
var ErrUnsupportedFormat = errors.New("unsupported image format")
var ErrLimit = errors.New("image processing limit exceeded")

type Crop struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Crop coordinates refer to the oriented root original after scale/flip and clockwise rotation.
// Positive rotation is clockwise. Negative scales flip the corresponding axis.
type TransformOptions struct {
	Crop     *Crop   `json:"crop,omitempty"`
	Rotate   int     `json:"rotate"`
	ScaleX   float64 `json:"scale_x"`
	ScaleY   float64 `json:"scale_y"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Fit      string  `json:"fit"`
	Position string  `json:"position"`
	Quality  int     `json:"quality"`
}
type Result struct {
	Bytes    []byte
	MIMEType string
	Width    int
	Height   int
}
type Processor interface {
	Transform(context.Context, io.Reader, TransformOptions) (Result, error)
}
type Limits struct {
	SourceDimension    int     `json:"source_dimension"`
	SourcePixels       int64   `json:"source_pixels"`
	OutputDimension    int     `json:"output_dimension"`
	OutputPixels       int64   `json:"output_pixels"`
	ThumbnailDimension int     `json:"thumbnail_dimension"`
	MaxBytes           int64   `json:"max_bytes"`
	MinScale           float64 `json:"min_scale"`
	MaxScale           float64 `json:"max_scale"`
	MinQuality         int     `json:"min_quality"`
	MaxQuality         int     `json:"max_quality"`
}

func DefaultLimits() Limits {
	return Limits{
		SourceDimension: 8192, SourcePixels: 32_000_000,
		OutputDimension: 4096, OutputPixels: 16_000_000,
		ThumbnailDimension: 512, MaxBytes: 32 << 20,
		MinScale: 0.1, MaxScale: 4, MinQuality: 1, MaxQuality: 100,
	}
}
func (l Limits) Validate() error {
	if l.SourceDimension < 1 || l.SourceDimension > 32768 ||
		l.SourcePixels < 1 || l.SourcePixels > 100_000_000 ||
		l.OutputDimension < 1 || l.OutputDimension > 16384 ||
		l.OutputPixels < 1 || l.OutputPixels > 100_000_000 ||
		l.ThumbnailDimension < 64 || l.ThumbnailDimension > 1024 ||
		l.MaxBytes < 1 || l.MaxBytes > 128<<20 ||
		l.MinScale <= 0 || l.MaxScale < l.MinScale || l.MaxScale > 16 ||
		l.MinQuality < 1 || l.MaxQuality > 100 || l.MinQuality > l.MaxQuality {
		return ErrLimit
	}
	return nil
}
func (l Limits) Dimensions(w, h int, source bool) error {
	dim, pixels := l.OutputDimension, l.OutputPixels
	if source {
		dim, pixels = l.SourceDimension, l.SourcePixels
	}
	if w < 1 || h < 1 || w > dim || h > dim || int64(w)*int64(h) > pixels {
		return ErrLimit
	}
	return nil
}
func Normalize(o TransformOptions, l Limits) (TransformOptions, error) {
	if err := l.Validate(); err != nil {
		return o, err
	}
	if o.ScaleX == 0 {
		o.ScaleX = 1
	}
	if o.ScaleY == 0 {
		o.ScaleY = 1
	}
	if o.Fit == "" {
		o.Fit = "contain"
	}
	if o.Position == "" {
		o.Position = "center"
	}
	if o.Quality == 0 {
		o.Quality = 85
	}
	if o.Rotate%90 != 0 || o.Rotate < -360 || o.Rotate > 360 {
		return o, ErrInvalidTransform
	}
	o.Rotate = (o.Rotate%360 + 360) % 360
	for _, s := range []float64{o.ScaleX, o.ScaleY} {
		if math.IsNaN(s) || math.IsInf(s, 0) || math.Abs(s) < l.MinScale || math.Abs(s) > l.MaxScale {
			return o, ErrInvalidTransform
		}
	}
	if o.Width < 0 || o.Height < 0 || o.Width > l.OutputDimension || o.Height > l.OutputDimension || int64(o.Width)*int64(o.Height) > l.OutputPixels {
		return o, ErrLimit
	}
	if o.Quality < l.MinQuality || o.Quality > l.MaxQuality {
		return o, ErrInvalidTransform
	}
	switch o.Fit {
	case "contain", "cover", "stretch":
	default:
		return o, ErrInvalidTransform
	}
	switch o.Position {
	case "center", "top", "bottom", "left", "right", "top-left", "top-right", "bottom-left", "bottom-right":
	default:
		return o, ErrInvalidTransform
	}
	if c := o.Crop; c != nil {
		if c.X < 0 || c.Y < 0 || c.Width < 1 || c.Height < 1 || c.Width > l.SourceDimension || c.Height > l.SourceDimension || c.X > l.SourceDimension-c.Width || c.Y > l.SourceDimension-c.Height {
			return o, ErrInvalidTransform
		}
		v := *c
		o.Crop = &v
	}
	return o, nil
}
func EditableMIME(m string) bool { return m == "image/jpeg" || m == "image/png" }
