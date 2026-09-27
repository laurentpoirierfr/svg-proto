package encode

import (
	"encoding/hex"
	"image"
	"image/color"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/elfeo/svg-proto/internal/curvefit"
	"github.com/elfeo/svg-proto/internal/dom"
	"github.com/elfeo/svg-proto/internal/pixelbuf"
	"github.com/elfeo/svg-proto/internal/quant"
)

// solid builds a w*h image from a per-pixel function returning straight RGBA.
func solid(w, h int, f func(x, y int) color.RGBA) (*pixelbuf.Image, *quant.Palette) {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.Set(x, y, f(x, y))
		}
	}
	im := pixelbuf.FromImage(m)
	q := quant.Default()
	q.K = 4
	return im, quant.Reduce(&quant.Input{W: im.W, H: im.H,
		L: im.LabChannel(0), A: im.LabChannel(1), B: im.LabChannel(2), Alpha: im.Alpha01()}, q)
}

func encodeOrFail(t *testing.T, im *pixelbuf.Image, pal *quant.Palette, o Options) *Result {
	t.Helper()
	res, err := Encode(im, pal, o)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return res
}

func pathData(t *testing.T, d *dom.Doc) []string {
	t.Helper()
	var out []string
	var walk func(*dom.Element)
	walk = func(e *dom.Element) {
		if e.Name == "path" {
			out = append(out, e.Get("d"))
		}
		for _, c := range e.Children {
			if el, ok := c.(*dom.Element); ok {
				walk(el)
			}
		}
	}
	walk(d.Root)
	return out
}

func TestTwoToneImageIsOneRectAndOnePath(t *testing.T) {
	// Left half black, right half white. The dark half is the base band and
	// becomes a rectangle, the light half is a traced band.
	im, pal := solid(16, 8, func(x, y int) color.RGBA {
		if x < 8 {
			return color.RGBA{0, 0, 0, 255}
		}
		return color.RGBA{255, 255, 255, 255}
	})
	res := encodeOrFail(t, im, pal, Options{})
	if res.Shapes != 2 {
		t.Errorf("got %d shapes, want 2: %v", res.Shapes, res.Notes)
	}
	ds := pathData(t, res.Doc)
	if len(ds) != 1 {
		t.Fatalf("got %d paths, want 1", len(ds))
	}
	// The white region starts at x=8 and runs to the right edge, which the padding
	// closes just outside the frame.
	if !strings.Contains(ds[0], "8") {
		t.Errorf("path %q does not mention the boundary at x=8", ds[0])
	}
	if res.Coverage != 1 {
		t.Errorf("coverage %v, want 1 for an opaque image", res.Coverage)
	}
}

func TestBandOrderPaintsBrightOnTopOfDark(t *testing.T) {
	// The regression test for the nesting bug: painting the bands largest-last
	// buries every bright region under the dim one. The document still parses and
	// the node count is unchanged, so only the rendered pixels show it. Here the
	// lightest pixel must end up in the topmost band, and since the encoder emits
	// darkest first, the white path must be the last path in the document.
	im, pal := solid(24, 8, func(x, y int) color.RGBA {
		switch {
		case x < 8:
			return color.RGBA{0, 0, 0, 255}
		case x < 16:
			return color.RGBA{128, 128, 128, 255}
		default:
			return color.RGBA{255, 255, 255, 255}
		}
	})
	res := encodeOrFail(t, im, pal, Options{})

	// Every painted element, in document order. Rectangles count: the base band
	// is a rectangle for an opaque image, so skipping them would hide the very
	// first element of the stack.
	var order []string
	var walk func(*dom.Element)
	walk = func(e *dom.Element) {
		if e.Get("fill") != "" {
			order = append(order, e.Get("fill"))
		}
		for _, c := range e.Children {
			if el, ok := c.(*dom.Element); ok {
				walk(el)
			}
		}
	}
	walk(res.Doc.Root)
	if len(order) < 2 {
		t.Fatalf("got %d filled elements, want at least 2: %v", len(order), order)
	}
	// The invariant, which does not depend on the quantiser keeping all three
	// tones: brightness never decreases along the paint order. Each band is a
	// superset of the next one's region, so painting them in order is what leaves
	// every band visible; painting them largest-last buries the bright ones under
	// a dim one, and the document still parses and still has the same node count.
	lum := func(fill string) float64 {
		var r, g, b int
		raw, err := hex.DecodeString(strings.TrimPrefix(fill, "#"))
		if err != nil || len(raw) != 3 {
			t.Fatalf("bad fill %q", fill)
		}
		r, g, b = int(raw[0]), int(raw[1]), int(raw[2])
		return 0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b)
	}
	prev := -1.0
	for i, f := range order {
		if l := lum(f); l < prev-1e-9 {
			t.Errorf("path %d is %s, darker than the path before it: bands are painted out of order", i, f)
		} else {
			prev = l
		}
	}
}

func TestTransparentPixelsAreLeftUnpainted(t *testing.T) {
	// A fully transparent pixel must not be painted at all, not painted black, and
	// not covered by the base rectangle.
	im, pal := solid(16, 8, func(x, y int) color.RGBA {
		if x < 4 {
			return color.RGBA{255, 0, 0, 0} // transparent
		}
		if x < 8 {
			return color.RGBA{0, 0, 255, 255}
		}
		return color.RGBA{255, 255, 255, 255}
	})
	res := encodeOrFail(t, im, pal, Options{})
	if res.Coverage != 0.75 {
		t.Errorf("coverage %v, want 0.75", res.Coverage)
	}
	// A non-opaque image cannot use the full-frame base rectangle, since that
	// would paint the transparent strip.
	for _, el := range res.Doc.Root.Children {
		e, ok := el.(*dom.Element)
		if !ok {
			continue
		}
		if e.Name == "rect" && e.Get("width") == "16" {
			t.Errorf("a full-frame rect was emitted for a partially transparent image")
		}
	}
}

func TestAlphaCutoffLeavesPixelsUnpainted(t *testing.T) {
	im, pal := solid(16, 8, func(x, y int) color.RGBA {
		if x < 8 {
			return color.RGBA{255, 255, 255, uint8(x * 8)}
		}
		return color.RGBA{0, 0, 0, 255}
	})
	// Half-transparent pixels are below a cutoff of 0.5.
	res := encodeOrFail(t, im, pal, Options{AlphaCutoff: 0.5})
	if res.Coverage != 0.5 {
		t.Errorf("coverage %v, want 0.5", res.Coverage)
	}
	if len(res.Notes) == 0 {
		t.Error("dropping pixels silently is not acceptable; a note is expected")
	}
	// A cutoff of zero must not discard a merely faint pixel: only a fully
	// transparent one. The comparison has to be strict for that to hold.
	im2, pal2 := solid(16, 8, func(x, y int) color.RGBA {
		if x < 8 {
			return color.RGBA{255, 255, 255, 1} // alpha 1/255, not zero
		}
		return color.RGBA{0, 0, 0, 255}
	})
	res2 := encodeOrFail(t, im2, pal2, Options{})
	if res2.Coverage != 1 {
		t.Errorf("coverage %v, want 1: a pixel with alpha 1/255 is not transparent", res2.Coverage)
	}
}

func TestEveryBandCarriesAFillOpacityWhenItIsTranslucent(t *testing.T) {
	// Alpha is not a palette dimension, so it travels as fill-opacity. A band whose
	// pixels are half transparent must say so, or it renders fully opaque.
	im, pal := solid(16, 8, func(x, y int) color.RGBA {
		if x < 8 {
			return color.RGBA{255, 255, 255, 128}
		}
		return color.RGBA{0, 0, 0, 255}
	})
	res := encodeOrFail(t, im, pal, Options{AlphaLevels: 8})
	found := false
	var walk func(*dom.Element)
	walk = func(e *dom.Element) {
		if e.Get("fill-opacity") != "" {
			found = true
			if v, err := strconv.ParseFloat(e.Get("fill-opacity"), 64); err != nil || v <= 0 || v > 1 {
				t.Errorf("fill-opacity %q is not a fraction", e.Get("fill-opacity"))
			}
		}
		for _, c := range e.Children {
			if el, ok := c.(*dom.Element); ok {
				walk(el)
			}
		}
	}
	walk(res.Doc.Root)
	if !found {
		t.Error("no fill-opacity emitted for a half-transparent band")
	}
}

func TestNodeBudgetIsRespectedByRaisingTheTolerance(t *testing.T) {
	// A noisy image has far more contour than a budget allows, so the encoder has
	// to coarsen rather than truncate. Truncating would leave a band unpainted and
	// a hole in the picture.
	im, pal := solid(120, 90, func(x, y int) color.RGBA {
		v := uint8((x*7 + y*13 + (x*y)/3) % 256)
		return color.RGBA{v, v / 2, 255 - v, 255}
	})
	loose := encodeOrFail(t, im, pal, Options{MaxNodes: 400000})
	tight := encodeOrFail(t, im, pal, Options{MaxNodes: 1500})
	if tight.Tolerance <= loose.Tolerance {
		t.Errorf("tight budget used tolerance %v, loose used %v: the budget is not being applied",
			tight.Tolerance, loose.Tolerance)
	}
	if tight.Nodes >= loose.Nodes {
		t.Errorf("tight budget emitted %d nodes, loose emitted %d", tight.Nodes, loose.Nodes)
	}
	if tight.Truncated {
		t.Errorf("tight budget still reports truncation at %d nodes: %v", tight.Nodes, tight.Notes)
	}
	// Coarsening must not stop the bands being painted at all.
	if tight.Shapes < loose.Shapes/2 {
		t.Errorf("tight budget emitted %d shapes against %d: bands are being dropped",
			tight.Shapes, loose.Shapes)
	}
}

func TestMinAreaDropsSlivers(t *testing.T) {
	// A one-pixel dot is not worth a path node, and at a realistic MinArea it must
	// disappear entirely.
	im, pal := solid(40, 20, func(x, y int) color.RGBA {
		if x == 20 && y == 10 {
			return color.RGBA{255, 255, 255, 255}
		}
		return color.RGBA{0, 0, 0, 255}
	})
	withDot := encodeOrFail(t, im, pal, Options{MinArea: 0})
	without := encodeOrFail(t, im, pal, Options{MinArea: 4})
	if withDot.Nodes <= without.Nodes {
		t.Errorf("MinArea=0 gave %d nodes, MinArea=4 gave %d: the sliver was not dropped",
			withDot.Nodes, without.Nodes)
	}
}

func TestOutputIsDeterministic(t *testing.T) {
	// Byte-for-byte reproducibility, which is what makes the documents diffable
	// and the benchmarks comparable. Anything order-dependent, such as iterating a
	// map, shows up here.
	im, pal := solid(60, 40, func(x, y int) color.RGBA {
		v := uint8((x*3 + y*5) % 256)
		return color.RGBA{v, v, 255 - v, 255}
	})
	first := encodeOrFail(t, im, pal, Options{}).Doc.Encode()
	for i := 0; i < 5; i++ {
		again := encodeOrFail(t, im, pal, Options{}).Doc.Encode()
		if string(first) != string(again) {
			t.Fatalf("run %d differs from the first", i)
		}
	}
}

func TestCoordinatesAreRoundedToTheEmittedPrecision(t *testing.T) {
	// Rounding is not cosmetic: at two decimals a half-pixel contour is exact, and
	// without it a value like 3.4999999999999996 is written out in full.
	im, pal := solid(16, 16, func(x, y int) color.RGBA {
		if x >= 4 && x < 12 && y >= 4 && y < 12 {
			return color.RGBA{255, 255, 255, 255}
		}
		return color.RGBA{0, 0, 0, 255}
	})
	res := encodeOrFail(t, im, pal, Options{Precision: 2})
	for _, d := range pathData(t, res.Doc) {
		for _, tok := range strings.FieldsFunc(d, func(r rune) bool {
			return r == 'M' || r == 'L' || r == 'Z' || r == ' '
		}) {
			if dot := strings.IndexByte(tok, '.'); dot >= 0 {
				if frac := len(tok) - dot - 1; frac > 2 {
					t.Errorf("coordinate %q has %d decimals, want at most 2", tok, frac)
				}
			}
			if strings.ContainsAny(tok, "eE") {
				t.Errorf("coordinate %q is in exponent form", tok)
			}
		}
	}
}

func TestSingleColourImageNeedsNoContour(t *testing.T) {
	im, pal := solid(8, 8, func(x, y int) color.RGBA { return color.RGBA{10, 20, 30, 255} })
	res := encodeOrFail(t, im, pal, Options{MaxNodes: 10})
	if res.Shapes != 1 {
		t.Errorf("got %d shapes, want 1 rectangle: %v", res.Shapes, res.Notes)
	}
	if res.Paths != 0 {
		t.Errorf("got %d paths, want none", res.Paths)
	}
	// A fixed node count must not send the budget loop round in circles raising
	// the tolerance it can never satisfy.
	if res.Tolerance != DefaultToleranceForTest {
		t.Errorf("tolerance moved to %v on a single-colour image, want it left at the default",
			res.Tolerance)
	}
}

// DefaultToleranceForTest mirrors the encoder's own starting tolerance, spelled
// out here so a change to the default has to be made deliberately in two places
// rather than silently accepted.
const DefaultToleranceForTest = 0.25

func TestOnePixelImage(t *testing.T) {
	m := image.NewRGBA(image.Rect(0, 0, 1, 1))
	m.Set(0, 0, color.RGBA{1, 2, 3, 255})
	im := pixelbuf.FromImage(m)
	q := quant.Default()
	pal := quant.Reduce(&quant.Input{W: im.W, H: im.H,
		L: im.LabChannel(0), A: im.LabChannel(1), B: im.LabChannel(2), Alpha: im.Alpha01()}, q)
	res, err := Encode(im, pal, Options{})
	if err != nil {
		t.Fatalf("a 1x1 image must encode, not fail: %v", err)
	}
	if res.Shapes != 1 {
		t.Errorf("got %d shapes, want 1", res.Shapes)
	}
}

func TestRejectsBadInput(t *testing.T) {
	if _, err := Encode(nil, nil, Options{}); err == nil {
		t.Error("nil image and palette must be an error, not a panic")
	}
	im, _ := solid(4, 4, func(x, y int) color.RGBA { return color.RGBA{0, 0, 0, 255} })
	if _, err := Encode(im, nil, Options{}); err == nil {
		t.Error("nil palette must be an error")
	}
}

func TestAlphaLevelSnapping(t *testing.T) {
	// Eight buckets means eight opacities, 0/7 through 7/7, so 0.5 lands on
	// 4/7 rather than on 0.5. Snapping to the nearest bucket is the point: a
	// per-pixel float would be a long number and a false claim of precision.
	for _, c := range []struct {
		in   float64
		want float64
	}{
		{0, 0}, {1, 1}, {0.5, 4.0 / 7}, {-1, 0}, {2, 1}, {0.4, 3.0 / 7},
	} {
		if got := alphaLevel(c.in, 8); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("alphaLevel(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	// A single level means fully opaque, not a division by zero.
	if got := alphaLevel(0.3, 1); got != 1 {
		t.Errorf("alphaLevel(0.3, 1) = %v, want 1", got)
	}
	if got := alphaLevel(0.3, 0); got != 1 {
		t.Errorf("alphaLevel(0.3, 0) = %v, want 1", got)
	}
}

// disc builds a hard-edged disc, the shape curve fitting is supposed to exist
// for. A rasterised circle traced by marching squares is a staircase, which makes
// it the honest test: a fitter that only looks good on synthetic geometry would
// not survive it.
func disc(w, h, r int, f func(x, y int) color.RGBA) (*pixelbuf.Image, *quant.Palette) {
	return solid(w, h, func(x, y int) color.RGBA {
		dx, dy := float64(x)-float64(w)/2, float64(y)-float64(h)/2
		if dx*dx+dy*dy <= float64(r*r) {
			return color.RGBA{20, 20, 20, 255}
		}
		return color.RGBA{245, 245, 245, 255}
	})
}

// Curves must be opt-in. The default is the traced staircase, and a feature that
// cannot be compared against the thing it replaces is not a feature.
func TestCurvesAreOffByDefault(t *testing.T) {
	im, pal := disc(120, 120, 44, nil)
	for _, d := range pathData(t, encodeOrFail(t, im, pal, Options{}).Doc) {
		if strings.Contains(d, "C") {
			t.Errorf("default output contains a curve command: %s", d)
		}
	}
}

// With the option on, the same shape must actually be emitted as curves.
func TestCurvesEmitBezierCommands(t *testing.T) {
	im, pal := disc(120, 120, 44, nil)
	res := encodeOrFail(t, im, pal, Options{Curves: true})
	curves := 0
	for _, d := range pathData(t, res.Doc) {
		curves += strings.Count(d, "C")
	}
	if curves == 0 {
		t.Error("Curves was set but no curve command was emitted")
	}
}

// Every subpath has to be closed. One path element carries several subpaths, so
// the invariant is a moveto per closepath rather than a single moveto per element.
// Path is closed by construction, so emitting the repeated closing point as well
// would open a seam that the renderer has to guess about.
func TestFittedSubpathsAreClosedAndWellFormed(t *testing.T) {
	im, pal := disc(120, 120, 44, nil)
	for _, d := range pathData(t, encodeOrFail(t, im, pal, Options{Curves: true}).Doc) {
		moves, closes := strings.Count(d, "M"), strings.Count(d, "Z")
		if moves == 0 {
			t.Errorf("path data has no moveto: %s", d)
			continue
		}
		if moves != closes {
			t.Errorf("%d subpaths but %d closepath commands: %s", moves, closes, d)
		}
		if !strings.HasSuffix(d, "Z") {
			t.Errorf("subpath is not closed: %s", d)
		}
	}
}

// A cubic puts three coordinate pairs in the file where a line puts one, so it
// has to be charged three nodes. Counting it as one would let the node ceiling
// from decision D6 stop meaning anything the moment curves are enabled.
func TestACubicCostsThreeNodes(t *testing.T) {
	line := fittedNodes(curvefit.Path{Segs: []curvefit.Seg{{Kind: curvefit.Line}}})
	cubic := fittedNodes(curvefit.Path{Segs: []curvefit.Seg{{Kind: curvefit.Cubic}}})
	if line != 1 {
		t.Errorf("a line segment counts as %d nodes, want 1", line)
	}
	if cubic != 3 {
		t.Errorf("a cubic counts as %d nodes, want 3", cubic)
	}
}
