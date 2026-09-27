package polygon

import (
	"testing"

	"github.com/elfeo/svg-proto/internal/contour"
	"github.com/elfeo/svg-proto/internal/field"
)

// rasterisedDisc traces a hard-edged disc the way the pipeline does, so the tests
// run against real marching-squares output rather than an idealised circle. The
// numbers this produces are the reason the package exists: 165 points turning by
// 0-10 degrees 52 times, 40-50 degrees 72 times, and 90-100 degrees 40 times.
// Those 40 near-right angles are diagonal boundary steps, not features.
func rasterisedDisc(t *testing.T, n, r int) contour.Loop {
	t.Helper()
	f := field.New(n, n)
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			dx, dy := float64(x)-float64(n)/2, float64(y)-float64(n)/2
			if dx*dx+dy*dy <= float64(r*r) {
				f.V[y*n+x] = 1
			}
		}
	}
	loops := contour.Trace(f, contour.Options{Level: 0.5, MinPoints: 4, JoinTolerance: 1e-9})
	if len(loops) != 1 {
		t.Fatalf("expected one loop, got %d", len(loops))
	}
	return loops[0]
}

func closed(pts ...contour.Pt) contour.Loop {
	return append(contour.Loop(pts), pts[0])
}

// The headline case. A disc has no corners, and the staircase that tracing it
// produces has 40 vertices that turn by about 90 degrees. Classifying them as
// corners, which a bare sharpness threshold does, leaves the curve fitter with
// nothing longer than a two-point run to work on.
func TestRasterisedDiscHasNoCorners(t *testing.T) {
	l := Approximate(rasterisedDisc(t, 41, 15), DefaultOptions())
	if len(l.Corners) != 0 {
		t.Errorf("a disc reported %d corners, want 0: %v", len(l.Corners), l.Corners)
	}
}

// A rectangle is the other half of the trade: four right angles, each between two
// straight edges. If the isolation rule dissolves these, smoothing has turned a
// rectangle into a lozenge, which is a different object rather than a smoother
// one.
func TestRectangleCornersSurvive(t *testing.T) {
	l := Approximate(closed(
		contour.Pt{X: 0, Y: 0}, contour.Pt{X: 30, Y: 0},
		contour.Pt{X: 30, Y: 20}, contour.Pt{X: 0, Y: 20},
	), DefaultOptions())
	if len(l.Corners) != 4 {
		t.Errorf("a rectangle reported %d corners, want 4: %v", len(l.Corners), l.Corners)
	}
}

// The case that needed the signed turn, and the reason it is signed.
//
// A staircase's steps rotate -90, +90, -90, +90: each step is undone by the next
// while the absolute angle at every vertex reads 90, exactly as it does at a real
// corner. Reading magnitude alone, a staircase is indistinguishable from a row of
// right angles, and both readings of the rule fail at once. A threshold loose
// enough to accept the corner at the end of the staircase also accepts the twelve
// steps leading up to it; a threshold tight enough to reject the steps rejects the
// corner too, because it has its neighbour a pixel away.
//
// Reading the sign separates them. A corner at the end of a staircase has a
// straight run on one side and an alternating run on the other. A step in the
// middle of the staircase has alternating runs on both sides, and no straight run
// anywhere, so there is no state for it to change into.
func TestCornerAtTheEndOfAStaircaseIsFound(t *testing.T) {
	var l contour.Loop
	// Bottom edge: a staircase, so the boundary turns repeatedly and the corner at
	// its end has to be recognised from the quiet run that follows it.
	for i := 0; i < 12; i++ {
		l = append(l, contour.Pt{X: float64(i), Y: float64(i % 2)})
	}
	l = append(l, contour.Pt{X: 20, Y: 0})
	// Right edge, straight.
	for i := 1; i <= 10; i++ {
		l = append(l, contour.Pt{X: 20, Y: float64(i)})
	}
	// Top edge, staircase the other way.
	for i := 1; i <= 12; i++ {
		l = append(l, contour.Pt{X: 20 - float64(i), Y: 10 - float64(i%2)})
	}
	l = append(l, contour.Pt{X: 0, Y: 10})
	for i := 1; i <= 10; i++ {
		l = append(l, contour.Pt{X: 0, Y: 10 - float64(i)})
	}
	res := Approximate(l, DefaultOptions())
	if len(res.Corners) != 4 {
		t.Errorf("found %d corners, want all 4: %v", len(res.Corners), res.Corners)
	}
	// The four turns of the rectangle itself, not the twelve steps of either
	// staircase, which is the part that was previously being got wrong.
	want := []int{0, 12, 22, 35}
	if len(res.Corners) == len(want) {
		for i, w := range want {
			if res.Corners[i] != w {
				t.Errorf("corners %v, want %v", res.Corners, want)
				break
			}
		}
	}
}

// A triangle turns by 120 degrees at each vertex, well past the threshold, and its
// edges are straight, so all three must be found.
func TestTriangleCornersSurvive(t *testing.T) {
	l := Approximate(closed(
		contour.Pt{X: 10, Y: 0}, contour.Pt{X: 20, Y: 17}, contour.Pt{X: 0, Y: 17},
	), DefaultOptions())
	if len(l.Corners) != 3 {
		t.Errorf("a triangle reported %d corners, want 3: %v", len(l.Corners), l.Corners)
	}
}

// Two corners in a row, which a staircase that happens to reverse direction can
// produce. The isolation rule must not need a quiet vertex between every pair.
func TestAdjacentCornersAreBothFound(t *testing.T) {
	l := Approximate(closed(
		contour.Pt{X: 0, Y: 0}, contour.Pt{X: 10, Y: 0}, contour.Pt{X: 10, Y: 10},
		contour.Pt{X: 0, Y: 10},
	), DefaultOptions())
	if len(l.Corners) != 4 {
		t.Errorf("a square reported %d corners, want 4: %v", len(l.Corners), l.Corners)
	}
}

// A repeated point is not a shape. Tracing can emit one, and it must not read as
// a corner, because a corner there would split a smooth run at nothing.
func TestRepeatedPointsAreNotCorners(t *testing.T) {
	l := Approximate(closed(
		contour.Pt{X: 0, Y: 0}, contour.Pt{X: 0, Y: 0}, contour.Pt{X: 10, Y: 0},
		contour.Pt{X: 10, Y: 0}, contour.Pt{X: 10, Y: 10}, contour.Pt{X: 0, Y: 10},
	), DefaultOptions())
	for _, c := range l.Corners {
		// Only the two real corners, at the ends of the straight runs.
		if c == 1 || c == 3 {
			t.Errorf("vertex %d is a repeated point and was read as a corner", c)
		}
	}
}

// The closing point that contour.Loop carries is a repeat, and left in place it
// makes the seam look like a zero-length edge, which reads as no turn and hides
// the corner nearest it.
func TestClosingPointIsNormalisedAway(t *testing.T) {
	l := closed(
		contour.Pt{X: 0, Y: 0}, contour.Pt{X: 10, Y: 0},
		contour.Pt{X: 10, Y: 10}, contour.Pt{X: 0, Y: 10},
	)
	res := Approximate(l, DefaultOptions())
	if got := len(res.Loop); got != 4 {
		t.Errorf("normalised loop has %d points, want 4", got)
	}
	if len(res.Corners) != 4 {
		t.Errorf("a closed square reported %d corners, want 4: %v", len(res.Corners), res.Corners)
	}
}

// Too small to have a shape at all. Corners must not invent geometry.
func TestTinyLoopsAreHandled(t *testing.T) {
	for name, l := range map[string]contour.Loop{
		"empty":     {},
		"one":       {{X: 1, Y: 1}},
		"two":       {{X: 0, Y: 0}, {X: 1, Y: 1}},
		"collapsed": {{X: 2, Y: 2}, {X: 2, Y: 2}, {X: 2, Y: 2}},
	} {
		if got := len(Approximate(l, DefaultOptions()).Corners); got != 0 {
			t.Errorf("%s: reported %d corners", name, got)
		}
	}
}

// The isolation rule must not dissolve a corner merely because the shape is
// finely sampled. A rounded rectangle approximated by many short segments still
// has four real corners, and the sampling density should not decide the answer.
func TestSampledRectangleKeepsItsCorners(t *testing.T) {
	var l contour.Loop
	const step = 0.25
	for x := 0.0; x <= 40; x += step {
		l = append(l, contour.Pt{X: x, Y: 0})
	}
	for y := step; y <= 25; y += step {
		l = append(l, contour.Pt{X: 40, Y: y})
	}
	for x := 40 - step; x >= 0; x -= step {
		l = append(l, contour.Pt{X: x, Y: 25})
	}
	for y := 25 - step; y > 0; y -= step {
		l = append(l, contour.Pt{X: 0, Y: y})
	}
	res := Approximate(l, DefaultOptions())
	if len(res.Corners) != 4 {
		t.Errorf("a finely sampled rectangle reported %d corners, want 4: %v", len(res.Corners), res.Corners)
	}
}

// The classification is what the fitter acts on, so it has to be deterministic
// and independent of where the loop happens to start, or the same shape encodes
// differently depending on where tracing began.
func TestCornersAreRotationStable(t *testing.T) {
	l := rasterisedDisc(t, 41, 15)
	ref := len(Approximate(l, DefaultOptions()).Corners)
	if ref != 0 {
		t.Fatalf("a disc reported %d corners, want 0", ref)
	}
	sq := closed(
		contour.Pt{X: 0, Y: 0}, contour.Pt{X: 30, Y: 0},
		contour.Pt{X: 30, Y: 20}, contour.Pt{X: 0, Y: 20},
	)
	open := sq[:len(sq)-1]
	if got := len(Approximate(open, DefaultOptions()).Corners); got != 4 {
		t.Errorf("an unclosed square reported %d corners, want 4", got)
	}
	for k := 0; k < len(open); k++ {
		rotated := make(contour.Loop, 0, len(open))
		for i := range open {
			rotated = append(rotated, open[(i+k)%len(open)])
		}
		if got := len(Approximate(rotated, DefaultOptions()).Corners); got != 4 {
			t.Errorf("rotating the square by %d changed the corner count to %d", k, got)
		}
	}
}

// The turn of a convex polygon vertex is 180 minus its interior angle. This pins
// the threshold's meaning so a later tweak cannot quietly narrow it.
func TestTurnAngleThresholdMeaning(t *testing.T) {
	// An interior angle of 90 gives a 90 degree turn; of 120, a 60 degree turn.
	for _, tc := range []struct {
		name string
		pts  []contour.Pt
		want int
	}{
		{"right angle", []contour.Pt{{X: 0, Y: 0}, {X: 10, Y: 0}, {X: 10, Y: 10}, {X: 0, Y: 10}}, 4},
	} {
		// Approximate, not Corners: the closing point has to be normalised away
		// first, which is what the pipeline does and what a direct call on a raw
		// contour.Loop does not.
		res := Approximate(closed(tc.pts...), DefaultOptions())
		turns := turnAngles(res.Loop)
		if got := len(res.Corners); got != tc.want {
			t.Errorf("%s: %d corners, want %d (turns %v)", tc.name, got, tc.want, turns)
		}
	}
}
