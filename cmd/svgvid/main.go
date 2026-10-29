// Command svgvid converts a sequence of raster frames to the D1 container: a
// poster SVG, full keyframes, per-frame delta layers, a JSON timeline, and a
// minimal player.
//
// Frames are read from a directory, sorted by filename, and must share one size.
// Extract them with any tool, for example:
//
//	ffmpeg -i clip.mp4 -vf fps=24 frames/%04d.png
//	svgvid -in frames -out out -fps 24 -k 16
//
// The node budget is per layer, not per byte: a delta and a keyframe are each
// held to -max-nodes. The report prints the cost of the layered clip against
// encoding every frame independently, so the gain or its absence is visible.
package main

import (
	"flag"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/elfeo/svg-proto/internal/anim"
	"github.com/elfeo/svg-proto/internal/encode"
	"github.com/elfeo/svg-proto/internal/pixelbuf"
)

func main() {
	var (
		in        = flag.String("in", "", "directory of frame images, sorted by filename (required)")
		out       = flag.String("out", "assets/video", "output directory")
		fps       = flag.Float64("fps", 24, "playback rate written to the timeline")
		k         = flag.Int("k", 16, "palette size for the whole clip")
		gop       = flag.Int("gop", 0, "largest number of frames between keyframes; 0 chooses by node cost")
		maxNodes  = flag.Int("max-nodes", 0, "per-layer node budget; 0 means no cap")
		tol       = flag.Float64("tol", 0.25, "starting simplification tolerance in px")
		minArea   = flag.Float64("min-area", 1, "smallest region in px^2 worth emitting")
		alphaLvl  = flag.Int("alpha-levels", 8, "distinct fill-opacity values to emit")
		curves    = flag.Bool("curves", false, "fit Bezier segments instead of the traced staircase")
		curveTol  = flag.Float64("curve-tol", 0.4, "largest distance in px between a fitted curve and its points")
		curveSimp = flag.Float64("curve-simplify", 0, "simplification tolerance that strips the staircase before fitting (default 1.5)")
		verbose   = flag.Bool("v", false, "print one line per layer")
	)
	flag.Parse()

	if *in == "" {
		fmt.Fprintln(os.Stderr, "svgvid: -in is required")
		flag.Usage()
		os.Exit(2)
	}
	frames, names, err := loadFrames(*in)
	if err != nil {
		fail("%v", err)
	}

	start := time.Now()
	clip, err := anim.Encode(frames, anim.Options{
		FPS:      *fps,
		Colors:   *k,
		GOP:      *gop,
		MaxNodes: *maxNodes,
		Encode: encode.Options{
			MaxNodes:               *maxNodes,
			Tolerance:              *tol,
			AlphaLevels:            *alphaLvl,
			MinArea:                *minArea,
			Precision:              2,
			Curves:                 *curves,
			CurveTolerance:         *curveTol,
			CurveSimplifyTolerance: *curveSimp,
		},
	})
	if err != nil {
		fail("%v", err)
	}
	manifest, err := clip.Write(*out)
	if err != nil {
		fail("%v", err)
	}
	elapsed := time.Since(start)

	keys, deltas, nodes := clip.Stats()
	bytesOut := 0
	truncated := 0
	worst := 0
	for i, l := range clip.Layers {
		b := len(l.Doc.Encode())
		bytesOut += b
		if l.Truncated {
			truncated++
		}
		if l.Nodes > clip.Layers[worst].Nodes {
			worst = i
		}
		if *verbose {
			fmt.Printf("  %-4d %-8s %7d nodes %7.2f%% changed %8d bytes  %s\n",
				i, l.Kind, l.Nodes, 100*l.Changed, b, names[i])
		}
	}
	ratio := 0.0
	if nodes > 0 {
		ratio = float64(clip.AllINodes) / float64(nodes)
	}

	fmt.Printf("%d frames, %dx%d, %.1f fps, palette %d\n",
		len(clip.Layers), clip.Width, clip.Height, clip.FPS, len(manifest.Palette))
	fmt.Printf("  %-18s %d keyframes, %d deltas\n", "layers", keys, deltas)
	fmt.Printf("  %-18s %d published, %.1f/frame (%d all-I, %.2fx less)\n",
		"nodes", nodes, float64(nodes)/float64(len(clip.Layers)), clip.AllINodes, ratio)
	fmt.Printf("  %-18s %d bytes across %d SVG layers\n", "svg bytes", bytesOut, len(clip.Layers))
	fmt.Printf("  %-18s frame %d, %d nodes\n", "largest layer", worst, clip.Layers[worst].Nodes)
	if truncated > 0 {
		fmt.Printf("  %-18s %d layers over the node budget\n", "status", truncated)
	}
	fmt.Printf("  %-18s %s\n", "encode time", elapsed.Round(time.Millisecond))
	fmt.Printf("wrote %s (poster.svg, %d layers, timeline.json, index.html)\n",
		*out, len(clip.Layers))
	if *gop == 0 && deltas > 0 {
		fmt.Println("note: keyframes are chosen by node cost; set -gop to bound scrubbing")
	}
}

// loadFrames reads every image in dir in filename order. Zero-padded names are
// the caller's job; lexicographic order is the only promise, because guessing a
// numeric suffix would silently reorder names that do not follow the convention.
func loadFrames(dir string) ([]*pixelbuf.Image, []string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".png", ".jpg", ".jpeg", ".gif":
			files = append(files, e.Name())
		}
	}
	if len(files) == 0 {
		return nil, nil, fmt.Errorf("%s: no png, jpeg or gif frames", dir)
	}
	sort.Strings(files)

	frames := make([]*pixelbuf.Image, 0, len(files))
	names := make([]string, 0, len(files))
	for _, name := range files {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return nil, nil, err
		}
		src, _, err := image.Decode(f)
		f.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %v", name, err)
		}
		frames = append(frames, pixelbuf.FromImage(src))
		names = append(names, name)
	}
	return frames, names, nil
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "svgvid: "+format+"\n", args...)
	os.Exit(1)
}
