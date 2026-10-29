package quant

import (
	"image"
	"image/color"
	"testing"

	"github.com/elfeo/svg-proto/internal/pixelbuf"
)

func reduceImage(src image.Image, opts Options) *Palette {
	im := pixelbuf.FromImage(src)
	lab := im.Lab()
	n := im.W * im.H
	l := make([]float64, n)
	a := make([]float64, n)
	b := make([]float64, n)
	al := make([]float64, n)
	for i := 0; i < n; i++ {
		l[i], a[i], b[i] = lab.Pix[3*i], lab.Pix[3*i+1], lab.Pix[3*i+2]
		al[i] = float64(im.A[i]) / 255
	}
	return Reduce(&Input{W: im.W, H: im.H, L: l, A: a, B: b, Alpha: al}, opts)
}

func TestReduceHonoursK(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			src.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 4), G: uint8(y * 4), B: 128, A: 255})
		}
	}
	for _, k := range []int{2, 4, 8, 16, 32} {
		opts := Default()
		opts.K = k
		pal := reduceImage(src, opts)
		if pal.Len() > k {
			t.Errorf("K=%d: got %d colours, want at most %d", k, pal.Len(), k)
		}
		if pal.Len() < 2 {
			t.Errorf("K=%d: got %d colours, want at least 2", k, pal.Len())
		}
	}
}

func TestReduceNeverExceedsKEvenForFewColours(t *testing.T) {
	// The failure mode this guards: a 4-colour image asked for 16 colours must
	// not invent 12 more. Phantom palette entries would become phantom shapes.
	src := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			c := color.NRGBA{A: 255}
			if x < 4 {
				c.R = 255
			} else {
				c.B = 255
			}
			src.SetNRGBA(x, y, c)
		}
	}
	pal := reduceImage(src, Default())
	if pal.Len() > 4 {
		t.Errorf("got %d colours for a 2-colour image, want at most 4", pal.Len())
	}
}

func TestReduceIndexInRange(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			src.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 8), G: uint8(y * 8), B: uint8(x ^ y), A: 255})
		}
	}
	pal := reduceImage(src, Default())
	if len(pal.Index) != 32*32 {
		t.Fatalf("index length = %d, want %d", len(pal.Index), 32*32)
	}
	for i, v := range pal.Index {
		if int(v) >= pal.Len() {
			t.Fatalf("Index[%d] = %d, out of range for a %d-colour palette", i, v, pal.Len())
		}
	}
}

func TestReduceExactOnImagesWithinK(t *testing.T) {
	// If the image has K or fewer distinct colours, median cut must find them
	// exactly. Anything less is a quantisation bug, not a budget compromise.
	want := []color.NRGBA{
		{R: 255, A: 255}, {G: 255, A: 255}, {B: 255, A: 255},
		{R: 255, G: 255, A: 255}, {R: 0, G: 0, B: 0, A: 255},
	}
	src := image.NewNRGBA(image.Rect(0, 0, len(want)*4, 4))
	i := 0
	for y := 0; y < 4; y++ {
		for x := 0; x < len(want)*4; x++ {
			src.SetNRGBA(x, y, want[i%len(want)])
			i++
		}
	}
	opts := Default()
	opts.K = len(want)
	pal := reduceImage(src, opts)
	if pal.Len() != len(want) {
		t.Fatalf("got %d colours, want exactly %d", pal.Len(), len(want))
	}
	got := map[uint32]bool{}
	for i := 0; i < pal.Len(); i++ {
		r, g, b := pal.RGB(i)
		got[uint32(r)<<16|uint32(g)<<8|uint32(b)] = true
	}
	for _, c := range want {
		if !got[uint32(c.R)<<16|uint32(c.G)<<8|uint32(c.B)] {
			t.Errorf("colour %v missing from the palette", c)
		}
	}
}

func TestReduceIsDeterministic(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 40, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 40; x++ {
			src.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 6), G: uint8(y * 6), B: uint8(x * y % 251), A: 255})
		}
	}
	a := reduceImage(src, Default())
	b := reduceImage(src, Default())
	if a.Len() != b.Len() {
		t.Fatalf("palette length differs between runs: %d vs %d", a.Len(), b.Len())
	}
	for i := range a.Colors {
		if a.Colors[i] != b.Colors[i] {
			t.Fatalf("colour %d differs between runs: %d vs %d", i, a.Colors[i], b.Colors[i])
		}
	}
}

func TestReduceIgnoresFullyTransparentPixels(t *testing.T) {
	// Transparent pixels have no meaningful colour, so letting them vote would
	// drag the palette towards whatever RGB happened to be stored there.
	src := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			if x < 8 {
				src.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
			} else {
				// strong garbage colour, fully transparent
				src.SetNRGBA(x, y, color.NRGBA{R: 0, G: 255, B: 0, A: 0})
			}
		}
	}
	pal := reduceImage(src, Default())
	if pal.Rejected != 8*16 {
		t.Errorf("Rejected = %d, want %d", pal.Rejected, 8*16)
	}
	// The palette must be the opaque colour only.
	found := false
	for i := 0; i < pal.Len(); i++ {
		if r, g, b := pal.RGB(i); r == 255 && g == 0 && b == 0 {
			found = true
		}
	}
	if !found {
		t.Error("the opaque colour is missing from the palette")
	}
}

func TestReduceAllTransparentDoesNotPanic(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	pal := reduceImage(src, Default())
	if pal.Len() < 1 {
		t.Error("an all-transparent image still needs at least one palette entry")
	}
}

func TestRGBOutOfRangeIsBlack(t *testing.T) {
	p := &Palette{Colors: []uint8{1, 2, 3, 255}}
	if r, _, _ := p.RGB(-1); r != 0 {
		t.Errorf("RGB(-1) = %d, want 0", r)
	}
	if r, _, _ := p.RGB(1); r != 0 {
		t.Errorf("RGB(1) on a 1-colour palette = %d, want 0", r)
	}
}

func TestAlphaIsAlwaysOpaqueInThePalette(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			src.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 16), G: 0, B: 0, A: uint8(x * 16)})
		}
	}
	pal := reduceImage(src, Default())
	for i := 0; i < pal.Len(); i++ {
		if pal.Colors[4*i+3] != 255 {
			t.Fatalf("palette entry %d has alpha %d, want 255", i, pal.Colors[4*i+3])
		}
	}
}

// The floor's contract, tested directly rather than through an emergent property:
// a box holding fewer pixels than the floor is never chosen to be split again, so
// a run of negligible cells is walked once and then left alone.
//
// The end-to-end effect is measured by the corpus benchmark, where the badge's
// antialiased fringe goes from nine palette entries to one. This test pins the rule
// that produces it, which is the only part that can rot silently.
func TestAManagerableClusterOfNegligibleCellsIsWalkedOnlyOnce(t *testing.T) {
	// Eight large cells and eight tiny ones, laid out along lightness so the
	// first cut separates the two groups. The tiny group is then the only
	// multi-cell box left, and it is below the floor, so it must survive whole.
	// Without the floor it would be split seven more times and hand seven palette
	// entries to 32 pixels.
	const big, small = 8, 8
	cells := make([]cell, big+small)
	live := make([]int, 0, big+small)
	for i := range cells {
		n := 1000
		if i >= big {
			n = 4
		}
		l := float64(i) / float64(big+small)
		cells[i] = cell{n: n, nAlpha: float64(n), minL: l, maxL: l, sumL: l * float64(n)}
		live = append(live, i)
	}
	total := 0
	for _, ci := range live {
		total += cells[ci].n
	}
	floor := total / 100
	if floor > MinSplitPixels {
		floor = MinSplitPixels
	}
	if floor <= small*4 {
		t.Fatalf("fixture is wrong: floor %d does not exceed the %d-pixel small group", floor, small*4)
	}
	boxes := medianCut(live, cells, 16)

	// The eight large cells are 1000 pixels each, far above the floor, so each
	// earns an entry and the cut separating them is correct. The invariant is about
	// the small group: it must arrive as one box, not as eight.
	smallCells := 0
	for _, b := range boxes {
		n := 0
		for _, ci := range b {
			n += cells[ci].n
		}
		if n == small*4 {
			smallCells += len(b)
		}
	}
	if smallCells != small {
		t.Errorf("the %d-cell sub-floor cluster arrived as %d cells across the boxes, want %d in one box: it was subdivided",
			small, smallCells, small)
	}
	t.Logf("%d boxes: %d large cells separated, sub-floor cluster left whole", len(boxes), big)
}

// Raising K past the number of colours an image actually has must buy nothing
// rather than subdivide the image to fill the slots.
func TestKIsAnUpperBoundNotATarget(t *testing.T) {
	cols := []color.NRGBA{
		{R: 200, A: 255}, {G: 200, A: 255}, {B: 200, A: 255}, {R: 60, G: 60, B: 60, A: 255},
	}
	im := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			im.SetNRGBA(x, y, cols[(x/8+y/8)%len(cols)])
		}
	}
	for _, k := range []int{4, 8, 16} {
		pal := reduceImage(im, Options{K: k, Bits: DefaultBits, AlphaFloor: 1.0 / 255,
			Weighted: true})
		if pal.Len() > len(cols) {
			t.Errorf("K=%d produced %d entries for a %d-colour image", k, pal.Len(), len(cols))
		}
		if pal.Len() != len(cols) {
			t.Errorf("K=%d produced %d entries, want the %d the image has", k, pal.Len(), len(cols))
		}
	}
}

// A lone speck is a single histogram cell that the very first split separates, so
// no floor can stop it and none should try. It keeps its palette entry, and the
// encoder's MinArea is then free to decide the speck is not worth a path. An
// area-based rule deleted it inside the palette instead, where the caller has no
// knob, and silently.
func TestLoneSpeckKeepsItsEntrySoMinAreaCanDropTheRegion(t *testing.T) {
	im := image.NewNRGBA(image.Rect(0, 0, 20, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 20; x++ {
			im.SetNRGBA(x, y, color.NRGBA{A: 255})
		}
	}
	im.SetNRGBA(10, 10, color.NRGBA{R: 255, A: 255})
	im.SetNRGBA(11, 10, color.NRGBA{R: 255, A: 255})
	pal := reduceImage(im, Options{K: 16, Bits: DefaultBits, AlphaFloor: 1.0 / 255, Weighted: true})
	if pal.Len() != 2 {
		t.Errorf("palette has %d colours, want 2: a 2-pixel red dot on black was merged away", pal.Len())
	}
}

// The floor must never block a small image from reproducing its own colours.
// This is the case that a plain absolute floor breaks: a 27-pixel image whose
// three colours are 9 pixels each cannot be split at all.
func TestSmallImagesStillReduceExactly(t *testing.T) {
	cols := []color.NRGBA{
		{R: 255, A: 255}, {G: 255, A: 255}, {B: 255, A: 255},
	}
	im := image.NewNRGBA(image.Rect(0, 0, 3, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 3; x++ {
			im.SetNRGBA(x, y, cols[y])
		}
	}
	pal := reduceImage(im, Options{K: 3, Bits: DefaultBits, AlphaFloor: 1.0 / 255, Weighted: true})
	if pal.Len() != 3 {
		t.Errorf("palette has %d colours, want 3: the floor blocked the splits", pal.Len())
	}
}

// A one-pixel white line is 0,125% of a 40x20 image, under any relative area
// threshold, and a real part of the drawing. The palette must keep it so the
// encoder's MinArea can decide whether the region is worth a path; folding it
// inside the palette would delete it where the caller has no knob.
func TestThinDistinctColourKeepsItsEntry(t *testing.T) {
	im := image.NewNRGBA(image.Rect(0, 0, 40, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			im.SetNRGBA(x, y, color.NRGBA{A: 255})
		}
	}
	// A one-pixel white line on black: 0,125% of the image, which is exactly what a
	// relative area threshold swallows, and a real part of the drawing besides.
	for y := 0; y < 20; y++ {
		im.SetNRGBA(20, y, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	}
	pal := reduceImage(im, Options{K: 16, Bits: DefaultBits, AlphaFloor: 1.0 / 255,
		Weighted: true})
	if pal.Len() != 2 {
		t.Errorf("palette has %d colours, want 2: a 1px white line on black needs its own entry", pal.Len())
	}
}
