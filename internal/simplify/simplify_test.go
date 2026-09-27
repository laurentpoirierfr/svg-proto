package simplify

import (
	"math"
	"testing"

	"github.com/elfeo/svg-proto/internal/contour"
	"github.com/elfeo/svg-proto/internal/field"
)

// pt is a contour point. Keyed struct literals in every test would bury the
// geometry in punctuation, and go vet rejects the unkeyed form for a type from
// another package.
func pt(x, y float64) contour.Pt { return contour.Pt{X: x, Y: y} }

func closed(pts ...contour.Pt) contour.Loop {
	return append(append(contour.Loop{}, pts...), pts[0])
}

func maxDeviation(got, want contour.Loop) float64 {
	// Worst distance from any point of the simplified polyline to the original,
	// measured both ways, since a chord may cut a corner.
	worst := 0.0
	for _, p := range got[:len(got)-1] {
		worst = math.Max(worst, polylineDistance(p, want))
	}
	for _, p := range want[:len(want)-1] {
		worst = math.Max(worst, polylineDistance(p, got))
	}
	return worst
}

func polylineDistance(p contour.Pt, l contour.Loop) float64 {
	best := math.Inf(1)
	for i := 1; i < len(l); i++ {
		best = math.Min(best, pointSegmentDistance(p, l[i-1], l[i]))
	}
	return best
}

func pointSegmentDistance(p, a, b contour.Pt) float64 {
	dx, dy := b.X-a.X, b.Y-a.Y
	if dx == 0 && dy == 0 {
		return math.Hypot(p.X-a.X, p.Y-a.Y)
	}
	t := ((p.X-a.X)*dx + (p.Y-a.Y)*dy) / (dx*dx + dy*dy)
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return math.Hypot(p.X-(a.X+t*dx), p.Y-(a.Y+t*dy))
}

func TestZeroToleranceIsLossless(t *testing.T) {
	l := closed(pt(0, 0), pt(1, 0), pt(2, 0.4), pt(3, 2), pt(2, 3), pt(0, 3))
	o := DefaultOptions()
	o.Tolerance = 0
	got := Loop(l, nil, o)
	if len(got) != len(l) {
		t.Errorf("got %d points, want %d: zero tolerance must change nothing", len(got), len(l))
	}
}

func TestCollinearPointsAreDroppedExactly(t *testing.T) {
	// A run of collinear points carries no information, and removing them is
	// exact, so it must happen even with a tolerance that would otherwise keep
	// them.
	l := closed(
		pt(0, 0), pt(1, 0), pt(2, 0), pt(3, 0),
		pt(4, 0), pt(4, 4), pt(0, 4),
	)
	o := DefaultOptions()
	o.Tolerance = 1e-9
	got := Loop(l, nil, o)
	want := closed(pt(0, 0), pt(4, 0), pt(4, 4), pt(0, 4))
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("point %d = %v, want %v", i, got[i], want[i])
		}
	}
	// And the shape is unchanged.
	if d := maxDeviation(got, l); d > 1e-12 {
		t.Errorf("deviation %v, want 0 for a lossless pass", d)
	}
}

func TestResultIsClosedAndKeepsTheFirstPoint(t *testing.T) {
	l := closed(pt(0, 0), pt(10, 0), pt(10, 10), pt(0, 10))
	got := Loop(l, nil, DefaultOptions())
	if got[0] != got[len(got)-1] {
		t.Errorf("result is not closed: first %v, last %v", got[0], got[len(got)-1])
	}
	if got[0] != l[0] {
		t.Errorf("first point moved from %v to %v", l[0], got[0])
	}
}

func TestDeviationStaysInsideTheWeightedBudget(t *testing.T) {
	// The real property, checked over a range of tolerances: a point is kept
	// whenever the error of dropping it would exceed its own budget. A list of
	// hand-computed expectations would only pin the cases somebody thought of.
	ring := []contour.Pt{pt(0, 0), pt(5, 0.2), pt(10, 0), pt(12, 4), pt(10, 8), pt(12, 12), pt(5, 11.8), pt(0, 12), pt(-2, 8), pt(-2, 4)}
	l := closed(ring...)
	grads := make([]float64, len(l))
	for i := range grads {
		// A gradient that varies around the ring, so the weighting is actually
		// exercised: half the ring is a hard edge, half is flat.
		if i < len(l)/2 {
			grads[i] = 5
		} else {
			grads[i] = 0.001
		}
	}
	for _, tol := range []float64{0.05, 0.1, 0.25, 0.5, 1, 2} {
		o := DefaultOptions()
		o.Tolerance = tol
		o.Reference = 0.05
		got := Loop(l, grads, o)
		// The worst deviation over all points, relative to the largest budget in
		// play, must be inside the tolerance. Budgets here span
		// tol*0.05/(0.05+5) to tol*0.05/(0.05+0.001), i.e. about 1/101 to 49/51
		// of tol, so the global check is against tol.
		if d := maxDeviation(got, l); d > tol {
			t.Errorf("tolerance %v: worst deviation %v exceeds it", tol, d)
		}
		if len(got) > len(l) {
			t.Errorf("tolerance %v: grew the loop from %d to %d points", tol, len(l), len(got))
		}
	}
}

func TestSteepGradientKeepsMorePointsThanFlatGround(t *testing.T) {
	// The whole point of the weighting. Same geometry, same tolerance, gradient
	// only on one side of the ring: the steep side must survive largely intact
	// while the flat side collapses.
	ring := []contour.Pt{pt(0, 0), pt(4, 0.3), pt(8, 0), pt(12, 0.3), pt(16, 0), pt(16, 6), pt(12, 6.3), pt(8, 6), pt(4, 6.3), pt(0, 6)}
	l := closed(ring...)
	n := len(l)
	steep := make([]float64, n)
	flat := make([]float64, n)
	for i := 0; i < n; i++ {
		// First third steep, rest flat.
		if i < n/3 {
			steep[i], flat[i] = 20, 0
		} else {
			steep[i], flat[i] = 0, 0
		}
	}
	o := DefaultOptions()
	o.Tolerance = 0.3
	o.Reference = 0.05
	byPoint := Loop(l, steep, o)
	uniform := Loop(l, flat, o)
	if len(byPoint) <= len(uniform) {
		t.Errorf("weighted %d points, uniform %d: the steep half should be kept, so weighted must be larger",
			len(byPoint), len(uniform))
	}
	// The steep run must be reproduced nearly point for point.
	kept := 0
	for _, p := range l[:n/3] {
		for _, q := range byPoint {
			if q == p {
				kept++
				break
			}
		}
	}
	if kept < n/3-1 {
		t.Errorf("kept only %d of %d steep points", kept, n/3)
	}
}

func TestWeightsComeFromTheFieldGradient(t *testing.T) {
	f := field.New(5, 5)
	for i := range f.V {
		f.V[i] = 0
	}
	// A step in the middle column: a hard vertical edge.
	for y := 0; y < 5; y++ {
		f.V[y*5+3] = 1
	}
	l := closed(pt(2.5, 0), pt(2.5, 2), pt(2.5, 4))
	mag := field.GradientMagnitudeOf(f)
	w := Weights(mag, l)
	if len(w) != len(l) {
		t.Fatalf("got %d weights for %d points", len(w), len(l))
	}
	// Every sample lies on the step, so every gradient must be large, not zero.
	for i, g := range w {
		if g <= 0.01 {
			t.Errorf("point %d on a step edge has weight %v, want a large gradient", i, g)
		}
	}
	// A uniform field has no gradient anywhere, so every weight is 0, and budget
	// turns 0 into the full tolerance. The weight is a gradient, not an
	// allowance, and conflating the two is the easy mistake here.
	flat := field.New(5, 5)
	for i := range flat.V {
		flat.V[i] = 0.5
	}
	wf := Weights(field.GradientMagnitudeOf(flat), l)
	for i, g := range wf {
		if g != 0 {
			t.Errorf("point %d on a flat field has gradient %v, want 0", i, g)
		}
		if b := budget(g, 0.25, 0.05); math.Abs(b-0.25) > 1e-12 {
			t.Errorf("point %d: budget %v, want the full tolerance 0.25", i, b)
		}
	}
	// A nil field is not a crash, it is the same as flat: no gradient, full budget.
	wn := Weights(nil, l)
	for i, g := range wn {
		if g != 0 {
			t.Errorf("point %d with no field has gradient %v, want 0", i, g)
		}
	}
}

func TestBudgetIsMonotonicAndBounded(t *testing.T) {
	prev := math.Inf(1)
	for _, g := range []float64{0, 0.01, 0.1, 1, 10, 1000} {
		b := budget(g, 0.25, 0.05)
		if b > 0.25 {
			t.Errorf("gradient %v: budget %v exceeds the tolerance", g, b)
		}
		if b > prev {
			t.Errorf("gradient %v: budget %v rose from %v, want monotonic decrease", g, b, prev)
		}
		prev = b
	}
	if b := budget(0, 0.25, 0.05); math.Abs(b-0.25) > 1e-12 {
		t.Errorf("flat ground budget = %v, want the full tolerance 0.25", b)
	}
	if b := budget(1e9, 0.25, 0); b <= 0 {
		t.Errorf("a zero reference must not divide by zero, got %v", b)
	}
}

func TestDegenerateInputsSurvive(t *testing.T) {
	// A tracer can hand over a sliver, a spike or a two-point loop. Simplify must
	// not index past the end or invent geometry.
	cases := []contour.Loop{
		closed(pt(0, 0), pt(1, 0)),
		closed(pt(0, 0), pt(1, 0), pt(0, 1)),
		closed(pt(0, 0), pt(0, 0), pt(0, 0), pt(0, 0)),
		closed(pt(0, 0), pt(10, 0), pt(0, 0.001), pt(10, 0.001)),
	}
	for i, l := range cases {
		got := Loop(l, nil, DefaultOptions())
		if len(got) == 0 {
			t.Errorf("case %d: result is empty", i)
			continue
		}
		if got[0] != got[len(got)-1] {
			t.Errorf("case %d: result is not closed: %v", i, got)
		}
		if d := maxDeviation(got, l); d > DefaultOptions().Tolerance*50 {
			t.Errorf("case %d: deviation %v is absurd for input %v", i, d, l)
		}
	}
}

func TestMismatchedWeightSliceFallsBackToUniform(t *testing.T) {
	// A length mismatch must not read past the end of the weights; it degrades
	// to a uniform budget, which is what a nil slice already means.
	l := closed(pt(0, 0), pt(4, 0.5), pt(8, 0), pt(8, 8), pt(0, 8))
	got := Loop(l, []float64{1, 2}, DefaultOptions())
	if len(got) < 4 {
		t.Errorf("got %d points, want at least 4", len(got))
	}
	if d := maxDeviation(got, l); d > DefaultOptions().Tolerance*50 {
		t.Errorf("deviation %v", d)
	}
}
