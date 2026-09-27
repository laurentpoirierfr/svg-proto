// Package baseline implements the three reference encoders that Phase 1
// measures against.
//
// They exist to be beaten. A raster-to-SVG converter has exactly one honest
// referential point:
//
//	<svg viewBox="0 0 W H"><image href="data:image/png;base64,..."/></svg>
//
// which is already valid SVG, has perfect fidelity, and costs a single DOM node.
// A traceur only earns its place on content where editing matters. Measuring
// against anything weaker is self-deception, and measuring against nothing is
// worse. These three are:
//
//	Embed    - the null hypothesis: lossless, one node
//	Grid     - quantise, then one <rect> per tile, colour averaged per tile
//	RunLength- quantise, then one <rect> per horizontal run of equal colour
//
// Grid and RunLength are "the best of the dumb options". They are included
// because a lot of real converters do roughly this, and because they cost about
// 200 lines between them, so there is no excuse for not knowing the number.
package baseline

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"math"

	"github.com/elfeo/svg-proto/internal/colorconv"
	"github.com/elfeo/svg-proto/internal/dom"
	"github.com/elfeo/svg-proto/internal/pixelbuf"
	"github.com/elfeo/svg-proto/internal/quant"
)

// Result carries the encoded document plus the diagnostics that make the
// encoder's choices auditable. An encoder that cannot explain itself cannot be
// debugged, and cannot be improved either.
type Result struct {
	Doc *dom.Doc
	// Shapes is the number of paint elements emitted.
	Shapes int
	// Rects is the same as Shapes for these encoders, kept for clarity when
	// the contour tracer joins.
	Rects int
	// PaletteLen is the number of colours actually used.
	PaletteLen int
	// Truncated reports that the node budget cut encoding short.
	Truncated bool
	// Coverage is the fraction of pixels the emitted shapes cover, 0..1. Should
	// be 1 for Grid and RunLength; anything less means a bug.
	Coverage float64
	// Notes are human-readable remarks worth surfacing in a report.
	Notes []string
}

// Options configures a baseline encoder.
type Options struct {
	// Tile is the grid cell size in pixels for Grid. 1 gives one rect per pixel,
	// which is the worst possible encoding and a useful sanity floor.
	Tile int
	// MaxNodes, when > 0, caps the number of emitted shapes. Hitting the cap
	// sets Truncated. The budget is a contract (D6, reflexion 10.1): bytes are
	// compressible, nodes are not.
	MaxNodes int
	// AlphaLevels is how many distinct fill-opacity values to emit. 1 means
	// fully opaque only, which is wrong for an image with alpha but cheap.
	AlphaLevels int
	// Annotate adds a <metadata> block recording the encoder settings. Off by
	// default because it is a per-file cost; on when the output is archived.
	Annotate bool
	// Title, if set, becomes <title> and an aria-label. Free accessibility, and
	// it costs one element.
	Title string
}

func (o Options) withDefaults() Options {
	if o.Tile <= 0 {
		o.Tile = 8
	}
	if o.AlphaLevels <= 0 {
		o.AlphaLevels = 8
	}
	return o
}

// Embed returns the raw source bytes and produces the lossless baseline.
//
// Note what this is not: an optimisation. It is a one-element document with a
// data URI, and it wins on every cost metric except editability. It is in the
// harness because every honest comparison needs it.
func Embed(src image.Image, opts Options) (*Result, error) {
	opts = opts.withDefaults()
	b := src.Bounds()
	w, h := float64(b.Dx()), float64(b.Dy())

	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, src); err != nil {
		return nil, fmt.Errorf("re-encode source: %w", err)
	}

	doc := dom.NewDoc(w, h)
	// data: rather than a separate file keeps the document self-contained, which
	// is the whole reason to prefer SVG over PNG in the first place.
	img := dom.E("image",
		dom.A("x", "0"), dom.A("y", "0"),
		dom.AF("width", w, 3), dom.AF("height", h, 3),
		dom.A("href", "data:image/png;base64,"+base64.StdEncoding.EncodeToString(buf.Bytes())),
	)
	doc.Root.Append(img)
	finish(doc, opts, 1, len(buf.Bytes()))

	return &Result{
		Doc: doc, Shapes: 1, Rects: 1, Coverage: 1,
		Notes: []string{"lossless: the raster is embedded, nothing is vectorised"},
	}, nil
}

// Grid quantises the image and emits one rect per tile, each filled with the
// alpha-weighted mean of the tile.
//
// Averaging rather than majority vote is deliberate: it is what a mosaic really
// does, and it gives lower error for the same shape count, which makes this a
// stronger baseline and therefore a more useful one.
func Grid(im *pixelbuf.Image, pal *quant.Palette, opts Options) (*Result, error) {
	opts = opts.withDefaults()
	t := opts.Tile
	res := &Result{Doc: dom.NewDoc(float64(im.W), float64(im.H)), PaletteLen: pal.Len()}

	nx := (im.W + t - 1) / t
	ny := (im.H + t - 1) / t
	if opts.MaxNodes > 0 && nx*ny > opts.MaxNodes {
		// Grow the tile until the budget is met. Coarsening is strictly better
		// than truncating: a truncated document is missing a corner of the image.
		for t < max(im.W, im.H) && nx*ny > opts.MaxNodes {
			t *= 2
			nx = (im.W + t - 1) / t
			ny = (im.H + t - 1) / t
		}
		res.Truncated = true
		res.Notes = append(res.Notes, fmt.Sprintf("tile grown to %dpx to fit the %d-node budget", t, opts.MaxNodes))
	}

	// Group by colour so a re-style is one attribute, not N. Grouping also
	// removes the per-rect fill, which is the single largest text saving
	// available in an SVG.
	byColor := map[uint32][]rect{}
	order := []uint32{}
	acc := &labAccumulator{}
	for ty := 0; ty < ny; ty++ {
		for tx := 0; tx < nx; tx++ {
			acc.reset()
			for y := ty * t; y < min((ty+1)*t, im.H); y++ {
				for x := tx * t; x < min((tx+1)*t, im.W); x++ {
					acc.add(im, pal, y*im.W+x)
				}
			}
			r, g, b, a := acc.mean()
			key := packRGBA(r, g, b, alphaLevel(a, opts.AlphaLevels))
			if _, seen := byColor[key]; !seen {
				order = append(order, key)
			}
			byColor[key] = append(byColor[key], rect{
				x: tx * t, y: ty * t,
				w: min((tx+1)*t, im.W) - tx*t,
				h: min((ty+1)*t, im.H) - ty*t,
			})
		}
	}
	res.Shapes = nx * ny
	res.Rects = res.Shapes
	res.Coverage = 1
	appendRects(res.Doc.Root, byColor, order, opts)
	finish(res.Doc, opts, res.Shapes, res.PaletteLen)
	return res, nil
}

// RunLength quantises the image and emits one rect per horizontal run of equal
// (colour, alpha level).
//
// This is exact with respect to the quantised palette, so it is the strongest
// non-vector baseline: any error it makes is the quantiser's, not the
// representation's. Beating it therefore requires genuinely better geometry,
// not better bookkeeping.
func RunLength(im *pixelbuf.Image, pal *quant.Palette, opts Options) (*Result, error) {
	opts = opts.withDefaults()
	res := &Result{Doc: dom.NewDoc(float64(im.W), float64(im.H)), PaletteLen: pal.Len()}

	byColor := map[uint32][]rect{}
	order := []uint32{}
	truncated := false

	for y := 0; y < im.H; y++ {
		if truncated {
			break
		}
		x := 0
		for x < im.W {
			i := y*im.W + x
			idx := pal.Index[i]
			a := float64(im.A[i]) / 255
			level := alphaLevel(a, opts.AlphaLevels)
			// Extend the run while colour and alpha level both hold.
			x2 := x + 1
			for x2 < im.W {
				j := y*im.W + x2
				if pal.Index[j] != idx || alphaLevel(float64(im.A[j])/255, opts.AlphaLevels) != level {
					break
				}
				x2++
			}
			r, g, b := pal.RGB(int(idx))
			key := packRGBA(r, g, b, level)
			if _, seen := byColor[key]; !seen {
				order = append(order, key)
			}
			byColor[key] = append(byColor[key], rect{x: x, y: y, w: x2 - x, h: 1})
			res.Shapes++
			if opts.MaxNodes > 0 && res.Shapes >= opts.MaxNodes {
				truncated = true
				break
			}
			x = x2
		}
	}
	if truncated {
		res.Truncated = true
		res.Notes = append(res.Notes, fmt.Sprintf("encoding stopped at the %d-node budget", opts.MaxNodes))
		res.Coverage = float64(res.Shapes) / float64(im.W*im.H)
		res.Notes = append(res.Notes, fmt.Sprintf("coverage %.1f%% of the image", res.Coverage*100))
	} else {
		res.Coverage = 1
	}
	res.Rects = res.Shapes
	appendRects(res.Doc.Root, byColor, order, opts)
	finish(res.Doc, opts, res.Shapes, res.PaletteLen)
	return res, nil
}

// ---------------------------------------------------------------------------

type rect struct{ x, y, w, h int }

// appendRects emits one <g> per colour, each holding that colour's rects with a
// single fill on the group. Order is the insertion order, so output is
// deterministic.
func appendRects(root *dom.Element, byColor map[uint32][]rect, order []uint32, opts Options) {
	for _, key := range order {
		rs := byColor[key]
		cr, cg, cb, al := unpackRGBA(key)
		grp := dom.E("g", dom.A("fill", rgbHex(cr, cg, cb)))
		if al < 255 {
			grp.Set("fill-opacity", alphaText(al))
		}
		for _, q := range rs {
			grp.Append(rectEl(q))
		}
		root.Append(grp)
	}
}

func rectEl(q rect) *dom.Element {
	return dom.E("rect",
		dom.AI("x", q.x), dom.AI("y", q.y),
		dom.AI("width", q.w), dom.AI("height", q.h),
	)
}

// finish appends the accessibility and provenance metadata that costs
// essentially nothing and is the difference between a file a screen reader can
// describe and a file it cannot.
func finish(doc *dom.Doc, opts Options, shapes, palette int) {
	if opts.Title != "" {
		// <title> is the accessible name of the whole graphic. It must be the
		// first child to be picked up reliably.
		doc.Root.Append(dom.E("title").Append(dom.Text(opts.Title)))
		doc.Root.Set("aria-label", opts.Title)
		doc.Root.Set("role", "img")
	}
	if opts.Annotate {
		m := dom.E("metadata")
		m.Append(dom.Text(fmt.Sprintf(
			"generator=svg-proto; shapes=%d; palette=%d; alpha-levels=%d",
			shapes, palette, opts.AlphaLevels)))
		doc.Root.Append(m)
	}
}

func rgbHex(r, g, b uint8) string {
	const hex = "0123456789abcdef"
	buf := [7]byte{'#', 0, 0, 0, 0, 0, 0}
	// offsets 0, 2, 4 so the two nibbles land at buf[1..2], [3..4], [5..6].
	put := func(i int, v uint8) {
		buf[i+1] = hex[v>>4]
		buf[i+2] = hex[v&0xf]
	}
	put(0, r)
	put(2, g)
	put(4, b)
	return string(buf[:])
}

func alphaText(a uint8) string {
	// Two decimals is enough to round-trip a fill-opacity that any renderer
	// will treat as distinct, and shorter than the full float.
	return fmt.Sprintf("%.2f", math.Round(float64(a)/255*100)/100)
}

func packRGBA(r, g, b, a uint8) uint32 {
	return uint32(r)<<24 | uint32(g)<<16 | uint32(b)<<8 | uint32(a)
}

func unpackRGBA(k uint32) (r, g, b, a uint8) {
	return uint8(k >> 24), uint8(k >> 16), uint8(k >> 8), uint8(k)
}

// alphaLevel snaps alpha to one of n buckets. A per-pixel float opacity would
// be both a long number and a false claim of precision.
func alphaLevel(a float64, levels int) uint8 {
	if levels <= 1 {
		return 255
	}
	i := int(math.Round(a * float64(levels-1)))
	if i < 0 {
		i = 0
	}
	if i > levels-1 {
		i = levels - 1
	}
	return uint8(math.Round(float64(i) / float64(levels-1) * 255))
}

// labAccumulator averages a tile in OKLab, alpha-weighted.
//
// Averaging in OKLab rather than sRGB is not a refinement: the sRGB average of
// two colours straddling a boundary is a colour that appears nowhere in the
// image, and on a high-contrast edge it is a visible false colour.
type labAccumulator struct{ l, a, b, w float64 }

func (acc *labAccumulator) reset() { *acc = labAccumulator{} }

func (acc *labAccumulator) add(im *pixelbuf.Image, pal *quant.Palette, i int) {
	alpha := float64(im.A[i]) / 255
	// Colour comes from the palette, not the source pixel: the shape will be
	// filled with a palette colour, so the tile must average the same thing.
	r, g, bb := pal.RGB(int(pal.Index[i]))
	L, ca, cb := oklab(r, g, bb)
	acc.l += L * alpha
	acc.a += ca * alpha
	acc.b += cb * alpha
	acc.w += alpha
}

func (acc *labAccumulator) mean() (r, g, b uint8, alpha float64) {
	if acc.w <= 0 {
		return 0, 0, 0, 0
	}
	r, g, bb := oklabToRGB(acc.l/acc.w, acc.a/acc.w, acc.b/acc.w)
	return r, g, bb, math.Min(1, acc.w)
}

// Small local helpers, kept here so the baseline package has no dependency on
// pixelbuf's internals. They are exactly colorconv's conversions.
func oklab(r, g, b uint8) (L, a, bb float64) {
	return colorconv.RGBToLabFloat(float64(r), float64(g), float64(b))
}

func oklabToRGB(L, a, b float64) (r, g, bb uint8) {
	return colorconv.OKLabToRGB(L, a, b)
}
