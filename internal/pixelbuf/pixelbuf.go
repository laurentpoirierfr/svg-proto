// Package pixelbuf turns a decoded image into the flat, planar buffers the rest
// of the pipeline works on.
//
// Two rules, both learned the hard way:
//
//  1. Convert once, to a flat slice, and never touch image.Image again.
//     image.Image.At is an interface call per pixel, the concrete types are not
//     all safe for concurrent reads, and every stage downstream wants to be a
//     pure function over a slice.
//
//  2. Alpha is stored straight (non-premultiplied), because that is what the
//     source pixels actually mean. Un-premultiplying a renderer's output in
//     linear light and compositing over a background is the renderer's job, at
//     a known background. Doing it early, against an unknown one, is where the
//     coloured halos on antialiased edges come from.
//
// Colour is kept as 8-bit sRGB (exact, no rounding away the source) and
// converted to OKLab on demand, once, and cached.
package pixelbuf

import (
	"fmt"
	"image"
	"image/color"
	"io"

	"github.com/elfeo/svg-proto/internal/colorconv"
)

// Image is a decoded raster as four straight-alpha 8-bit planes.
type Image struct {
	W, H       int
	R, G, B, A []uint8
	lab        *LabPlane
}

// LabPlane is an OKLab image, 3 float64 per pixel.
type LabPlane struct {
	W, H int
	Pix  []float64 // interleaved L, a, b
}

// Decode reads any image format registered with the image package. The caller
// is responsible for the blank imports (png, jpeg, and x/image for the rest).
func Decode(r io.Reader) (*Image, error) {
	src, _, err := image.Decode(r)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return FromImage(src), nil
}

// FromImage converts any image.Image into planar straight-alpha sRGB.
// color.NRGBAModel is the safe normaliser: it un-premultiplies correctly for
// every concrete image type, including the ones that store premultiplied data.
func FromImage(src image.Image) *Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	im := &Image{
		W: w, H: h,
		R: make([]uint8, w*h),
		G: make([]uint8, w*h),
		B: make([]uint8, w*h),
		A: make([]uint8, w*h),
	}
	// A single scratch colour: the conversion allocates nothing per pixel.
	var c color.NRGBA
	i := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c = color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA)
			im.R[i], im.G[i], im.B[i], im.A[i] = c.R, c.G, c.B, c.A
			i++
		}
	}
	return im
}

// Lab returns the OKLab plane, computing it on first use. Cached because every
// later stage wants it and it is the single most expensive conversion here.
func (im *Image) Lab() *LabPlane {
	if im.lab != nil {
		return im.lab
	}
	p := &LabPlane{W: im.W, H: im.H, Pix: make([]float64, 3*im.W*im.H)}
	for i := 0; i < im.W*im.H; i++ {
		L, a, b := colorconv.RGBToLabFloat(float64(im.R[i]), float64(im.G[i]), float64(im.B[i]))
		p.Pix[3*i], p.Pix[3*i+1], p.Pix[3*i+2] = L, a, b
	}
	im.lab = p
	return p
}

// AlphaAt returns the alpha of pixel (x, y) in [0,1], clamping out-of-range
// coordinates. Bounds checks in hot loops are a real cost, so this exists.
// LabChannel returns one axis of the cached OKLab plane as its own slice: L, a
// or b for offset 0, 1 or 2. The returned slice is a copy, because the
// quantiser keeps it and the cache must not be aliased into a long-lived
// document.
func (im *Image) LabChannel(offset int) []float64 {
	pix := im.Lab().Pix
	out := make([]float64, len(pix)/3)
	for i := range out {
		out[i] = pix[3*i+offset]
	}
	return out
}

// Alpha01 returns the straight alpha plane scaled to [0,1], which is the form the
// quantiser weights by.
func (im *Image) Alpha01() []float64 {
	out := make([]float64, len(im.A))
	for i, a := range im.A {
		out[i] = float64(a) / 255
	}
	return out
}

func (im *Image) AlphaAt(x, y int) float64 {
	if x < 0 || y < 0 || x >= im.W || y >= im.H {
		return 0
	}
	return float64(im.A[y*im.W+x]) / 255
}

// Opaque reports whether every pixel is fully opaque. Worth knowing: a fully
// opaque image has no alpha representation cost, which changes the shape of the
// budget.
func (im *Image) Opaque() bool {
	for _, a := range im.A {
		if a != 255 {
			return false
		}
	}
	return true
}

// AlphaLevels counts the distinct alpha values, capped at limit+1. Used to
// decide whether alpha needs to appear in the output at all.
func (im *Image) AlphaLevels(limit int) int {
	seen := make(map[uint8]struct{}, limit+1)
	for _, a := range im.A {
		seen[a] = struct{}{}
		if len(seen) > limit {
			return limit + 1
		}
	}
	return len(seen)
}
