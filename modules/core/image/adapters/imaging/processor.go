package imaging

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	stdimage "image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math"

	lib "github.com/disintegration/imaging"
	image "github.com/vernal96/go-cms-kernel/modules/core/image"
)

type Processor struct{ limits image.Limits }

func New(l image.Limits) (*Processor, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	return &Processor{l}, nil
}
func (p *Processor) Transform(ctx context.Context, source io.Reader, options image.TransformOptions) (image.Result, error) {
	var result image.Result
	o, err := image.Normalize(options, p.limits)
	if err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	raw, err := io.ReadAll(io.LimitReader(source, p.limits.MaxBytes+1))
	if err != nil {
		return result, err
	}
	if int64(len(raw)) > p.limits.MaxBytes {
		return result, image.ErrLimit
	}

	// Only invoke the explicitly supported standard-library decoders. Other
	// formats registered by an adapter must never parse untrusted input here.
	var cfg stdimage.Config
	var format string
	switch {
	case bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")):
		format = "png"
		cfg, err = png.DecodeConfig(bytes.NewReader(raw))
	case bytes.HasPrefix(raw, []byte{0xff, 0xd8, 0xff}):
		format = "jpeg"
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(raw))
	default:
		return result, image.ErrUnsupportedFormat
	}
	if err != nil {
		return result, fmt.Errorf("%w: invalid raster header", image.ErrUnsupportedFormat)
	}
	if format == "png" && animatedPNG(raw) {
		return result, image.ErrUnsupportedFormat
	}
	if err = p.limits.Dimensions(cfg.Width, cfg.Height, true); err != nil {
		return result, err
	}
	src, err := lib.Decode(bytes.NewReader(raw), lib.AutoOrientation(true))
	if err != nil {
		return result, fmt.Errorf("%w: invalid raster", image.ErrUnsupportedFormat)
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	w, h := int(math.Round(float64(src.Bounds().Dx())*math.Abs(o.ScaleX))), int(math.Round(float64(src.Bounds().Dy())*math.Abs(o.ScaleY)))
	if err = p.limits.Dimensions(w, h, true); err != nil {
		return result, err
	}
	if o.ScaleX < 0 {
		src = lib.FlipH(src)
	}
	if o.ScaleY < 0 {
		src = lib.FlipV(src)
	}
	if w != src.Bounds().Dx() || h != src.Bounds().Dy() {
		src = lib.Resize(src, w, h, lib.Lanczos)
	}
	switch o.Rotate {
	case 90:
		src = lib.Rotate270(src)
	case 180:
		src = lib.Rotate180(src)
	case 270:
		src = lib.Rotate90(src)
	}
	w, h = src.Bounds().Dx(), src.Bounds().Dy()
	if c := o.Crop; c != nil {
		if c.X > w-c.Width || c.Y > h-c.Height {
			return result, image.ErrInvalidTransform
		}
		src = lib.Crop(src, stdimage.Rect(c.X, c.Y, c.X+c.Width, c.Y+c.Height))
		w, h = c.Width, c.Height
	}
	ow, oh := o.Width, o.Height
	if ow == 0 && oh == 0 {
		ow, oh = w, h
	} else if ow == 0 {
		ow = max(1, int(math.Round(float64(w)*float64(oh)/float64(h))))
	} else if oh == 0 {
		oh = max(1, int(math.Round(float64(h)*float64(ow)/float64(w))))
	}
	if err = p.limits.Dimensions(ow, oh, false); err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	anchor := map[string]lib.Anchor{"center": lib.Center, "top": lib.Top, "bottom": lib.Bottom, "left": lib.Left, "right": lib.Right, "top-left": lib.TopLeft, "top-right": lib.TopRight, "bottom-left": lib.BottomLeft, "bottom-right": lib.BottomRight}[o.Position]
	switch o.Fit {
	case "stretch":
		src = lib.Resize(src, ow, oh, lib.Lanczos)
	case "cover":
		// Crop before resizing, including very thin inputs. Fill's resize-first
		// path for small images can allocate an enormous intermediate raster.
		cw, ch := w, h
		if int64(w)*int64(oh) > int64(h)*int64(ow) {
			cw = max(1, int(math.Round(float64(h)*float64(ow)/float64(oh))))
		} else {
			ch = max(1, int(math.Round(float64(w)*float64(oh)/float64(ow))))
		}
		src = lib.Resize(lib.CropAnchor(src, cw, ch, anchor), ow, oh, lib.Lanczos)
	case "contain":
		ratio := math.Min(float64(ow)/float64(w), float64(oh)/float64(h))
		rw, rh := max(1, int(math.Round(float64(w)*ratio))), max(1, int(math.Round(float64(h)*ratio)))
		scaled := lib.Resize(src, rw, rh, lib.Lanczos)
		x, y := (ow-rw)/2, (oh-rh)/2
		switch o.Position {
		case "left", "top-left", "bottom-left":
			x = 0
		case "right", "top-right", "bottom-right":
			x = ow - rw
		}
		switch o.Position {
		case "top", "top-left", "top-right":
			y = 0
		case "bottom", "bottom-left", "bottom-right":
			y = oh - rh
		}
		src = lib.Paste(lib.New(ow, oh, color.NRGBA{}), scaled, stdimage.Pt(x, y))
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	var out bytes.Buffer
	encoding := lib.PNG
	mime := "image/png"
	if format == "jpeg" {
		encoding = lib.JPEG
		mime = "image/jpeg"
		src = lib.Overlay(lib.New(ow, oh, color.White), src, stdimage.Point{}, 1)
	}
	if err = lib.Encode(&out, src, encoding, lib.JPEGQuality(o.Quality)); err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if int64(out.Len()) > p.limits.MaxBytes {
		return result, image.ErrLimit
	}
	return image.Result{Bytes: out.Bytes(), MIMEType: mime, Width: ow, Height: oh}, nil
}
func animatedPNG(raw []byte) bool {
	for pos := 8; pos+12 <= len(raw); {
		n := int64(binary.BigEndian.Uint32(raw[pos : pos+4]))
		if string(raw[pos+4:pos+8]) == "acTL" {
			return true
		}
		if n > int64(len(raw)-pos-12) {
			break
		}
		pos += int(n) + 12
	}
	return false
}
