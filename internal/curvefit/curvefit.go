// Package curvefit replaces the staircase of a traced polyline with Bézier
// segments, potrace-style.
//
// A marching-squares contour is exact at the sample points and wrong everywhere
// in between: a straight edge comes out as a run of tiny steps, and a curve as a
// polygon inscribed in it. The polygon is inscribed, so it is systematically
// inside the true shape, and the error grows with the sampling step rather than
// shrinking. That is why the pixel-art case, which has no antialiasing to blame
// and where grid reproduces the image exactly, is the worst result in the corpus:
// the staircase is not an approximation there, it is the whole error.
//
// The fit works in three steps, and the third is what keeps it honest:
//
//  1. Split the closed loop into runs of smooth points separated by corners. A
//     corner is a direction change no curve can pass through, so it must stay a
//     join between two segments. Guessing these wrong is the usual way this kind
//     of code rounds a rectangle's corners off, so CornerAngle is an option and
//     its effect is measured rather than assumed.
//
//  2. Fit one cubic to each run, with the endpoints pinned to the polygon's and
//     the tangents estimated from the neighbouring points.
//
//  3. Measure the fit against the polygon it claims to replace, and split the run
//     where it is worst if it is worse than Tolerance. This is the step that makes
//     the result trustworthy: no segment is ever emitted because it looked smooth,
//     only because it was checked.
package curvefit

import (
	"math"

	"github.com/elfeo/svg-proto/internal/contour"
	"github.com/elfeo/svg-proto/internal/polygon"
)

// Kind distinguishes the two segment shapes. A straight edge stays a line even
// though a cubic could express it, because a cubic needs two extra coordinate
// pairs and a flat edge needs none of them: the cheapest correct encoding of a
// straight line is a line.
type Kind uint8

const (
	Line Kind = iota
	Cubic
)

// Seg is one path segment. P0 and P3 are the endpoints; C1 and C2 carry the
// control points and are only meaningful for Cubic. For a Line, P3 is the other
// end and the control points are left zero so a zero value cannot be mistaken for
// a deliberate control point.
type Seg struct {
	Kind           Kind
	P0, C1, C2, P3 contour.Pt
}

// Path is a fitted contour. It is always closed: the last segment ends where the
// first begins.
type Path struct {
	Segs []Seg
}

// Options configures the fit.
type Options struct {
	// Tolerance is the largest distance the fitted curve may sit from the polygon
	// it replaces, in the same units as the contour. It is not a smoothing
	// amount: a smaller value fits harder and emits more segments. Zero means
	// fall back to DefaultTolerance, because a tolerance of exactly zero cannot be
	// met by any curve through a staircase and would recurse forever.
	Tolerance float64
	// CornerAngle is the turn, in degrees, above which a vertex is treated as a
	// corner and no curve is allowed through it. Low values protect more corners
	// and leave more polylines; high values smooth more and round off more.
	CornerAngle float64
	// MaxDepth stops the subdivision so a pathological run cannot recurse without
	// bound. A run of n points cannot usefully need more than about n segments.
	MaxDepth int
}

// DefaultTolerance is half a pixel. A marching-squares contour is accurate to
// about half a sample spacing, so asking the curve to stay within the same
// distance of the polygon means the curve is as close to the traced shape as the
// trace was to the image.
const DefaultTolerance = 0.5

// DefaultCornerAngle is 80 degrees. A right angle, the commonest corner in UI
// artwork, is 90, so it is protected with a little margin; and a curve sampled
// finely enough to be worth replacing a staircase turns by far less than this
// between neighbouring points.
//
// The margin is on the correct side, which is easy to get backwards. The test
// compares a cosine, and cosine is decreasing, so a threshold *above* 90 degrees
// would demand a turn sharper than a right angle and let right angles through as
// smooth. At 100 degrees a square has no corners at all: the fit still came back
// as four lines, but only because the corner was never known and the fitter ran
// out of depth subdividing around it. The shape survived by accident, and a
// deeper budget would have rounded the rectangle into a lozenge. The threshold
// sits below 90 so a right angle is recognised as a corner on purpose.
const DefaultCornerAngle = 80.0

// DefaultMaxDepth bounds recursion for a run that no subdivision can satisfy.
const DefaultMaxDepth = 10

// DefaultOptions returns the settings the CLI uses.
func DefaultOptions() Options {
	return Options{Tolerance: DefaultTolerance, CornerAngle: DefaultCornerAngle, MaxDepth: DefaultMaxDepth}
}

func (o Options) withDefaults() Options {
	if o.Tolerance <= 0 {
		o.Tolerance = DefaultTolerance
	}
	if o.CornerAngle <= 0 {
		o.CornerAngle = DefaultCornerAngle
	}
	if o.MaxDepth <= 0 {
		o.MaxDepth = DefaultMaxDepth
	}
	return o
}

// Fit replaces a closed loop's staircase with fitted segments. The result always
// passes through the original loop's points at every corner and at the ends of
// every run, so a loop that is already exact comes back as lines and costs
// nothing.
func Fit(l contour.Loop, o Options) Path {
	o = o.withDefaults()
	approx := polygon.Approximate(l, polygon.Options{CornerAngle: o.CornerAngle})
	l = approx.Loop
	n := len(l)
	if n < 2 {
		return Path{}
	}

	corners := approx.Corners
	if len(corners) == 0 {
		// A closed loop with no corner anywhere is a circle, or noise. Treating
		// every vertex as smooth means one run spanning the whole loop, which is a
		// run whose end is its own start: a single cubic cannot represent that,
		// because its chord would be zero. The run is marked closed and the fitter
		// cuts it rather than fitting it whole.
		corners = []int{0}
	}

	var out Path
	for ci := 0; ci < len(corners); ci++ {
		start := corners[ci]
		end := corners[(ci+1)%len(corners)]
		// A run is closed only when there is exactly one corner, so that the single
		// pair is (0,0) and the run wraps all the way back to its own start.
		closed := len(corners) == 1 && start == end
		run := runBetween(l, start, end, n)
		out.Segs = append(out.Segs, fitRun(l, run, closed, o)...)
	}
	return Path{Segs: out.Segs}
}

// runBetween returns the indices of the run from start to end inclusive, walking
// forward around the loop.
//
// Inclusive at both ends is not a detail. A run spans the edge between two
// corners, so it owns both of them; excluding the end point silently deletes that
// edge, and a polygon that is nothing but corners — a rectangle — comes back with
// no segments at all. Neighbouring runs then share their common corner, which is
// exactly what chaining needs.
//
// A run whose start is its own end is the whole loop, so the walk is bounded by
// the point count rather than by reaching the end: a plain "until we reach end"
// loop would exit immediately on that case and report an empty run for every
// circle.
func runBetween(l contour.Loop, start, end, n int) []int {
	run := make([]int, 0, n+1)
	for i := start; len(run) <= n; i = (i + 1) % n {
		run = append(run, i)
		// Not on the first step: a closed run has start == end, and breaking
		// there would hand back a single point and erase the whole loop.
		if i == end && len(run) > 1 {
			break
		}
	}
	return run
}

// fitRun fits one run of smooth points, subdividing where the fit is worst.
//
// A closed run cannot be fitted whole, because its first and last point are the
// same point and the chord is zero. It is cut in half instead, which is also the
// only place a straight cut is acceptable: both halves are then ordinary open
// runs that the tolerance check governs like any other.
func fitRun(l contour.Loop, run []int, closed bool, o Options) []Seg {
	if len(run) < 2 {
		return nil
	}
	if len(run) == 2 {
		return []Seg{line(l[run[0]], l[run[1]])}
	}
	if closed {
		half := len(run) / 2
		inner := o
		inner.MaxDepth = o.MaxDepth - 1
		return append(fitRun(l, run[:half+1], false, inner), fitRun(l, run[half:], false, inner)...)
	}

	seg, err := fitCubic(l, run)
	if err <= o.Tolerance {
		return []Seg{seg}
	}
	if o.MaxDepth <= 1 {
		// Out of subdivisions and still over tolerance. The best cubic found is
		// kept rather than degraded to a line between the endpoints: the cubic
		// passes through those same two points with interior control points, so a
		// line is strictly worse geometrically and would break the tolerance the
		// caller asked for. Over-tolerance is surfaced by the path data, which is
		// better than hiding it behind a flatter curve.
		return []Seg{seg}
	}
	// Split at the point furthest from the fitted curve, which is where a
	// recursive fitter has the most to gain.
	half := worstSplit(l, run, seg)
	if half <= 0 || half >= len(run)-1 {
		return []Seg{seg}
	}
	inner := o
	inner.MaxDepth = o.MaxDepth - 1
	left := fitRun(l, run[:half+1], false, inner)
	right := fitRun(l, run[half:], false, inner)
	return append(left, right...)
}

// line is a straight segment between two points.
func line(a, b contour.Pt) Seg {
	return Seg{Kind: Line, P0: a, P3: b}
}

// fitCubic returns the single cubic that best approximates the run, and the
// largest distance from the run's points to that curve.
//
// The endpoints are pinned. The tangents come from the points just outside the
// run, which is what makes the curve join its neighbour smoothly instead of
// arriving at an angle. The control-point distance is not solved for: a handful
// of candidate distances are tried and the one with the smallest error wins,
// which is more robust than a closed-form fit that goes unstable when the run is
// nearly straight, and costs nothing at this scale.
func fitCubic(l contour.Loop, run []int) (Seg, float64) {
	n := len(l)
	first, last := run[0], run[len(run)-1]
	p0, p3 := l[first], l[last]

	// Tangent seeds from the neighbours just outside the run.
	before := l[(first-1+n)%n]
	after := l[(last+1)%n]
	t1 := unit(p0.Sub(before))
	t2 := unit(after.Sub(p3))
	if t1 == (contour.Pt{}) {
		t1 = unit(l[run[1]].Sub(p0))
	}
	if t2 == (contour.Pt{}) {
		t2 = unit(p3.Sub(l[run[len(run)-2]]))
	}
	if t1 == (contour.Pt{}) {
		t1 = unit(p3.Sub(p0))
	}
	if t2 == (contour.Pt{}) {
		t2 = t1
	}

	chord := p0.Dist(p3)
	// Candidate control distances, as multiples of the chord. Below about a third
	// the curve is pulled taut and bulges; above about one chord it loops.
	//
	// C1 is p0 plus the incoming tangent, but C2 is p3 minus the outgoing one.
	// The asymmetry is not a typo: a cubic's derivative at its end is p3 minus C2,
	// so adding the tangent there would make the curve arrive travelling backwards
	// against its own direction. That fit is bad enough to fail the tolerance on
	// any curve at all, which is what drives the fitter into subdividing until it
	// degenerates into straight lines.
	best := Seg{Kind: Cubic, P0: p0, P3: p3, C1: p0.Add(t1.Mul(chord / 3)), C2: p3.Sub(t2.Mul(chord / 3))}
	bestErr := maxPointToCurve(runPoints(l, run), best)
	for _, a := range []float64{0.15, 0.25, 0.5, 0.75, 1.0} {
		cand := Seg{Kind: Cubic, P0: p0, P3: p3, C1: p0.Add(t1.Mul(chord * a)), C2: p3.Sub(t2.Mul(chord * a))}
		if e := maxPointToCurve(runPoints(l, run), cand); e < bestErr {
			best, bestErr = cand, e
		}
	}
	return best, bestErr
}

// runPoints materialises a run's coordinates.
func runPoints(l contour.Loop, run []int) []contour.Pt {
	pts := make([]contour.Pt, len(run))
	for i, idx := range run {
		pts[i] = l[idx]
	}
	return pts
}

// worstSplit returns the index within the run of the point furthest from the
// segment. That is where a recursive fitter has the most to gain: cutting
// elsewhere leaves the worst-fitting stretch attached to one half and it comes
// back on the next pass. The result is clamped into the interior, because a split
// at either end would not reduce the run and the recursion would not terminate.
func worstSplit(l contour.Loop, run []int, seg Seg) int {
	pts := runPoints(l, run)
	samples := curveSamples(len(pts))
	worst, at := -1.0, len(pts)/2
	for i, p := range pts {
		if d := distToCurve(p, seg, samples); d > worst {
			worst, at = d, i
		}
	}
	if at <= 0 {
		at = 1
	}
	if at >= len(pts)-1 {
		at = len(pts) - 2
	}
	return at
}

// maxPointToCurve is the largest distance from any of the run's points to the
// fitted curve. This is the tolerance contract: the curve has to pass within
// Tolerance of every point it was fitted from.
//
// The direction is the whole point, and getting it backwards is silently
// catastrophic. Measuring curve-to-points instead reports the polygon's own
// sampling density as fitting error: the midpoint of an edge with no vertex
// nearby sits half an edge away from the nearest point, no matter how closely
// the curve tracks it. A 12-point semicircle then reads as badly fitted however
// perfect the fit is, so the fitter subdivides until it runs out of depth and
// returns nothing but straight lines, which is the curve fitter degenerating
// into the polygon it was meant to replace.
func maxPointToCurve(pts []contour.Pt, seg Seg) float64 {
	worst := 0.0
	for _, p := range pts {
		if d := distToCurve(p, seg, curveSamples(len(pts))); d > worst {
			worst = d
		}
	}
	return worst
}

// distToCurve is the smallest distance from p to a sample of the curve.
func distToCurve(p contour.Pt, seg Seg, samples int) float64 {
	best := math.Inf(1)
	for i := 0; i <= samples; i++ {
		if d := evalCubic(seg, float64(i)/float64(samples)).Dist(p); d < best {
			best = d
		}
	}
	return best
}

// curveSamples sets the resolution at which curves are evaluated. It scales with
// the run so a long run is not measured coarsely, and never drops low enough for
// a curve to slip between samples and hide its own error.
func curveSamples(n int) int {
	if s := 8 * n; s > 32 {
		return s
	}
	return 32
}

// evalCubic is the Bernstein form at parameter t.
func evalCubic(s Seg, t float64) contour.Pt {
	mt := 1 - t
	a := mt * mt * mt
	b := 3 * mt * mt * t
	c := 3 * mt * t * t
	d := t * t * t
	return contour.Pt{
		X: a*s.P0.X + b*s.C1.X + c*s.C2.X + d*s.P3.X,
		Y: a*s.P0.Y + b*s.C1.Y + c*s.C2.Y + d*s.P3.Y,
	}
}

// unit returns v scaled to length 1, or the zero point if v has no length.
func unit(v contour.Pt) contour.Pt {
	l := math.Hypot(v.X, v.Y)
	if l == 0 {
		return contour.Pt{}
	}
	return contour.Pt{X: v.X / l, Y: v.Y / l}
}
