package contour

import (
	"math"
	"testing"

	"github.com/elfeo/svg-proto/internal/field"
)

// FuzzTrace asserts the invariants a caller is entitled to rely on: every loop
// is closed, has enough points to enclose area, and is finite. A tracer that
// panics on a NaN or a degenerate plateau takes the whole conversion with it, and
// the saddle cases are exactly the inputs a hand-written test list forgets.
func FuzzTrace(fz *testing.F) {
	fz.Add(4, 4, "0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0")
	fz.Add(2, 2, "0.6 0 0 0.6")
	fz.Add(9, 7, "0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5 0.5")
	fz.Fuzz(func(t *testing.T, w, h int, blob string) {
		if w < 1 || w > 40 || h < 1 || h > 40 || len(blob) != w*h {
			return
		}
		fl := field.New(w, h)
		for i := 0; i < w*h; i++ {
			fl.V[i] = float64(blob[i]) / 255
		}
		for _, lvl := range []float64{0, 0.25, 0.5, 1} {
			loops := Trace(fl, Options{Level: lvl, MinPoints: 3, JoinTolerance: 1e-9})
			st := Stats(loops)
			if len(loops) > 0 && !st.ClosedOK {
				t.Fatalf("level %v: unclosed loop", lvl)
			}
			for i, l := range loops {
				if len(l) < 4 {
					t.Fatalf("level %v: loop %d has %d points", lvl, i, len(l))
				}
				if a := l.Area(); math.IsNaN(a) || math.IsInf(a, 0) {
					t.Fatalf("level %v: loop %d area %v", lvl, i, a)
				}
				for _, p := range l {
					if math.IsNaN(p.X) || math.IsNaN(p.Y) ||
						math.IsInf(p.X, 0) || math.IsInf(p.Y, 0) {
						t.Fatalf("level %v: loop %d has a non-finite point %v", lvl, i, p)
					}
				}
			}
		}
	})
}
