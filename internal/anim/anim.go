// Package anim encodes a sequence of frames as the container decided in D1: a
// poster SVG, full keyframes, and per-frame delta layers, plus a JSON sidecar
// that a small player reads.
//
// The unit is the layer, not the frame. A keyframe is a complete document; a
// delta is a transparent document that repaints only the pixels whose palette
// index changed since the previous frame. Compositing a keyframe and every delta
// up to frame i reproduces frame i exactly, because each changed pixel is
// painted with its new colour and every unchanged pixel keeps the colour it had.
//
// The palette is built once for the whole clip from a strided sample of every
// frame, then every frame is assigned to it. A per-frame palette would make the
// deltas cheaper but would repaint the whole frame whenever the palette drifts,
// which is exactly the cost the delta is meant to avoid.
package anim

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"

	"github.com/elfeo/svg-proto/internal/colorconv"
	"github.com/elfeo/svg-proto/internal/dom"
	"github.com/elfeo/svg-proto/internal/encode"
	"github.com/elfeo/svg-proto/internal/pixelbuf"
	"github.com/elfeo/svg-proto/internal/quant"
)

// Kind is what a layer is relative to the document before it.
type Kind uint8

const (
	// Poster is the first full frame. It is a keyframe that also serves as the
	// still preview.
	Poster Kind = iota
	// Keyframe is a later full frame; it hides everything below it.
	Keyframe
	// Delta paints only the pixels that changed since the previous frame.
	Delta
)

func (k Kind) String() string {
	switch k {
	case Poster:
		return "poster"
	case Keyframe:
		return "keyframe"
	case Delta:
		return "delta"
	}
	return "unknown"
}

// Options configures the encoder.
type Options struct {
	// FPS is the playback rate written to the sidecar. Zero means 24.
	FPS float64

	// Colors is the palette size for the whole clip. Zero means 16.
	Colors int

	// GOP is the largest number of frames between two keyframes. Zero leaves
	// keyframes to the cost test alone (emit one when a delta would not be
	// cheaper than a full frame), which is the node-optimal greedy choice but can
	// make scrubbing replay a long delta chain.
	GOP int

	// MaxNodes is the per-layer node budget. Zero means no budget. A keyframe and
	// a delta are each held to it, so the budget is a decode-time guarantee per
	// painted layer rather than an average.
	MaxNodes int

	// Encode is the tracer configuration shared by keyframes and deltas (curves,
	// tolerance, minimum area, precision, alpha levels).
	Encode encode.Options
}

// Layer is one emitted SVG document plus what the sidecar needs to place it.
type Layer struct {
	Kind Kind
	// Doc is the document to write as SVG.
	Doc *dom.Doc
	// Nodes is the path-node count the tracer reported.
	Nodes int
	// Base is the index of the keyframe a delta builds on. For a poster or a
	// keyframe it is the layer's own index.
	Base int
	// Changed is the fraction of pixels a delta repaints; zero for a keyframe.
	Changed float64
	// Truncated reports that the layer exceeded MaxNodes even after retracing.
	Truncated bool
}

// Clip is a fully encoded sequence, before filenames are assigned.
type Clip struct {
	Width, Height int
	FPS           float64
	// Palette holds RGBA entries, four bytes each.
	Palette []uint8
	Layers  []Layer
	// AllINodes is the cost of encoding every frame independently, which is the
	// number the keyframe/delta split has to beat.
	AllINodes int
}

// Frame is one entry of the sidecar timeline.
type Frame struct {
	Time      float64 `json:"t"`
	Kind      string  `json:"kind"`
	Src       string  `json:"src"`
	Base      int     `json:"base"`
	Nodes     int     `json:"nodes"`
	Changed   float64 `json:"changed,omitempty"`
	Truncated bool    `json:"truncated,omitempty"`
}

// Manifest is the sidecar JSON. Version is present so a player can refuse a
// format it does not understand instead of silently drawing the wrong thing.
type Manifest struct {
	Version int      `json:"version"`
	Width   int      `json:"width"`
	Height  int      `json:"height"`
	FPS     float64  `json:"fps"`
	Palette []string `json:"palette"`
	Frames  []Frame  `json:"frames"`
}

// sampleLimit caps the pixels the palette is built from. A palette is a summary;
// seeing more than this stops improving it and starts costing memory.
const sampleLimit = 200000

// Encode builds the clip. Frames must share one size; they are not copied.
func Encode(frames []*pixelbuf.Image, opts Options) (*Clip, error) {
	if len(frames) == 0 {
		return nil, fmt.Errorf("anim: no frames")
	}
	w, h := frames[0].W, frames[0].H
	if w < 1 || h < 1 {
		return nil, fmt.Errorf("anim: empty frame %dx%d", w, h)
	}
	for i, f := range frames {
		if f == nil {
			return nil, fmt.Errorf("anim: frame %d is nil", i)
		}
		if f.W != w || f.H != h {
			return nil, fmt.Errorf("anim: frame %d is %dx%d, want %dx%d", i, f.W, f.H, w, h)
		}
	}
	if opts.FPS <= 0 {
		opts.FPS = 24
	}
	if opts.Colors < 2 {
		opts.Colors = 16
	}
	eo := opts.Encode
	if opts.MaxNodes > 0 {
		eo.MaxNodes = opts.MaxNodes
	}

	gpal := quant.Reduce(sampleInput(frames), quant.Options{K: opts.Colors})
	if gpal.Len() == 0 {
		return nil, fmt.Errorf("anim: empty palette")
	}
	pl := paletteLab(gpal)
	idx := make([][]uint16, len(frames))
	for i, f := range frames {
		idx[i] = assign(f, pl)
	}

	clip := &Clip{
		Width: w, Height: h, FPS: opts.FPS,
		Palette: append([]uint8(nil), gpal.Colors...),
	}
	lastKey := 0
	for i, f := range frames {
		pal := &quant.Palette{Colors: gpal.Colors, Index: idx[i], W: w, H: h}
		full, err := encode.Encode(f, pal, eo)
		if err != nil {
			return nil, fmt.Errorf("anim: frame %d: %w", i, err)
		}
		clip.AllINodes += full.Nodes

		key := i == 0 || (opts.GOP > 0 && i-lastKey >= opts.GOP)
		var layer Layer
		if !key {
			d, changed, err := encodeDelta(f, idx[i], idx[i-1], gpal, eo)
			if err != nil {
				return nil, fmt.Errorf("anim: delta %d: %w", i, err)
			}
			// A delta at the same node count as a keyframe is not a win: it
			// leaves the decode chain long for no saving. Prefer the keyframe
			// then. A delta that repaints more than nine tenths of the frame is
			// a keyframe in disguise; forcing it keeps every delta mostly
			// transparent, which is what makes stacking them cheap to read.
			if changed == 0 || (changed <= 0.9 && d.Nodes < full.Nodes) {
				layer = Layer{Kind: Delta, Doc: d.Doc, Nodes: d.Nodes, Base: lastKey,
					Changed: changed, Truncated: d.Truncated}
			} else {
				key = true
			}
		}
		if key {
			kind := Keyframe
			if i == 0 {
				kind = Poster
			}
			layer = Layer{Kind: kind, Doc: full.Doc, Nodes: full.Nodes, Base: i,
				Truncated: full.Truncated}
			lastKey = i
		}
		clip.Layers = append(clip.Layers, layer)
	}
	return clip, nil
}

// sampleInput builds the palette input from a strided sample of every frame, so
// a clip whose first frame is blank does not quantise the rest to one colour.
func sampleInput(frames []*pixelbuf.Image) *quant.Input {
	w, h := frames[0].W, frames[0].H
	total := len(frames) * w * h
	step := 1
	if total > sampleLimit {
		step = (total + sampleLimit - 1) / sampleLimit
	}
	in := &quant.Input{H: 1}
	for _, f := range frames {
		lab := f.Lab()
		alpha := f.Alpha01()
		for i := 0; i < w*h; i += step {
			in.L = append(in.L, lab.Pix[3*i])
			in.A = append(in.A, lab.Pix[3*i+1])
			in.B = append(in.B, lab.Pix[3*i+2])
			in.Alpha = append(in.Alpha, alpha[i])
		}
	}
	in.W = len(in.L)
	return in
}

// paletteLab caches the palette in OKLab. The conversion is done once per clip,
// not once per pixel per frame.
func paletteLab(pal *quant.Palette) [][3]float64 {
	out := make([][3]float64, pal.Len())
	for i := range out {
		r, g, b := pal.RGB(i)
		out[i][0], out[i][1], out[i][2] = colorconv.RGBToLabFloat(float64(r), float64(g), float64(b))
	}
	return out
}

// assign maps every pixel to the nearest palette entry in OKLab. quant.Reduce
// indexes through a histogram because it owns the cells; for a frame that must
// reuse another image's palette, a nearest-neighbour search is the honest
// operation, and k is small.
func assign(im *pixelbuf.Image, pl [][3]float64) []uint16 {
	lab := im.Lab()
	idx := make([]uint16, im.W*im.H)
	for i := range idx {
		l, a, b := lab.Pix[3*i], lab.Pix[3*i+1], lab.Pix[3*i+2]
		best, bd := 0, math.Inf(1)
		for j, q := range pl {
			dl, da, db := l-q[0], a-q[1], b-q[2]
			d := dl*dl + da*da + db*db
			if d < bd {
				best, bd = j, d
			}
		}
		idx[i] = uint16(best)
	}
	return idx
}

// encodeDelta paints only the pixels whose index changed, with their new
// colour. Unchanged pixels get alpha zero, so the tracer's alpha cutoff leaves
// them out and the document has no full-frame base rectangle: it is transparent
// where the previous frame is still correct.
func encodeDelta(im *pixelbuf.Image, cur, prev []uint16, gpal *quant.Palette,
	eo encode.Options) (*encode.Result, float64, error) {

	used := make([]bool, gpal.Len())
	changed := 0
	for i := range cur {
		if cur[i] != prev[i] {
			used[cur[i]] = true
			changed++
		}
	}
	if changed == 0 {
		return &encode.Result{Doc: dom.NewDoc(float64(im.W), float64(im.H))}, 0, nil
	}

	// A delta palette holds only the colours this delta actually uses. Keeping
	// the global palette would make the darkest global entry the base band even
	// when no changed pixel has it, which traces the changed area twice: once in
	// the wrong colour, then again underneath the first real band.
	remap := make([]uint16, gpal.Len())
	colors := make([]uint8, 0, 4*len(used))
	for i, u := range used {
		if !u {
			continue
		}
		remap[i] = uint16(len(colors) / 4)
		r, g, b := gpal.RGB(i)
		colors = append(colors, r, g, b, 255)
	}

	src := image.NewRGBA(image.Rect(0, 0, im.W, im.H))
	didx := make([]uint16, len(cur))
	for i := range cur {
		if cur[i] == prev[i] {
			continue // zero RGBA is transparent, and stays unpainted
		}
		r, g, b := gpal.RGB(int(cur[i]))
		x, y := i%im.W, i/im.W
		src.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		didx[i] = remap[cur[i]]
	}
	dim := pixelbuf.FromImage(src)
	dpal := &quant.Palette{Colors: colors, Index: didx, W: im.W, H: im.H}
	res, err := encode.Encode(dim, dpal, eo)
	if err != nil {
		return nil, 0, err
	}
	return res, float64(changed) / float64(len(cur)), nil
}

// layerName is the file the sidecar points at.
func layerName(i int, k Kind) string {
	switch k {
	case Poster:
		return "poster.svg"
	case Keyframe:
		return fmt.Sprintf("keyframe-%04d.svg", i)
	default:
		return fmt.Sprintf("delta-%04d.svg", i)
	}
}

// Manifest assigns filenames and times without touching the filesystem.
func (c *Clip) Manifest() *Manifest {
	m := &Manifest{Version: 1, Width: c.Width, Height: c.Height, FPS: c.FPS}
	for i := 0; i+3 < len(c.Palette); i += 4 {
		m.Palette = append(m.Palette, hex(c.Palette[i], c.Palette[i+1], c.Palette[i+2]))
	}
	for i, l := range c.Layers {
		m.Frames = append(m.Frames, Frame{
			Time:      float64(i) / c.FPS,
			Kind:      l.Kind.String(),
			Src:       layerName(i, l.Kind),
			Base:      l.Base,
			Nodes:     l.Nodes,
			Changed:   math.Round(l.Changed*1e4) / 1e4,
			Truncated: l.Truncated,
		})
	}
	return m
}

// Stats returns the keyframe count (poster included) and the delta count.
func (c *Clip) Stats() (keys, deltas, nodes int) {
	for _, l := range c.Layers {
		if l.Kind == Delta {
			deltas++
		} else {
			keys++
		}
		nodes += l.Nodes
	}
	return keys, deltas, nodes
}

// Write emits every layer, the timeline, and the player into dir.
func (c *Clip) Write(dir string) (*Manifest, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	m := c.Manifest()
	for i, l := range c.Layers {
		dest := filepath.Join(dir, m.Frames[i].Src)
		if err := os.WriteFile(dest, l.Doc.Encode(), 0o644); err != nil {
			return nil, err
		}
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "timeline.json"), append(data, '\n'), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(playerHTML), 0o644); err != nil {
		return nil, err
	}
	return m, nil
}

func hex(r, g, b uint8) string {
	const digits = "0123456789abcdef"
	var s [7]byte
	s[0] = '#'
	for i, c := range [3]uint8{r, g, b} {
		s[1+i*2] = digits[c>>4]
		s[2+i*2] = digits[c&0xf]
	}
	return string(s[:])
}
