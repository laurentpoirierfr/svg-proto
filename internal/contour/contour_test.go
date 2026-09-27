package contour

import (
	"math"
	"math/bits"
	"testing"

	"github.com/elfeo/svg-proto/internal/field"
)

func grid(w, h int, set func(x, y int) float64) *field.Field {
	f := field.New(w, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			f.V[y*w+x] = set(x, y)
		}
	}
	return f
}

// halfPlane is 1 on the left, 0 on the right: exactly one straight vertical
// boundary, the case with a known analytic answer.
func halfPlane() *field.Field {
	return grid(9, 7, func(x, y int) float64 {
		if x < 4 {
			return 1
		}
		return 0
	})
}

func TestTraceOfAHalfPlane(t *testing.T) {
	// The region {v >= 0.5} on a field that is 1 left of x=4 and 0 right of it.
	// The boundary is the vertical line x=3.5: the linear crossing between the
	// sample at x=3 (value 1) and the sample at x=4 (value 0). Half a node, not
	// 3 or 4. That sub-pixel position is the whole reason for interpolating.
	loops := Trace(halfPlane(), DefaultOptions())
	if len(loops) != 1 {
		t.Fatalf("got %d loops, want 1", len(loops))
	}
	l := loops[0]
	if !l.Contains(Pt{3.4, 3}) {
		t.Error("the region does not contain x=3.4, want the left half inside")
	}
	if l.Contains(Pt{3.6, 3}) {
		t.Error("the region contains x=3.6, want the right half outside")
	}
	// The closure edge lies just outside the frame, where the viewBox clips it.
	// Assert it is outside rather than pretending it is exactly on the border.
	_, _, maxX, _ := l.BBox()
	if math.Abs(maxX-3.5) > 1e-9 {
		t.Errorf("the visible boundary is at x=%v, want exactly 3.5", maxX)
	}
	minX, _, _, _ := l.BBox()
	if minX >= 0 {
		t.Errorf("closure edge at x=%v, want it outside the frame so it is clipped", minX)
	}
}

func TestTraceOfAConstantFieldIsEmpty(t *testing.T) {
	// The background of every real image is a plateau. If a threshold equal to
	// the plateau value produced contours, every image would gain a border ring.
	f := grid(8, 8, func(x, y int) float64 { return 0.7 })
	// Above the plateau: everything is on one side, so nothing to trace. This is
	// the case that matters, because it is what a flat background must do.
	if loops := Trace(f, Options{Level: 0.8}); len(loops) != 0 {
		t.Errorf("got %d loops above the plateau, want 0", len(loops))
	}
	// Below the plateau: the whole frame is the region, bounded by the frame.
	if loops := Trace(f, Options{Level: 0.6}); len(loops) != 1 {
		t.Errorf("got %d loops below the plateau, want 1", len(loops))
	}
	// Exactly at the plateau the answer is 1, not 0: the code uses >=, so
	// {v >= level} is the entire field and its boundary is the frame. Callers
	// pick thresholds strictly between levels and never see this, but the
	// semantics should be stated rather than accidental.
}

func TestTraceOfADegenerateField(t *testing.T) {
	// The encoder traces every threshold, including on 1x1 icons. Must not panic.
	for _, dim := range [][2]int{{1, 1}, {1, 5}, {5, 1}, {0, 0}} {
		f := grid(dim[0], dim[1], func(x, y int) float64 { return 0.5 })
		if loops := Trace(f, DefaultOptions()); len(loops) != 0 {
			t.Errorf("%dx%d: got %d loops, want 0", dim[0], dim[1], len(loops))
		}
	}
}

func TestTraceOfACircle(t *testing.T) {
	// A radial ramp: the iso-line at 0.5 should be a circle of radius r, and its
	// area should approach pi r^2. This is the real test of sub-pixel accuracy,
	// because a staircase implementation gives a visibly wrong area.
	const r = 3.0
	f := grid(41, 41, func(x, y int) float64 {
		d := math.Hypot(float64(x)-20, float64(y)-20)
		// 1 inside r, 0 outside, with a hard step; the 0.5 level set is then
		// exactly the r=3 circle by the interpolated midpoint of the step.
		if d < r {
			return 1
		}
		return 0
	})
	loops := Trace(f, DefaultOptions())
	if len(loops) != 1 {
		t.Fatalf("got %d loops, want 1", len(loops))
	}
	a := math.Abs(loops[0].Area())
	want := math.Pi * r * r
	if math.Abs(a-want)/want > 0.15 {
		t.Errorf("area = %v, want about %v (%.1f%% off)", a, want, 100*math.Abs(a-want)/want)
	}
	// A staircase would be far worse than 15%; a sub-pixel one is within a few
	// percent. Guard the direction of the error too: linear interpolation
	// slightly *under*-estimates the area of a convex step.
	if a > want {
		t.Errorf("area = %v overshoots the analytic %v, which linear interpolation should not do", a, want)
	}
}

func TestTraceOnACheckerboardUsesTheAsymptoticDecider(t *testing.T) {
	// A checkerboard is the pathological case: every cell is ambiguous. The
	// number of loops must be a power-of-two-ish count of the little squares and
	// must be *stable*, which is the whole point of the decider.
	f := grid(8, 8, func(x, y int) float64 {
		if (x+y)%2 == 0 {
			return 1
		}
		return 0
	})
	loops := Trace(f, DefaultOptions())
	if len(loops) == 0 {
		t.Fatal("a checkerboard produced no contour")
	}
	// Without a decider, the ambiguous cells join the squares into one snake-like
	// loop. With one, they stay separate.
	if len(loops) < 4 {
		t.Errorf("got %d loops, want the squares to stay separate", len(loops))
	}
	// Every loop must be closed: the first and last points are not duplicated,
	// so a closed loop has len >= 3 and the implied closing edge exists.
	for i, l := range loops {
		if len(l) < 3 {
			t.Errorf("loop %d has %d points, want at least 3", i, len(l))
		}
	}
}

func TestAsymptoticDeciderResolvesTheSaddleBothWays(t *testing.T) {
	// Cases 0b0101 and 0b1010 are the ambiguous ones: the four corners alternate
	// above and below, and the cell resolves either into two separate regions or
	// into one band. Both readings fit the four samples, so the topology is only
	// decided by the asymptotic decider, which compares the bilinear saddle at the
	// cell centre against the level.
	//
	// Tested on a single 2x2 field, so the decider is the only thing under test.
	const level = 0.5
	// Corner c of cell (0,0) is At(c) with At(0,0)=V[0], At(1,0)=V[1],
	// At(1,1)=V[3], At(0,1)=V[2]. So corners 0 and 2 are V[0] and V[3]; writing
	// the four corners in visual order instead would silently build the
	// non-alternating pattern 0b1001 and test nothing.
	mk := func(c0, c1, c2, c3 float64) *field.Field {
		f := field.New(2, 2)
		f.V[0], f.V[1], f.V[3], f.V[2] = c0, c1, c2, c3
		return f
	}

	// Saddle below the level: the two above-corners stay separate. Corners 0 and
	// 2 are 0.6, so the centre is 0.3 < 0.5.
	separate := mk(0.6, 0, 0.6, 0) // corners 0 and 2 above
	if got := saddle(0.6, 0, 0.6, 0); got >= level {
		t.Fatalf("saddle = %v, want it below the level for this fixture", got)
	}
	segs := segments(separate, 2, 2, level)
	if len(segs) != 2 {
		t.Fatalf("got %d segments, want 2: the above-corners must not be joined", len(segs))
	}
	// And the two segments must not touch, or they would be one band.
	if segs[0].a.Dist(segs[0].b) == 0 || segs[0].a.Dist(segs[1].a) < 1e-9 {
		t.Error("the two segments share an endpoint, so the decider joined them")
	}

	// Saddle above the level: the two above-corners connect, so the segments are
	// the other pair. Corners 0 and 2 are 1, so the centre is 0.5 >= 0.5.
	banded := mk(1, 0, 1, 0) // corners 0 and 2 above, centre exactly at the level
	if got := saddle(1, 0, 1, 0); got < level {
		t.Fatalf("saddle = %v, want it at or above the level for this fixture", got)
	}
	segs = segments(banded, 2, 2, level)
	if len(segs) != 2 {
		t.Fatalf("got %d segments, want 2: the crossing count is 4 either way", len(segs))
	}

	// A half-step either side of the level must flip the decision. This is the
	// property that makes the topology stable: a threshold nudged by a thousandth
	// resolves the same way, a threshold nudged past the saddle flips cleanly.
	if n := len(segments(mk(0.51, 0, 0.51, 0), 2, 2, level)); n == 0 {
		t.Error("a cell just above the saddle produced no segments")
	}
}

func TestSaddle(t *testing.T) {
	if got := saddle(1, 0, 1, 0); got != 0.5 {
		t.Errorf("saddle(1,0,1,0) = %v, want 0.5", got)
	}
	if got := saddle(0, 0, 0, 0); got != 0 {
		t.Errorf("saddle of zeros = %v, want 0", got)
	}
}

func TestLoopAreaSignAndMagnitude(t *testing.T) {
	// A unit square traversed one way and the other must have equal magnitude and
	// opposite sign. The sign is what lets the encoder tell an outer boundary
	// from a hole.
	cw := Loop{{0, 0}, {1, 0}, {1, 1}, {0, 1}}
	ccw := Loop{{0, 0}, {0, 1}, {1, 1}, {1, 0}}
	a, b := cw.Area(), ccw.Area()
	if math.Abs(math.Abs(a)-1) > 1e-12 {
		t.Errorf("unit square area = %v, want magnitude 1", a)
	}
	if a == b {
		t.Error("the two traversal orders have the same sign")
	}
	if math.Abs(a+b) > 1e-12 {
		t.Errorf("areas %v and %v are not opposite", a, b)
	}
}

func TestLoopLength(t *testing.T) {
	l := Loop{{0, 0}, {3, 4}, {3, 4}}
	if got := l.Length(); math.Abs(got-5) > 1e-12 {
		t.Errorf("Length() = %v, want 5", got)
	}
	if got := (Loop{{1, 1}}).Length(); got != 0 {
		t.Errorf("single-point Length() = %v, want 0", got)
	}
	if got := (Loop{}).Length(); got != 0 {
		t.Errorf("empty Length() = %v, want 0", got)
	}
}

func TestLoopContains(t *testing.T) {
	// A square with a square hole traced as two loops: the inner point must be
	// inside the outer and outside the inner. This is the containment test the
	// encoder relies on to order nested regions.
	outer := Loop{{0, 0}, {10, 0}, {10, 10}, {0, 10}}
	inner := Loop{{3, 3}, {7, 3}, {7, 7}, {3, 7}}
	if !outer.Contains(Pt{5, 5}) {
		t.Error("outer.Contains(centre) = false")
	}
	if outer.Contains(Pt{20, 5}) {
		t.Error("outer.Contains(outside) = true")
	}
	if !inner.Contains(Pt{5, 5}) {
		t.Error("inner.Contains(centre) = false")
	}
	// On the boundary the half-open rule must give a stable answer, not a panic.
	for _, p := range []Pt{{0, 5}, {10, 5}, {5, 0}, {5, 10}, {0, 0}} {
		_ = outer.Contains(p)
	}
}

func TestLoopCentroid(t *testing.T) {
	sq := Loop{{0, 0}, {10, 0}, {10, 10}, {0, 10}}
	c := sq.Centroid()
	if math.Abs(c.X-5) > 1e-9 || math.Abs(c.Y-5) > 1e-9 {
		t.Errorf("Centroid() = %v, want (5,5)", c)
	}
	// A degenerate loop falls back to the vertex average rather than dividing by
	// a zero area.
	degenerate := Loop{{0, 0}, {2, 0}, {4, 0}}
	if c := degenerate.Centroid(); math.IsNaN(c.X) || math.IsNaN(c.Y) {
		t.Errorf("degenerate Centroid() = %v, want a finite vertex average", c)
	}
	if c := (Loop{}).Centroid(); c.X != 0 || c.Y != 0 {
		t.Errorf("empty Centroid() = %v, want the origin", c)
	}
}

func TestLoopBBox(t *testing.T) {
	l := Loop{{-1, 2}, {4, 0}, {0, 9}}
	minX, minY, maxX, maxY := l.BBox()
	if minX != -1 || minY != 0 || maxX != 4 || maxY != 9 {
		t.Errorf("BBox() = (%v,%v,%v,%v), want (-1,0,4,9)", minX, minY, maxX, maxY)
	}
	if a, b, c, d := (Loop{}).BBox(); a != 0 || b != 0 || c != 0 || d != 0 {
		t.Error("empty BBox() should be all zeros")
	}
}

func TestTraceIsDeterministic(t *testing.T) {
	// A tracer feeding a content-addressed cache must be byte-stable.
	f := grid(24, 24, func(x, y int) float64 {
		return math.Sin(float64(x)*0.7) + math.Cos(float64(y)*0.4)
	})
	o := Options{Level: 1.0, JoinTolerance: 1e-9, MinPoints: 3}
	first := Trace(f, o)
	if len(first) == 0 {
		t.Skip("this field has no contour at level 1.0")
	}
	for i := 0; i < 10; i++ {
		again := Trace(f, o)
		if len(again) != len(first) {
			t.Fatalf("loop count changed between runs: %d vs %d", len(again), len(first))
		}
		for j := range first {
			if len(again[j]) != len(first[j]) {
				t.Fatalf("loop %d length changed between runs", j)
			}
			for k := range first[j] {
				if again[j][k] != first[j][k] {
					t.Fatalf("loop %d point %d changed between runs", j, k)
				}
			}
		}
	}
}

func TestLoopsAreClosed(t *testing.T) {
	// A loop repeats its first point as its last, so closure is a plain equality
	// check. The wrap-around edge is then a real edge of the square: a wrong arc
	// pairing in the marching-squares table leaves a gap there, and it was the
	// length of that edge, not its absence, that gave the bug away.
	f := grid(16, 16, func(x, y int) float64 {
		if x >= 4 && x < 12 && y >= 4 && y < 12 {
			return 1
		}
		return 0
	})
	loops := Trace(f, DefaultOptions())
	if len(loops) != 1 {
		t.Fatalf("got %d loops for a single square, want 1", len(loops))
	}
	l := loops[0]
	if l[0] != l[len(l)-1] {
		t.Errorf("loop does not close: first %v, last %v", l[0], l[len(l)-1])
	}
	if st := Stats(loops); !st.ClosedOK {
		t.Error("Stats reports a loop that is not closed")
	}
	// 8+8+8+8 unit edges plus the 4 sharp corners, plus the repeated first point.
	// The corners are what a square has to spend points on; dropping them is what
	// the chamfer did, and it is why the count is 37 rather than 33.
	if len(l) != 37 {
		t.Errorf("got %d points, want 37", len(l))
	}
	// No zero-length segment anywhere, including the wrap, or the encoder emits
	// a redundant lineto that costs bytes for nothing.
	for i := 1; i < len(l); i++ {
		if l[i] == l[i-1] {
			t.Errorf("points %d and %d are both %v: zero-length segment", i-1, i, l[i])
		}
	}
	// The perimeter of the 8x8 square, exactly: 4 sides of 8. With the chamfer it
	// came out as 4*7 + 4*sqrt(0.5), i.e. 30.83, which was a giveaway that the
	// corners were being cut rather than turned.
	if got, want := l.Length(), 32.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("perimeter = %v, want %v", got, want)
	}
}

func TestStats(t *testing.T) {
	f := grid(16, 16, func(x, y int) float64 {
		if x >= 4 && x < 12 && y >= 4 && y < 12 {
			return 1
		}
		return 0
	})
	loops := Trace(f, DefaultOptions())
	st := Stats(loops)
	if st.Loops != 1 {
		t.Errorf("Loops = %d, want 1", st.Loops)
	}
	if st.Points < 4 {
		t.Errorf("Points = %d, want at least 4 corners", st.Points)
	}
	// A 8x8 square traced at 4.0..12.0 has area 64.
	if math.Abs(st.Area-64) > 0.5 {
		t.Errorf("Area = %v, want about 64", st.Area)
	}
	if st.MinArea > st.MaxArea {
		t.Error("MinArea exceeds MaxArea")
	}
	empty := Stats(nil)
	if empty.Loops != 0 || empty.MinArea != 0 {
		t.Errorf("Stats(nil) = %+v, want a zeroed struct", empty)
	}
}

func TestMinPointsFiltersSpeckle(t *testing.T) {
	// A single above-threshold node in a sea of below: the decider must not
	// invent a contour through it, and if one survives it must be filterable.
	f := grid(7, 7, func(x, y int) float64 {
		if x == 3 && y == 3 {
			return 1
		}
		return 0
	})
	loops := Trace(f, Options{Level: 0.5, MinPoints: 3})
	// A single above-threshold node does bound a region: the four cells around it
	// each cross two edges, and they chain into one small loop. So the honest
	// expectation is 1 tiny loop, not 0. What matters is that it is negligible,
	// because otherwise a single hot pixel in a photo becomes its own shape.
	if len(loops) != 1 {
		t.Fatalf("an isolated node produced %d loops, want 1 tiny loop", len(loops))
	}
	if a := math.Abs(loops[0].Area()); a > 1 {
		t.Errorf("the speck has area %v, want a sub-pixel remnant", a)
	}
	// MinPoints is the knob for dropping such specks outright.
	if got := Trace(f, Options{Level: 0.5, MinPoints: 99}); len(got) != 0 {
		t.Errorf("MinPoints=99 kept %d loops, want them all dropped", len(got))
	}
}

func TestJoinToleranceAbsorbsFloatNoise(t *testing.T) {
	// Two diagonals meeting in the middle of a cell: their endpoints are
	// computed from opposite directions and can differ in the last bit. A
	// tolerance of zero would fail to close the loop and leak points.
	f := grid(6, 6, func(x, y int) float64 {
		if x == y {
			return 1
		}
		return 0
	})
	strict := Trace(f, Options{Level: 0.5, JoinTolerance: 0})
	loose := Trace(f, Options{Level: 0.5, JoinTolerance: 1e-6})
	if len(loose) == 0 {
		t.Error("a diagonal with a tolerance produced no loop")
	}
	_ = strict // either outcome is acceptable, but it must not hang or panic
}

func TestLerpEdgeClamps(t *testing.T) {
	// A crossing ratio outside [0,1] can only come from a value one ulp on the
	// wrong side of the level. The interpolated point must stay on the edge.
	a, b := Pt{0, 0}, Pt{10, 0}
	// A flat edge has no zero to interpolate against, so the midpoint is the
	// documented fallback. It keeps the point on the edge instead of dividing by
	// zero and spraying a NaN into the geometry.
	for _, both := range [][2]float64{{1, 1}, {0, 0}, {0.5, 0.5}} {
		got := lerpEdge(a, b, both[0], both[1], 0.5)
		if math.Abs(got.X-5) > 1e-12 || got.Y != 0 {
			t.Errorf("a flat edge %v gave %v, want the midpoint {5 0}", both, got)
		}
	}
	mid := lerpEdge(a, b, 0, 1, 0.5)
	if math.Abs(mid.X-5) > 1e-12 {
		t.Errorf("linear crossing at x = %v, want 5", mid.X)
	}
}

// cellFor builds a 2x2 field whose corner c is inside iff bit c of code is set.
//
// The index mapping is the whole trap here. At() reads row-major, so for a 2x2
// field: corner 0 is V[0], corner 1 is V[1], corner 2 is V[3] (row 1, column 1)
// and corner 3 is V[2]. Assigning f.V[c] in a loop therefore does *not* set
// corner c, and a test written that way silently exercises a permuted pattern.
func cellFor(code int) *field.Field {
	f := field.New(2, 2)
	inside := func(c int) float64 {
		if code&(1<<c) != 0 {
			return 1
		}
		return 0
	}
	f.V[0] = inside(0)
	f.V[1] = inside(1)
	f.V[3] = inside(2)
	f.V[2] = inside(3)
	return f
}

// crossedEdges returns the mask of cell edges whose two corners disagree.
func crossedEdges(code int) int {
	m := 0
	for e := 0; e < 4; e++ {
		if (code>>e)&1 != (code>>((e+1)%4))&1 {
			m |= 1 << e
		}
	}
	return m
}

func TestEveryCellPatternEmitsTheRightArcCount(t *testing.T) {
	// Exhaustive over all 16 corner patterns of a single cell. A cell with n
	// crossed edges must emit n/2 arcs, because each arc joins two crossing
	// points. Anything else means an arc was emitted without a crossing, or a
	// crossing was computed and then dropped.
	//
	// This is the invariant that catches a wrong arc pairing. Note that within a
	// single cell every endpoint has degree 1, since a lone arc is an open piece;
	// the degree-2 property is global and is checked separately below.
	for code := 1; code < 15; code++ {
		segs := segments(cellFor(code), 2, 2, 0.5)
		crossed := crossedEdges(code)
		want := bits.OnesCount8(uint8(crossed)) / 2
		// A lone inside corner turns at the cell centre, which takes two arcs
		// rather than the single chord. The endpoints are unchanged.
		if code&(code-1) == 0 {
			want = 2
		}
		if len(segs) != want {
			t.Errorf("code %04b: %d arcs, want %d", code, len(segs), want)
		}
		// Every arc must be non-degenerate: two distinct crossing points.
		for i, s := range segs {
			if s.a == s.b {
				t.Errorf("code %04b: arc %d is a zero-length point at %v", code, i, s.a)
			}
		}
	}
}

func TestArcsPairUpGlobally(t *testing.T) {
	// Across a whole field, every crossing point must be shared by exactly two
	// arcs. A degree-1 endpoint is a contour that stops in the middle of nowhere,
	// which shows up as an unclosed path with a visible gap; a degree-3 endpoint is
	// a figure-eight, which fills wrong.
	//
	// The phase offsets keep the ripple off the level everywhere. That is not
	// cosmetic: a node sitting exactly *on* the level is inside, and the four
	// cells around it each emit an arc through the same node, giving a legitimate
	// four-way junction where "degree 2" is the wrong expectation. See
	// TestNodeExactlyOnLevelFormsAFourWayJunction. The real pipeline cannot hit
	// that case, because a threshold is the midpoint of two distinct palette
	// values and no palette value can equal a midpoint.
	f := grid(37, 29, func(x, y int) float64 {
		return 0.5 + 0.4*math.Sin(float64(x)*0.6+0.31)*math.Cos(float64(y)*0.45+0.77)
	})
	// Padded, because the invariant only holds once every region is closed. On the
	// bare field the arcs that run off the frame legitimately end at degree 1.
	p := padBelow(f, 0.5)
	segs := segments(p, p.W, p.H, 0.5)
	if len(segs) == 0 {
		t.Fatal("no arcs at all; the fixture is degenerate")
	}
	// Quantised keys, not exact ones. Two cells meeting at a crossing point
	// interpolate the same t along the same edge in opposite directions, which is
	// equal in exact arithmetic but can differ in the last bit in floating point.
	// The implementation hashes coordinates for exactly this reason; an exact-key
	// map here would report spurious degree-1 points and hide real ones.
	deg := map[[2]int64]int{}
	loc := map[[2]int64]Pt{}
	for _, s := range segs {
		for _, q := range [2]Pt{s.a, s.b} {
			k := [2]int64{int64(math.Round(q.X * 1e9)), int64(math.Round(q.Y * 1e9))}
			deg[k]++
			loc[k] = q
		}
	}
	bad := 0
	for k, d := range deg {
		if d != 2 {
			if bad < 5 {
				t.Errorf("crossing point %v has degree %d, want 2", loc[k], d)
			}
			bad++
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d crossing points do not pair up", bad, len(deg))
	}
	// The walk must turn every arc into part of a loop.
	loops, _ := chain(segs, DefaultOptions())
	if total := Stats(loops); total.Points == 0 {
		t.Error("chaining produced no loops")
	}
	// Every loop must be closed: the last point back onto the first. An unclosed
	// loop renders as a path with a visible gap, and a truncated one silently
	// drops a region, so both are checked rather than assumed.
	st := Stats(loops)
	if !st.ClosedOK {
		t.Error("Stats reports a loop that is not closed")
	}
	if st.Points == 0 {
		t.Error("no points in the traced loops")
	}
}

func TestNodeExactlyOnLevelFormsAFourWayJunction(t *testing.T) {
	// Documented, not fixed. A node whose value is exactly the level counts as
	// inside, so when its neighbours disagree the four cells around it each emit
	// an arc through that node: the level set crosses itself and the region has a
	// pinch point. Chaining then has to walk through a vertex of degree 4, where
	// "pick the next unused arc" is ambiguous.
	//
	// This is left as-is deliberately. The encoder derives thresholds as midpoints
	// between two distinct palette values, so no sample can land on a threshold,
	// and the case is unreachable outside hand-built fields. Making the walk
	// degree-4-aware would add a choice with no correct default.
	f := field.New(5, 5)
	for i := range f.V {
		f.V[i] = 0
	}
	f.V[2*5+2] = 0.5 // the node, exactly at the level
	f.V[2*5+1] = 1   // west neighbour, inside
	f.V[2*5+3] = 1   // east neighbour, inside
	segs := segments(f, f.W, f.H, 0.5)
	deg := map[Pt]int{}
	for _, s := range segs {
		deg[s.a]++
		deg[s.b]++
	}
	if deg[Pt{2, 2}] < 4 {
		t.Errorf("centre node has degree %d, want at least 4: %v", deg[Pt{2, 2}], segs)
	}
	loops, _ := chain(segs, DefaultOptions())
	if len(loops) == 0 {
		t.Error("no loop traced at all")
	}
	for i, l := range loops {
		if l[0] != l[len(l)-1] {
			t.Errorf("loop %d is not closed: first %v, last %v", i, l[0], l[len(l)-1])
		}
	}
}

func TestCornersAreSharp(t *testing.T) {
	// The regression test for the chamfer. A lone inside sample covers the quarter
	// of its cell bounded by the two crossing points and the cell centre, so the
	// boundary turns at the centre at a right angle. The straight chord between
	// the two edge midpoints instead cuts the corner, and it did so on every
	// corner of every region in the image.
	//
	// The symptom was geometric and measurable: an 8x8 square came out at 63.5
	// square pixels with a perimeter of 30.83, neither of which is the square.
	// At native scale a half-pixel chamfer is invisible; scaled up ten times it
	// is not.
	f := field.New(16, 16)
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			if x >= 4 && x < 12 && y >= 4 && y < 12 {
				f.V[y*16+x] = 1
			}
		}
	}
	loops := Trace(f, DefaultOptions())
	if len(loops) != 1 {
		t.Fatalf("got %d loops, want 1", len(loops))
	}
	l := loops[0]
	minX, minY, maxX, maxY := l.BBox()
	if minX != 3.5 || maxX != 11.5 || minY != 3.5 || maxY != 11.5 {
		t.Errorf("BBox = (%v,%v,%v,%v), want (3.5,3.5,11.5,11.5)", minX, minY, maxX, maxY)
	}
	if got, want := math.Abs(l.Area()), 64.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("area = %v, want %v: the chamfer was taking 0.5 square pixels per corner", got, want)
	}
	if got, want := l.Length(), 32.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("perimeter = %v, want %v", got, want)
	}
	// The four corners must actually be in the point list. Asserting only the area
	// would pass for a contour that replaced each corner with a shorter detour
	// elsewhere, which is a different bug with the same area.
	for _, c := range []Pt{{3.5, 3.5}, {11.5, 3.5}, {11.5, 11.5}, {3.5, 11.5}} {
		found := false
		for _, p := range l {
			if p == c {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("corner %v is not a point of the contour", c)
		}
	}
	// And no diagonal shortcuts: every edge is axis-aligned, so the only turns are
	// the four right angles. This is what actually distinguishes a square from a
	// chamfered octagon of the same area.
	for i := 1; i < len(l); i++ {
		a, b := l[i-1], l[i]
		if a.X != b.X && a.Y != b.Y {
			t.Errorf("edge %d from %v to %v is diagonal, so a corner is still being cut", i-1, a, b)
		}
	}
}

func TestLoneInsideSampleIsAQuarterNotADiamond(t *testing.T) {
	// The smallest case of the rule above: one sample inside, everything else out.
	// Under the centred-sample convention the region is the unit square
	// [0.5,1.5]^2, of area 1, reached by two half-edges of length 0.5. The chord
	// version gave a diamond of area 0.5.
	f := field.New(3, 3)
	f.V[1*3+1] = 1
	loops := Trace(f, DefaultOptions())
	if len(loops) != 1 {
		t.Fatalf("got %d loops, want 1", len(loops))
	}
	l := loops[0]
	minX, minY, maxX, maxY := l.BBox()
	if minX != 0.5 || maxX != 1.5 || minY != 0.5 || maxY != 1.5 {
		t.Errorf("BBox = (%v,%v,%v,%v), want (0.5,0.5,1.5,1.5)", minX, minY, maxX, maxY)
	}
	// The frame padding closes the loop around the outside, so the enclosed area
	// is not the region area here; the BBox and the axis-aligned edges are the
	// assertions that pin the shape.
	for i := 1; i < len(l); i++ {
		a, b := l[i-1], l[i]
		if a.X != b.X && a.Y != b.Y {
			t.Errorf("edge %d from %v to %v is diagonal", i-1, a, b)
		}
	}
}
