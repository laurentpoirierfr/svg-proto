package colorconv

import (
	"math"
	"testing"
)

const eps = 1e-6

func TestLinearToOKLabKnownValues(t *testing.T) {
	// Reference values from Björn Ottosson's OKLab matrices. These are the
	// numbers every OKLab implementation is checked against, and getting them
	// wrong (most often by skipping the linear-light step) produces errors that
	// only show up on saturated colours.
	//
	// Note these are OKLab, not OKLCh: pure green is a = -2.4286, not -1.3862.
	// Conflating the two is an easy and very confusing mistake.
	cases := []struct {
		name     string
		r, g, b  float64
		L, a, bb float64
	}{
		{"white", 255, 255, 255, 1.0, 0.0, 0.0},
		{"black", 0, 0, 0, 0.0, 0.0, 0.0},
		{"50% grey", 127.5, 127.5, 127.5, 0.598181, 0.0, 0.0},
		{"sRGB 128 grey", 128, 128, 128, 0.599871, 0.0, 0.0},
		{"red", 255, 0, 0, 0.2104542553, 1.9779984951, 0.0259040371},
		{"green", 0, 255, 0, 0.7936177850, -2.4285922050, 0.7827717662},
		{"blue", 0, 0, 255, -0.0040720468, 0.4505937099, -0.8086757660},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			L, a, b := RGBToLabFloat(c.r, c.g, c.b)
			check(t, "L", L, c.L)
			check(t, "a", a, c.a)
			check(t, "b", b, c.bb)
		})
	}
}

func TestRoundTrip(t *testing.T) {
	// Every 4-bit RGB value, so the whole cube is covered rather than a sample.
	for r := 0; r < 16; r++ {
		for g := 0; g < 16; g++ {
			for b := 0; b < 16; b++ {
				r8, g8, b8 := uint8(r*17), uint8(g*17), uint8(b*17)
				L, a, bb := RGBToOKLab(r8, g8, b8)
				gr, gg, gb := OKLabToRGB(L, a, bb)
				if gr != r8 || gg != g8 || gb != b8 {
					t.Fatalf("round trip %d,%d,%d -> %d,%d,%d", r8, g8, b8, gr, gg, gb)
				}
			}
		}
	}
}

func TestSRGBTransferEndpoints(t *testing.T) {
	if got := SRGBToLinear(0); got != 0 {
		t.Errorf("SRGBToLinear(0) = %v, want 0", got)
	}
	if got := SRGBToLinear(1); math.Abs(got-1) > eps {
		t.Errorf("SRGBToLinear(1) = %v, want 1", got)
	}
	for _, v := range []float64{0, 0.01, 0.2, 0.5, 0.9, 1} {
		back := LinearToSRGB(SRGBToLinear(v))
		if math.Abs(back-v) > 1e-9 {
			t.Errorf("transfer round trip %v -> %v", v, back)
		}
	}
}

func TestOKLabDistIsSymmetricAndZeroOnIdentity(t *testing.T) {
	L1, a1, b1 := RGBToOKLab(200, 30, 40)
	L2, a2, b2 := RGBToOKLab(10, 220, 90)
	d1 := OKLabDist(L1, a1, b1, L2, a2, b2)
	d2 := OKLabDist(L2, a2, b2, L1, a1, b1)
	if d1 != d2 {
		t.Errorf("asymmetric: %v vs %v", d1, d2)
	}
	if d := OKLabDist(L1, a1, b1, L1, a1, b1); d != 0 {
		t.Errorf("self distance = %v, want 0", d)
	}
	// One JND of lightness difference should read as roughly JND.
	if got := OKLabDist(0.5, 0, 0, 0.5+JND, 0, 0); math.Abs(got-JND) > 1e-9 {
		t.Errorf("JND sanity: %v", got)
	}
}

func check(t *testing.T, field string, got, want float64) {
	t.Helper()
	tol := 2e-5
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.6f, want %.6f (delta %.6f)", field, got, want, got-want)
	}
}

// The composite is the one operation the encoder and the comparator must agree on
// exactly, so it is pinned here rather than through either caller. A caller test
// would catch a change in behaviour; only this one catches a change in the rule.
func TestCompositeOverWeightsBothTerms(t *testing.T) {
	cases := []struct {
		name     string
		premul   [3]float64
		coverage float64
		bg       [3]float64
		want     [3]float64
	}{
		{"opaque keeps its own colour", [3]float64{10, 20, 30}, 1, [3]float64{255, 255, 255}, [3]float64{10, 20, 30}},
		{"fully transparent is the backdrop", [3]float64{0, 0, 0}, 0, [3]float64{255, 255, 255}, [3]float64{255, 255, 255}},
		{"half red over white is pink", [3]float64{128, 0, 0}, 128.0 / 255.0, [3]float64{255, 255, 255}, [3]float64{255, 127, 127}},
		// A quarter of pure white is premultiplied 63.75, and the backdrop's
		// three-quarter share is what the composite adds on top of it.
		{"quarter white over grey", [3]float64{63.75, 63.75, 63.75}, 0.25, [3]float64{128, 128, 128}, [3]float64{159.75, 159.75, 159.75}},
		// The regression this exists for. Pure red at half coverage is the case
		// where the two terms meet exactly at 255: recovering the straight colour
		// and adding the backdrop weight unweighted gives 382.5 on red and 510 on
		// the others, and no clamp hides that it happened.
		{"half red over white lands on 255 exactly", [3]float64{127.5, 0, 0}, 0.5, [3]float64{255, 255, 255}, [3]float64{255, 127.5, 127.5}},
	}
	for _, c := range cases {
		r, g, b := CompositeOver(c.premul[0], c.premul[1], c.premul[2], c.coverage, c.bg[0], c.bg[1], c.bg[2])
		got := [3]float64{r, g, b}
		for i := range got {
			if math.Abs(got[i]-c.want[i]) > 1e-9 {
				t.Errorf("%s: channel %d = %v, want %v", c.name, i, got[i], c.want[i])
			}
		}
	}
}

// Compositing is idempotent, which is what makes a pre-composited reference
// interchangeable with a transparent one in the comparator.
func TestCompositeOverIsIdempotent(t *testing.T) {
	bg := [3]float64{18, 200, 90}
	for _, c := range []float64{0, 0.1, 0.25, 0.5, 0.75, 0.9, 1} {
		for _, v := range []float64{0, 1, 63.5, 128, 254.9, 255} {
			r, g, b := CompositeOver(v, v, v, c, bg[0], bg[1], bg[2])
			r2, g2, b2 := CompositeOver(r, g, b, 1, bg[0], bg[1], bg[2])
			if math.Abs(r-r2) > 1e-9 || math.Abs(g-g2) > 1e-9 || math.Abs(b-b2) > 1e-9 {
				t.Errorf("compositing %v at %v then again changed it: %v,%v,%v -> %v,%v,%v",
					v, c, r, g, b, r2, g2, b2)
			}
		}
	}
}

// The encoder and the comparator both resolve the backdrop string, so this parser
// is the only thing standing between a typo and a measurement taken against a
// differently-prepared reference. Rejecting is the important half: an unparsed
// colour that fell back to black would look deliberate in the output.
func TestParseBackdropAcceptsWhatPeopleType(t *testing.T) {
	cases := []struct {
		in   string
		want [3]float64
	}{
		{"white", [3]float64{255, 255, 255}},
		{"WHITE", [3]float64{255, 255, 255}},
		{"  white  ", [3]float64{255, 255, 255}},
		{"black", [3]float64{0, 0, 0}},
		{"grey", [3]float64{128, 128, 128}},
		{"gray", [3]float64{128, 128, 128}},
		{"#fff", [3]float64{255, 255, 255}},
		{"#f0f", [3]float64{255, 0, 255}},
		{"#ffffff", [3]float64{255, 255, 255}},
		{"#f0f0f0", [3]float64{240, 240, 240}},
		{"#202020", [3]float64{32, 32, 32}},
		{"0xf0f0f0", [3]float64{240, 240, 240}},
	}
	for _, c := range cases {
		got, err := ParseBackdrop(c.in)
		if err != nil {
			t.Errorf("ParseBackdrop(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseBackdrop(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseBackdropRejectsRatherThanGuessing(t *testing.T) {
	// "20" is here deliberately: bare two-character hex is a typo far more often
	// than a colour, so it must not resolve to #202020.
	for _, in := range []string{"", "#", "#ff", "#fffff", "#gggggg", "chartreuse", "rgb(1,2,3)", "#1234567", "0xzz", "20"} {
		if got, err := ParseBackdrop(in); err == nil {
			t.Errorf("ParseBackdrop(%q) = %v, want an error", in, got)
		}
	}
}
