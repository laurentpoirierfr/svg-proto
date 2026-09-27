// Package encode turns a quantised image into a contour-traced SVG document.
//
// The pipeline is: quantise, build a scalar lightness field from the palette,
// extract the iso-line between each pair of adjacent palette levels, simplify
// each contour, and stack the resulting paths. No diffusion is applied, because
// the field is already piecewise constant over palette colours and blurring it
// would move every region boundary away from where the quantiser put it.
//
// Stacking works because the regions are nested. With palette levels
// l[0] < l[1] < ... < l[k-1] and thresholds t[i] = (l[i]+l[i+1])/2, the region
// R[i] = {field >= t[i]} is everything at level l[i+1] or above, so
// R[0] contains R[1] contains ... . Painting from the brightest band downwards,
// each path overwrites the bands outside it, and the last path plus the base
// rectangle cover the canvas exactly once.
package encode

import (
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/elfeo/svg-proto/internal/colorconv"
	"github.com/elfeo/svg-proto/internal/contour"
	"github.com/elfeo/svg-proto/internal/curvefit"
	"github.com/elfeo/svg-proto/internal/dom"
	"github.com/elfeo/svg-proto/internal/field"
	"github.com/elfeo/svg-proto/internal/pixelbuf"
	"github.com/elfeo/svg-proto/internal/quant"
	"github.com/elfeo/svg-proto/internal/simplify"
)

// sentinel is the field value given to a pixel that must never be painted, which
// for now means a pixel whose alpha is below AlphaCutoff. It is far below every
// threshold, so such a pixel is excluded from every traced region without needing
// a separate mask threaded through the tracer.
const sentinel = -1e9

// Options configures the tracer.
type Options struct {
	// MaxNodes is the budget of path nodes, the hard constraint from decision
	// D6. Zero means no budget. When the traced document exceeds it, the
	// simplification tolerance is raised and the whole image is retraced, which
	// degrades every contour a little rather than dropping a band and leaving a
	// hole in the picture.
	MaxNodes int

	// Tolerance is the starting simplification tolerance, in pixels. See
	// simplify.Options.
	Tolerance float64

	// AlphaCutoff is the alpha below which a pixel is left unpainted. The default
	// of 0 treats every pixel as opaque.
	AlphaCutoff float64

	// Precision is the number of decimals kept in path coordinates. Half-pixel
	// contours only need two.
	Precision int

	// AlphaLevels is the number of distinct fill opacities, as in the baselines.
	AlphaLevels int

	// MinArea is the smallest region, in square pixels, worth emitting. Slivers
	// below it are dropped: each costs a path node and is invisible.
	MinArea float64

	// Curves replaces the traced staircase with fitted Bezier segments. It is off
	// by default, so the tracer's output is unchanged until a caller asks for it
	// and the default can be compared against the fitted one.
	Curves bool

	// CurveTolerance is the largest distance, in pixels, that a fitted curve may
	// sit from the points it was fitted from. See curvefit.Options.
	CurveTolerance float64

	// CurveDepth bounds the subdivision of a run that CurveTolerance cannot
	// satisfy. See curvefit.Options.
	CurveDepth int

	// CurveSimplifyTolerance is the simplification tolerance used on the curve
	// path, in pixels, and it is deliberately not Tolerance. See traceBand.
	CurveSimplifyTolerance float64
}

// withDefaults fills in the zero values that mean "unset".
func (o Options) withDefaults() Options {
	if o.Tolerance <= 0 {
		o.Tolerance = simplify.DefaultOptions().Tolerance
	}
	if o.Precision <= 0 {
		o.Precision = 2
	}
	if o.MinArea < 0 {
		o.MinArea = 0
	}
	// The curve options are defaulted here rather than left to the caller, and
	// CurveSimplifyTolerance in particular must not be allowed to reach
	// simplify.Loop as a zero: a zero tolerance is not a small tolerance but no
	// tolerance at all, and Douglas-Peucker then returns every traced point, which
	// is the one input the fitter cannot do anything useful with.
	if o.CurveTolerance <= 0 {
		o.CurveTolerance = curvefit.DefaultOptions().Tolerance
	}
	if o.CurveDepth <= 0 {
		o.CurveDepth = curvefit.DefaultOptions().MaxDepth
	}
	if o.CurveSimplifyTolerance <= 0 {
		o.CurveSimplifyTolerance = DefaultCurveSimplifyTolerance
	}
	return o
}

// Result carries the document plus the diagnostics needed to judge it.
type Result struct {
	Doc *dom.Doc
	// Shapes is the number of paint elements emitted.
	Shapes int
	// Paths is the number of <path> elements; Shapes minus Paths is the number of
	// rectangles.
	Paths int
	// Nodes is the total number of path points, which is what MaxNodes bounds.
	Nodes int
	// Loops is the number of contours traced, before simplification dropped any.
	Loops int
	// PaletteLen is the number of palette entries that reached the document.
	PaletteLen int
	// Bands is the number of stacked levels painted.
	Bands int
	// Tolerance is the tolerance actually used, which is larger than the
	// requested one when the node budget forced a retry.
	Tolerance float64
	// Truncated reports that a band could not be painted within the budget.
	Truncated bool
	// Coverage is the fraction of pixels covered by a paint, 0..1. Below 1 means
	// either transparency or a bug.
	Coverage float64
	// Notes are remarks worth surfacing in a report.
	Notes []string
}

// Encode builds the traced document for an already quantised image.
func Encode(im *pixelbuf.Image, pal *quant.Palette, opts Options) (*Result, error) {
	opts = opts.withDefaults()
	if im == nil || pal == nil {
		return nil, fmt.Errorf("encode: nil image or palette")
	}
	if im.W < 1 || im.H < 1 {
		return nil, fmt.Errorf("encode: empty image %dx%d", im.W, im.H)
	}
	res := &Result{Doc: dom.NewDoc(float64(im.W), float64(im.H)), Tolerance: opts.Tolerance}
	if pal.Len() == 0 {
		return nil, fmt.Errorf("encode: empty palette")
	}

	levels := buildField(pal)
	// Palette entries sorted by lightness, deduplicated. Two entries with the same
	// lightness have no threshold between them, so one band would be empty; the
	// second colour is simply unreachable and is dropped from the stack.
	order := make([]int, pal.Len())
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		return levels[order[i]].L < levels[order[j]].L
	})
	uniq := order[:0]
	for i, idx := range order {
		if i > 0 && levels[idx].L == levels[order[i-1]].L {
			continue
		}
		uniq = append(uniq, idx)
	}
	order = uniq

	// One attempt per tolerance. The node count falls faster than the tolerance
	// rises, so this converges in a couple of rounds.
	//
	// Every attempt builds a complete document, and the last one is returned even
	// if it is still over budget, with Truncated set. Returning nothing would be
	// the wrong failure: an over-budget document is a usable document, whereas a
	// nil one is not a document at all, and the caller has no way to tell the
	// difference from a bug.
	tol := opts.Tolerance
	var doc *dom.Doc
	var stats bandStats
	for attempt := 0; attempt < 6; attempt++ {
		doc, stats = buildDoc(im, pal, levels, order, tol, opts)
		if opts.MaxNodes <= 0 || stats.nodes <= opts.MaxNodes {
			break
		}
		if len(order) <= 1 {
			// A single colour: the node count is fixed and no tolerance can help.
			break
		}
		// Aim straight at the budget instead of merely doubling, so a document
		// that is twenty times over does not need several rounds to get close.
		ratio := float64(stats.nodes) / float64(opts.MaxNodes)
		tol *= math.Max(2, math.Sqrt(ratio))
	}

	res.Doc = doc
	res.Shapes = stats.shapes
	res.Paths = stats.paths
	res.Nodes = stats.nodes
	res.Loops = stats.loops
	res.Bands = stats.bands
	res.PaletteLen = len(order)
	res.Tolerance = tol
	res.Notes = stats.notes
	res.Truncated = opts.MaxNodes > 0 && stats.nodes > opts.MaxNodes
	if res.Truncated {
		res.Notes = append(res.Notes, fmt.Sprintf(
			"node budget %d exceeded: %d nodes at tolerance %v", opts.MaxNodes, stats.nodes, tol))
	}
	if stats.coverage > 0 {
		res.Coverage = stats.coverage
	}
	return res, nil
}

type bandStats struct {
	shapes   int
	paths    int
	nodes    int
	loops    int
	bands    int
	coverage float64
	notes    []string
}

// level is one palette entry reduced to what the field needs.
type level struct {
	L       float64
	idx     int
	r, g, b uint8
}

// buildField returns the lightness of each palette entry, normalised to [0,1]
// across the palette. Normalising per palette rather than per image keeps the
// thresholds a property of the palette, so two images sharing a palette share
// their band geometry.
func buildField(pal *quant.Palette) []level {
	ls := make([]float64, pal.Len())
	for i := 0; i < pal.Len(); i++ {
		r, g, b := pal.RGB(i)
		L, _, _ := colorconv.RGBToLabFloat(float64(r), float64(g), float64(b))
		ls[i] = L
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, L := range ls {
		lo = math.Min(lo, L)
		hi = math.Max(hi, L)
	}
	span := hi - lo
	if span <= 0 {
		span = 1
	}
	out := make([]level, pal.Len())
	for i := range out {
		r, g, b := pal.RGB(i)
		out[i] = level{L: (ls[i] - lo) / span, idx: i, r: r, g: g, b: b}
	}
	return out
}

// buildDoc traces every band at the given tolerance and assembles the document.
func buildDoc(im *pixelbuf.Image, pal *quant.Palette, levels []level, order []int,
	tol float64, opts Options) (*dom.Doc, bandStats) {

	var st bandStats
	doc := dom.NewDoc(float64(im.W), float64(im.H))

	// Two fields, and the distinction matters.
	//
	// sel decides which pixels belong to which band, so a pixel too transparent
	// to paint gets the sentinel and falls out of every region.
	//
	// lum is the plain lightness field with no sentinel, and it is what the
	// simplification weights are derived from. Using sel for that would be a
	// silent disaster: the sentinel is 1e9 away from its neighbours, so the
	// gradient at every contour touching transparency would be astronomically
	// large, every error budget would collapse to zero, and nothing would ever
	// simplify. The symptom is a document that ignores its node budget no matter
	// how far the tolerance is raised, which looks like a bug in the budget loop
	// rather than in the weights.
	sel := field.New(im.W, im.H)
	lum := field.New(im.W, im.H)
	alphaSum := make([]float64, pal.Len())
	alphaN := make([]int, pal.Len())
	painted := 0
	for y := 0; y < im.H; y++ {
		for x := 0; x < im.W; x++ {
			i := y*im.W + x
			a := float64(im.A[i]) / 255
			idx := int(pal.Index[i])
			lum.V[i] = levels[idx].L
			// Strictly greater: a cutoff of 0 must still paint a pixel that is
			// merely very transparent, or the default would silently discard every
			// soft edge in the image.
			if a <= opts.AlphaCutoff {
				sel.V[i] = sentinel
				continue
			}
			painted++
			sel.V[i] = levels[idx].L
			alphaSum[idx] += a
			alphaN[idx]++
		}
	}
	if painted == 0 {
		st.notes = append(st.notes, "every pixel is at or below the alpha cutoff; nothing painted")
		return doc, st
	}
	if painted < im.W*im.H {
		st.notes = append(st.notes, fmt.Sprintf("%d of %d pixels at or below the alpha cutoff %v, left unpainted",
			im.W*im.H-painted, im.W*im.H, opts.AlphaCutoff))
	}
	st.coverage = float64(painted) / float64(im.W*im.H)

	// One gradient field for the whole document, shared by every band and every
	// contour in it. See simplify.Weights on why this is a parameter and not an
	// internal computation.
	mag := field.GradientMagnitudeOf(lum)

	// fill renders one band: its colour, and the mean opacity of the pixels it
	// covers. Alpha is not a palette dimension, so it is carried per shape, the
	// same way the baselines carry it.
	fill := func(l level) []dom.Attr {
		attrs := []dom.Attr{dom.A("fill", hexColor(l))}
		if n := alphaN[l.idx]; n > 0 {
			if op := alphaLevel(alphaSum[l.idx]/float64(n), opts.AlphaLevels); op < 1 {
				attrs = append(attrs, dom.AF("fill-opacity", op, 2))
			}
		}
		return attrs
	}

	// The base band: the darkest level, over the whole painted area. As a
	// rectangle when the image is fully opaque, and as a traced path when
	// transparency means the painted area is not the whole frame.
	base := levels[order[0]]
	if painted == im.W*im.H {
		attrs := []dom.Attr{
			dom.AF("x", 0, opts.Precision), dom.AF("y", 0, opts.Precision),
			dom.AF("width", float64(im.W), opts.Precision),
			dom.AF("height", float64(im.H), opts.Precision),
		}
		all := append(attrs, fill(base)...)
		doc.Root.Append(dom.E("rect", all...))
		st.shapes++
	} else {
		d, n, nl := traceBand(sel, mag, baseThreshold, tol, opts)
		st.loops += nl
		if n > 0 {
			fa := append([]dom.Attr{dom.A("d", d), dom.A("fill-rule", "evenodd")}, fill(base)...)
			doc.Root.Append(dom.E("path", fa...))
			st.shapes++
			st.paths++
			st.nodes += n
		}
	}

	// Bands, darkest first.
	//
	// The regions are nested: band i covers everything at level i+1 or above, so
	// band i+1's region is strictly inside band i's. Painting them in ascending
	// order therefore lays a small bright region on top of a larger dim one, and
	// each band ends up visible exactly where it should be.
	//
	// Iterating the other way paints the largest region last, in a dimmer colour,
	// and it buries every band under it. The document still looks like a plausible
	// image and the node count is unchanged, so nothing but the pixel comparison
	// catches it: the colour error came out at 0.32 dE against the raster against
	// 0.066 for the run-length baseline.
	for i := 0; i <= len(order)-2; i++ {
		t := (levels[order[i]].L + levels[order[i+1]].L) / 2
		col := levels[order[i+1]]
		d, n, nl := traceBand(sel, mag, t, tol, opts)
		st.loops += nl
		if n == 0 {
			continue
		}
		fa := append([]dom.Attr{dom.A("d", d), dom.A("fill-rule", "evenodd")}, fill(col)...)
		doc.Root.Append(dom.E("path", fa...))
		st.shapes++
		st.paths++
		st.nodes += n
		st.bands++
	}
	return doc, st
}

// alphaLevel snaps an opacity to one of n buckets. A per-pixel float would be
// both a long number and a false claim of precision.
func alphaLevel(a float64, levels int) float64 {
	if levels <= 1 {
		return 1
	}
	i := int(math.Round(a * float64(levels-1)))
	i = max(0, min(i, levels-1))
	return float64(i) / float64(levels-1)
}

// baseThreshold sits just above the sentinel, so the region it selects is
// exactly the set of painted pixels.
const baseThreshold = sentinel / 2

// traceBand traces, simplifies and serialises every loop of one band. It returns
// the path data and the number of nodes it contains, which is zero when the band
// is empty or everything in it was below MinArea.
// DefaultCurveSimplifyTolerance is the simplification tolerance the curve path
// uses. It is much larger than the line path's, and it is a different quantity
// from the fitting tolerance: this one collapses the tracing staircase into a
// polygon, the fitter then puts the curvature back.
//
// 1.5 rather than 1 because 1 is dominated. Swept over the thirteen corpus cases
// at the corpus operating point, 1.5 removes 10.2x the nodes for a mean score
// cost of +0.0103, where 1 removes 6.6x for +0.0102. The same fidelity, half
// again the saving, so 1 has no reason to exist as a default. Below 1.5 the
// score starts to rise faster than the node count falls; above it, faster still.
const DefaultCurveSimplifyTolerance = 1.5

// traceBand traces one band and writes its path data.
//
// The curve path simplifies without the gradient weights, and that is the whole
// reason it can work. simplify.Weights scales each point's error budget by
// Reference/(Reference+gradient), which on a hard edge divides the tolerance by
// roughly thirty, and the intent is to protect hard edges. But on a boundary that
// is hard everywhere, such as a rasterised disc, the weights protect the tracing
// staircase: measured on a 41x41 disc, the weighted path returns 100 points at
// every tolerance from 0.05 to 3, so the knob does nothing at all and the fitter
// is handed the staircase to fit curves to. Unweighted, the same disc collapses to
// 12 points at a tolerance of 1 and the fitter returns 4 cubics.
//
// The weights stay on the line path, where they do what they were built for. It is
// only when curves are going to carry the shape that the staircase has to come
// off first, because a curve fitted through a staircase is not a curve.
func traceBand(f, mag *field.Field, level, tol float64, opts Options) (d string, nodes int, loops int) {
	traced := contour.Trace(f, contour.Options{Level: level, MinPoints: 4, JoinTolerance: 1e-9})
	so := simplify.Options{Tolerance: tol, Reference: simplify.DefaultOptions().Reference, MinPoints: 4}
	if opts.Curves {
		so.Tolerance = opts.CurveSimplifyTolerance
	}

	var b []byte
	n := 0
	for _, l := range traced {
		if math.Abs(l.Area()) < opts.MinArea {
			continue
		}
		// The weights are per loop, since they are read at each of that loop's
		// points. A field-wide table would be indexed by the wrong positions. They
		// are omitted entirely on the curve path; see traceBand.
		var weights []float64
		if !opts.Curves {
			weights = simplify.Weights(mag, l)
		}
		s := simplify.Loop(l, weights, so)
		if len(s) < 4 {
			continue
		}
		if len(b) > 0 {
			b = append(b, ' ')
		}
		if opts.Curves {
			p := curvefit.Fit(s, curvefit.Options{
				Tolerance: opts.CurveTolerance,
				MaxDepth:  opts.CurveDepth,
			})
			if len(p.Segs) == 0 {
				// Nothing fitted, so nothing to draw. Emitting a bare "M Z" would be
				// a shape with no extent, and counting its points would charge the
				// node budget for a path that draws nothing.
				b = b[:len(b)-1]
				continue
			}
			b = appendFittedSubpath(b, p, opts.Precision)
			n += fittedNodes(p)
		} else {
			b = appendSubpath(b, s, opts.Precision)
			n += len(s) - 1 // the repeated closing point is not a node
		}
	}
	return string(b), n, len(traced)
}

// fittedNodes counts a fitted path's nodes. A straight segment contributes one
// point, but a cubic contributes three, because that is what it actually puts in
// the file. Counting a curve as one node would let the fitter claim a budget win
// it did not take, and the node ceiling from decision D6 would stop meaning
// anything the moment curves are switched on.
func fittedNodes(p curvefit.Path) int {
	n := 0
	for _, s := range p.Segs {
		if s.Kind == curvefit.Cubic {
			n += 3
		} else {
			n++
		}
	}
	return n
}

// appendSubpath writes one closed contour as M p0 L p1 ... L pn Z, dropping the
// repeated closing point that contour.Loop carries.
func appendSubpath(b []byte, l contour.Loop, prec int) []byte {
	b = append(b, 'M')
	b = appendNum(b, l[0].X, prec)
	b = append(b, ' ')
	b = appendNum(b, l[0].Y, prec)
	for i := 1; i < len(l)-1; i++ {
		b = append(b, 'L')
		b = appendNum(b, l[i].X, prec)
		b = append(b, ' ')
		b = appendNum(b, l[i].Y, prec)
	}
	return append(b, 'Z')
}

// appendFittedSubpath writes one fitted path as a closed subpath: M for the
// first point, then L or C per segment, then Z. The repeated closing point that
// contour.Loop carries is not a shape here, because Path is closed by
// construction: the last segment already ends where the first one began, and
// repeating it would open a seam for no reason.
func appendFittedSubpath(b []byte, p curvefit.Path, prec int) []byte {
	b = append(b, 'M')
	b = appendPt(b, p.Segs[0].P0, prec)
	for _, s := range p.Segs {
		if s.Kind == curvefit.Cubic {
			b = append(b, 'C')
			b = appendPt(b, s.C1, prec)
			b = appendPt(b, s.C2, prec)
			b = appendPt(b, s.P3, prec)
			continue
		}
		b = append(b, 'L')
		b = appendPt(b, s.P3, prec)
	}
	return append(b, 'Z')
}

func appendPt(b []byte, p contour.Pt, prec int) []byte {
	b = appendNum(b, p.X, prec)
	b = append(b, ' ')
	b = appendNum(b, p.Y, prec)
	return append(b, ' ')
}

func appendNum(b []byte, v float64, prec int) []byte {
	return strconv.AppendFloat(b, round(v, prec), 'f', prec, 64)
}

// round snaps a coordinate to the emitted precision. Without this a value such as
// 3.4999999999999996 is written in full at two decimals anyway, but the rounding
// also makes equal inputs produce equal strings, which the byte-for-byte
// determinism check relies on.
func round(v float64, prec int) float64 {
	p := math.Pow(10, float64(prec))
	return math.Round(v*p) / p
}

func hexColor(l level) string {
	const hexdigits = "0123456789abcdef"
	var b [7]byte
	b[0] = '#'
	for i, c := range [3]uint8{l.r, l.g, l.b} {
		b[1+i*2] = hexdigits[c>>4]
		b[2+i*2] = hexdigits[c&0xf]
	}
	return string(b[:])
}
