// Package polygon approximates a traced contour: it normalises the loop and
// decides which of its vertices are real corners.
//
// It exists because corner classification is where raster tracing and curve
// fitting disagree, and getting it wrong is expensive in both directions. Call a
// rasterisation artefact a corner and the fitter will refuse to round it, leaving
// two-point runs and cubics that double back on themselves. Call a real corner an
// artefact and a rectangle comes back as a lozenge, which is a different object
// rather than a smoother one. Neither failure is loud, so the rule is worth
// stating precisely.
package polygon

import (
	"math"

	"github.com/elfeo/svg-proto/internal/contour"
)

// Options configures corner classification.
type Options struct {
	// CornerAngle is the turn, in degrees, above which a vertex is a corner
	// candidate. For a convex polygon the turn at a vertex is 180 minus the
	// interior angle, so this catches a right angle at 90 and a triangle's at
	// 120 while leaving a gently curving edge alone.
	CornerAngle float64

	// QuietAngle is the turn, in degrees, above which a neighbour counts as
	// another feature rather than as straight ground. Half of CornerAngle, so
	// that a staircase, whose turns arrive in a spread of similar sizes, marks off
	// its own corners, while a clean shape whose only turns are its corners does
	// not.
	QuietAngle float64

	// MinCornerGap is how much contour, in pixels, must lie on each side of a
	// corner before the shape is allowed to turn again. See Corners.
	MinCornerGap float64
}

// DefaultOptions classifies a right angle as a corner and requires 2px of
// boundary on either side of it before another turn counts as a feature.
func DefaultOptions() Options {
	return Options{CornerAngle: 80, QuietAngle: 40, MinCornerGap: 2}
}

func (o Options) withDefaults() Options {
	if o.CornerAngle <= 0 {
		o.CornerAngle = DefaultOptions().CornerAngle
	}
	if o.QuietAngle <= 0 {
		o.QuietAngle = DefaultOptions().QuietAngle
	}
	if o.MinCornerGap <= 0 {
		o.MinCornerGap = DefaultOptions().MinCornerGap
	}
	return o
}

// Result is an approximated contour: the loop with its closing point normalised
// away, and the indices of its real corners.
type Result struct {
	Loop    contour.Loop
	Corners []int
}

// Approximate normalises a loop and identifies its corners. The closing point
// that contour.Loop carries by convention is dropped, because it is a repeat
// rather than a shape: left in place it makes the seam look like a zero-length
// edge, which reads as "no turn" and hides the corner nearest to it.
func Approximate(l contour.Loop, o Options) Result {
	o = o.withDefaults()
	if len(l) >= 2 && l[0] == l[len(l)-1] {
		l = l[:len(l)-1]
	}
	return Result{Loop: l, Corners: Corners(l, o)}
}

// Corners returns the indices of the loop's real corners.
//
// A corner must be both sharp and isolated in scale. Sharpness alone is wrong,
// and the corpus shows why: the contour of a rasterised disc is 165 points long
// whose vertices turn by 0-10 degrees 52 times, by 40-50 degrees 72 times, and
// by 90-100 degrees 40 times. Those 40 near-right angles are not features of a
// circle, they are marching squares stepping along a diagonal boundary. A
// threshold that protects them, which is the obvious reading of "protect sharp
// corners", leaves the fitter 40 corners to work around and no smooth run longer
// than a couple of points.
//
// Sharpness combined with a quiet immediate neighbourhood is better but also
// wrong, in the other direction and for a reason worth spelling out. It passes
// every test on a finely sampled rectangle and then fails on the one the pipeline
// actually produces, because simplify drops collinear points: a rectangle arrives
// here as four points, and all four of its corners are adjacent, so none has a
// quiet neighbour and the rule dissolves all of them. A rule that only holds when
// the input is sampled more finely than the pipeline ever samples is a rule that
// will not fire.
//
// So isolation is measured in contour length rather than in neighbours. A corner
// needs MinCornerGap of boundary on each side before the shape turns again. The
// disc's steps are half a pixel to a pixel long, so every one of its near-right
// angles has another turn well inside the gap and is not a corner. A rectangle's
// corners have twenty to thirty pixels of straight edge around them and are.
// The price is a scale parameter, which is unavoidable: telling a corner from a
// tracing artefact is a question about size, and no function of local turn angles
// alone can answer it.
func Corners(l contour.Loop, o Options) []int {
	o = o.withDefaults()
	n := len(l)
	if n < 3 {
		return nil
	}
	turns := turnAngles(l)
	arc, total := arcLengths(l)
	var corners []int
	for i := 0; i < n; i++ {
		if math.IsNaN(turns[i]) || turns[i] < o.CornerAngle {
			continue
		}
		if gapBeforeFeature(turns, arc, total, i, +1, o) && gapBeforeFeature(turns, arc, total, i, -1, o) {
			corners = append(corners, i)
		}
	}
	return corners
}

// gapBeforeFeature walks from vertex i in direction dir and reports whether it
// reaches MinCornerGap of contour before meeting another feature.
//
// The distance to the next vertex is checked before that vertex's turn, and the
// order matters. A rectangle that simplify has reduced to four points has four
// adjacent corners, so testing the neighbour's angle first rejects all of them; but
// its edges are twenty to thirty pixels long, so the neighbour is already outside
// the gap and there was never any reason to ask what it turns at. A staircase is
// the mirror image: its steps are under a pixel, so the neighbour is well inside
// the gap, and asking there is exactly the question that answers it.
//
// Arc length is measured on the loop, not counted in vertices, because vertices
// are not comparable across shapes: a staircase and a rectangle can both arrive
// with four of them, and only their spacing separates a corner from a tracing
// artefact. A NaN turn comes from a repeated point and is skipped rather than
// treated as a feature, so a duplicate cannot disqualify the corner beside it.
func gapBeforeFeature(turns, arc []float64, total float64, i, dir int, o Options) bool {
	n := len(turns)
	for step := 1; step < n; step++ {
		at := (i + dir*step + n) % n
		if arcBetween(arc, total, i, at, dir) >= o.MinCornerGap {
			return true
		}
		if !quiet(turns[at], o) {
			return false
		}
	}
	return false
}

func quiet(turn float64, o Options) bool {
	return math.IsNaN(turn) || turn < o.QuietAngle
}

// arcBetween is the contour length from vertex i to vertex at in direction dir.
//
// The closing edge is counted, and that is not bookkeeping for its own sake: on a
// four-point square it is a ten-pixel edge, and leaving it out makes the step from
// vertex 0 back to vertex 3 measure zero. Vertex 0 then looks like it has nothing
// between it and a neighbour, and the corner there is rejected for sitting too
// close to the next one.
func arcBetween(arc []float64, total float64, i, at, dir int) float64 {
	if dir > 0 {
		if at >= i {
			return arc[at] - arc[i]
		}
		return total - arc[i] + arc[at]
	}
	if at <= i {
		return arc[i] - arc[at]
	}
	return total - arc[at] + arc[i]
}

// arcLengths is the cumulative contour length at each vertex, arc[i] being the
// distance from vertex 0 to vertex i, plus the total perimeter. The open loop's
// last cumulative length is not the perimeter, because the edge closing the loop
// is not in it.
func arcLengths(l contour.Loop) ([]float64, float64) {
	n := len(l)
	arc := make([]float64, n)
	for i := 1; i < n; i++ {
		arc[i] = arc[i-1] + l[i].Dist(l[i-1])
	}
	return arc, arc[n-1] + l[0].Dist(l[n-1])
}

// turnAngles is the turn at each vertex in degrees: 0 straight ahead, 180 a full
// reversal. A repeated point yields NaN, which reads as no turn and so never as a
// corner, which is right: marching squares can emit a repeated point and a
// repeated point is not a shape.
func turnAngles(l contour.Loop) []float64 {
	n := len(l)
	turns := make([]float64, n)
	for i := 0; i < n; i++ {
		in := l[i].Sub(l[(i-1+n)%n])
		out := l[(i+1)%n].Sub(l[i])
		a, b := math.Hypot(in.X, in.Y), math.Hypot(out.X, out.Y)
		if a == 0 || b == 0 {
			turns[i] = math.NaN()
			continue
		}
		c := (in.X*out.X + in.Y*out.Y) / (a * b)
		turns[i] = math.Acos(math.Max(-1, math.Min(1, c))) * 180 / math.Pi
	}
	return turns
}
