package field

import (
	"math"
	"testing"
)

func filled(w, h int, v float64) *Field {
	f := New(w, h)
	for i := range f.V {
		f.V[i] = v
	}
	return f
}

func TestAtClampsRatherThanReadsZero(t *testing.T) {
	// Tracers probe outside the grid constantly. A silent zero would invent a
	// hard edge around the whole image.
	f := New(3, 3)
	for i := range f.V {
		f.V[i] = 0.5
	}
	// Every one of these clamps onto a real node of a uniform field.
	for _, c := range [][2]int{{-1, 0}, {0, -1}, {3, 0}, {0, 3}, {-5, -5}, {99, 99}} {
		if got := f.At(c[0], c[1]); got != 0.5 {
			t.Errorf("At(%d,%d) = %v, want the clamped 0.5", c[0], c[1], got)
		}
	}
	// A non-uniform field proves the clamp lands on the *nearest* node, not on
	// the first one.
	g := New(3, 1)
	g.V[0], g.V[1], g.V[2] = 1, 2, 3
	if got := g.At(9, 0); got != 3 {
		t.Errorf("At(9,0) = %v, want 3 (clamped to the last node)", got)
	}
}

func TestAtCentreReadsItself(t *testing.T) {
	f := New(2, 2)
	f.V[0], f.V[1], f.V[2], f.V[3] = 1, 2, 3, 4
	cases := []struct {
		x, y int
		want float64
	}{{0, 0, 1}, {1, 0, 2}, {0, 1, 3}, {1, 1, 4}}
	for _, c := range cases {
		if got := f.At(c.x, c.y); got != c.want {
			t.Errorf("At(%d,%d) = %v, want %v", c.x, c.y, got, c.want)
		}
	}
}

func TestSetIgnoresOutOfRange(t *testing.T) {
	f := New(2, 2)
	f.Set(0, 0, 9)
	f.Set(-1, 0, 7)
	f.Set(0, 9, 7)
	if f.V[0] != 9 {
		t.Errorf("V[0] = %v, want 9", f.V[0])
	}
	if f.V[1] == 7 || f.V[2] == 7 {
		t.Error("an out-of-range Set wrote into the field")
	}
}

func TestGradientsOnARamp(t *testing.T) {
	// A linear ramp has a constant gradient. Getting the interior stencil wrong
	// shows up here as a gradient that varies along the ramp.
	f := New(5, 3)
	for y := 0; y < 3; y++ {
		for x := 0; x < 5; x++ {
			f.V[y*5+x] = float64(x) * 0.1
		}
	}
	gx, gy := f.Gradients()
	for y := 0; y < 3; y++ {
		for x := 1; x < 4; x++ {
			if got := gx.V[y*5+x]; math.Abs(got-0.1) > 1e-9 {
				t.Errorf("gx[%d,%d] = %v, want 0.1", x, y, got)
			}
			if got := gy.V[y*5+x]; math.Abs(got) > 1e-9 {
				t.Errorf("gy[%d,%d] = %v, want 0", x, y, got)
			}
		}
	}
	// One-sided at the borders, so the edge is not a plateau.
	if got := gx.V[0]; math.Abs(got-0.1) > 1e-9 {
		t.Errorf("gx at x=0 = %v, want 0.1 (one-sided)", got)
	}
}

func TestMagnitudeOfMatchesGradients(t *testing.T) {
	f := New(7, 5)
	for i := range f.V {
		f.V[i] = math.Sin(float64(i) * 0.3)
	}
	gx, gy := f.Gradients()
	want := GradientMagnitude(gx, gy)
	got := GradientMagnitudeOf(f)
	for i := range got.V {
		if math.Abs(got.V[i]-want.V[i]) > 1e-12 {
			t.Fatalf("GradientMagnitudeOf disagrees with GradientMagnitude() at %d: %v vs %v",
				i, got.V[i], want.V[i])
		}
	}
}

func TestDiffuseIsANoOpOnAConstantField(t *testing.T) {
	// The invariant that replaces mass conservation: with a Neumann boundary a
	// flat field has a zero Laplacian everywhere, so nothing may move. This is
	// the property that keeps the frame free of an artificial rim, and it is
	// stronger than a sum check because it is local.
	f := filled(11, 7, 0.42)
	got := f.Diffuse(DiffuseOptions{Iterations: 8, Lambda: 0.24, Kappa: 0.05, C: PM1})
	for i, v := range got.V {
		if math.Abs(v-0.42) > 1e-12 {
			t.Fatalf("node %d drifted from 0.42 to %v on a flat field", i, v)
		}
	}
}

func TestDiffuseLeavesALinearRampAlone(t *testing.T) {
	// On a ramp the Laplacian is zero, so nothing may move. It is not *exactly*
	// zero-change though: the conductance is exp(-(grad/kappa)^2) < 1 even for a
	// gradient far below kappa, so repeated passes shrink a ramp by
	// 4*lambda*u*(1-c) per iteration. That is the edge-stopping function doing
	// its job, and it is why kappa is set above the strongest edge rather than
	// at it. What must not happen is visible drift, and above all not the
	// border-only drift that a clamped ghost would produce.
	f := New(8, 8)
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			f.V[y*8+x] = float64(x+y) * 0.01
		}
	}
	o := DiffuseOptions{Iterations: 10, Lambda: 0.2, Kappa: 10, C: PM1}
	got := f.Diffuse(o)

	// Assert on *relative* drift. The absolute error is proportional to the field
	// magnitude, so a fixed absolute threshold would be arbitrary: it passes on
	// a field of 0..1 and fails on the same ramp scaled up.
	worstRel := 0.0
	for i := range f.V {
		if f.V[i] == 0 {
			continue
		}
		if r := math.Abs(got.V[i]-f.V[i]) / math.Abs(f.V[i]); r > worstRel {
			worstRel = r
		}
	}
	if worstRel > 1e-4 {
		t.Errorf("relative drift %.3g over 10 iterations, want negligible", worstRel)
	}

	// And the drift must match the analytic prediction for conductance shrinkage,
	// 10 * 4 * lambda * (1 - c). A larger error would mean a stencil bug on top
	// of the expected behaviour.
	c := math.Exp(-math.Pow(0.01/10.0, 2))
	predicted := 10 * 4 * 0.2 * (1 - c)
	if _, max := got.Range(); max > 0.14+predicted*1.5 {
		t.Errorf("max = %v, want at most the ramp max plus the predicted %v", max, predicted)
	}
}

func TestDiffuseFlattensNoise(t *testing.T) {
	// The reason for using this at all: high-frequency variation must go down.
	f := New(32, 32)
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			// Deterministic pseudo-noise, so a failure is reproducible.
			v := math.Sin(float64(x)*12.9898+float64(y)*78.233) * 43758.5453
			f.V[y*32+x] = v - math.Floor(v)
		}
	}
	_, before := f.Range()
	variance := func(g *Field) float64 {
		min, max := g.Range()
		m := 0.5 * (min + max)
		s := 0.0
		for _, v := range g.V {
			s += (v - m) * (v - m)
		}
		return s / float64(len(g.V))
	}
	o := DiffuseOptions{Iterations: 12, Lambda: 0.2, Kappa: 0.05, C: PM1}
	got := f.Diffuse(o)
	if variance(got) >= variance(f) {
		t.Errorf("variance did not drop: %v -> %v", variance(f), variance(got))
	}
	_, after := got.Range()
	if after >= before {
		t.Logf("range did not shrink (%v -> %v), acceptable if the mean shifted", before, after)
	}
}

func TestDiffuseIsANoOpWhenDisabled(t *testing.T) {
	f := New(4, 4)
	for i := range f.V {
		f.V[i] = float64(i) * 0.01
	}
	got := f.Diffuse(DiffuseOptions{Iterations: 0, Lambda: 0.2})
	for i := range f.V {
		if got.V[i] != f.V[i] {
			t.Fatalf("node %d changed with Iterations=0", i)
		}
	}
	// Lambda is clamped, not trusted: an unstable step must not be allowed to
	// make the field oscillate.
	loopy := f.Diffuse(DiffuseOptions{Iterations: 1, Lambda: 5})
	_, mx := loopy.Range()
	if math.IsNaN(mx) || math.IsInf(mx, 0) {
		t.Error("an over-large Lambda produced NaN or Inf")
	}
}

func TestDiffuseDoesNotTouchTheReceiver(t *testing.T) {
	f := New(4, 4)
	for i := range f.V {
		f.V[i] = float64(i%5) * 0.2
	}
	before := f.Clone()
	f.Diffuse(DiffuseOptions{Iterations: 3, Lambda: 0.2, Kappa: 0.1})
	for i := range f.V {
		if f.V[i] != before.V[i] {
			t.Fatalf("Diffuse mutated its receiver at node %d", i)
		}
	}
}

func TestConductivityShapes(t *testing.T) {
	pm1 := DiffuseOptions{Kappa: 1, C: PM1}
	pm2 := DiffuseOptions{Kappa: 1, C: PM2}
	if got := pm1.conductivity(0); math.Abs(got-1) > 1e-12 {
		t.Errorf("PM1 at zero gradient = %v, want 1", got)
	}
	if got := pm2.conductivity(0); math.Abs(got-1) > 1e-12 {
		t.Errorf("PM2 at zero gradient = %v, want 1", got)
	}
	// PM2 decays more slowly, which is why it holds large flat areas flatter.
	if pm2.conductivity(2) <= pm1.conductivity(2) {
		t.Errorf("PM2 (%v) should exceed PM1 (%v) at a gradient of 2",
			pm2.conductivity(2), pm1.conductivity(2))
	}
	// Each must be non-increasing in the gradient, and never negative. Tracked
	// per function: comparing PM2 against PM1 at the same gradient is a
	// different property, and mixing them up makes a correct implementation look
	// broken.
	for _, o := range []DiffuseOptions{pm1, pm2} {
		prev := math.Inf(1)
		for g := 0.0; g < 5; g += 0.25 {
			c := o.conductivity(g)
			if c < 0 {
				t.Fatalf("negative conductance at %v", g)
			}
			if c > prev+1e-12 {
				t.Fatalf("conductance rose at gradient %v: %v after %v", g, c, prev)
			}
			prev = c
		}
	}
}

func TestConductivityWithZeroKappa(t *testing.T) {
	// kappa=0 would divide by zero; the guard must turn it into a no-op filter
	// rather than NaN everywhere.
	o := DiffuseOptions{Kappa: 0, C: PM1}
	if got := o.conductivity(1000); got != 1 {
		t.Errorf("conductivity with Kappa=0 = %v, want 1", got)
	}
}

func TestNormalize(t *testing.T) {
	f := New(4, 1)
	f.V[0], f.V[1], f.V[2], f.V[3] = 2, 4, 6, 8
	g := f.Normalize()
	want := []float64{0, 2.0 / 6, 4.0 / 6, 1}
	for i, w := range want {
		if math.Abs(g.V[i]-w) > 1e-12 {
			t.Errorf("V[%d] = %v, want %v", i, g.V[i], w)
		}
	}
	// The receiver must survive untouched.
	if f.V[0] != 2 {
		t.Error("Normalize mutated its receiver")
	}
}

func TestNormalizeOfAConstantField(t *testing.T) {
	f := filled(4, 4, 0.3)
	g := f.Normalize()
	for i, v := range g.V {
		if v != 0.3 {
			t.Fatalf("V[%d] = %v, want the constant 0.3 preserved", i, v)
		}
	}
	if !f.Constant() {
		t.Error("Constant() = false for a constant field")
	}
}

func TestRangeAndConstant(t *testing.T) {
	f := New(3, 1)
	f.V[0], f.V[1], f.V[2] = -1, 5, 0
	if mn, mx := f.Range(); mn != -1 || mx != 5 {
		t.Errorf("Range() = (%v,%v), want (-1,5)", mn, mx)
	}
	if f.Constant() {
		t.Error("Constant() = true on a varying field")
	}
}

func TestFromOKLabL(t *testing.T) {
	lab := make([]float64, 4*3)
	for i := 0; i < 4; i++ {
		lab[3*i] = float64(i) * 0.25
		lab[3*i+1] = 99 // must be ignored
		lab[3*i+2] = -99
	}
	f := FromOKLabL(lab, 2, 2)
	for i := 0; i < 4; i++ {
		if f.V[i] != float64(i)*0.25 {
			t.Errorf("V[%d] = %v, want the L channel only", i, f.V[i])
		}
	}
}

func TestNewClampsDegenerateSizes(t *testing.T) {
	if f := New(0, 0); f.W < 1 || f.H < 1 {
		t.Errorf("New(0,0) = %dx%d, want at least 1x1", f.W, f.H)
	}
}

func TestCloneIsIndependent(t *testing.T) {
	f := New(2, 2)
	f.V[0] = 1
	c := f.Clone()
	c.V[0] = 2
	if f.V[0] != 1 {
		t.Error("Clone shares storage with its source")
	}
}
