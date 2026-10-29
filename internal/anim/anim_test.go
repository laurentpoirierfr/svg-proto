package anim

import (
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elfeo/svg-proto/internal/dom"
	"github.com/elfeo/svg-proto/internal/pixelbuf"
	"github.com/elfeo/svg-proto/internal/quant"
)

func frame(size int, bg color.RGBA, paint func(*image.RGBA)) *pixelbuf.Image {
	m := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(m, m.Bounds(), &image.Uniform{C: bg}, image.Point{}, draw.Src)
	paint(m)
	return pixelbuf.FromImage(m)
}

func movingFrames(n, size int) []*pixelbuf.Image {
	frames := make([]*pixelbuf.Image, n)
	for i := range frames {
		x := 1 + (i % 8)
		frames[i] = frame(size, color.RGBA{240, 240, 240, 255}, func(m *image.RGBA) {
			for y := 4; y < 12; y++ {
				for xx := x; xx < x+5 && xx < size; xx++ {
					m.SetRGBA(xx, y, color.RGBA{208, 64, 48, 255})
				}
			}
		})
	}
	return frames
}

func TestEncodeTimeline(t *testing.T) {
	frames := movingFrames(6, 16)
	clip, err := Encode(frames, Options{FPS: 24, Colors: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(clip.Layers) != len(frames) {
		t.Fatalf("layers = %d, want %d", len(clip.Layers), len(frames))
	}
	if clip.Layers[0].Kind != Poster || clip.Layers[0].Base != 0 {
		t.Fatalf("first layer = %v base %d, want poster base 0",
			clip.Layers[0].Kind, clip.Layers[0].Base)
	}
	keys, deltas, nodes := clip.Stats()
	if keys < 1 || deltas < 1 {
		t.Fatalf("keys %d deltas %d, want both", keys, deltas)
	}
	if nodes >= clip.AllINodes {
		t.Fatalf("layered nodes %d >= all-I %d, delta did not pay", nodes, clip.AllINodes)
	}
	if clip.AllINodes <= 0 {
		t.Fatal("AllINodes not accumulated")
	}

	m := clip.Manifest()
	if len(m.Frames) != len(frames) {
		t.Fatalf("manifest frames = %d", len(m.Frames))
	}
	if m.Version != 1 || m.Width != 16 || m.Height != 16 || m.FPS != 24 {
		t.Fatalf("manifest header = %+v", m)
	}
	if m.Frames[0].Src != "poster.svg" || m.Frames[0].Kind != "poster" {
		t.Fatalf("poster frame = %+v", m.Frames[0])
	}
	if got := m.Frames[3].Time; got != 3.0/24 {
		t.Fatalf("t(3) = %v", got)
	}
	for i, f := range m.Frames {
		if f.Kind == "delta" && f.Base > i {
			t.Fatalf("frame %d base %d points forward", i, f.Base)
		}
	}
}

func TestDeltasAreTransparent(t *testing.T) {
	clip, err := Encode(movingFrames(4, 16), Options{Colors: 4})
	if err != nil {
		t.Fatal(err)
	}
	for i, l := range clip.Layers {
		if l.Kind != Delta {
			continue
		}
		if hasRect(l.Doc.Root) {
			t.Fatalf("delta %d has a rectangle; it would paint the whole frame", i)
		}
	}
}

func hasRect(e *dom.Element) bool {
	for _, c := range e.Children {
		el, ok := c.(*dom.Element)
		if !ok {
			continue
		}
		if el.Name == "rect" || hasRect(el) {
			return true
		}
	}
	return false
}

func TestStaticFramesBecomeEmptyDeltas(t *testing.T) {
	base := frame(16, color.RGBA{240, 240, 240, 255}, func(m *image.RGBA) {
		for y := 4; y < 10; y++ {
			for x := 4; x < 10; x++ {
				m.SetRGBA(x, y, color.RGBA{48, 96, 208, 255})
			}
		}
	})
	clip, err := Encode([]*pixelbuf.Image{base, base, base}, Options{Colors: 4})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 3; i++ {
		l := clip.Layers[i]
		if l.Kind != Delta || l.Nodes != 0 || l.Changed != 0 {
			t.Fatalf("frame %d = %v nodes %d changed %v, want empty delta",
				i, l.Kind, l.Nodes, l.Changed)
		}
		if len(l.Doc.Root.Children) != 0 {
			t.Fatalf("frame %d delta has %d children, want none", i, len(l.Doc.Root.Children))
		}
	}
}

func TestGOPForcesKeyframes(t *testing.T) {
	clip, err := Encode(movingFrames(5, 16), Options{Colors: 4, GOP: 2})
	if err != nil {
		t.Fatal(err)
	}
	for i, l := range clip.Layers {
		if i%2 == 0 {
			if l.Kind == Delta {
				t.Fatalf("frame %d is a delta, want a keyframe at the GOP boundary", i)
			}
			if l.Base != i {
				t.Fatalf("frame %d base %d, want itself", i, l.Base)
			}
		}
	}
}

func TestDifferentSizesAreRejected(t *testing.T) {
	a := frame(16, color.RGBA{255, 255, 255, 255}, func(*image.RGBA) {})
	b := frame(8, color.RGBA{255, 255, 255, 255}, func(*image.RGBA) {})
	if _, err := Encode([]*pixelbuf.Image{a, b}, Options{}); err == nil {
		t.Fatal("mixed frame sizes accepted")
	}
}

func TestAssignFindsExactEntries(t *testing.T) {
	pal := &quant.Palette{Colors: []uint8{
		255, 0, 0, 255,
		0, 255, 0, 255,
		0, 0, 255, 255,
	}}
	im := frame(3, color.RGBA{255, 0, 0, 255}, func(m *image.RGBA) {
		m.SetRGBA(1, 0, color.RGBA{0, 255, 0, 255})
		m.SetRGBA(2, 0, color.RGBA{0, 0, 255, 255})
	})
	got := assign(im, paletteLab(pal))
	want := []uint16{0, 1, 2}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pixel %d = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestWriteEmitsLayersAndTimeline(t *testing.T) {
	clip, err := Encode(movingFrames(4, 16), Options{Colors: 4})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	m, err := clip.Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "timeline.json"))
	if err != nil {
		t.Fatal(err)
	}
	var back Manifest
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("timeline is not valid JSON: %v", err)
	}
	if len(back.Frames) != len(m.Frames) {
		t.Fatalf("timeline frames = %d, want %d", len(back.Frames), len(m.Frames))
	}
	for _, f := range back.Frames {
		if _, err := os.Stat(filepath.Join(dir, f.Src)); err != nil {
			t.Errorf("%s: %v", f.Src, err)
		}
	}
	html, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if len(html) == 0 {
		t.Fatal("player is empty")
	}
	for _, want := range []string{"timeline.json", "scrub"} {
		if !strings.Contains(string(html), want) {
			t.Errorf("player does not mention %q", want)
		}
	}
}

func TestFullFrameChangeBecomesKeyframe(t *testing.T) {
	a := frame(16, color.RGBA{250, 250, 250, 255}, func(m *image.RGBA) {
		for y := 2; y < 14; y++ {
			for x := 2; x < 14; x++ {
				m.SetRGBA(x, y, color.RGBA{200, 40, 40, 255})
			}
		}
	})
	b := frame(16, color.RGBA{10, 20, 30, 255}, func(m *image.RGBA) {
		for y := 2; y < 14; y++ {
			for x := 2; x < 14; x++ {
				m.SetRGBA(x, y, color.RGBA{40, 200, 90, 255})
			}
		}
	})
	clip, err := Encode([]*pixelbuf.Image{a, b}, Options{Colors: 4})
	if err != nil {
		t.Fatal(err)
	}
	if clip.Layers[1].Kind != Keyframe {
		t.Fatalf("frame that repaints everything became %v, want keyframe", clip.Layers[1].Kind)
	}
}
