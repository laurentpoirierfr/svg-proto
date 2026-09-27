// Command svgstat measures an SVG document: what it costs (bytes, gzip bytes,
// element count, path commands, coordinate precision) and, given a reference
// raster, how faithful it is (SSIM, PSNR, mean OKLab distance).
//
// This is the project's measuring instrument. It exists so that no claim about
// quality or size is ever made without a number attached.
//
//	svgstat logo.svg
//	svgstat logo.svg --ref logo.png
//	svgstat before.svg after.svg
//	svgstat logo.svg --json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/elfeo/svg-proto/internal/colorconv"
	"github.com/elfeo/svg-proto/internal/imcompare"
	"github.com/elfeo/svg-proto/internal/svgstat"
)

func main() {
	var (
		refPath  = flag.String("ref", "", "reference raster (png or jpg) to compare the render against")
		renderW  = flag.Int("render-width", 0, "render width in px (default: the reference raster's width)")
		renderH  = flag.Int("render-height", 0, "render height in px (default: derived from the aspect ratio)")
		asJSON   = flag.Bool("json", false, "emit JSON")
		format   = flag.String("format", "report", "output format: report, json, row")
		category = flag.String("category", "", "category for -format row (aggregation label)")
		name     = flag.String("name", "", "file name for -format row (defaults to the file's base name)")
		oracle   = flag.String("oracle", "inkscape", "render oracle: inkscape or resvg")
		keepTemp = flag.Bool("keep-temp", false, "keep the temporary render")
		quiet    = flag.Bool("quiet", false, "suppress the report; only the exit status matters")
		bgFlag   = flag.String("bg", "white", "backdrop both the reference and the render are composited over before measuring; must match svgimg's -bg")
	)
	flag.Usage = usage
	// stdlib/flag insists flags come before positional arguments, which is not
	// what anyone types. Permute first so both orders work.
	flag.CommandLine.Parse(reorderArgs(flag.CommandLine, os.Args[1:]))

	// Resolved through the same parser the encoder uses, because a backdrop the
	// two sides spell differently is a silently wrong measurement rather than an
	// error, and this is the one place that can catch it.
	backdrop, err := colorconv.ParseBackdrop(*bgFlag)
	if err != nil {
		fail("-bg %s: %v", *bgFlag, err)
	}

	args := flag.Args()
	switch len(args) {
	case 1:
	case 2:
		if *refPath != "" {
			fmt.Fprintln(os.Stderr, "svgstat: -ref applies to a single document; it has no meaning in a two-file comparison")
			os.Exit(2)
		}
	default:
		usage()
		os.Exit(2)
	}

	a, err := svgstat.AnalyzeFile(args[0])
	if err != nil {
		fail("%v", err)
	}

	if len(args) == 2 {
		if *format == "row" {
			fmt.Fprintln(os.Stderr, "svgstat: -format row compares a single document")
			os.Exit(2)
		}
		b, err := svgstat.AnalyzeFile(args[1])
		if err != nil {
			fail("%v", err)
		}
		report(args, a, b, nil, *asJSON, *quiet, "report", "", "")
		return
	}

	var fidelity *imcompare.Result
	if *refPath != "" {
		fidelity, err = measure(args[0], *refPath, *oracle, *renderW, *renderH, *keepTemp, backdrop)
		if err != nil {
			fmt.Fprintf(os.Stderr, "svgstat: render oracle: %v\n", err)
			os.Exit(1)
		}
		applyFidelity(a, fidelity)
		attachRasterSize(a, *refPath)
	}
	if *name == "" {
		*name = strings.TrimSuffix(filepath.Base(args[0]), filepath.Ext(args[0]))
	}
	report(args, a, nil, fidelity, *asJSON, *quiet, *format, *category, *name)
}

// reorderArgs moves flags ahead of positional arguments, leaving "--" as a
// terminator. A non-boolean flag given in the separated form ("-ref x.png")
// consumes the following argument, so the next argument is not treated as a
// positional.
func reorderArgs(fs *flag.FlagSet, args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			positional = append(positional, args[i+1:]...)
			return append(flags, positional...)
		case len(a) < 2 || a[0] != '-':
			positional = append(positional, a)
		default:
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if idx := strings.Index(name, "="); idx >= 0 {
				continue // value attached, next arg is not ours
			}
			f := fs.Lookup(name)
			if f == nil {
				continue // unknown; let flag.Parse report it
			}
			if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
				continue
			}
			if i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		}
	}
	return append(flags, positional...)
}

// report writes the measurement in the requested format. A single file can be
// emitted as a full report, as JSON, or as one row of the benchmark table; two
// files can only be a report or JSON.
func report(args []string, a, b *svgstat.Stats, fid *imcompare.Result, asJSON, quiet bool, format, category, name string) {
	if quiet {
		return
	}
	switch format {
	case "row":
		fmt.Print(markdownRow(a, category, name))
		return
	case "json":
		var v any = a
		switch {
		case b != nil:
			v = map[string]any{"a": a, "b": b, "delta": svgstat.Compare(a, b)}
		case fid != nil:
			v = map[string]any{"stats": a, "fidelity": fid}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(v); err != nil {
			fail("%v", err)
		}
		return
	case "report":
	default:
		fail("unknown -format %q (want report, json or row)", format)
	}
	if b != nil {
		fmt.Printf("A: %s\nB: %s\n\n%s", args[0], args[1], svgstat.FormatDeltaTable(svgstat.Compare(a, b)))
		return
	}
	fmt.Printf("%s\n\n%s", args[0], a.String())
	if fid != nil {
		fmt.Printf("  score           %8.5f  (gradient-weighted OKLab RMSE, lower is better)\n", fid.Score)
	}
}

// markdownRow emits one row of the benchmark table. Bytes are reported but are
// not a success criterion (D6); the columns that carry weight are points and
// score.
func markdownRow(a *svgstat.Stats, category, name string) string {
	if category == "" {
		category = "-"
	}
	bytesCol, gzipCol := "-", "-"
	if a.Bytes > 0 {
		bytesCol = strconv.Itoa(a.Bytes)
	}
	if a.GzipBytes > 0 {
		gzipCol = strconv.Itoa(a.GzipBytes)
	}
	return fmt.Sprintf("| %s | %s | %s | %s | %d | %d | %d | %d | %s | %s | %s | %s | %s |\n",
		category, name, bytesCol, gzipCol, a.Elements, a.Paths, a.PolylinePoints,
		a.DistinctFills, ratioCell(a.RatioPNG, a.RatioJPG),
		numCell(a.SSIM), numCell(a.PSNR), numCell(a.DeltaE), numCell(a.Score))
}

func ratioCell(png, jpg float64) string {
	r := png
	if r == 0 {
		r = jpg
	}
	if r == 0 {
		return "-"
	}
	return strconv.FormatFloat(r, 'f', 3, 64)
}

func numCell(v float64) string {
	if v == 0 {
		return "-"
	}
	if math.IsInf(v, 0) {
		return "inf"
	}
	return strconv.FormatFloat(v, 'f', 4, 64)
}

func applyFidelity(a *svgstat.Stats, f *imcompare.Result) {
	a.Renders = true
	a.PSNR = round(f.PSNR, 3)
	a.SSIM = round(f.SSIM, 5)
	a.DeltaE = round(f.DeltaE, 5)
	a.Score = f.Score
}

// measure renders the SVG with an external oracle and compares it to the
// reference. The core library stays dependency-free and cgo-free; the renderer
// is a test-time tool, picked for quality rather than portability, because a
// mediocre oracle poisons every measurement made with it.
func measure(svgPath, refPath, oracle string, w, h int, keepTemp bool, bg [3]float64) (*imcompare.Result, error) {
	ref, err := decodeImage(refPath)
	if err != nil {
		return nil, err
	}
	rb := ref.Bounds()
	if w == 0 {
		w = rb.Dx()
	}
	if h == 0 {
		h = int(float64(rb.Dy()) * float64(w) / float64(rb.Dx()))
	}

	tmp, err := os.MkdirTemp("", "svgstat-")
	if err != nil {
		return nil, err
	}
	if !keepTemp {
		defer os.RemoveAll(tmp)
	}
	out := filepath.Join(tmp, "render.png")

	if err := render(oracle, svgPath, out, w, h); err != nil {
		return nil, err
	}
	got, err := decodeImage(out)
	if err != nil {
		return nil, err
	}
	// A comparison needs identical dimensions. If the oracle ignored the size
	// request, say so, rather than resampling behind the caller's back and
	// reporting a number that is not a comparison.
	if gb := got.Bounds(); gb.Dx() != w || gb.Dy() != h {
		return nil, fmt.Errorf("oracle rendered %dx%d, wanted %dx%d", gb.Dx(), gb.Dy(), w, h)
	}
	return imcompare.CompareWith(ref, got, imcompare.Options{Bg: bg, EdgeGain: imcompare.DefaultEdgeGain()})
}

func render(oracle, in, out string, w, h int) error {
	var cmd *exec.Cmd
	switch oracle {
	case "inkscape":
		cmd = exec.Command(oracle, "--export-type=png",
			"--export-filename="+out,
			fmt.Sprintf("--export-width=%d", w),
			fmt.Sprintf("--export-height=%d", h),
			in)
	case "resvg":
		cmd = exec.Command(oracle, "--width", strconv.Itoa(w), "--height", strconv.Itoa(h), in, out)
	default:
		return fmt.Errorf("unknown oracle %q (want inkscape or resvg)", oracle)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", oracle, err)
	}
	return nil
}

func decodeImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	im, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return im, nil
}

// attachRasterSize fills in the size ratio against the reference image, which is
// the single most decisive number in the tool: above 1.0 the SVG is bigger than
// the bitmap it came from, and no amount of quality makes that a good trade.
func attachRasterSize(a *svgstat.Stats, refPath string) {
	fi, err := os.Stat(refPath)
	if err != nil {
		return
	}
	switch strings.ToLower(filepath.Ext(refPath)) {
	case ".jpg", ".jpeg":
		a.RatiosAgainstRasters(0, int(fi.Size()))
	default:
		a.RatiosAgainstRasters(int(fi.Size()), 0)
	}
}

func round(v float64, digits int) float64 {
	f, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'f', digits, 64), 64)
	return f
}

func usage() {
	fmt.Fprint(os.Stderr, `svgstat - measure an SVG document

usage:
  svgstat <file.svg> [flags]        measure one document
  svgstat <a.svg> <b.svg>           compare the cost metrics of two documents

A ratio above 1.0 against the reference raster means the vector form is bigger
than the bitmap it came from, which is a failure no matter how it looks.

flags:
`)
	flag.PrintDefaults()
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "svgstat: "+format+"\n", args...)
	os.Exit(1)
}
