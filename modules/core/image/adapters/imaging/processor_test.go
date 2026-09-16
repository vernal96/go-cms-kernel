package imaging

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	stdimage "image"
	"image/color"
	"image/png"
	"testing"

	image "github.com/vernal96/go-cms-kernel/modules/core/image"
)

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := stdimage.NewNRGBA(stdimage.Rect(0, 0, 8, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.NRGBA{R: uint8(x * 30), G: uint8(y * 60), A: 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestTransformModesAndRotation(t *testing.T) {
	p, _ := New(image.DefaultLimits())
	for _, fit := range []string{"contain", "cover", "stretch"} {
		for _, rotation := range []int{0, 90, 180, 270} {
			r, err := p.Transform(context.Background(), bytes.NewReader(pngBytes(t)), image.TransformOptions{Width: 16, Height: 12, Fit: fit, Rotate: rotation, ScaleX: -1})
			if err != nil {
				t.Fatal(err)
			}
			cfg, format, err := stdimage.DecodeConfig(bytes.NewReader(r.Bytes))
			if err != nil || format != "png" || cfg.Width != 16 || cfg.Height != 12 || r.MIMEType != "image/png" {
				t.Fatalf("result %+v %v", cfg, err)
			}
		}
	}
}
func TestTransformRejectsInvalidInput(t *testing.T) {
	p, _ := New(image.DefaultLimits())
	tests := []struct {
		name string
		raw  []byte
		o    image.TransformOptions
		want error
	}{
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), image.TransformOptions{}, image.ErrUnsupportedFormat},
		{"crop", pngBytes(t), image.TransformOptions{Crop: &image.Crop{X: 7, Width: 2, Height: 2}}, image.ErrInvalidTransform},
		{"output", pngBytes(t), image.TransformOptions{Width: 9000}, image.ErrLimit},
		{"pixels", pngBytes(t), image.TransformOptions{Width: 4096, Height: 4096}, image.ErrLimit},
		{"scale", pngBytes(t), image.TransformOptions{ScaleX: 10}, image.ErrInvalidTransform},
		{"quality", pngBytes(t), image.TransformOptions{Quality: 101}, image.ErrInvalidTransform},
		{"rotation", pngBytes(t), image.TransformOptions{Rotate: 45}, image.ErrInvalidTransform},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := p.Transform(context.Background(), bytes.NewReader(tt.raw), tt.o)
			if !errors.Is(err, tt.want) {
				t.Fatalf("got %v want %v", err, tt.want)
			}
		})
	}
}
func TestSourceDimensionsRejectedBeforeDecode(t *testing.T) {
	p, _ := New(image.DefaultLimits())
	raw := pngBytes(t)
	binary.BigEndian.PutUint32(raw[16:20], 9000)
	binary.BigEndian.PutUint32(raw[29:33], crc32.ChecksumIEEE(raw[12:29]))
	_, err := p.Transform(context.Background(), bytes.NewReader(raw), image.TransformOptions{Width: 128, Height: 128})
	if !errors.Is(err, image.ErrLimit) {
		t.Fatal(err)
	}
}
func TestAnimatedPNGRejected(t *testing.T) {
	p, _ := New(image.DefaultLimits())
	raw := pngBytes(t)
	chunk := []byte{0, 0, 0, 8, 'a', 'c', 'T', 'L', 0, 0, 0, 2, 0, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(chunk[16:], crc32.ChecksumIEEE(chunk[4:16]))
	raw = append(append(append([]byte{}, raw[:33]...), chunk...), raw[33:]...)
	_, err := p.Transform(context.Background(), bytes.NewReader(raw), image.TransformOptions{})
	if !errors.Is(err, image.ErrUnsupportedFormat) {
		t.Fatal(err)
	}
}

func TestCoverThinSourceIsBounded(t *testing.T) {
	p, _ := New(image.DefaultLimits())
	src := stdimage.NewNRGBA(stdimage.Rect(0, 0, 1, 8192))
	var raw bytes.Buffer
	png.Encode(&raw, src)
	r, err := p.Transform(context.Background(), &raw, image.TransformOptions{Width: 128, Height: 128, Fit: "cover"})
	if err != nil || r.Width != 128 || r.Height != 128 {
		t.Fatal("thin source", err)
	}
}
func TestFlipThenRotateMatchesBrowserCoordinates(t *testing.T) {
	p, _ := New(image.DefaultLimits())
	src := stdimage.NewNRGBA(stdimage.Rect(0, 0, 2, 1))
	src.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	src.SetNRGBA(1, 0, color.NRGBA{G: 255, A: 255})
	var raw bytes.Buffer
	png.Encode(&raw, src)
	r, err := p.Transform(context.Background(), &raw, image.TransformOptions{Rotate: 90, ScaleX: -1, Fit: "stretch"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := png.Decode(bytes.NewReader(r.Bytes))
	if err != nil {
		t.Fatal(err)
	}
	red, green, _, _ := result.At(0, 0).RGBA()
	if red != 0 || green != 65535 || result.Bounds().Dx() != 1 || result.Bounds().Dy() != 2 {
		t.Fatal("flip and rotation order disagrees with editor")
	}
}

func TestSourcePixelBudgetBeforeDecode(t *testing.T) {
	p, _ := New(image.DefaultLimits())
	raw := pngBytes(t)
	binary.BigEndian.PutUint32(raw[16:20], 8192)
	binary.BigEndian.PutUint32(raw[20:24], 8192)
	binary.BigEndian.PutUint32(raw[29:33], crc32.ChecksumIEEE(raw[12:29]))
	_, err := p.Transform(context.Background(), bytes.NewReader(raw), image.TransformOptions{Width: 128, Height: 128})
	if !errors.Is(err, image.ErrLimit) {
		t.Fatal(err)
	}
}
