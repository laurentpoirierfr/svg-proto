// Package field holds the scalar field the contour tracer runs on.
//
// Why a field and not the image: tracing is a *topological* problem, and topology
// lives in a function over the plane, not in a grid of colours. Isolating the
// scalar field makes the two halves of the pipeline testable apart:
//
//	image -> [pixelbuf] -> OKLab L -> [field] -> smoothed scalar field
//	      -> [contour] -> loops -> [simplify] -> loops -> [encode] -> SVG
//
// # The grid is nodes, not pixels
//
// Field samples sit on *nodes*. An image of W*H pixels gives a field of W*H
// nodes, and marching squares then sees (W-1)*(H-1) cells. This is a deliberate
// off-by-one: the contour runs between pixel centres, which is the correct place
// for a boundary, and it is what makes sub-pixel interpolation meaningful at all.
//
// # The field is scalar, deliberately
//
// Tracing one iso-level of L and repainting it k times is not just simpler than
// tracing k colours: it is *cheaper*, because the k-1 thresholds share almost
// all of their geometry. A colour boundary is where two levels disagree, so the
// layered scheme emits each boundary exactly once. The alternative, a
// k-way vector field, costs a full contour extraction per colour.
package field

import "math"

// Field is a scalar field over a rectangular grid of nodes.
type Field struct {
	W, H int
	V    []float64
}

// New returns a zero field.
func New(w, h int) *Field {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return &Field{W: w, H: h, V: make([]float64, w*h)}
}

// FromOKLabL takes the L channel of an interleaved OKLab plane.
func FromOKLabL(lab []float64, w, h int) *Field {
	f := New(w, h)
	n := w * h
	if len(lab) < 3*n {
		n = len(lab) / 3
	}
	for i := 0; i < n; i++ {
		f.V[i] = lab[3*i]
	}
	return f
}

// At reads a node, clamping out-of-range coordinates to the border.
//
// Clamping rather than returning zero is deliberate: contour tracing probes
// neighbours that fall outside the grid, and a silent zero there would invent a
// hard edge along every image border.
func (f *Field) At(x, y int) float64 {
	if x < 0 {
		x = 0
	} else if x >= f.W {
		x = f.W - 1
	}
	if y < 0 {
		y = 0
	} else if y >= f.H {
		y = f.H - 1
	}
	return f.V[y*f.W+x]
}

// Set writes a node, ignoring out-of-range coordinates.
func (f *Field) Set(x, y int, v float64) {
	if x < 0 || x >= f.W || y < 0 || y >= f.H {
		return
	}
	f.V[y*f.W+x] = v
}

// Clone returns an independent copy.
func (f *Field) Clone() *Field {
	c := &Field{W: f.W, H: f.H, V: make([]float64, len(f.V))}
	copy(c.V, f.V)
	return c
}

// Range returns the extrema. An empty or constant field reports (0, 0).
func (f *Field) Range() (min, max float64) {
	if len(f.V) == 0 {
		return 0, 0
	}
	min, max = f.V[0], f.V[0]
	for _, v := range f.V {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	return min, max
}

// Constant reports whether the field has no variation at all, in which case
// there is nothing to trace and the caller should emit a flat fill.
func (f *Field) Constant() bool {
	min, max := f.Range()
	return max-min <= 1e-12
}

// Normalize rescales the field to [0,1]. Returns a new field; the receiver is
// untouched so callers can keep the original for fidelity scoring.
func (f *Field) Normalize() *Field {
	min, max := f.Range()
	out := f.Clone()
	span := max - min
	if span <= 1e-12 {
		return out
	}
	inv := 1 / span
	for i, v := range out.V {
		out.V[i] = (v - min) * inv
	}
	return out
}

// ---------------------------------------------------------------------------
// derivatives
// ---------------------------------------------------------------------------

// Gradients returns the partial derivatives by central difference, one-sided at
// the borders.
func (f *Field) Gradients() (gx, gy *Field) {
	gx, gy = New(f.W, f.H), New(f.W, f.H)
	for y := 0; y < f.H; y++ {
		for x := 0; x < f.W; x++ {
			// Central in the interior, one-sided on the edge. A zero gradient at
			// the border would make the border look like a plateau to the
			// diffusion step, which shows up as a visible rim.
			var dx, dy float64
			if x == 0 {
				dx = f.At(1, y) - f.At(0, y)
			} else if x == f.W-1 {
				dx = f.At(x, y) - f.At(x-1, y)
			} else {
				dx = (f.At(x+1, y) - f.At(x-1, y)) / 2
			}
			if y == 0 {
				dy = f.At(x, 1) - f.At(x, 0)
			} else if y == f.H-1 {
				dy = f.At(x, y) - f.At(x, y-1)
			} else {
				dy = (f.At(x, y+1) - f.At(x, y-1)) / 2
			}
			gx.V[y*f.W+x] = dx
			gy.V[y*f.W+x] = dy
		}
	}
	return gx, gy
}

// Abs returns |value| elementwise. Deliberately not called Magnitude: the
// gradient norm of a field is hypot(dx, dy), and a method returning sqrt(v*v) of
// its own values while claiming to be a magnitude is a trap that silently halves
// every edge weight downstream.
func (f *Field) Abs() *Field {
	m := New(f.W, f.H)
	for i, v := range f.V {
		m.V[i] = math.Abs(v)
	}
	return m
}

// GradientMagnitude combines two gradient fields into their norm. This is the
// one to use for edge weighting during simplification.
func GradientMagnitude(gx, gy *Field) *Field {
	if gx.W != gy.W || gx.H != gy.H {
		return nil
	}
	m := New(gx.W, gx.H)
	for i := range m.V {
		m.V[i] = math.Hypot(gx.V[i], gy.V[i])
	}
	return m
}

// GradientMagnitudeOf computes the gradient norm of f directly, without
// allocating two intermediate gradient fields.
func GradientMagnitudeOf(f *Field) *Field {
	m := New(f.W, f.H)
	for y := 0; y < f.H; y++ {
		for x := 0; x < f.W; x++ {
			var dx, dy float64
			if x == 0 {
				dx = f.At(1, y) - f.At(0, y)
			} else if x == f.W-1 {
				dx = f.At(x, y) - f.At(x-1, y)
			} else {
				dx = (f.At(x+1, y) - f.At(x-1, y)) / 2
			}
			if y == 0 {
				dy = f.At(x, 1) - f.At(x, 0)
			} else if y == f.H-1 {
				dy = f.At(x, y) - f.At(x, y-1)
			} else {
				dy = (f.At(x, y+1) - f.At(x, y-1)) / 2
			}
			m.V[y*f.W+x] = math.Hypot(dx, dy)
		}
	}
	return m
}

// ---------------------------------------------------------------------------
// anisotropic diffusion
// ---------------------------------------------------------------------------

// Conductivity is the Perona-Malik edge-stopping function.
type Conductivity int

const (
	// PM1 is exp(-(grad/kappa)^2): the classic Perona-Malik conductance. Favours
	// smooth ramps and thin edges equally.
	PM1 Conductivity = iota
	// PM2 is 1/(1+(grad/kappa)^2): keeps wide flat regions flatter than PM1 does,
	// which is usually what a logo wants.
	PM2
)

// DiffuseOptions configures anisotropic diffusion.
type DiffuseOptions struct {
	// Iterations is the number of passes. Each pass moves a pixel by at most
	// Lambda, so this is the single knob that trades detail against node count.
	Iterations int
	// Lambda is the step size, in (0, 0.25]. Above 0.25 the explicit scheme
	// becomes unstable and the field oscillates instead of smoothing.
	Lambda float64
	// Kappa is the edge threshold, in the units of the field. Gradients above
	// kappa are stopped, so kappa should sit near the strongest edge in the
	// image and well above the noise floor.
	Kappa float64
	// C selects the edge-stopping function.
	C Conductivity
}

// DefaultDiffuse returns conservative settings: barely any smoothing.
func DefaultDiffuse() DiffuseOptions {
	return DiffuseOptions{Iterations: 4, Lambda: 0.15, Kappa: 0.08, C: PM1}
}

func (o DiffuseOptions) conductivity(grad float64) float64 {
	if o.Kappa <= 0 {
		return 1
	}
	r := grad / o.Kappa
	switch o.C {
	case PM2:
		return 1 / (1 + r*r)
	default:
		return math.Exp(-r * r)
	}
}

// Diffuse returns a smoothed copy.
//
// Perona-Malik, in the von Neumann stencil form: each neighbour's contribution
// is gated by the conductance across the edge to that neighbour, so diffusion
// flows freely inside a flat area and stops at a contrast boundary. That is the
// whole point of using an *anisotropic* filter: a Gaussian would round the corners
// off, and corners are most of what makes a logo recognisable.
//
// The border replicates rather than zero-pads, so no artificial edge is created
// at the frame. Padding with zero is the classic way to get a dark rim around
// every traced image.
func (f *Field) Diffuse(o DiffuseOptions) *Field {
	if o.Iterations <= 0 || o.Lambda <= 0 {
		return f.Clone()
	}
	lambda := math.Min(o.Lambda, 0.25)
	cur := f.Clone()
	next := New(f.W, f.H)

	// ghost returns the neighbour value in each direction, using linear
	// extrapolation (a Neumann condition) at the borders.
	//
	// The obvious alternative, clamping the way At does, is wrong here. Clamping
	// gives a west ghost equal to the centre, so that direction contributes
	// nothing to the Laplacian and a linear ramp *drifts* at the frame: an
	// artificial rim that the tracer then faithfully outlines. Zero-padding is
	// worse. 2*u - u_neighbour extrapolates a ramp exactly, so ramps stay put and
	// the border sees only real image content.
	ghost := func(x, y, dx, dy int) float64 {
		nx, ny := x+dx, y+dy
		switch {
		case nx < 0:
			return 2*cur.At(x, y) - cur.At(x+1, y)
		case nx >= f.W:
			return 2*cur.At(x, y) - cur.At(x-1, y)
		case ny < 0:
			return 2*cur.At(x, y) - cur.At(x, y+1)
		case ny >= f.H:
			return 2*cur.At(x, y) - cur.At(x, y-1)
		default:
			return cur.At(nx, ny)
		}
	}

	for it := 0; it < o.Iterations; it++ {
		for y := 0; y < f.H; y++ {
			for x := 0; x < f.W; x++ {
				i := y*f.W + x
				u := cur.V[i]
				// Each conductance gates the neighbour behind that edge, so
				// diffusion flows freely across a flat area and stalls at a
				// contrast boundary. That anisotropy is the entire reason for
				// using this filter instead of a Gaussian: a Gaussian rounds off
				// corners, and corners are most of what makes a logo readable.
				w := ghost(x, y, -1, 0)
				e := ghost(x, y, 1, 0)
				n := ghost(x, y, 0, -1)
				s := ghost(x, y, 0, 1)
				sum := o.conductivity(u-w)*w + o.conductivity(e-u)*e +
					o.conductivity(u-n)*n + o.conductivity(s-u)*s
				// The centre keeps the residual weight 1, which is what makes the
				// stencil conservative in the interior.
				next.V[i] = u + lambda*(sum-4*u)
			}
		}
		cur, next = next, cur
	}
	return cur
}
