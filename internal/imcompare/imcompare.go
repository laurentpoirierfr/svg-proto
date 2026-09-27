// Package imcompare compares a rendered SVG against a reference raster.
//
// This is the only honest way to know whether a vectorisation improved
// anything. Judging path data by eye, or by counting points, or by bytes, all
// systematically mislead: bytes can always be traded for quality, point count
// ignores that a polyline and a cubic are not equivalent, and the eye is
// unforgiving about exactly the thing vectorisers get wrong, which is edges.
//
// Reference metric, per docs/plan.md: MSE in OKLab weighted by gradient
// magnitude, so error concentrates where the eye notices. The unweighted
// numbers are reported too, so results stay comparable with the literature.
package imcompare

import (
	"fmt"
	"image"
	"math"

	"github.com/elfeo/svg-proto/internal/colorconv"
)

// edgeGain is how much more an edge pixel counts than an interior one.
const edgeGain = 8

// Result holds every fidelity number we track.
type Result struct {
	Width  int     `json:"width"`
	Height int     `json:"height"`
	PSNR   float64 `json:"psnrDb"`
	SSIM   float64 `json:"ssim"`
	// DeltaE is the mean OKLab distance. 0.02 is roughly one just-noticeable
	// difference, so this reads directly as "how different does it look".
	DeltaE float64 `json:"deltaEOKLabMean"`
	// RMSE is the root of the plain OKLab MSE, same units as DeltaE.
	RMSE float64 `json:"rmseOKLab"`
	// GradWeightedMSE is the OKLab MSE weighted by the reference's own edge
	// structure, rescaled to DeltaE units. It is the ranking metric.
	GradWeighted float64 `json:"gradWeightedRmseOKLab"`
	// Score is the single number used to rank candidates. Lower is better.
	Score float64 `json:"score"`
}

// Options controls the comparison.
type Options struct {
	// Bg is the background to composite transparent pixels over, in sRGB 8-bit.
	// White by default. Comparing a transparent render against an opaque source
	// is meaningless without this.
	Bg [3]float64
	// AlphaAffectsLuma offloads nothing; kept for future masks/alpha work.
	EdgeGain float64
}

func defaultOptions() Options {
	return Options{Bg: [3]float64{255, 255, 255}, EdgeGain: edgeGain}
}

// DefaultEdgeGain is the weight a caller must reuse when it builds Options itself,
// so that overriding the backdrop does not quietly change the ranking metric too.
func DefaultEdgeGain() float64 { return edgeGain }

// Compare requires two images of identical dimensions. Resampling here would
// silently contaminate the measurement; scale the reference explicitly to the
// render size instead.
func Compare(ref, got image.Image) (*Result, error) { return CompareWith(ref, got, defaultOptions()) }

// CompareWith is Compare with explicit options.
func CompareWith(ref, got image.Image, opt Options) (*Result, error) {
	b := ref.Bounds()
	if g := got.Bounds(); b.Dx() != g.Dx() || b.Dy() != g.Dy() {
		return nil, fmt.Errorf("dimension mismatch: reference %dx%d, render %dx%d",
			b.Dx(), b.Dy(), g.Dx(), g.Dy())
	}
	w, h := b.Dx(), b.Dy()
	n := w * h
	if n == 0 {
		return nil, fmt.Errorf("empty image")
	}

	res := &Result{Width: w, Height: h}
	refLab := make([]float64, 3*n)
	gotLab := make([]float64, 3*n)
	refGray := make([]float64, n)
	gotGray := make([]float64, n)

	var sumDE, sumSE float64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			rr, rg, rb := sampleRGB(ref, b.Min.X+x, b.Min.Y+y, opt.Bg)
			gr, gg, gb := sampleRGB(got, b.Min.X+x, b.Min.Y+y, opt.Bg)
			L1, a1, b1 := colorconv.RGBToLabFloat(rr, rg, rb)
			L2, a2, b2 := colorconv.RGBToLabFloat(gr, gg, gb)
			refLab[3*i], refLab[3*i+1], refLab[3*i+2] = L1, a1, b1
			gotLab[3*i], gotLab[3*i+1], gotLab[3*i+2] = L2, a2, b2
			refGray[i] = clamp01(L1)
			gotGray[i] = clamp01(L2)

			dL, da, db := L1-L2, a1-a2, b1-b2
			se := dL*dL + da*da + db*db
			sumDE += math.Sqrt(se)
			sumSE += se
		}
	}
	res.DeltaE = sumDE / float64(n)
	res.RMSE = math.Sqrt(sumSE / float64(n))

	// Gradient weighting: the reference's own edge structure decides where
	// error is cheap. Interior error is nearly free; error on an edge is what
	// the eye reads as "the shape is wrong".
	grad := sobelMagnitude(refGray, w, h)
	gain := opt.EdgeGain
	if gain == 0 {
		gain = edgeGain
	}
	var sumW, sumWD float64
	for i := 0; i < n; i++ {
		gw := 1 + grad[i]*gain
		dL := refLab[3*i] - gotLab[3*i]
		da := refLab[3*i+1] - gotLab[3*i+1]
		db := refLab[3*i+2] - gotLab[3*i+2]
		sumWD += gw * (dL*dL + da*da + db*db)
		sumW += gw
	}
	if sumW > 0 {
		res.GradWeighted = math.Sqrt(sumWD / sumW)
	}
	res.Score = res.GradWeighted

	// PSNR on lightness, in dB, data range 1.0.
	var seGray float64
	for i := 0; i < n; i++ {
		d := refGray[i] - gotGray[i]
		seGray += d * d
	}
	if mse := seGray / float64(n); mse <= 0 {
		res.PSNR = math.Inf(1)
	} else {
		res.PSNR = 10 * math.Log10(1/mse)
	}
	res.SSIM = SSIM(refGray, gotGray, w, h)
	return res, nil
}

// sampleRGB returns sRGB in [0,255], composited over bg. image.RGBA and friends
// expose premultiplied alpha; see colorconv.CompositeOver for why the composite
// needs no unpremultiply, and for the bug that this sharing now makes impossible
// to reintroduce in one place only.
func sampleRGB(im image.Image, x, y int, bg [3]float64) (r, g, b float64) {
	R, G, B, A := im.At(x, y).RGBA()
	return colorconv.CompositeOver(float64(R)/257, float64(G)/257, float64(B)/257,
		float64(A)/65535, bg[0], bg[1], bg[2])
}

func clamp01(v float64) float64 {
	if !(v > 0) {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// sobelMagnitude returns the Sobel gradient magnitude of a plane, normalised so
// that a full black-to-white step is 1.0.
func sobelMagnitude(p []float64, w, h int) []float64 {
	out := make([]float64, len(p))
	for y := 0; y < h; y++ {
		ym1, y0, yp1 := rowClamp(y-1, h), y, rowClamp(y+1, h)
		for x := 0; x < w; x++ {
			xm1, xp1 := colClamp(x-1, w), colClamp(x+1, w)
			gx := -p[ym1*w+xm1] - 2*p[y0*w+xm1] - p[yp1*w+xm1] +
				p[ym1*w+xp1] + 2*p[y0*w+xp1] + p[yp1*w+xp1]
			gy := -p[ym1*w+xm1] - 2*p[ym1*w+x] - p[ym1*w+xp1] +
				p[yp1*w+xm1] + 2*p[yp1*w+x] + p[yp1*w+xp1]
			out[y*w+x] = math.Sqrt(gx*gx+gy*gy) / 4
		}
	}
	return out
}

func rowClamp(i, h int) int {
	if i < 0 {
		return 0
	}
	if i >= h {
		return h - 1
	}
	return i
}

func colClamp(i, w int) int {
	if i < 0 {
		return 0
	}
	if i >= w {
		return w - 1
	}
	return i
}

// ---------------------------------------------------------------------------
// SSIM
// ---------------------------------------------------------------------------

const (
	ssimC1 = (0.01 * 255) * (0.01 * 255)
	ssimC2 = (0.03 * 255) * (0.03 * 255)
)

// SSIM is the mean structural similarity of two [0,1] planes, using Wang et
// al.'s 11x11 Gaussian window with sigma 1.5, the population variance
// estimator, and the window border excluded, as in the original formulation.
func SSIM(x, y []float64, w, h int) float64 {
	if w < 11 || h < 11 {
		return ssimGlobal(x, y)
	}
	k := gaussianWindow()
	r := len(k) / 2

	mx := conv1D(x, k, w, h)
	my := conv1D(y, k, w, h)
	xx := conv1D(mul(x, x), k, w, h)
	yy := conv1D(mul(y, y), k, w, h)
	xy := conv1D(mul(x, y), k, w, h)

	total, count := 0.0, 0
	for j := r; j < h-r; j++ {
		for i := r; i < w-r; i++ {
			o := j*w + i
			ux, uy := mx[o]*255, my[o]*255
			vx := xx[o]*255*255 - ux*ux
			vy := yy[o]*255*255 - uy*uy
			vxy := xy[o]*255*255 - ux*uy
			num := (2*ux*uy + ssimC1) * (2*vxy + ssimC2)
			den := (ux*ux + uy*uy + ssimC1) * (vx + vy + ssimC2)
			if den == 0 {
				total++
			} else {
				total += num / den
			}
			count++
		}
	}
	if count == 0 {
		return 1
	}
	return total / float64(count)
}

// ssimGlobal is the fallback for images too small for the Gaussian window.
// Fuzzing produces 3x3 images, and a stats tool that panics is a bad tool.
func ssimGlobal(x, y []float64) float64 {
	n := float64(len(x))
	if n == 0 {
		return 1
	}
	var sx, sy, sxx, syy, sxy float64
	for i := range x {
		ux, uy := x[i]*255, y[i]*255
		sx += ux
		sy += uy
		sxx += ux * ux
		syy += uy * uy
		sxy += ux * uy
	}
	mx, my := sx/n, sy/n
	vx := sxx/n - mx*mx
	vy := syy/n - my*my
	vxy := sxy/n - mx*my
	num := (2*mx*my + ssimC1) * (2*vxy + ssimC2)
	den := (mx*mx + my*my + ssimC1) * (vx + vy + ssimC2)
	if den == 0 {
		return 1
	}
	return num / den
}

func gaussianWindow() []float64 {
	const sigma = 1.5
	const r = 5
	k := make([]float64, 2*r+1)
	sum := 0.0
	for i := -r; i <= r; i++ {
		v := math.Exp(-float64(i*i) / (2 * sigma * sigma))
		k[i+r] = v
		sum += v
	}
	for i := range k {
		k[i] /= sum
	}
	return k
}

// conv1D applies a separable Gaussian to a plane, clamping at the borders.
func conv1D(p, k []float64, w, h int) []float64 {
	tmp := make([]float64, len(p))
	r := len(k) / 2
	for y := 0; y < h; y++ {
		row := y * w
		for x := 0; x < w; x++ {
			s := 0.0
			for t := -r; t <= r; t++ {
				s += k[t+r] * p[row+colClamp(x+t, w)]
			}
			tmp[row+x] = s
		}
	}
	out := make([]float64, len(p))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			s := 0.0
			for t := -r; t <= r; t++ {
				s += k[t+r] * tmp[rowClamp(y+t, h)*w+x]
			}
			out[y*w+x] = s
		}
	}
	return out
}

func mul(a, b []float64) []float64 {
	out := make([]float64, len(a))
	for i := range a {
		out[i] = a[i] * b[i]
	}
	return out
}
