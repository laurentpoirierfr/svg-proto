package baseline

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/elfeo/svg-proto/internal/pixelbuf"
	"github.com/elfeo/svg-proto/internal/quant"
)

// checkerboard is the adversarial case for the grid encoder: a tile straddling
// an edge averages to a colour that appears nowhere in the image, which is
// exactly where a mosaic loses fidelity.
func checkerboard(w, h, n int) image.Image {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if (x/n+y/n)%2 == 0 {
				m.SetNRGBA(x, y, color.NRGBA{R: 250, A: 255})
			} else {
				m.SetNRGBA(x, y, color.NRGBA{B: 250, A: 255})
			}
		}
	}
	return m
}

func paletteFor(t *testing.T, src image.Image, k int) (*pixelbuf.Image, *quant.Palette) {
	t.Helper()
	im := pixelbuf.FromImage(src)
	lab := im.Lab()
	n := im.W * im.H
	in := &quant.Input{W: im.W, H: im.H, Alpha: make([]float64, n)}
	for i := 0; i < n; i++ {
		in.L = append(in.L, lab.Pix[3*i])
		in.A = append(in.A, lab.Pix[3*i+1])
		in.B = append(in.B, lab.Pix[3*i+2])
		in.Alpha[i] = float64(im.A[i]) / 255
	}
	opts := quant.Default()
	opts.K = k
	return im, quant.Reduce(in, opts)
}

func wellFormed(t *testing.T, res *Result) {
	t.Helper()
	out := res.Doc.Encode()
	dec := xml.NewDecoder(bytes.NewReader(out))
	for {
		_, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				return
			}
			t.Fatalf("output is not well-formed XML: %v\n%s", err, out)
		}
	}
}

func TestEmbedIsWellFormedAndOneNode(t *testing.T) {
	res, err := Embed(checkerboard(16, 16, 4), Options{})
	if err != nil {
		t.Fatal(err)
	}
	wellFormed(t, res)
	if res.Shapes != 1 {
		t.Errorf("Shapes = %d, want 1", res.Shapes)
	}
	// The whole point of the null hypothesis: a single node, and lossless.
	if c := res.Doc.Census(); c.Elements > 2 {
		t.Errorf("Elements = %d, want at most 2 (svg + image)", c.Elements)
	}
	if !bytes.Contains(res.Doc.Encode(), []byte("data:image/png;base64,")) {
		t.Error("embed did not emit a data URI")
	}
}

func TestEmbedRoundTripsThePixels(t *testing.T) {
	src := checkerboard(16, 16, 4)
	res, err := Embed(src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Pull the base64 back out and decode it. If this fails, "lossless" is a lie
	// and every fidelity number measured against the embed baseline is void.
	out := string(res.Doc.Encode())
	i := strings.Index(out, "base64,")
	if i < 0 {
		t.Fatal("no data URI in the output")
	}
	// The payload ends at the closing quote of the href attribute.
	rest := out[i+len("base64,"):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		t.Fatal("unterminated href attribute")
	}
	dec, err := base64Decode(rest[:j])
	if err != nil {
		t.Fatal(err)
	}
	got, err := png.Decode(bytes.NewReader(dec))
	if err != nil {
		t.Fatal(err)
	}
	if got.Bounds() != src.Bounds() {
		t.Fatalf("bounds = %v, want %v", got.Bounds(), src.Bounds())
	}
	wantR, wantG, wantB, _ := src.At(3, 5).RGBA()
	gotR, gotG, gotB, _ := got.At(3, 5).RGBA()
	if wantR != gotR || wantG != gotG || wantB != gotB {
		t.Errorf("pixel (3,5) = (%d,%d,%d), want (%d,%d,%d)", gotR, gotG, gotB, wantR, wantG, wantB)
	}
}

func TestGridCoversTheWholeImage(t *testing.T) {
	src := checkerboard(32, 24, 4)
	im, pal := paletteFor(t, src, 2)
	res, err := Grid(im, pal, Options{Tile: 4})
	if err != nil {
		t.Fatal(err)
	}
	wellFormed(t, res)
	if res.Coverage != 1 {
		t.Errorf("Coverage = %v, want 1: a grid must tile the whole image", res.Coverage)
	}
	// 8 x 6 tiles, and no more: the budget is a promise about node count.
	want := (32 / 4) * (24 / 4)
	if res.Shapes != want {
		t.Errorf("Shapes = %d, want %d", res.Shapes, want)
	}
}

func TestGridGrowsTheTileToMeetTheBudget(t *testing.T) {
	// Truncating would leave part of the image uncovered; coarsening does not.
	// The budget is a node cap, and the only honest way to honour it on a grid is
	// to make the cells bigger.
	src := checkerboard(64, 64, 4)
	im, pal := paletteFor(t, src, 2)
	res, err := Grid(im, pal, Options{Tile: 1, MaxNodes: 16})
	if err != nil {
		t.Fatal(err)
	}
	if res.Shapes > 16 {
		t.Errorf("Shapes = %d, want at most the 16-node budget", res.Shapes)
	}
	if !res.Truncated {
		t.Error("Truncated = false even though the budget forced a coarser grid")
	}
	if res.Coverage != 1 {
		t.Errorf("Coverage = %v, want 1: coarsening must still cover everything", res.Coverage)
	}
}

func TestGridTileOfOneIsOneRectPerPixel(t *testing.T) {
	src := checkerboard(8, 8, 2)
	im, pal := paletteFor(t, src, 4)
	res, err := Grid(im, pal, Options{Tile: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Shapes != 64 {
		t.Errorf("Shapes = %d, want 64 (one per pixel)", res.Shapes)
	}
}

func TestRunLengthCoversTheWholeImage(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 20, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 20; x++ {
			src.SetNRGBA(x, y, color.NRGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	im, pal := paletteFor(t, src, 4)
	res, err := RunLength(im, pal, Options{})
	if err != nil {
		t.Fatal(err)
	}
	wellFormed(t, res)
	// A uniform image collapses to one rect per row: the encoder's best case.
	if res.Shapes != 10 {
		t.Errorf("Shapes = %d, want 10 (one per row)", res.Shapes)
	}
	if res.Coverage != 1 {
		t.Errorf("Coverage = %v, want 1", res.Coverage)
	}
}

func TestRunLengthIsExactForAPaletteImage(t *testing.T) {
	// An image already using few colours should be reproduced exactly, so any
	// error here is a bug in the run splitting rather than a quantisation loss.
	cols := []color.NRGBA{
		{R: 255, A: 255}, {G: 255, A: 255}, {B: 255, A: 255},
	}
	m := image.NewNRGBA(image.Rect(0, 0, 9, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 9; x++ {
			m.SetNRGBA(x, y, cols[x/3])
		}
	}
	im, pal := paletteFor(t, m, 3)
	if pal.Len() != 3 {
		t.Fatalf("palette has %d colours, want 3", pal.Len())
	}
	res, err := RunLength(im, pal, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// 3 blocks per row.
	if res.Shapes != 9 {
		t.Errorf("Shapes = %d, want 9 (3 blocks x 3 rows)", res.Shapes)
	}
}

func TestRunLengthRespectsTheNodeBudget(t *testing.T) {
	src := checkerboard(64, 64, 1)
	im, pal := paletteFor(t, src, 2)
	res, err := RunLength(im, pal, Options{MaxNodes: 100})
	if err != nil {
		t.Fatal(err)
	}
	if res.Shapes > 100 {
		t.Errorf("Shapes = %d, want at most 100", res.Shapes)
	}
	if !res.Truncated {
		t.Error("Truncated = false despite hitting the budget")
	}
	if res.Coverage >= 1 {
		t.Errorf("Coverage = %v, want < 1 for a truncated document", res.Coverage)
	}
}

func TestRunLengthStopsAtTheAlphaLevel(t *testing.T) {
	// Two pixels differing only in alpha must not merge into one rect, or the
	// semi-transparent part of the image silently becomes opaque.
	m := image.NewNRGBA(image.Rect(0, 0, 4, 1))
	m.SetNRGBA(0, 0, color.NRGBA{A: 255})
	m.SetNRGBA(1, 0, color.NRGBA{A: 255})
	m.SetNRGBA(2, 0, color.NRGBA{A: 128})
	m.SetNRGBA(3, 0, color.NRGBA{A: 128})
	im, pal := paletteFor(t, m, 2)
	// 8 levels, not 2: alpha 128/255 = 0.502 rounds up into the top bucket at
	// two levels, so two levels would legitimately merge these into one run.
	res, err := RunLength(im, pal, Options{AlphaLevels: 8})
	if err != nil {
		t.Fatal(err)
	}
	if res.Shapes != 2 {
		t.Errorf("Shapes = %d, want 2 (opaque run + semi-transparent run)", res.Shapes)
	}
	if !strings.Contains(string(res.Doc.Encode()), "fill-opacity") {
		t.Error("a semi-transparent region produced no fill-opacity")
	}
}

func TestAlphaLevelsOneIsAlwaysOpaque(t *testing.T) {
	m := image.NewNRGBA(image.Rect(0, 0, 4, 1))
	for x := 0; x < 4; x++ {
		m.SetNRGBA(x, 0, color.NRGBA{R: 10, A: uint8(x * 60)})
	}
	im, pal := paletteFor(t, m, 2)
	res, err := RunLength(im, pal, Options{AlphaLevels: 1})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(res.Doc.Encode()), "fill-opacity") {
		t.Error("AlphaLevels=1 emitted fill-opacity; it should force full opacity")
	}
}

func TestOutputIsDeterministic(t *testing.T) {
	src := checkerboard(24, 24, 3)
	build := func() string {
		im, pal := paletteFor(t, src, 5)
		res, err := RunLength(im, pal, Options{AlphaLevels: 4})
		if err != nil {
			t.Fatal(err)
		}
		return string(res.Doc.Encode())
	}
	first := build()
	for i := 0; i < 10; i++ {
		if got := build(); got != first {
			t.Fatalf("RunLength is not deterministic")
		}
	}
}

func TestTitleAndMetadata(t *testing.T) {
	src := checkerboard(8, 8, 2)
	im, pal := paletteFor(t, src, 2)
	res, err := RunLength(im, pal, Options{Title: "A & B", Annotate: true})
	if err != nil {
		t.Fatal(err)
	}
	out := string(res.Doc.Encode())
	if !strings.Contains(out, "A &amp; B") {
		t.Errorf("title not escaped:\n%s", out)
	}
	if !strings.Contains(out, `aria-label="A &amp; B"`) {
		t.Errorf("aria-label missing:\n%s", out)
	}
	if !strings.Contains(out, "generator=svg-proto") {
		t.Errorf("metadata missing:\n%s", out)
	}
	wellFormed(t, res)
}

func TestRGBHexIsCorrect(t *testing.T) {
	// An off-by-one in this helper produced index-out-of-range panics once
	// already; the three cases below pin the nibble order.
	cases := []struct {
		r, g, b uint8
		want    string
	}{
		{0, 0, 0, "#000000"},
		{255, 255, 255, "#ffffff"},
		{0xe0, 0xe3, 0x4f, "#e0e34f"},
		{1, 2, 3, "#010203"},
		{16, 32, 48, "#102030"},
	}
	for _, c := range cases {
		if got := rgbHex(c.r, c.g, c.b); got != c.want {
			t.Errorf("rgbHex(%d,%d,%d) = %s, want %s", c.r, c.g, c.b, got, c.want)
		}
	}
}

func TestAlphaLevelBuckets(t *testing.T) {
	if got := alphaLevel(1, 1); got != 255 {
		t.Errorf("alphaLevel(1,1) = %d, want 255", got)
	}
	if got := alphaLevel(0, 8); got != 0 {
		t.Errorf("alphaLevel(0,8) = %d, want 0", got)
	}
	if got := alphaLevel(0.5, 2); got != 255 {
		t.Errorf("alphaLevel(0.5,2) = %d, want 255 (the top bucket)", got)
	}
	if got := alphaLevel(0.4, 2); got != 0 {
		t.Errorf("alphaLevel(0.4,2) = %d, want 0 (the bottom bucket)", got)
	}
}

func base64Decode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(strings.TrimSpace(s))
}
