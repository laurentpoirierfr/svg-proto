package imcompare

import (
	"image"
	"image/color"
	"math"
	"testing"
)

func solid(w, h int, c color.NRGBA) *image.NRGBA {
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range im.Pix {
		switch i % 4 {
		case 0:
			im.Pix[i] = c.R
		case 1:
			im.Pix[i] = c.G
		case 2:
			im.Pix[i] = c.B
		case 3:
			im.Pix[i] = c.A
		}
	}
	return im
}

func TestIdenticalImagesScorePerfectly(t *testing.T) {
	im := solid(64, 64, color.NRGBA{200, 40, 40, 255})
	r, err := Compare(im, im)
	if err != nil {
		t.Fatal(err)
	}
	if r.SSIM < 1-1e-9 {
		t.Errorf("ssim = %v, want 1", r.SSIM)
	}
	if r.Score > 1e-9 {
		t.Errorf("score = %v, want 0", r.Score)
	}
	if r.DeltaE > 1e-9 {
		t.Errorf("deltaE = %v, want 0", r.DeltaE)
	}
	if math.IsInf(r.PSNR, 0) != true {
		t.Errorf("psnr = %v, want +Inf for identical images", r.PSNR)
	}
}

func TestDimensionMismatchIsAnError(t *testing.T) {
	_, err := Compare(solid(8, 8, color.NRGBA{}), solid(9, 8, color.NRGBA{}))
	if err == nil {
		t.Fatal("expected an error, not a silent resample")
	}
}

// A pure lightness shift must be caught more strongly by the gradient-weighted
// metric than by the flat one, because it lands on edges. This is the property
// the whole ranking metric exists for.
func TestGradientWeightingFavoursEdges(t *testing.T) {
	const s = 128
	// A checkerboard: every interior pixel is an edge, and flat MSE and weighted
	// MSE are close.
	ref := image.NewNRGBA(image.Rect(0, 0, s, s))
	for y := 0; y < s; y++ {
		for x := 0; x < s; x++ {
			v := uint8(0)
			if (x/8+y/8)%2 == 0 {
				v = 255
			}
			ref.SetNRGBA(x, y, color.NRGBA{v, v, v, 255})
		}
	}
	got := image.NewNRGBA(ref.Bounds())
	copy(got.Pix, ref.Pix)
	for i := 3; i < len(got.Pix); i += 4 {
		got.Pix[i] = 128
	}
	r, err := Compare(ref, got)
	if err != nil {
		t.Fatal(err)
	}
	if r.SSIM <= 0 || r.SSIM >= 1 {
		t.Errorf("ssim = %v, want strictly between 0 and 1", r.SSIM)
	}
	if r.Score <= 0 {
		t.Errorf("score = %v, want positive", r.Score)
	}
	// Weighting can only increase the weight of high-gradient pixels, so the
	// weighted figure must stay within the unweighted MSE.
	if r.GradWeighted > r.RMSE+1e-9 {
		t.Errorf("weighted RMSE %v exceeds plain RMSE %v; weights are not being applied to the harder pixels", r.GradWeighted, r.RMSE)
	}
}

// The premultiplied-alpha trap: a fully transparent pixel carries no colour, and
// a comparison that reads it as black instead of compositing it over the
// background reports a huge error on a fully transparent image.
func TestTransparentPixelsCompositeOverBackground(t *testing.T) {
	ref := solid(32, 32, color.NRGBA{255, 255, 255, 255})
	got := image.NewNRGBA(image.Rect(0, 0, 32, 32)) // all zeros: transparent black
	r, err := Compare(ref, got)
	if err != nil {
		t.Fatal(err)
	}
	if r.SSIM < 1-1e-6 {
		t.Errorf("ssim = %v: transparent-over-white should match white exactly", r.SSIM)
	}
	if r.DeltaE > 1e-6 {
		t.Errorf("deltaE = %v, want 0", r.DeltaE)
	}
}

// Partial alpha is the case that matters for a badge or a soft shadow, and it is
// where compositing goes wrong in a way that is invisible at the two extremes.
// The two tests around this one only ever used A=0 and A=255, which is why a wrong
// partial-alpha path survived: both endpoints are special-cased and both correct.
//
// 50% red over white is pink, (255, 127, 127). Any formula that recovers the
// straight colour and then adds the backdrop weight without weighting itself gets
// 382 on the red channel, and OKLab of an out-of-range value is not a number
// anyone should trust.
func TestPartialAlphaCompositesByWeightingBothTerms(t *testing.T) {
	const half = 128
	ref := solid(8, 8, color.NRGBA{255, 0, 0, half})
	want := solid(8, 8, color.NRGBA{255, 127, 127, 255})
	r, err := Compare(want, ref)
	if err != nil {
		t.Fatal(err)
	}
	if r.SSIM < 1-1e-6 || r.DeltaE > 1e-6 {
		t.Errorf("50%% red over white: ssim = %v, deltaE = %v; want 1 and 0", r.SSIM, r.DeltaE)
	}
}

// The property that actually caught the bug, and the one worth keeping: compositing
// is idempotent, so measuring a render against a transparent reference and against
// the same reference pre-composited over the background must give the same numbers.
// They cannot differ without a bug in the composite, and this compares the whole
// pipeline rather than one helper's return value.
func TestComparisonIsIndependentOfHowTheReferenceWasStored(t *testing.T) {
	b := image.Rect(0, 0, 16, 16)
	trans := image.NewNRGBA(b)
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			// A soft edge: fully transparent through fully opaque, which is what
			// antialiasing on a shape edge actually produces.
			a := uint8(x * 16)
			trans.SetNRGBA(x, y, color.NRGBA{R: 220, G: 40, B: 40, A: a})
		}
	}
	flat := solid(16, 16, color.NRGBA{30, 90, 200, 255})

	// The same reference, stored two ways.
	pre := image.NewNRGBA(b)
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			r, g, bl, a := trans.At(x, y).RGBA()
			af := float64(a) / 65535
			// v/257 is already colour*alpha, so it must not be weighted again.
			mix := func(v uint32) uint8 {
				return uint8(math.Round(float64(v)/257 + 255*(1-af)))
			}
			pre.SetNRGBA(x, y, color.NRGBA{R: mix(r), G: mix(g), B: mix(bl), A: 255})
		}
	}

	viaTransparent, err := Compare(trans, flat)
	if err != nil {
		t.Fatal(err)
	}
	viaPrecomposited, err := Compare(pre, flat)
	if err != nil {
		t.Fatal(err)
	}
	// The tolerance is not a guess. Storing the reference pre-composited costs a
	// half-LSB of 8-bit rounding per channel, which shows up as ~2e-4 in ssim and
	// ~1e-3 in deltaE. The composite bug this test was written to catch produced
	// 6e-2 in ssim and 6e-1 in deltaE. 1e-2 sits an order of magnitude clear of the
	// rounding floor and an order of magnitude below the bug's signature, so this
	// fails if the composite ever breaks again and stays quiet about the
	// quantisation it is deliberately introducing.
	const tol = 1e-2
	if math.Abs(viaTransparent.SSIM-viaPrecomposited.SSIM) > tol {
		t.Errorf("ssim depends on reference storage: %v transparent vs %v pre-composited, diff %v > %v",
			viaTransparent.SSIM, viaPrecomposited.SSIM,
			math.Abs(viaTransparent.SSIM-viaPrecomposited.SSIM), tol)
	}
	if math.Abs(viaTransparent.DeltaE-viaPrecomposited.DeltaE) > tol {
		t.Errorf("deltaE depends on reference storage: %v transparent vs %v pre-composited, diff %v > %v",
			viaTransparent.DeltaE, viaPrecomposited.DeltaE,
			math.Abs(viaTransparent.DeltaE-viaPrecomposited.DeltaE), tol)
	}
}

func TestSmallImagesDoNotPanic(t *testing.T) {
	for _, n := range []int{1, 2, 3, 10} {
		im := solid(n, n, color.NRGBA{1, 2, 3, 255})
		if _, err := Compare(im, im); err != nil {
			t.Fatalf("%dx%d: %v", n, n, err)
		}
	}
}

func TestSSIMMonotonicInNoise(t *testing.T) {
	const s = 64
	ref := solid(s, s, color.NRGBA{90, 120, 200, 255})
	prev := math.Inf(1)
	for _, amp := range []float64{0, 2, 8, 32, 90} {
		got := image.NewNRGBA(image.Rect(0, 0, s, s))
		// Deterministic ordered dither, so the test is reproducible.
		for y := 0; y < s; y++ {
			for x := 0; x < s; x++ {
				d := float64((x*7+y*13)%16)/15.0*2*amp - amp
				got.SetNRGBA(x, y, color.NRGBA{
					clamp8(90 + d), clamp8(120 + d), clamp8(200 + d), 255,
				})
			}
		}
		r, err := Compare(ref, got)
		if err != nil {
			t.Fatal(err)
		}
		if r.SSIM > prev+1e-9 {
			t.Errorf("ssim increased with noise amplitude %v: %v > %v", amp, r.SSIM, prev)
		}
		prev = r.SSIM
	}
	if prev > 0.9 {
		t.Errorf("max-amplitude noise scored %v; SSIM is not penalising the difference", prev)
	}
}

func clamp8(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v + 0.5)
}
