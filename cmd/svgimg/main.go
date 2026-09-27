// Command svgimg converts a raster image to SVG.
//
// Phase 1 ships only the reference encoders, and that is on purpose: they are
// the numbers every later stage has to beat. Adding a traceur before the
// baselines are on record is how a project convinces itself it is winning.
//
//	svgimg -in logo.png -out logo.svg -mode embed
//	svgimg -in photo.png -out photo.svg -mode runlength -k 32 -max-nodes 20000
//	svgimg -in logo.png -mode trace -k 8 -max-nodes 5000 -report
//	svgimg -in logo.png -mode trace -k 8 -curves -curve-tol 0.4 -curve-simplify 1 -report
//	svgimg -in a.png -mode grid -tile 16 -report
//
// Every run prints the same table, so the encoders are directly comparable.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	// Standard-library decoders only. The library itself never sees a format: it
	// takes an image.Image, so supporting webp or tiff is a one-line opt-in by
	// the caller, not a dependency this module drags in.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/elfeo/svg-proto/internal/baseline"
	"github.com/elfeo/svg-proto/internal/colorconv"
	"github.com/elfeo/svg-proto/internal/dom"
	"github.com/elfeo/svg-proto/internal/encode"
	"github.com/elfeo/svg-proto/internal/pixelbuf"
	"github.com/elfeo/svg-proto/internal/quant"
)

func main() {
	var (
		mode      = flag.String("mode", "runlength", "encoder: embed, grid, runlength or trace")
		in        = flag.String("in", "", "source image (required)")
		out       = flag.String("out", "", "output SVG (default: alongside the source, in assets/svg)")
		outDir    = flag.String("out-dir", "assets/svg", "directory for the default output path")
		k         = flag.Int("k", 16, "palette size, for grid, runlength and trace")
		tile      = flag.Int("tile", 8, "tile size in px, for grid")
		maxNodes  = flag.Int("max-nodes", 0, "cap on emitted shapes; 0 means no cap (the node budget, not a byte budget)")
		alphaLvl  = flag.Int("alpha-levels", 8, "distinct fill-opacity values to emit")
		tol       = flag.Float64("tol", 0.25, "starting simplification tolerance in px, for trace")
		minArea   = flag.Float64("min-area", 1, "smallest region in px^2 worth emitting, for trace")
		title     = flag.String("title", "", "accessible name for the document")
		annotate  = flag.Bool("annotate", false, "embed a <metadata> block with the encoder settings")
		report    = flag.Bool("report", false, "print the diagnostics even on success")
		stdout    = flag.Bool("stdout", false, "write the SVG to stdout instead of a file")
		prettyOut = flag.Bool("pretty", false, "indent the output")
		bg        = flag.String("bg", "", "composite the source over this sRGB colour before vectorising (e.g. white, #f0f0f0); empty keeps the source alpha")
		curves    = flag.Bool("curves", false, "for trace: fit Bezier segments instead of emitting the traced staircase")
		curveTol  = flag.Float64("curve-tol", 0.5, "for trace: largest distance in px between a fitted curve and the points it was fitted from")
		curveSimp = flag.Float64("curve-simplify", 0, "for trace -curves: simplification tolerance in px that strips the tracing staircase before fitting (default 1.5)")
	)
	flag.Parse()

	if *in == "" {
		fmt.Fprintln(os.Stderr, "svgimg: -in is required")
		flag.Usage()
		os.Exit(2)
	}
	f, err := os.Open(*in)
	if err != nil {
		fail("%v", err)
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		fail("decode %s: %v", *in, err)
	}
	if *bg != "" {
		backdrop, err := colorconv.ParseBackdrop(*bg)
		if err != nil {
			fail("-bg %s: %v", *bg, err)
		}
		src = compositeOver(src, backdrop)
	}

	opts := baseline.Options{
		Tile:        *tile,
		MaxNodes:    *maxNodes,
		AlphaLevels: *alphaLvl,
		Title:       *title,
		Annotate:    *annotate,
	}

	start := time.Now()
	var res *baseline.Result
	var tres *encode.Result

	switch *mode {
	case "embed":
		res, err = baseline.Embed(src, opts)
	case "grid", "runlength", "trace":
		im, pal := quantise(src, *k)
		switch *mode {
		case "grid":
			res, err = baseline.Grid(im, pal, opts)
		case "runlength":
			res, err = baseline.RunLength(im, pal, opts)
		case "trace":
			tres, err = encode.Encode(im, pal, encode.Options{
				MaxNodes:               *maxNodes,
				Tolerance:              *tol,
				AlphaLevels:            *alphaLvl,
				MinArea:                *minArea,
				Precision:              2,
				Curves:                 *curves,
				CurveTolerance:         *curveTol,
				CurveSimplifyTolerance: *curveSimp,
			})
		}
	default:
		fail("unknown -mode %q (want embed, grid, runlength or trace)", *mode)
	}
	if err != nil {
		fail("%s: %v", *mode, err)
	}
	elapsed := time.Since(start)

	// Both encoders hand back a *dom.Doc, so the writing path is shared; only the
	// report differs, because a path node is not a rectangle and the tracer's
	// tolerance is part of its result.
	var (
		outDoc *dom.Doc
		sum    string
	)
	if tres != nil {
		outDoc = tres.Doc
		sum = describeTrace(*in, tres, len(outDoc.Encode()), elapsed)
	} else {
		outDoc = res.Doc
		sum = describe(*in, res, len(outDoc.Encode()), elapsed)
	}
	outDoc.Indent = indentFor(*prettyOut)
	encoded := outDoc.Encode()
	if *stdout {
		os.Stdout.Write(encoded)
		if !*report {
			return
		}
		fmt.Fprint(os.Stderr, sum)
		return
	}
	dest := *out
	if dest == "" {
		dest = filepath.Join(*outDir, strings.TrimSuffix(filepath.Base(*in), filepath.Ext(*in))+".svg")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		fail("%v", err)
	}
	if err := os.WriteFile(dest, encoded, 0o644); err != nil {
		fail("%v", err)
	}
	fmt.Print(sum)
	fmt.Printf("wrote %s\n", dest)
}

// describeTrace reports the tracer's numbers, which are not the baselines' ones:
// a path node is not a rectangle, and the tolerance is part of the result.
func describeTrace(in string, res *encode.Result, bytesOut int, d time.Duration) string {
	var b strings.Builder
	census := res.Doc.Census()
	if fi, err := os.Stat(in); err == nil {
		fmt.Fprintf(&b, "%s\n", filepath.Base(in))
		fmt.Fprintf(&b, "  %-18s %8d  (gzip handled at packaging time)\n", "bytes", bytesOut)
		fmt.Fprintf(&b, "  %-18s %7.3fx source\n", "vs source bytes", float64(bytesOut)/float64(fi.Size()))
	}
	fmt.Fprintf(&b, "  %-18s %8d  (%d paths, %d rects)\n", "shapes", res.Shapes, res.Paths, res.Shapes-res.Paths)
	fmt.Fprintf(&b, "  %-18s %8d  (%d bands, %d loops traced)\n", "nodes", res.Nodes, res.Bands, res.Loops)
	fmt.Fprintf(&b, "  %-18s %8.4f px\n", "tolerance", res.Tolerance)
	fmt.Fprintf(&b, "  %-18s %8d  (max depth %d)\n", "elements", census.Elements, census.MaxDepth)
	fmt.Fprintf(&b, "  %-18s %8.1f%%\n", "coverage", res.Coverage*100)
	fmt.Fprintf(&b, "  %-18s %8s\n", "encode time", d.Round(time.Millisecond))
	if res.Truncated {
		fmt.Fprintf(&b, "  %-18s OVER the node budget\n", "status")
	}
	for _, n := range res.Notes {
		fmt.Fprintf(&b, "  note: %s\n", n)
	}
	return b.String()
}

// quantise is the shared front half of every palette-based encoder. Kept in one
// place so the three encoders are provably looking at the same palette: a
// difference in their results is then down to the encoding, not to the
// quantiser.
func quantise(src image.Image, k int) (*pixelbuf.Image, *quant.Palette) {
	im := pixelbuf.FromImage(src)
	qopts := quant.Default()
	qopts.K = k
	return im, quant.Reduce(&quant.Input{
		W: im.W, H: im.H,
		L:     im.LabChannel(0),
		A:     im.LabChannel(1),
		B:     im.LabChannel(2),
		Alpha: im.Alpha01(),
	}, qopts)
}

func indentFor(pretty bool) int {
	if pretty {
		return 2
	}
	return 0
}

func describe(in string, res *baseline.Result, bytesOut int, d time.Duration) string {
	var b strings.Builder
	census := res.Doc.Census()
	srcSize := int64(-1)
	if fi, err := os.Stat(in); err == nil {
		srcSize = fi.Size()
	}
	fmt.Fprintf(&b, "%s\n", filepath.Base(in))
	fmt.Fprintf(&b, "  %-18s %8d  (gzip handled at packaging time)\n", "bytes", bytesOut)
	if srcSize > 0 {
		fmt.Fprintf(&b, "  %-18s %7.3fx source\n", "vs source bytes", float64(bytesOut)/float64(srcSize))
	}
	fmt.Fprintf(&b, "  %-18s %8d  (%d rects, %d colours)\n", "shapes", res.Shapes, res.Rects, res.PaletteLen)
	fmt.Fprintf(&b, "  %-18s %8d  (max depth %d)\n", "elements", census.Elements, census.MaxDepth)
	fmt.Fprintf(&b, "  %-18s %8.1f%%\n", "coverage", res.Coverage*100)
	fmt.Fprintf(&b, "  %-18s %8s\n", "encode time", d.Round(time.Millisecond))
	if res.Truncated {
		fmt.Fprintf(&b, "  %-18s TRUNCATED at the node budget\n", "status")
	}
	for _, n := range res.Notes {
		fmt.Fprintf(&b, "  note: %s\n", n)
	}
	return b.String()
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "svgimg: "+format+"\n", args...)
	os.Exit(1)
}

// compositeOver flattens src onto an opaque backdrop, producing an NRGBA the rest
// of the pipeline cannot tell apart from an image that was always opaque. It
// delegates the arithmetic to colorconv.CompositeOver, the same function the
// comparator uses, so the encoder and the measurement can never disagree about
// what "over white" means.
func compositeOver(src image.Image, bg [3]float64) *image.NRGBA {
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			r, g, bl, a := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			cr, cg, cb := colorconv.CompositeOver(float64(r)/257, float64(g)/257, float64(bl)/257,
				float64(a)/65535, bg[0], bg[1], bg[2])
			dst.SetNRGBA(x, y, color.NRGBA{R: uint8(cr + 0.5), G: uint8(cg + 0.5), B: uint8(cb + 0.5), A: 255})
		}
	}
	return dst
}
