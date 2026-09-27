// Package colorconv implements the colour-space conversions the whole project
// depends on: sRGB transfer, linear-light, and OKLab.
//
// OKLab is the project's canonical working space. Two reasons:
//
//   - Euclidean distance in sRGB over-weights blue and green, so naive
//     quantisation, simplification tolerances and error metrics are all
//     miscalibrated in a way that shows up visibly on saturated colours.
//   - OKLab is roughly perceptually uniform, which makes a single scalar
//     tolerance meaningful across hue and lightness, and therefore makes
//     simplification and curve-fitting errors interpretable.
//
// The conversion chain is sRGB -> linear-light -> OKLab. Skipping the
// linear-light step is a common bug and produces visibly wrong results on
// saturated colours.
//
// No dependencies outside the standard library.
package colorconv

import (
	"fmt"
	"strconv"
	"strings"
)

import "math"

// SRGBToLinear converts one sRGB component in [0,1] to linear-light.
func SRGBToLinear(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

// LinearToSRGB converts one linear-light component in [0,1] to sRGB.
func LinearToSRGB(c float64) float64 {
	if c <= 0.0031308 {
		return c * 12.92
	}
	return 1.055*math.Pow(c, 1/2.4) - 0.055
}

// LinearToOKLab converts linear-light LMS to OKLab.
func LinearToOKLab(l, m, s float64) (L, a, b float64) {
	l_ := math.Cbrt(l)
	m_ := math.Cbrt(m)
	s_ := math.Cbrt(s)

	L = 0.2104542553*l_ + 0.7936177850*m_ - 0.0040720468*s_
	a = 1.9779984951*l_ - 2.4285922050*m_ + 0.4505937099*s_
	b = 0.0259040371*l_ + 0.7827717662*m_ - 0.8086757660*s_
	return L, a, b
}

// OKLabToLinear converts OKLab to linear-light LMS.
func OKLabToLinear(L, a, b float64) (l, m, s float64) {
	lp := L + 0.3963377774*a + 0.2158037573*b
	mp := L - 0.1055613458*a - 0.0638541728*b
	sp := L - 0.0894841775*a - 1.2914855480*b

	return lp * lp * lp, mp * mp * mp, sp * sp * sp
}

// RGBToOKLab converts 8-bit sRGB to OKLab.
func RGBToOKLab(r, g, b uint8) (L, a, bb float64) {
	return RGBToLabFloat(float64(r), float64(g), float64(b))
}

// RGBToLabFloat converts sRGB components in [0,255] to OKLab.
func RGBToLabFloat(r, g, b float64) (L, a, bb float64) {
	rl := SRGBToLinear(r / 255)
	gl := SRGBToLinear(g / 255)
	bl := SRGBToLinear(b / 255)
	return LinearToOKLab(rl, gl, bl)
}

// OKLabToRGB converts OKLab to 8-bit sRGB, clamping to [0,255].
func OKLabToRGB(L, a, b float64) (r, g, bb uint8) {
	rl, gl, bl := OKLabToLinear(L, a, b)
	return clamp255(LinearToSRGB(rl)), clamp255(LinearToSRGB(gl)), clamp255(LinearToSRGB(bl))
}

// clampf is clamp255 without the rounding, for callers that carry sub-LSB
// precision and must not have it discarded.
func clampf(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

func clamp255(v float64) uint8 {
	if !(v > 0) { // also catches NaN
		return 0
	}
	if v >= 1 {
		return 255
	}
	return uint8(v*255 + 0.5)
}

// JND is an approximate just-noticeable difference in OKLab Euclidean distance.
// Conventions differ by a factor of ~3 across the literature; the value below
// is the one that matches the "noticeable on a calibrated display" reading, and
// is only ever used relatively, never as an absolute threshold.
const JND = 0.02

// OKLabDist returns the Euclidean distance between two OKLab colours. Roughly
// interpretable via JND; see the note there.
func OKLabDist(L1, a1, b1, L2, a2, b2 float64) float64 {
	dL := L1 - L2
	da := a1 - a2
	db := b1 - b2
	return math.Sqrt(dL*dL + da*da + db*db)
}

// LinearLuminance returns the relative luminance (Y) of a linear-light triple.
func LinearLuminance(l, m, s float64) float64 { return 0.2126*l + 0.7152*m + 0.0722*s }

// CompositeOver returns an sRGB triple in [0,255] for a pixel of the given
// premultiplied 8-bit components and coverage, laid over an opaque backdrop.
//
// The premultiplied value is already colour*coverage, so it is the weighted term
// and needs no unpremultiply: premultiplied + backdrop*(1-coverage). Dividing by
// coverage first to recover the straight colour, then adding the backdrop's
// weight without weighting the recovered colour, is a mistake that reads as a
// plausible unpremultiply and overshoots badly: half-transparent red over white
// gives 255 + 127 = 382 on the red channel instead of 255.
//
// This exists as one shared function on purpose. The encoder composites its input
// here, and the comparator composites the reference and the render here, and if
// those two ever disagreed the measurement would be comparing a render against a
// reference prepared by a different rule, which is the one mistake no amount of
// careful metric choice survives.
func CompositeOver(premulR, premulG, premulB float64, coverage float64, bgR, bgG, bgB float64) (r, g, b float64) {
	if coverage >= 1 {
		return clampf(premulR), clampf(premulG), clampf(premulB)
	}
	if coverage <= 0 {
		return clampf(bgR), clampf(bgG), clampf(bgB)
	}
	rest := 1 - coverage
	return clampf(premulR + rest*bgR), clampf(premulG + rest*bgG), clampf(premulB + rest*bgB)
}

// ParseBackdrop accepts the forms a person actually types for a background colour:
// #rgb, #rrggbb, 0x-prefixed hex, and the handful of names worth having. It lives
// here rather than in either command because the encoder and the comparator must
// resolve the same string the same way: a backdrop the two disagree on produces a
// measurement of a render against a differently-prepared reference, which is
// indistinguishable from a bad encode.
//
// An unparseable colour is an error rather than a silent black, because a backdrop
// chosen by accident looks exactly like one chosen on purpose in the output.
func ParseBackdrop(s string) ([3]float64, error) {
	names := map[string][3]float64{
		"white": {255, 255, 255}, "black": {0, 0, 0},
		"grey": {128, 128, 128}, "gray": {128, 128, 128},
	}
	trimmed := strings.TrimSpace(s)
	if c, ok := names[strings.ToLower(trimmed)]; ok {
		return c, nil
	}
	// Bare hex without a marker is deliberately not accepted: a two-character
	// string is far more likely a typo than a colour, and guessing here is the
	// failure this function exists to prevent.
	hex := strings.TrimPrefix(strings.TrimPrefix(trimmed, "#"), "0x")
	parse := func(s string) (float64, error) {
		v, err := strconv.ParseUint(s, 16, 8)
		return float64(v), err
	}
	var out [3]float64
	var err error
	switch len(hex) {
	case 3:
		for i := 0; i < 3; i++ {
			out[i], err = parse(hex[i:i+1] + hex[i:i+1])
			if err != nil {
				return out, err
			}
		}
	case 6:
		for i := 0; i < 3; i++ {
			out[i], err = parse(hex[2*i : 2*i+2])
			if err != nil {
				return out, err
			}
		}
	default:
		return out, fmt.Errorf("want #rgb, #rrggbb, or a colour name, got %q", s)
	}
	return out, nil
}
