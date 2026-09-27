// Package contour extracts iso-lines from a scalar field.
//
// # Marching squares, and the two cases that matter
//
// The naive 16-case table is only correct for 14 of the 16 cases. Cases 5 and 10
// are *ambiguous*: the four corners alternate above and below the threshold, and
// the cell can be resolved in two different ways — two separate corners, or one
// band running through the middle. Both readings are consistent with the four
// corner samples, and they give completely different topology.
//
// Picking the wrong one is the classic source of "the logo has a mysterious hole
// in it" bugs. This package uses the **asymptotic decider**: compare the bilinear
// saddle value at the cell centre against the threshold. If the saddle is above
// the threshold, the two corners are connected; otherwise the band is. That is a
// decision, not a heuristic, and it is what makes the topology stable under
// small changes to the threshold.
//
// # Sub-pixel by construction
//
// Crossing points are linearly interpolated along cell edges, so a straight
// intensity ramp produces a perfectly straight iso-line, not a staircase. The
// error of linear interpolation along an edge is second order, which is well
// below the error the later simplification stage introduces anyway.
//
// # Output is geometry, not SVG
//
// Loops come out as plain coordinate slices. Nothing here knows about paths,
// fills or fill rules; that is [internal/encode]'s problem.
package contour

import (
	"math"
	"math/bits"
	"sort"

	"github.com/elfeo/svg-proto/internal/field"
)

// Pt is a point in field-node coordinates. Node (i,j) sits at (i,j), so the
// origin is the top-left sample and y grows downwards, matching image order.
type Pt struct{ X, Y float64 }

func (a Pt) sub(b Pt) Pt      { return Pt{a.X - b.X, a.Y - b.Y} }
func (a Pt) add(b Pt) Pt      { return Pt{a.X + b.X, a.Y + b.Y} }
func (a Pt) mul(s float64) Pt { return Pt{a.X * s, a.Y * s} }
func (a Pt) dist(b Pt) float64 {
	return math.Hypot(a.X-b.X, a.Y-b.Y)
}

// Loop is a closed contour. The first point is repeated as the last one, so
// closure is a plain equality check and Length covers the whole perimeter. The
// encoder emits M p0 L p1 ... L p[n-2] Z, dropping the duplicate.
type Loop []Pt

// Options configures extraction.
type Options struct {
	// Level is the iso-value to extract. The field should be normalised to [0,1]
	// first, so this reads as a fraction.
	Level float64
	// JoinTolerance is how far apart two endpoints may be and still be considered
	// the same point when chaining segments into loops. Marching squares on a
	// shared edge produces bit-identical coordinates, so the only reason this is
	// not zero is to absorb the last-bit float differences of a value computed
	// from the two neighbouring cells.
	JoinTolerance float64
	// MinPoints drops loops with fewer points. A 3-point loop is a triangle and
	// legitimate; a degenerate 2-point one is a seam artefact.
	MinPoints int
}

// DefaultOptions returns settings for a normalised field.
func DefaultOptions() Options {
	return Options{Level: 0.5, JoinTolerance: 1e-9, MinPoints: 3}
}

// Corner order throughout: 0 = top-left, 1 = top-right, 2 = bottom-right,
// 3 = bottom-left. Cell edge e joins corner e to corner (e+1)%4, so edge 0 is the
// top, 1 the right, 2 the bottom, 3 the left.

// Trace extracts the iso-line at o.Level from f, returning closed loops.
//
// # The semantics
//
// What comes back is the boundary of the region **{v >= Level}**, closed, in
// original image coordinates. The region, not the iso-line: a level set that runs
// off the edge of the image is an open arc, and an open arc cannot be filled.
//
// # Why the field is padded
//
// Marching squares on the bare field returns open polylines for every region that
// touches the frame, which is most of them: a sky background, a white margin, a
// photo edge. So the field is padded by one node on each side, filled with a
// value strictly *below* the level, which forces every region to close. The
// padding is subtracted from the output coordinates afterwards, so the shape
// reaches half a node outside the image, and the root viewBox clips it. Without
// this, a full-bleed background either disappears or needs a fake frame drawn
// around it.
//
// Padding *below* rather than above is what makes this correct: it guarantees
// "the region is a closed subset", never "the complement is". A closed
// complement would be wrong for the encoder, which paints the region itself.
func Trace(f *field.Field, o Options) []Loop {
	// A field needs four corners to have a cell, so anything smaller has no
	// iso-line to find. Returning nil rather than panicking matters because the
	// encoder calls this on every threshold of every image, including 1x1 icons.
	if f.W < 2 || f.H < 2 {
		return nil
	}
	padded := padBelow(f, o.Level)
	segs := segments(padded, padded.W, padded.H, o.Level)
	loops, _ := chain(segs, o)
	// Undo the padding: padded node (i,j) is original node (i-1, j-1).
	for _, l := range loops {
		for k := range l {
			l[k].X--
			l[k].Y--
		}
	}
	return loops
}

// padBelow returns a copy of f with a one-node border filled strictly below the
// level, so that every {v >= level} region becomes a closed set.
//
// The closure edge lands just outside the frame and is clipped away by the root
// viewBox, which is why the exact padding constant does not matter. It does have
// one visible consequence worth knowing about: at the four corners the closure
// runs diagonally instead of square, at a position set by the padding value. All
// four of those diagonals lie in the quadrant outside the image, so they are
// clipped and never rendered. The *interior* boundary, which is what shows, is
// interpolated between real samples and is therefore exact.
//
// Two alternatives were tried and rejected. Mirroring the border about the level
// (border = 2*level - interior) squares the corners, but it also flips the
// border to the opposite side of the level from its neighbour, so a full-bleed
// region sprouts a second contour along the opposite frame edge. Replicating the
// border leaves the outermost edge open, which is the problem padding exists to
// solve.
func padBelow(f *field.Field, level float64) *field.Field {
	w, h := f.W+2, f.H+2
	p := field.New(w, h)
	below := level - 1
	for i := range p.V {
		p.V[i] = below
	}
	for y := 0; y < f.H; y++ {
		copy(p.V[(y+1)*w+1:(y+1)*w+1+f.W], f.V[y*f.W:(y+1)*f.W])
	}
	return p
}

// segments walks the cells and returns the raw segments.
func segments(f *field.Field, w, h int, level float64) []seg {
	var out []seg
	for y := 0; y < h-1; y++ {
		for x := 0; x < w-1; x++ {
			// Corner values, clockwise from the top-left.
			v0 := f.At(x, y)
			v1 := f.At(x+1, y)
			v2 := f.At(x+1, y+1)
			v3 := f.At(x, y+1)

			// Bit i is set when corner i is at or above the level. A node exactly
			// at the level counts as inside, so {v >= level} is what gets traced.
			var code int
			if v0 >= level {
				code |= 1 << 0
			}
			if v1 >= level {
				code |= 1 << 1
			}
			if v2 >= level {
				code |= 1 << 2
			}
			if v3 >= level {
				code |= 1 << 3
			}
			if code == 0 || code == 0b1111 {
				continue // wholly one side
			}

			// Interpolated crossing points, one per cell edge. Edge e joins corner
			// e to corner (e+1)%4, so edge 0 is the top, 1 the right, 2 the bottom,
			// 3 the left.
			var cp [4]Pt
			// Edge 0, the top, from v0 to v1.
			cp[0] = lerpEdge(Pt{float64(x), float64(y)}, Pt{float64(x + 1), float64(y)}, v0, v1, level)
			// Edge 1, the east side, from v1 to v2.
			cp[1] = lerpEdge(Pt{float64(x + 1), float64(y)}, Pt{float64(x + 1), float64(y + 1)}, v1, v2, level)
			// Edge 2, the south side, from v2 to v3.
			cp[2] = lerpEdge(Pt{float64(x + 1), float64(y + 1)}, Pt{float64(x), float64(y + 1)}, v2, v3, level)
			// Edge 3, the west side, from v3 to v0.
			cp[3] = lerpEdge(Pt{float64(x), float64(y + 1)}, Pt{float64(x), float64(y)}, v3, v0, level)

			// The case table is *derived*, not written out by hand.
			//
			// A hand-written 16-entry table is the traditional way to write
			// marching squares, and it is exactly the kind of code that looks
			// right: a mistake in one of the fourteen unambiguous cases produces
			// garbage contours that still close up, so nothing crashes and the
			// bug survives. It happened here. The rule is one line instead:
			//
			//     edge e is crossed  <=>  corner e and corner (e+1)%4 differ
			//
			// Two crossed edges means one arc between them. Four crossed edges is
			// the ambiguous saddle, where the arc placement needs a decision.
			crossed := 0
			for e := 0; e < 4; e++ {
				if (code>>e)&1 != (code>>((e+1)%4))&1 {
					crossed |= 1 << e
				}
			}
			switch bits.OnesCount8(uint8(crossed)) {
			case 2:
				e0, e1 := edgePair(crossed)
				if code&(code-1) == 0 {
					// A single corner inside: this is a corner of the region, and
					// the sharp one is the cell centre, not the chord between the
					// two edge midpoints.
					//
					// With the centred-sample convention a sample at (x,y) owns
					// the square [x-0.5,x+0.5] x [y-0.5,y+0.5], so a lone inside
					// sample covers exactly the quarter of this cell bounded by
					// the two crossing points and the cell centre. Its boundary is
					// therefore two half-edges meeting at a right angle there, and
					// the straight diagonal cuts the corner off, shrinking every
					// region by 0.5px on each side. On a hard-edged square that
					// turned an 8x8 region into 63.5 square pixels.
					//
					// The cost is one extra point per real corner, which is exactly
					// the point budget a square corner has to spend.
					centre := Pt{float64(x) + 0.5, float64(y) + 0.5}
					out = append(out, seg{cp[e0], centre}, seg{centre, cp[e1]})
					break
				}
				out = append(out, seg{cp[e0], cp[e1]})
			case 4:
				// Ambiguous: the four corners alternate, and the cell resolves
				// either into a band through the middle or into two isolated
				// corners. Both readings fit the four samples.
				//
				// The asymptotic decider settles it: compare the bilinear saddle
				// at the cell centre against the level. If the centre is on the
				// inside, the two inside corners are joined through the middle and
				// the two *outside* corners are the isolated ones, so the arcs cut
				// them off. If the centre is on the outside, the roles swap.
				//
				// Getting this backwards is invisible on symmetric inputs and
				// turns a ring into a blob on asymmetric ones.
				inside := code
				if saddle(v0, v1, v2, v3) >= level {
					inside ^= 0b1111
				}
				for c := 0; c < 4; c++ {
					if (inside>>c)&1 == 0 {
						// An arc cutting off corner c spans the two edges incident
						// to it: edge c and edge (c+3)%4.
						out = append(out, seg{cp[c], cp[(c+3)%4]})
					}
				}
			}
		}
	}
	return out
}

// edgePair returns the two edge indices set in a 4-bit mask, low first.
func edgePair(mask int) (int, int) {
	for e := 0; e < 4; e++ {
		if mask&(1<<e) != 0 {
			for f := e + 1; f < 4; f++ {
				if mask&(1<<f) != 0 {
					return e, f
				}
			}
		}
	}
	return 0, 0
}

// lerpEdge finds where the threshold is crossed along one cell edge by linear
// interpolation. Returns the midpoint if the edge is degenerate, which can only
// happen if both endpoints sit exactly on the level.
func lerpEdge(a, b Pt, va, vb, level float64) Pt {
	d := vb - va
	if d == 0 {
		return a.mul(0.5).add(b.mul(0.5))
	}
	t := (level - va) / d
	// Clamp: a value within one ulp of the level on the "wrong" side must not
	// throw the crossing point outside the edge.
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	return a.add(b.sub(a).mul(t))
}

// saddle returns the value of the bilinear interpolant at the cell centre, which
// is the average of the four corners. This is the asymptotic decider: if the
// centre is above the level, the two above-corners belong to the same region.
func saddle(v0, v1, v2, v3 float64) float64 {
	return (v0 + v1 + v2 + v3) / 4
}

// ---------------------------------------------------------------------------
// chaining
// ---------------------------------------------------------------------------

type seg struct{ a, b Pt }

// chain links segments into closed loops.
//
// Marching squares emits each cell's segment with an arbitrary orientation, and
// neighbouring cells do not agree on it, so the walk cannot assume a segment
// starts where the previous one ended. It must match on *either* endpoint and
// continue from the other one. Getting this wrong truncates the walk after a few
// edges and the loop comes back unclosed, which is exactly what the closing-edge
// assertion in the tests is there to catch.
//
// The search uses a spatial hash, so the whole pass is O(n). The alternative,
// scanning for the nearest endpoint, is O(n^2) and would dominate the runtime of
// the entire tracer on a large image.
func chain(segs []seg, o Options) ([]Loop, int) {
	if len(segs) == 0 {
		return nil, 0
	}
	tol := o.JoinTolerance
	if tol <= 0 {
		tol = 1e-9
	}
	// Quantise to a bucket size derived from the tolerance, and search the 3x3
	// neighbourhood, so a tolerance spanning more than one cell still finds its
	// partner.
	cell := tol
	key := func(p Pt) [2]int64 {
		return [2]int64{int64(math.Floor(p.X / cell)), int64(math.Floor(p.Y / cell))}
	}

	// buckets maps an endpoint key to the segments that touch it.
	buckets := map[[2]int64][]int{}
	for i, s := range segs {
		for _, p := range [2]Pt{s.a, s.b} {
			k := key(p)
			if !contains(buckets[k], i) {
				buckets[k] = append(buckets[k], i)
			}
		}
	}

	used := make([]bool, len(segs))
	var loops []Loop
	dropped := 0
	// A loop needs at least three distinct points to enclose area, plus the
	// repeated closing point.
	minPts := o.MinPoints
	if minPts < 3 {
		minPts = 3
	}

	// Deterministic order: start from the lowest available segment index.
	for start := 0; start < len(segs); start++ {
		if used[start] {
			continue
		}
		used[start] = true
		loop := Loop{segs[start].a, segs[start].b}
		cur := segs[start].b
		closed := false

		// Bounded by the segment count: every step consumes one, and a
		// configuration where a threshold lands exactly on a plateau can leave an
		// endpoint with two partners, so an unbounded walk could spin.
		for steps := 0; steps < len(segs); steps++ {
			if near(cur, loop[0], tol) {
				closed = true
				break
			}
			i, other, ok := partner(cur, key, buckets, segs, used, tol)
			if !ok {
				break // ran off the end of an open chain
			}
			used[i] = true
			loop = append(loop, other)
			cur = other
		}

		// Only closed walks become loops. An open walk means the walk hit a
		// vertex with more than two arcs and no way to pick a successor, which
		// happens when a node sits exactly on the level. Emitting it would put a
		// subpath with a visible gap in the SVG, so it is dropped instead. The
		// encoder cannot reach that state, because a threshold is the midpoint of
		// two distinct palette values and no sample can land on a midpoint.
		if !closed {
			dropped++
			continue
		}
		// The walk stopped because it got within tolerance of the start, not
		// because it landed on it exactly, so store the start itself as the
		// closing point. The two differ by at most JoinTolerance and the polyline
		// is identical to within that, and in exchange closure is exact equality
		// everywhere instead of a tolerance comparison.
		//
		// Keeping the first point as the last one makes a closed loop recognisable
		// by inspection and lets Length count the full perimeter. The encoder
		// drops the duplicate and closes with Z instead; see Loop.
		// The two differ by at most JoinTolerance, so this snaps rather than
		// appends: appending would leave a zero-length closing segment.
		loop[len(loop)-1] = loop[0]
		if len(loop) >= minPts+1 {
			loops = append(loops, loop)
		}
	}

	// Raster order by first point, so the output does not depend on map iteration
	// order.
	sort.SliceStable(loops, func(i, j int) bool {
		a, b := loops[i][0], loops[j][0]
		if a.Y != b.Y {
			return a.Y < b.Y
		}
		return a.X < b.X
	})
	return loops, dropped
}

func contains(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// partner finds an unused segment with an endpoint at cur, and returns the index
// of that segment together with the endpoint to continue from.
func partner(cur Pt, key func(Pt) [2]int64, buckets map[[2]int64][]int,
	segs []seg, used []bool, tol float64) (int, Pt, bool) {
	k := key(cur)
	for dy := int64(-1); dy <= 1; dy++ {
		for dx := int64(-1); dx <= 1; dx++ {
			for _, i := range buckets[[2]int64{k[0] + dx, k[1] + dy}] {
				if used[i] {
					continue
				}
				// Continue from whichever end is *not* at cur.
				if near(segs[i].a, cur, tol) {
					return i, segs[i].b, true
				}
				if near(segs[i].b, cur, tol) {
					return i, segs[i].a, true
				}
			}
		}
	}
	return 0, Pt{}, false
}

func near(a, b Pt, tol float64) bool {
	return math.Abs(a.X-b.X) <= tol && math.Abs(a.Y-b.Y) <= tol
}

// ---------------------------------------------------------------------------
// measurements
// ---------------------------------------------------------------------------

// Length returns the polyline length.
func (l Loop) Length() float64 {
	if len(l) < 2 {
		return 0
	}
	total := 0.0
	for i := 1; i < len(l); i++ {
		total += l[i-1].dist(l[i])
	}
	return total
}

// Area returns the signed area (shoelace). Positive means clockwise in image
// coordinates, where y grows downwards. The magnitude is what matters for
// filtering speckle; the sign is what the encoder needs to orient an outer
// boundary against a hole.
func (l Loop) Area() float64 {
	if len(l) < 3 {
		return 0
	}
	a := 0.0
	for i, p := range l {
		q := l[(i+1)%len(l)]
		a += p.X*q.Y - q.X*p.Y
	}
	return a / 2
}

// BBox is the axis-aligned bounding box of a loop.
func (l Loop) BBox() (minX, minY, maxX, maxY float64) {
	if len(l) == 0 {
		return
	}
	minX, minY = l[0].X, l[0].Y
	maxX, maxY = minX, minY
	for _, p := range l[1:] {
		minX = math.Min(minX, p.X)
		minY = math.Min(minY, p.Y)
		maxX = math.Max(maxX, p.X)
		maxY = math.Max(maxY, p.Y)
	}
	return
}

// Centroid returns the area centroid, or the vertex average for a degenerate
// loop. Used to order nested loops outside-in, and to label a region.
func (l Loop) Centroid() Pt {
	a := l.Area()
	if math.Abs(a) < 1e-12 {
		if len(l) == 0 {
			return Pt{}
		}
		s := Pt{}
		for _, p := range l {
			s = s.add(p)
		}
		return s.mul(1 / float64(len(l)))
	}
	cx, cy := 0.0, 0.0
	for i, p := range l {
		q := l[(i+1)%len(l)]
		cross := p.X*q.Y - q.X*p.Y
		cx += (p.X + q.X) * cross
		cy += (p.Y + q.Y) * cross
	}
	return Pt{cx / (6 * a), cy / (6 * a)}
}

// Contains reports whether p is inside the loop, by ray casting to +infinity on
// the x axis. Half-open crossing rule, so a vertex hit counts once.
func (l Loop) Contains(p Pt) bool {
	inside := false
	n := len(l)
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		yi, yj := l[i].Y, l[j].Y
		if (yi > p.Y) == (yj > p.Y) {
			continue
		}
		x := l[i].X + (p.Y-yi)/(yj-yi)*(l[j].X-l[i].X)
		if p.X < x {
			inside = !inside
		}
	}
	return inside
}

// TotalStats aggregates a set of loops, for budget reporting.
type TotalStats struct {
	Loops    int
	Points   int
	Length   float64
	Area     float64
	MinArea  float64
	MaxArea  float64
	ClosedOK bool
}

// Stats summarises loops.
// Stats summarizes a set of loops. ClosedOK is computed, not assumed: a caller
// that trusts it to be true must be able to, because an unclosed loop renders as
// a path with a visible gap.
func Stats(loops []Loop) TotalStats {
	st := TotalStats{ClosedOK: true, MinArea: math.Inf(1)}
	for _, l := range loops {
		st.Loops++
		st.Points += len(l)
		st.Length += l.Length()
		a := math.Abs(l.Area())
		st.Area += a
		st.MinArea = math.Min(st.MinArea, a)
		st.MaxArea = math.Max(st.MaxArea, a)
		if len(l) < 4 || l[0] != l[len(l)-1] {
			st.ClosedOK = false
		}
	}
	if st.Loops == 0 {
		st.MinArea = 0
	}
	return st
}
