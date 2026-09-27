// Package simplify reduces a traced contour to the smallest polyline that still
// stays within a tolerance of it.
//
// The tolerance is not uniform. A contour point sitting on a soft gradient can
// be moved a long way before the rendered pixels change, while a point on a hard
// edge cannot be moved at all without a visible shift. So each point carries a
// weight derived from the gradient magnitude of the field it was traced from,
// and the simplification error is measured against that per-point budget.
//
// The goal is the node budget from decision D6: not the fewest bytes, but the
// fewest path nodes for a given fidelity.
package simplify

import (
	"math"

	"github.com/elfeo/svg-proto/internal/contour"
	"github.com/elfeo/svg-proto/internal/field"
)

// Options configures simplification.
type Options struct {
	// Tolerance is the largest deviation, in pixels, allowed for a point whose
	// weight is the reference gradient. Points on a steeper gradient get a
	// proportionally smaller budget.
	//
	// Zero keeps every point, so simplification is a no-op and the result is
	// still exact.
	Tolerance float64

	// Reference is the gradient magnitude, in field units per pixel, that is
	// considered "average". It scales the per-point budget: the effective
	// tolerance is Tolerance * Reference / (Reference + gradient), so it tends
	// to Tolerance on flat ground and to zero on a hard edge. Zero is treated as
	// a sensible default rather than dividing by zero.
	Reference float64

	// MinPoints is the fewest points a closed loop may keep, including the
	// repeated closing point. Below three distinct points a loop encloses no
	// area and is not worth emitting. Values below four are raised to four.
	MinPoints int
}

// DefaultOptions returns settings suited to a field normalised to [0,1]: a
// quarter-pixel budget on average ground, and a half-pixel floor on the loop
// size.
func DefaultOptions() Options {
	return Options{Tolerance: 0.25, Reference: 0.05, MinPoints: 4}
}

// Weights returns the gradient magnitude at each point of l, one entry per point
// and in the same order. These are raw gradients, not error budgets: budget turns
// them into a deviation allowance, so a large gradient here means a small
// allowance there.
//
// mag is a gradient-magnitude field, normally field.GradientMagnitudeOf(f), and it
// is a parameter rather than computed here on purpose. Computing it per contour
// is O(pixels) for a quantity that does not depend on the contour, and a
// photographic image produces thousands of contours per band, so it turns a
// linear trace into a quadratic one. The caller computes it once per document.
//
// A nil field, or a point outside the field, yields 0, which budget maps to the
// full tolerance. That is the aggressive end, and it is the right default for
// "no information": a caller without a field has said nothing about where the
// edges are.
//
// Points exactly on a grid node take that node's gradient, and everything else is
// bilinearly interpolated, so a contour running along a node row does not see the
// weight jump as it crosses cell boundaries.
func Weights(mag *field.Field, l contour.Loop) []float64 {
	w := make([]float64, len(l))
	if mag == nil {
		return w
	}
	for i, p := range l {
		w[i] = sampleBilinear(mag, p.X, p.Y)
	}
	return w
}

const defaultReference = 0.05

// sampleBilinear reads mag at a possibly fractional position, clamping to the
// field. Returns 0 outside, which the caller turns into a tight budget: a point
// off the edge of the field came from the padding ring, where the field is
// constant and the contour position is an artefact anyway.
func sampleBilinear(mag *field.Field, x, y float64) float64 {
	if mag.W == 0 || mag.H == 0 {
		return 0
	}
	// Bilinear over the four surrounding nodes, with the fractional part
	// clamped at the border so a point on the last row or column stays on the
	// field instead of extrapolating.
	fx, fy := x, y
	if fx < 0 {
		fx = 0
	}
	if fy < 0 {
		fy = 0
	}
	if fx > float64(mag.W-1) {
		fx = float64(mag.W - 1)
	}
	if fy > float64(mag.H-1) {
		fy = float64(mag.H - 1)
	}
	x0 := int(math.Floor(fx))
	y0 := int(math.Floor(fy))
	x1, y1 := x0+1, y0+1
	if x1 > mag.W-1 {
		x1 = mag.W - 1
	}
	if y1 > mag.H-1 {
		y1 = mag.H - 1
	}
	tx, ty := fx-float64(x0), fy-float64(y0)
	top := mag.At(x0, y0)*(1-tx) + mag.At(x1, y0)*tx
	bot := mag.At(x0, y1)*(1-tx) + mag.At(x1, y1)*tx
	return top*(1-ty) + bot*ty
}

// budget turns a gradient magnitude into the deviation allowed at that point.
//
//	budget = Tolerance * Reference / (Reference + gradient)
//
// which is monotonic in the gradient and bounded by Tolerance, so a hard edge
// keeps its points and flat ground does not. The +Reference in the denominator
// keeps it finite at a gradient of zero and avoids a division per point.
func budget(grad, tol, reference float64) float64 {
	if tol <= 0 {
		return 0
	}
	if reference <= 0 {
		reference = defaultReference
	}
	return tol * reference / (reference + grad)
}

// Loop returns l with redundant points removed.
//
// l must be a closed loop as produced by contour.Trace, with its first point
// repeated as the last. The result has the same shape: a slice of distinct
// points followed by the repeated first one. If the loop cannot be reduced
// without going below MinPoints it is returned unchanged.
func Loop(l contour.Loop, w []float64, o Options) contour.Loop {
	if len(l) < 4 {
		return l
	}
	if o.MinPoints < 4 {
		o.MinPoints = 4
	}
	if o.Tolerance <= 0 {
		return l
	}
	reference := o.Reference
	if reference <= 0 {
		reference = defaultReference
	}
	// A weight per point, or nothing. A slice of the wrong length is a caller bug,
	// and indexing it anyway panics deep inside the recursion where the cause is
	// invisible; degrading to a uniform budget is both safe and the obvious
	// meaning of "no weights".
	if len(w) != len(l) {
		w = nil
	}

	// Work on the open polyline, then restore the closing point, so the
	// collinearity pass and the Douglas-Peucker recursion each see a plain
	// sequence and cannot get confused by the repeated endpoint.
	n := len(l) - 1
	pts := make([]sample, n)
	for i := 0; i < n; i++ {
		var g float64
		if w != nil {
			g = w[i]
		}
		pts[i] = sample{p: l[i], grad: g}
	}

	// The weight travels with the point. Carrying it in a parallel slice indexed
	// by the original positions looks simpler and is wrong: dropCollinear
	// removes points, so every later index would be reading a neighbour's
	// gradient, silently applying a hard edge's budget to flat ground.
	lossless := dropCollinear(pts)
	pts = douglasPeucker(lossless, o.Tolerance, reference)

	// Too few to be worth emitting: fall back to the lossless collinear-only
	// result, not to the original. Falling all the way back would hand back the
	// un-simplified loop, which is strictly worse than what we already had, and
	// it would do so exactly when the contour is most compressible. A loop with
	// fewer than three distinct points encloses no area; dropping it is
	// contour's job via MinArea, not inventing a degenerate triangle here.
	if len(pts) < o.MinPoints-1 {
		pts = lossless
	}
	if len(pts) == 0 {
		return l
	}
	out := make(contour.Loop, 0, len(pts)+1)
	for _, s := range pts {
		out = append(out, s.p)
	}
	return append(out, out[0])
}

// sample is a contour point together with the gradient it was traced on, kept
// paired so that removing points cannot misalign the two.
type sample struct {
	p    contour.Pt
	grad float64
}

// dropCollinear removes points that lie exactly on the segment joining their
// neighbours, with a repeated point treated as collinear.
//
// This is lossless, unlike Douglas-Peucker, and on a marching-squares contour it
// is most of the win: long straight runs of a hard-edged region collapse to
// their two endpoints, and those two endpoints are what the path actually needs.
func dropCollinear(pts []sample) []sample {
	n := len(pts)
	if n < 3 {
		return pts
	}
	// One pass over the original neighbours, not over a progressively filtered
	// list. Filtering as it goes looks tidier but needs an accumulator, and the
	// accumulator is empty on the first iteration; it also makes each decision
	// depend on decisions not yet taken, so a run of collinear points can
	// collapse one end at a time and leave one behind.
	out := make([]sample, 0, n)
	for i := 0; i < n; i++ {
		prev := pts[(i+n-1)%n].p
		cur := pts[i].p
		next := pts[(i+1)%n].p
		if cur == prev {
			continue // exact duplicate
		}
		if collinear(prev, cur, next) {
			continue
		}
		out = append(out, pts[i])
	}
	// Everything was collinear, so there is no shape here to keep. Returning the
	// input is the honest answer; the caller falls back to it too.
	if len(out) < 3 {
		return pts
	}
	return out
}

func collinear(a, b, c contour.Pt) bool {
	return (b.X-a.X)*(c.Y-a.Y)-(b.Y-a.Y)*(c.X-a.X) == 0
}

// douglasPeucker returns the subset of pts within the weighted tolerance.
//
// The recursion is done with an explicit stack rather than by recursing, because
// a contour can have hundreds of thousands of points and a degenerate spiral
// would otherwise put the call stack under the runtime's grow limit.
func douglasPeucker(pts []sample, tol, reference float64) []sample {
	n := len(pts)
	if n < 3 {
		return pts
	}
	keep := make([]bool, n)
	keep[0] = true
	keep[n-1] = true

	type span struct{ lo, hi int }
	stack := []span{{0, n - 1}}
	for len(stack) > 0 {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if s.hi-s.lo < 2 {
			continue
		}
		// Furthest point from the chord, in units of its own budget. Normalising
		// per point is what makes the weighting meaningful: it asks "would this
		// point's own error budget be exceeded", not "is this a long way from the
		// chord in absolute terms".
		worst, worstIdx := -1.0, -1
		lo, hi := s.lo, s.hi
		for i := lo + 1; i < hi; i++ {
			d := math.Abs(perpDistance(pts[i].p, pts[lo].p, pts[hi].p))
			if d == 0 {
				continue
			}
			score := d / budget(pts[i].grad, tol, reference)
			if score > worst {
				worst, worstIdx = score, i
			}
		}
		if worstIdx < 0 || worst <= 1 {
			continue // within budget, drop the whole span
		}
		keep[worstIdx] = true
		stack = append(stack, span{lo, worstIdx}, span{worstIdx, hi})
	}

	out := make([]sample, 0, n)
	for i, s := range pts {
		if keep[i] {
			out = append(out, s)
		}
	}
	if len(out) < 3 {
		return pts
	}
	return out
}

// perpDistance is the distance from p to the infinite line through a and b.
//
// It is used in preference to the distance to the segment, because a contour
// that doubles back would otherwise report a large distance for a point that is
// in fact on the path, and keep points that Douglas-Peucker is meant to remove.
func perpDistance(p, a, b contour.Pt) float64 {
	dx, dy := b.X-a.X, b.Y-a.Y
	if dx == 0 && dy == 0 {
		return math.Hypot(p.X-a.X, p.Y-a.Y)
	}
	return math.Abs(dx*(a.Y-p.Y)-(a.X-p.X)*dy) / math.Hypot(dx, dy)
}
