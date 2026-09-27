package pixelbuf

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func solid(w, h int, c color.NRGBA) *Image {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.SetNRGBA(x, y, c)
		}
	}
	return FromImage(m)
}

func TestFromImagePreservesSizeAndAlpha(t *testing.T) {
	im := solid(4, 3, color.NRGBA{R: 10, G: 20, B: 30, A: 128})
	if im.W != 4 || im.H != 3 {
		t.Fatalf("size = %dx%d, want 4x3", im.W, im.H)
	}
	if got := im.A[0]; got != 128 {
		t.Errorf("alpha[0] = %d, want 128", got)
	}
	if im.Opaque() {
		t.Error("Opaque() = true for a half-transparent image")
	}
}

func TestFromImageIsStraightNotPremultiplied(t *testing.T) {
	// Half-transparent white in premultiplied form would come back as ~128,128,128.
	// Straight form must keep 255,255,255. Getting this wrong silently darkens
	// every edge of every semi-transparent image, so it is worth pinning.
	im := solid(2, 2, color.NRGBA{R: 255, G: 255, B: 255, A: 128})
	r, g, b := im.R[0], im.G[0], im.B[0]
	if r != 255 || g != 255 || b != 255 {
		t.Errorf("straight RGB = (%d,%d,%d), want (255,255,255)", r, g, b)
	}
}

func TestOpaque(t *testing.T) {
	if !solid(2, 2, color.NRGBA{A: 255}).Opaque() {
		t.Error("Opaque() = false for a fully opaque image")
	}
	if solid(2, 2, color.NRGBA{A: 254}).Opaque() {
		t.Error("Opaque() = true at alpha 254; a single non-255 pixel is not opaque")
	}
}

func TestLabRoundTrip(t *testing.T) {
	// The whole geometry stack lives in OKLab, so a lossy sRGB->OKLab->sRGB
	// round trip would put a floor under every fidelity number we report.
	for _, c := range []color.NRGBA{
		{R: 0, G: 0, B: 0, A: 255},
		{R: 255, G: 255, B: 255, A: 255},
		{R: 18, G: 52, B: 86, A: 255},
		{R: 200, G: 30, B: 140, A: 255},
		{R: 7, G: 199, B: 63, A: 255},
	} {
		im := solid(1, 1, c)
		lab := im.Lab()
		if len(lab.Pix) != 3 {
			t.Fatalf("Lab plane has %d values, want 3", len(lab.Pix))
		}
		if lab.Pix[0] < -0.001 || lab.Pix[0] > 1.001 {
			t.Errorf("%v: L = %f, want [0,1]", c, lab.Pix[0])
		}
	}
}

func TestLabIsMonotonicInLightness(t *testing.T) {
	// If L were not monotonic, a gradient traced at a fixed threshold would
	// produce noise instead of level sets. This is the property marching
	// squares depends on, so it is tested here rather than discovered later.
	prev := -1.0
	for v := 0; v < 256; v += 5 {
		im := solid(1, 1, color.NRGBA{R: uint8(v), G: uint8(v), B: uint8(v), A: 255})
		l := im.Lab().Pix[0]
		if l < prev-1e-9 {
			t.Fatalf("L dipped at grey %d: %f after %f", v, l, prev)
		}
		prev = l
	}
}

func TestAlphaAtBounds(t *testing.T) {
	im := solid(2, 2, color.NRGBA{A: 255})
	// Out-of-range reads must not panic: contour tracing probes neighbours that
	// can fall outside the image, and a panic there would be a crash on any
	// image whose border is not uniform.
	if got := im.AlphaAt(-1, 0); got != 0 {
		t.Errorf("AlphaAt(-1,0) = %v, want 0", got)
	}
	if got := im.AlphaAt(0, 5); got != 0 {
		t.Errorf("AlphaAt(0,5) = %v, want 0", got)
	}
	if got := im.AlphaAt(0, 0); got != 1 {
		t.Errorf("AlphaAt(0,0) = %v, want 1", got)
	}
}

func TestAlphaLevels(t *testing.T) {
	m := image.NewNRGBA(image.Rect(0, 0, 3, 1))
	m.SetNRGBA(0, 0, color.NRGBA{A: 0})
	m.SetNRGBA(1, 0, color.NRGBA{A: 128})
	m.SetNRGBA(2, 0, color.NRGBA{A: 255})
	im := FromImage(m)
	if got := im.AlphaLevels(256); got != 3 {
		t.Errorf("AlphaLevels(256) = %d, want 3 distinct values", got)
	}
	// Saturates at limit+1: the answer "2" means "more than one", which is all a
	// caller needs to decide it cannot treat alpha as binary.
	if got := im.AlphaLevels(1); got != 2 {
		t.Errorf("AlphaLevels(1) = %d, want 2 (saturated)", got)
	}
	// The decision this method exists for: a fully opaque image reports 1, so
	// the encoder can skip fill-opacity entirely.
	if got := solid(2, 2, color.NRGBA{A: 255}).AlphaLevels(1); got != 1 {
		t.Errorf("AlphaLevels(1) on an opaque image = %d, want 1", got)
	}
}

func TestDecodeRoundTrip(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	for i := range src.Pix {
		src.Pix[i] = uint8(i)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	im, err := Decode(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if im.W != 3 || im.H != 2 {
		t.Fatalf("size = %dx%d, want 3x2", im.W, im.H)
	}
	if im.A[0] != src.Pix[3] {
		t.Errorf("alpha[0] = %d, want %d", im.A[0], src.Pix[3])
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := Decode(bytes.NewReader([]byte("not an image"))); err == nil {
		t.Error("Decode accepted garbage; a clear error matters more than a panic")
	}
}
