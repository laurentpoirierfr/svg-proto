// Package quant reduces an image to a fixed palette.
//
// Median cut (Heckbert 1980) over a 5-bit-per-channel histogram, but every
// decision that affects the result is taken in OKLab:
//
//   - boxes are split along OKLab axes, so the splits are perceptually even.
//     Splitting in sRGB is the classic bug: the blue-green axis is far wider in
//     sRGB than in perception, so a sRGB split spends boxes separating hues that
//     nobody can tell apart;
//   - a box's representative colour is the alpha-weighted mean in OKLab, then
//     converted back to sRGB. That lands inside the box's own gamut. Averaging
//     sRGB values does not, and can produce a colour that was never in the image.
//
// Two deliberate non-features:
//
//   - No dithering. Error diffusion injects high-frequency noise, and noise
//     vectorises catastrophically: thousands of one-pixel contours. For flat
//     regions it is also simply wrong. Dither-then-trace is a disaster; trace
//     the quantised image instead.
//   - No alpha in the palette. Alpha is carried per shape (fill-opacity), not as
//     a fourth palette dimension, which would multiply k by the number of alpha
//     levels for no benefit.
//
// The package takes plain slices rather than an image type, so it stays
// independently testable and so no stage depends on a decoder.
package quant

import (
	"math"
	"sort"

	"github.com/elfeo/svg-proto/internal/colorconv"
)

// DefaultBits is the histogram resolution per OKLab axis. 5 bits gives 32 768
// cells, far more resolution than any sane k, while keeping the build
// O(pixels) rather than O(pixels log pixels).
const DefaultBits = 5

// Input is the flat, per-pixel data the reduction needs. All slices are
// parallel and of length W*H.
type Input struct {
	W, H int
	// OKLab coordinates. L in [0,1], a and b roughly in [-0.5,0.5].
	L, A, B []float64
	// Alpha in [0,1], straight (non-premultiplied).
	Alpha []float64
}

func (in *Input) N() int { return in.W * in.H }

// Palette is a reduced image: one palette index per pixel, plus the colours.
type Palette struct {
	// Colors holds RGBA entries, 4 bytes each. A is always 255: alpha is not
	// part of the palette.
	Colors []uint8
	// Index is row-major, length W*H.
	Index []uint16
	W, H  int
	// Cells is how many histogram cells were non-empty before splitting, which
	// is a useful diagnostic: the ratio Cells/K says how much structure the
	// budget is fighting.
	Cells int
	// Merged counts palette entries that prune folded into a neighbour because
	// they carried too little of the image to deserve a slot.
	Merged int
	// Rejected counts pixels excluded by AlphaFloor.
	Rejected int
}

func (p *Palette) Len() int { return len(p.Colors) / 4 }

// RGB returns the palette entry i as sRGB.
func (p *Palette) RGB(i int) (r, g, b uint8) {
	if i < 0 || i >= p.Len() {
		return 0, 0, 0
	}
	return p.Colors[4*i], p.Colors[4*i+1], p.Colors[4*i+2]
}

// Options controls the reduction.
type Options struct {
	// K is the number of palette entries; below 2 is raised to 2.
	K int
	// Bits is the histogram resolution per OKLab axis.
	Bits int
	// AlphaFloor excludes pixels whose alpha is below it. Fully transparent
	// pixels carry no colour, and letting them vote drags the palette towards
	// whatever the encoder happened to store there.
	AlphaFloor float64
	// Weighted alpha-weights the histogram. A semi-transparent pixel should
	// influence the palette less than an opaque one.
	Weighted bool
	// MergeDelta is the OKLab distance below which two palette entries are
	// treated as the same colour, and the nearer one absorbs the other. It makes
	// K an upper bound rather than a target. Zero disables merging, which is only
	// interesting for reproducing the failure it fixes.
	MergeDelta float64
}

// MinSplitPixels is the smallest box medianCut will split: a box holding fewer
// pixels than this does not get a palette entry of its own.
//
// 64 is a judgement, and the measurement behind it is that it is roughly the
// smallest mark a viewer can pick out in context. It caps the floor rather than
// setting it, so that a small image is not blocked from reproducing its own
// colours exactly; see medianCut. It is not the same as the encoder's MinArea,
// which governs whether a traced region is worth emitting as geometry: this one
// stops a colour from being invented, that one stops a region from being drawn,
// and neither can do the other's job.
const MinSplitPixels = 64

// DefaultMergeDelta is the default for Options.MergeDelta, as a distance in the
// L-weighted metric 3*dL^2 + da^2 + db^2.
//
// The threshold is a redundancy test, not an area test, and the distinction is
// the whole point. An area threshold cannot tell a one-pixel antialiasing
// artefact from a one-pixel logo mark: they weigh the same, so whichever way it
// goes it is wrong for one of them. A colour threshold tells them apart without
// being told which is which, because an antialiasing artefact is by definition
// very close to the colour it fringes while a mark has a colour of its own.
//
// 0.02 sits just above the OKLab just-noticeable difference, so an entry is
// dropped only when folding it into its neighbour is imperceptible rather than
// merely small.
const DefaultMergeDelta = 0.02

// Default returns a sensible reduction: 16 colours, alpha-weighted, with
// visually redundant entries merged.
func Default() Options {
	return Options{K: 16, Bits: DefaultBits, AlphaFloor: 1.0 / 255, Weighted: true, MergeDelta: DefaultMergeDelta}
}

type cell struct {
	n                int
	nAlpha           float64
	sumL, sumA, sumB float64
	minL, maxL       float64
	minA, maxA       float64
	minB, maxB       float64
}

func (c *cell) centroid() (l, a, b float64) {
	if c.nAlpha <= 0 {
		return 0, 0, 0
	}
	return c.sumL / c.nAlpha, c.sumA / c.nAlpha, c.sumB / c.nAlpha
}

// histogram accumulates the OKLab cells the median cut runs on, skipping pixels
// below the alpha floor. It is separate from Reduce so that the box arithmetic can
// be tested against a known cell layout instead of only through its output.
func histogram(in *Input, opts Options) ([]cell, int) {
	side := 1 << opts.Bits
	scale := float64(side - 1)
	cells := make([]cell, side*side*side)
	nonEmpty := 0

	for i := 0; i < in.N(); i++ {
		if in.Alpha[i] < opts.AlphaFloor {
			continue
		}
		ci := cellIndex(in.L[i], in.A[i], in.B[i], side, scale)
		c := &cells[ci]
		if c.n == 0 {
			nonEmpty++
			c.minL, c.maxL = in.L[i], in.L[i]
			c.minA, c.maxA = in.A[i], in.A[i]
			c.minB, c.maxB = in.B[i], in.B[i]
		}
		w := 1.0
		if opts.Weighted {
			w = in.Alpha[i]
			if w <= 0 {
				w = 1e-6 // keep the cell alive without letting it dominate
			}
		}
		c.n++
		c.nAlpha += w
		c.sumL += in.L[i] * w
		c.sumA += in.A[i] * w
		c.sumB += in.B[i] * w
		c.minL = math.Min(c.minL, in.L[i])
		c.maxL = math.Max(c.maxL, in.L[i])
		c.minA = math.Min(c.minA, in.A[i])
		c.maxA = math.Max(c.maxA, in.A[i])
		c.minB = math.Min(c.minB, in.B[i])
		c.maxB = math.Max(c.maxB, in.B[i])
	}
	return cells, nonEmpty
}

// Reduce quantises the image to at most K colours.
func Reduce(in *Input, opts Options) *Palette {
	if opts.K < 2 {
		opts.K = 2
	}
	if opts.Bits <= 0 || opts.Bits > 8 {
		opts.Bits = DefaultBits
	}
	if opts.AlphaFloor < 0 {
		opts.AlphaFloor = 0
	}

	cells, nonEmpty := histogram(in, opts)
	side := 1 << opts.Bits
	scale := float64(side - 1)

	live := make([]int, 0, nonEmpty)
	for i := range cells {
		if cells[i].n > 0 {
			live = append(live, i)
		}
	}
	boxes := medianCut(live, cells, opts.K)
	boxes, merged := prune(boxes, cells, opts.MergeDelta)

	pal := &Palette{
		W: in.W, H: in.H, Cells: nonEmpty, Merged: merged,
		Index:  make([]uint16, in.N()),
		Colors: make([]uint8, 0, 4*len(boxes)),
	}
	for _, b := range boxes {
		var sL, sA, sB, sW float64
		for _, ci := range b {
			c := &cells[ci]
			sL += c.sumL
			sA += c.sumA
			sB += c.sumB
			sW += c.nAlpha
		}
		if sW <= 0 {
			pal.Colors = append(pal.Colors, 0, 0, 0, 255)
			continue
		}
		r, g, bl := colorconv.OKLabToRGB(sL/sW, sA/sW, sB/sW)
		pal.Colors = append(pal.Colors, r, g, bl, 255)
	}

	// Indexing is a table read, not a nearest-neighbour search: every histogram
	// cell already knows which box it landed in. That keeps the whole reduction
	// O(pixels) with no k factor.
	lookup := make([]uint16, len(cells))
	for bi, b := range boxes {
		for _, ci := range b {
			lookup[ci] = uint16(bi)
		}
	}
	for i := 0; i < in.N(); i++ {
		if in.Alpha[i] < opts.AlphaFloor {
			pal.Rejected++
			pal.Index[i] = 0
			continue
		}
		pal.Index[i] = lookup[cellIndex(in.L[i], in.A[i], in.B[i], side, scale)]
	}
	return pal
}

// medianCut repeatedly bisects the box with the largest OKLab volume along its
// longest axis, at the weighted median, until K boxes exist or no box can be
// split.
//
// A box holding fewer than MinSplitPixels pixels is not split at all, and that
// floor is the single most important number in this function.
//
// Without it, the palette is spent on whatever is hardest to compress rather than
// on whatever is most visible. A flat logo is a few large areas that each land in
// a single histogram cell, plus a thin antialiased fringe along every edge. The
// flat areas cannot be usefully split, so every split falls on the fringe, which
// is a chain of near-identical cells spread across a wide lightness range and
// therefore always has the largest volume. On testdata/corpus/alpha/badge.png
// seven of the ten splits went to boxes holding under 2.4% of the image, and the
// result was 9 of 11 palette entries for 130 pixels out of 7 744.
//
// The cost is not only a bad palette. Each entry becomes a band, and a band over
// a semi-transparent fringe is painted with a fill-opacity averaged across pixels
// of very different alpha, so stacking them smears the transparency the logo was
// drawn with. It is why that image scored 0.55 where runlength scored 0.03.
//
// The floor is the smaller of an absolute pixel count and a fraction of the
// image, because each of those alone is wrong in a way the other fixes.
//
// A pure fraction, 1/(2K), behaves correctly on a 128x128 badge and then falls
// apart on a 1024x1024 render of the same artwork, where "1.5% of the image" is
// 31 000 pixels: refusing to split a box that size coarsens every band until the
// contours turn into staircases, and logo/mark-scaled went from 2 896 points to
// 16 680, a 5.8x regression on what had been the best result in the corpus.
//
// A pure absolute count, 64, is the opposite failure: on a 27-pixel test image
// three colours of 9 pixels each it blocks the splits outright, so an image with
// three colours reduces to one. Recovering the exact colours of an image that
// already has few is not a budget compromise, it is correctness.
//
// Taking the smaller of the two keeps both properties. A large image is capped at
// MinSplitPixels in absolute terms, and a small one scales down so that exact
// reconstruction is never blocked. Note this only ever declines to *create* a
// box. Nothing is merged and no pixel is reassigned, so small features keep their
// colour and the encoder's MinArea stays in charge of whether they are worth
// drawing.
func medianCut(live []int, cells []cell, k int) [][]int {
	var n int
	for _, ci := range live {
		n += cells[ci].n
	}
	floor := n / 100
	if floor > MinSplitPixels {
		floor = MinSplitPixels
	}
	boxes := [][]int{live}
	for len(boxes) < k {
		bi, best := -1, math.Inf(-1)
		for i, b := range boxes {
			if len(b) < 2 {
				continue
			}
			var n int
			for _, ci := range b {
				n += cells[ci].n
			}
			if n < floor {
				continue
			}
			if v := volume(b, cells); v > best {
				best, bi = v, i
			}
		}
		if bi < 0 {
			break // no box is both splittable and big enough to deserve a slot
		}
		left, right := bisect(boxes[bi], cells)
		if len(left) == 0 || len(right) == 0 {
			break
		}
		next := make([][]int, 0, len(boxes)+1)
		next = append(next, boxes[:bi]...)
		next = append(next, left, right)
		next = append(next, boxes[bi+1:]...)
		boxes = next
	}
	return boxes
}

// prune merges away boxes that carry too little of the image to be worth a
// palette entry, repeatedly, until every survivor is big enough or one is left.
//
// Median cut is an area-blind splitter, and on flat artwork that is not a
// limitation, it is backwards. A hard-edged logo is a handful of large flat
// areas plus a thin antialiased fringe along every edge. The flat areas land in
// single histogram cells, so their boxes have zero extent and can never be the
// best split, while the fringe is a chain of near-identical cells strung across
// a wide lightness range and therefore always wins. Every split lands on the
// fringe. K entries come out as two real colours and a ladder of greys, and on
// testdata/corpus/alpha/badge.png that was 9 of 11 entries for 130 pixels out of
// 7 744.
//
// Population weighting in volume does not fix it, and the reason is worth
// recording: the flat colours rank at zero extent, so they are not merely losing
// the comparison, they are not in it. There is no weighting that makes a box of
// one cell worth splitting, because it is already exactly one colour.
//
// Merging afterwards is the only place the trade can be made honestly, because by
// then the question is no longer "where to cut" but "is this entry worth its
// slot". A box within MergeDelta of a neighbour is folded into it, repeatedly,
// smallest first, until none is left that close to anything.
//
// The test is colour distance and not area share, and an area test was tried
// first and is wrong in a way that reaches outside this package. Pruning by area
// silently deletes small features, which is MinArea's job, and it does it in the
// palette where the caller has no knob: a one-pixel white dot on black is 0.125%
// of a 40x20 image, so an area rule folds it into black and the geometry-level
// MinArea never sees a region to drop. Colour distance draws the line where the
// eye does instead: that dot is nowhere near black, so it keeps its entry and
// MinArea disposes of it exactly as configured.
//
// This never fires on the current corpus, and that is measured rather than
// assumed: Merged is 0 on all seven corpus rasters and on both photos, at
// MergeDelta 0.02 and at 0. The split floor already refuses to create the small
// box this would clean up. It is kept as a net because the corpus holds 7 cases
// where the plan calls for 30 to 60, and "never seen in seven" is not "never".
// See docs/phase2.md, section "prune ne fait rien, et c'est mesure".
func prune(boxes [][]int, cells []cell, delta float64) (kept [][]int, merged int) {
	if delta <= 0 || len(boxes) < 2 {
		return boxes, 0
	}
	alive := make([]bool, len(boxes))
	mass := make([]float64, len(boxes))
	rep := make([][3]float64, len(boxes))
	for i, b := range boxes {
		alive[i] = true
		var sL, sA, sB, sW float64
		for _, ci := range b {
			c := &cells[ci]
			sL += c.sumL
			sA += c.sumA
			sB += c.sumB
			sW += c.nAlpha
		}
		mass[i] = sW
		if sW > 0 {
			rep[i] = [3]float64{sL / sW, sA / sW, sB / sW}
		}
	}
	// Smallest first, so a run of near-identical entries collapses towards its
	// largest neighbour instead of each pairing off with the next one along.
	for {
		victim, best, bestD := -1, -1, delta*delta
		for i := range boxes {
			if !alive[i] {
				continue
			}
			near, nearD := -1, math.Inf(1)
			for j := range boxes {
				if !alive[j] || j == i {
					continue
				}
				if d := dist(rep[i], rep[j]); d < nearD {
					nearD, near = d, j
				}
			}
			// A box with no neighbour within delta is a real colour: keep it.
			if near < 0 || nearD > bestD {
				continue
			}
			if victim < 0 || mass[i] < mass[victim] || (mass[i] == mass[victim] && nearD < bestD) {
				victim, best, bestD = i, near, nearD
			}
		}
		if victim < 0 {
			break
		}
		// Copy rather than append in place. Every box median cut produced is a
		// sub-slice of one shared array, so appending to one of them can
		// overwrite a neighbour's cells, and the symptom is a colour that
		// silently absorbs another one: on alpha/badge the red square turned navy
		// because its box was clobbered. Bisect's b[:h] / b[h:] halves are
		// adjacent views of the same array, so this is the likely case, not a
		// remote one.
		joined := make([]int, 0, len(boxes[best])+len(boxes[victim]))
		joined = append(joined, boxes[best]...)
		joined = append(joined, boxes[victim]...)
		boxes[best] = joined
		mass[best] += mass[victim]
		alive[victim] = false
		merged++
	}
	kept = make([][]int, 0, len(boxes))
	for i, b := range boxes {
		if alive[i] {
			kept = append(kept, b)
		}
	}
	return kept, merged
}

// dist is the L-weighted squared OKLab distance, the same metric volume and
// bisect rank by, so "too close to tell apart" means the same thing everywhere in
// this file.
func dist(x, y [3]float64) float64 {
	return 3*sq(x[0]-y[0]) + sq(x[1]-y[1]) + sq(x[2]-y[2])
}

func sq(x float64) float64 { return x * x }

// volume ranks how much a box still needs splitting. L is weighted up because
// an error on lightness is far more visible than the same error on chroma, so
// splitting L first pays off more.
//
// The extent is weighted by the box's population, and that factor is not a
// refinement. Ranking on extent alone spends the palette on the least visible
// pixels in the image: an antialiased edge is a chain of near-identical cells
// strung across a wide lightness range, so it has an enormous extent and almost
// no area, and pure-extent ranking splits it again and again. On
// testdata/corpus/alpha/badge.png that handed 9 of the 11 palette entries to
// 130 edge pixels while the two colours covering 96% of the image got one each.
//
// The damage is not only a bad palette. Every extra entry becomes another band,
// and a band on a semi-transparent edge is painted with a fill-opacity averaged
// over pixels of wildly different alpha, so stacking them smears the
// transparency: dE 0.0295 with the edge collapsed, 0.3337 as shipped.
//
// Population times extent is the standard fix and it lands where it should: the
// flat majority colours split first because that is where the quantisation error
// actually is, and the edge chain gets the one or two entries it needs to avoid
// a visible fringe.
func volume(b []int, cells []cell) float64 {
	var minL, maxL, minA, maxA, minB, maxB float64 = math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	var mass float64
	for _, ci := range b {
		c := &cells[ci]
		minL, maxL = math.Min(minL, c.minL), math.Max(maxL, c.maxL)
		minA, maxA = math.Min(minA, c.minA), math.Max(maxA, c.maxA)
		minB, maxB = math.Min(minB, c.minB), math.Max(maxB, c.maxB)
		mass += c.nAlpha
	}
	return mass * (3*(maxL-minL) + (maxA - minA) + (maxB - minB))
}

func bisect(b []int, cells []cell) (left, right []int) {
	var minL, maxL, minA, maxA, minB, maxB float64 = math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	for _, ci := range b {
		l, a, bb := cells[ci].centroid()
		minL, maxL = math.Min(minL, l), math.Max(maxL, l)
		minA, maxA = math.Min(minA, a), math.Max(maxA, a)
		minB, maxB = math.Min(minB, bb), math.Max(maxB, bb)
	}
	axis, extent := 0, maxL-minL
	if e := maxA - minA; e > extent {
		axis, extent = 1, e
	}
	if e := maxB - minB; e > extent {
		axis, extent = 2, e
	}
	if extent <= 0 {
		// All centroids coincide. Split evenly anyway so K stays reachable; the
		// duplicate entries are harmless and the diagnostic Cells/K exposes it.
		h := len(b) / 2
		if h == 0 {
			return nil, b
		}
		return b[:h], b[h:]
	}
	proj := func(ci int) float64 {
		l, a, bb := cells[ci].centroid()
		switch axis {
		case 0:
			return l
		case 1:
			return a
		default:
			return bb
		}
	}
	sort.SliceStable(b, func(i, j int) bool { return proj(b[i]) < proj(b[j]) })

	total := 0.0
	for _, ci := range b {
		total += cells[ci].nAlpha
	}
	var acc float64
	cut := len(b) / 2
	for i, ci := range b {
		acc += cells[ci].nAlpha
		if acc >= total/2 {
			cut = i + 1
			break
		}
	}
	if cut <= 0 {
		cut = 1
	}
	if cut >= len(b) {
		cut = len(b) - 1
	}
	return b[:cut], b[cut:]
}

// cellIndex maps an OKLab triple to a histogram cell. L in [0,1] and a/b
// roughly in [-0.5,0.5] both land inside the cube after the +0.5 shift; the
// clamps only guard against out-of-gamut input.
func cellIndex(l, a, b float64, side int, scale float64) int {
	li := clampIdx(int((l+0.5)*scale), side)
	ai := clampIdx(int((a+0.5)*scale), side)
	bi := clampIdx(int((b+0.5)*scale), side)
	return (li*side+ai)*side + bi
}

func clampIdx(i, side int) int {
	if i < 0 {
		return 0
	}
	if i >= side {
		return side - 1
	}
	return i
}
