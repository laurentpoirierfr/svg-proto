package curvefit

import (
	"math"
	"testing"

	"github.com/elfeo/svg-proto/internal/contour"
	"github.com/elfeo/svg-proto/internal/polygon"
)

// circle returns a closed loop approximating a circle, the way marching squares
// would: points on the circle, connected by chords, so the polygon is inscribed
// and systematically inside the true shape.
func circle(cx, cy, r float64, n int) contour.Loop {
	l := make(contour.Loop, n)
	for i := 0; i < n; i++ {
		a := 2 * math.Pi * float64(i) / float64(n)
		l[i] = contour.Pt{X: cx + r*math.Cos(a), Y: cy + r*math.Sin(a)}
	}
	return l
}

// square returns a closed loop with four right angles.
func square(x0, y0, x1, y1 float64) contour.Loop {
	return contour.Loop{
		{X: x0, Y: y0}, {X: x1, Y: y0}, {X: x1, Y: y1}, {X: x0, Y: y1}, {X: x0, Y: y0},
	}
}

// pathError is the largest distance from any of the loop's points to the fitted
// path, which is the number the tolerance is about and so the number the tests
// have to check.
//
// The sampling has to be dense enough that the measurement resolves the
// tolerance rather than its own resolution. At 16 samples per segment a quarter
// circle of radius 40 leaves roughly 2px between samples, and a polygon point
// then reads as 2px away from a curve that actually passes within a fraction of
// that. A loose tolerance would then "fail" on a fit that is comfortably inside
// it, which is a confusing way to learn nothing.
func pathError(l contour.Loop, p Path) float64 {
	const samples = 256
	worst := 0.0
	for _, pt := range l {
		best := math.Inf(1)
		for _, s := range p.Segs {
			for i := 0; i <= samples; i++ {
				var q contour.Pt
				if s.Kind == Line {
					q = s.P0.Add(s.P3.Sub(s.P0).Mul(float64(i) / float64(samples)))
				} else {
					q = evalCubic(s, float64(i)/float64(samples))
				}
				if d := q.Dist(pt); d < best {
					best = d
				}
			}
		}
		if best > worst {
			worst = best
		}
	}
	return worst
}

// The tolerance is the contract: a fit that claims to be within Tolerance of the
// polygon must actually be. Checked on a circle, where the polygon is inscribed
// and so a curve is genuinely needed to stay close.
func TestFitStaysWithinTolerance(t *testing.T) {
	for _, n := range []int{8, 16, 32, 64, 128} {
		l := circle(0, 0, 40, n)
		for _, tol := range []float64{0.05, 0.25, 0.5, 1.0} {
			p := Fit(l, Options{Tolerance: tol})
			if e := pathError(l, p); e > tol+1e-9 {
				t.Errorf("circle n=%d tol=%v: fit is %v from the polygon, over tolerance", n, tol, e)
			}
		}
	}
}

// A shape that is already exact must come back exact, and must not be inflated
// into curves it never needed. A square is the case: its edges are straight, so
// curves would be strictly worse in bytes and neutral at best in fidelity.
func TestStraightEdgesStayLines(t *testing.T) {
	l := square(0, 0, 30, 20)
	p := Fit(l, DefaultOptions())
	if len(p.Segs) != 4 {
		t.Fatalf("got %d segments, want 4", len(p.Segs))
	}
	for i, s := range p.Segs {
		if s.Kind != Line {
			t.Errorf("segment %d is a curve; a straight edge should stay a line", i)
		}
	}
	if e := pathError(l, p); e > 1e-9 {
		t.Errorf("error %v on a shape made of exact lines", e)
	}
}

// The four corners of a square must survive as corners. This is the failure mode
// that makes naive smoothing useless: round the corners off and a rectangle
// becomes a lozenge, which is a different object, not a smoother one.
func TestCornersSurviveTheFit(t *testing.T) {
	l := square(0, 0, 30, 20)
	p := Fit(l, DefaultOptions())
	corners := map[contour.Pt]bool{}
	for _, s := range p.Segs {
		corners[s.P0] = true
		corners[s.P3] = true
	}
	for _, want := range l {
		if !corners[want] {
			t.Errorf("corner %v is not a segment endpoint: it was rounded away", want)
		}
	}
}

// A circle has no corner anywhere, so it must come back as curves rather than as
// the polygon it started as. If this fails, corner detection is finding spurious
// corners and the fit never gets a run to work on.
func TestACircleNeedsNoCorners(t *testing.T) {
	l := circle(0, 0, 40, 24)
	p := Fit(l, DefaultOptions())
	curves := 0
	for _, s := range p.Segs {
		if s.Kind == Cubic {
			curves++
		}
	}
	if curves == 0 {
		t.Errorf("a 24-gon circle came back with no curves at all: %d segments", len(p.Segs))
	}
	// And it must be markedly smaller than the polygon it replaces, which is the
	// entire reason for doing this.
	if len(p.Segs) >= len(l) {
		t.Errorf("fitting made it bigger: %d segments for %d points", len(p.Segs), len(l))
	}
}

// Endpoints must be exact, because the segments are chained and any drift
// accumulates around a closed loop into a visible gap.
func TestSegmentsChainWithoutGaps(t *testing.T) {
	for _, l := range []contour.Loop{circle(10, 10, 25, 20), square(0, 0, 30, 20)} {
		p := Fit(l, DefaultOptions())
		for i := 1; i < len(p.Segs); i++ {
			a, b := p.Segs[i-1].P3, p.Segs[i].P0
			if a != b {
				t.Errorf("segment %d ends at %v but segment %d starts at %v", i-1, a, i, b)
			}
		}
		last := p.Segs[len(p.Segs)-1].P3
		if first := p.Segs[0].P0; last != first {
			t.Errorf("path is not closed: ends at %v, starts at %v", last, first)
		}
	}
}

// A degenerate loop must not hang or panic. Marching squares can hand over a
// repeated point, and a two-point loop is a seam artefact.
func TestDegenerateLoopsDoNotPanic(t *testing.T) {
	cases := map[string]contour.Loop{
		"empty":        {},
		"one point":    {{X: 1, Y: 1}},
		"two points":   {{X: 0, Y: 0}, {X: 1, Y: 1}},
		"all same":     {{X: 3, Y: 3}, {X: 3, Y: 3}, {X: 3, Y: 3}},
		"with repeats": {{X: 0, Y: 0}, {X: 0, Y: 0}, {X: 5, Y: 0}, {X: 5, Y: 5}, {X: 0, Y: 5}},
	}
	for name, l := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: panicked: %v", name, r)
				}
			}()
			_ = Fit(l, DefaultOptions())
		}()
	}
}

// A loop with no corner at all is a circle, and the run spanning it starts and
// ends at the same index. The fitter has to cope with that without emitting a
// zero-length segment, which would be an invisible artefact in the path data.
func TestNoZeroLengthSegments(t *testing.T) {
	for _, l := range []contour.Loop{circle(0, 0, 30, 16), square(0, 0, 10, 10)} {
		p := Fit(l, DefaultOptions())
		for i, s := range p.Segs {
			if s.Kind == Line && s.P0 == s.P3 {
				t.Errorf("segment %d is a zero-length line", i)
			}
			if s.Kind == Cubic && s.C1 == s.C2 && s.C1 == s.P0 {
				t.Errorf("segment %d is a cubic with every point coincident", i)
			}
		}
	}
}

// The fit must not depend on where the loop happens to start, or the same shape
// would encode differently depending on where marching squares happened to begin.
func TestFitIsIndependentOfTheStartingVertex(t *testing.T) {
	l := circle(0, 0, 30, 24)
	ref := Fit(l, DefaultOptions())
	for k := 1; k < len(l); k++ {
		rotated := make(contour.Loop, 0, len(l))
		for i := range l {
			rotated = append(rotated, l[(i+k)%len(l)])
		}
		got := Fit(rotated, DefaultOptions())
		if len(got.Segs) != len(ref.Segs) {
			t.Errorf("rotating by %d changed the segment count: %d vs %d", k, len(got.Segs), len(ref.Segs))
			continue
		}
		if e := pathError(rotated, got); e > DefaultTolerance+1e-9 {
			t.Errorf("rotating by %d pushed the fit to %v, over tolerance", k, e)
		}
	}
}

// A right angle has to be recognised as a corner by the corner test itself, not
// merely survive the fit. The distinction matters because a missed corner still
// produces a plausible result — the fit is simply not told there is a corner
// there, and subdividing around one eventually bottoms out into lines that happen
// to land on the right points. That accidental survival is what let a 100-degree
// threshold pass every fidelity test while quietly mislabelling every rectangle
// as a smooth blob.
func TestRightAnglesAreDetectedAsCorners(t *testing.T) {
	sq := polygon.Approximate(square(0, 0, 30, 20), polygon.DefaultOptions()).Loop
	if got := len(polygon.Corners(sq, polygon.Options{CornerAngle: DefaultCornerAngle})); got != 4 {
		t.Errorf("a square has %d corners at the default threshold, want 4", got)
	}
}

// The counterpart: a curve sampled finely enough to be worth replacing must not
// be mistaken for a polygon, or nothing ever gets fitted.
func TestSmoothRunsAreNotCorners(t *testing.T) {
	// 24 points on a circle of radius 400 turn 15 degrees per vertex.
	arc := circle(0, 0, 400, 24)
	if got := len(polygon.Corners(arc, polygon.Options{CornerAngle: DefaultCornerAngle})); got != 0 {
		t.Errorf("a smooth arc has %d corners, want 0", got)
	}
}

// Corners must survive with room to spare, so the result cannot be an artefact of
// running out of subdivision depth.
func TestCornersSurviveAtHighDepth(t *testing.T) {
	l := square(0, 0, 30, 20)
	p := Fit(l, Options{Tolerance: 0.01, MaxDepth: 16})
	for i, s := range p.Segs {
		if s.Kind != Line {
			t.Errorf("segment %d is a curve at high depth; the edge is straight", i)
		}
	}
	if e := pathError(l, p); e > 1e-9 {
		t.Errorf("error %v at high depth on a shape made of exact lines", e)
	}
}
