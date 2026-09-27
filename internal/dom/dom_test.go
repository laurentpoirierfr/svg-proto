package dom

import (
	"strings"
	"testing"
)

func TestNewDocCarriesBothSizeAndViewBox(t *testing.T) {
	// width/height alone gives no scaling, viewBox alone gives no intrinsic
	// size. Only the pair is resolution-independent *and* well-defined, so the
	// constructor has to emit both and the test pins that.
	got := string(NewDoc(800, 600).Encode())
	for _, want := range []string{
		`width="800"`, `height="600"`, `viewBox="0 0 800 600"`,
		`xmlns="http://www.w3.org/2000/svg"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %s\n%s", want, got)
		}
	}
}

func TestEmptyAttrsAreDropped(t *testing.T) {
	d := NewDoc(10, 10)
	d.Root.Append(E("rect", A("x", ""), A("y", "3"), AI("width", 4)))
	out := string(d.Encode())
	if strings.Contains(out, `x=""`) {
		t.Errorf("empty attribute was emitted:\n%s", out)
	}
	if !strings.Contains(out, `y="3"`) {
		t.Errorf("non-empty attribute was dropped:\n%s", out)
	}
}

func TestAttributeOrderIsPreserved(t *testing.T) {
	// Map iteration would randomise this, and random output breaks golden files,
	// content-addressed caches and reviewable diffs.
	d := NewDoc(4, 4)
	out := string(d.Encode())
	ia := strings.Index(out, `xmlns=`)
	ib := strings.Index(out, `width=`)
	ic := strings.Index(out, `viewBox=`)
	if !(ia < ib && ib < ic) {
		t.Errorf("attribute order is not insertion order: %s", out)
	}
}

func TestEncodeIsDeterministic(t *testing.T) {
	build := func() string {
		d := NewDoc(20, 20)
		g := E("g", A("fill", "#ff0000"))
		for i := 0; i < 5; i++ {
			g.Append(E("rect", AI("x", i), AI("y", i), AI("width", 2), AI("height", 2)))
		}
		d.Root.Append(g)
		return string(d.Encode())
	}
	first := build()
	for i := 0; i < 20; i++ {
		if got := build(); got != first {
			t.Fatalf("encode is not deterministic:\n%s\nvs\n%s", first, got)
		}
	}
}

func TestEscaping(t *testing.T) {
	d := NewDoc(4, 4)
	d.Root.Append(E("title").Append(Text(`a & b < c > d " e ' f`)))
	out := string(d.Encode())
	for _, want := range []string{"&amp;", "&lt;", "&gt;", "&quot;", "&apos;"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing escape %s in:\n%s", want, out)
		}
	}
	// The ampersand must be escaped exactly once, not twice.
	if strings.Contains(out, "&amp;amp;") {
		t.Errorf("double escaping:\n%s", out)
	}
}

func TestRawTextIsNotEscaped(t *testing.T) {
	d := NewDoc(4, 4)
	m := E("metadata")
	m.Text = "<x:y/>"
	m.Raw = true
	d.Root.Append(m)
	if out := string(d.Encode()); !strings.Contains(out, "<x:y/>") {
		t.Errorf("raw text was escaped:\n%s", out)
	}
}

func TestEmptyElementSelfCloses(t *testing.T) {
	d := NewDoc(4, 4)
	d.Root.Append(E("g"))
	out := string(d.Encode())
	if !strings.Contains(out, "<g/>") {
		t.Errorf("empty element not self-closed:\n%s", out)
	}
}

func TestIndentIsOptionalAndValid(t *testing.T) {
	d := NewDoc(8, 8)
	d.Indent = 2
	g := E("g")
	g.Append(E("rect", AI("width", 1)))
	d.Root.Append(g)
	out := string(d.Encode())
	if !strings.Contains(out, "\n") {
		t.Error("Indent=2 produced no newlines")
	}
	// Newlines between elements are legal, but a newline inside <text> is
	// content. Pin that the indent never lands inside a text element.
	if strings.Contains(out, "<title>\n") {
		t.Error("indent leaked into text content")
	}
}

func TestCensus(t *testing.T) {
	d := NewDoc(4, 4)
	g := E("g")
	g.Append(E("rect"), E("path"), E("circle"))
	d.Root.Append(g)
	c := d.Census()
	// svg + g + 3 shapes
	if c.Elements != 5 {
		t.Errorf("Elements = %d, want 5", c.Elements)
	}
	if c.Shapes != 3 {
		t.Errorf("Shapes = %d, want 3", c.Shapes)
	}
	if c.MaxDepth != 3 {
		t.Errorf("MaxDepth = %d, want 3", c.MaxDepth)
	}
}

func TestSetReplacesInPlace(t *testing.T) {
	d := NewDoc(4, 4)
	d.Root.Set("width", "99")
	if got := d.Root.Get("width"); got != "99" {
		t.Errorf("Get(width) = %q, want 99", got)
	}
	// Replacing must not move the attribute, or the output order shifts.
	out := string(d.Encode())
	if strings.Index(out, "width=") > strings.Index(out, "viewBox=") {
		t.Errorf("Set moved the attribute:\n%s", out)
	}
	if strings.Count(out, `width=`) != 1 {
		t.Errorf("Set duplicated the attribute:\n%s", out)
	}
}

func TestGetMissingAttr(t *testing.T) {
	if got := E("rect").Get("fill"); got != "" {
		t.Errorf("Get on a missing attribute = %q, want empty", got)
	}
}
