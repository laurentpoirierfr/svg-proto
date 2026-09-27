// Package dom is the internal SVG tree.
//
// Deliberately a tree of plain structs, not a string builder and not
// encoding/xml. Three reasons, in order of importance:
//
//  1. The core must not emit SVG text until the very last stage. Path
//     coordinates, relative-versus-absolute choice, curve type, colour grouping
//     and number formatting can change an output by a factor of three to ten,
//     and none of those choices should reach back into the algorithms.
//  2. The same tree can be serialised to SVG, dumped for debugging, or walked by
//     the budget accounting, without re-running any geometry.
//  3. Attributes are an ordered slice, not a map, because map iteration order
//     is random and the output has to be byte-for-byte deterministic. VCS diffs,
//     golden tests and content-addressed caches all depend on that.
//
// Empty attributes are dropped on encode: an emitted `fill=""` is noise, and
// noise costs bytes and reviewer attention.
package dom

import (
	"strconv"
	"strings"
)

// Attr is one attribute. Order is preserved as given.
type Attr struct {
	Name  string
	Value string
}

// Node is an element or a text node.
type Node interface{ node() }

// Element is an SVG element.
type Element struct {
	Name     string
	Attrs    []Attr
	Children []Node
	// Text, when non-empty and Children is empty, is written as character data.
	Text string
	// Raw suppresses escaping of Text, for content that is already valid
	// (currently only <metadata> payloads).
	Raw bool
}

func (*Element) node() {}

// A is shorthand for a new attribute.
func A(name, value string) Attr { return Attr{name, value} }

// AF is a float attribute, rounded to prec decimals with trailing zeros
// stripped. Trailing zeros are pure waste and they are the single most common
// source of needless bytes in a path-heavy document, so every float in the tree
// goes through this one function.
func AF(name string, v float64, prec int) Attr {
	return Attr{name, num(v, prec)}
}

// AI is an integer attribute.
func AI(name string, v int) Attr { return Attr{name, strconv.Itoa(v)} }

// E builds an element.
func E(name string, attrs ...Attr) *Element {
	return &Element{Name: name, Attrs: attrs}
}

// TextNode is character data. A distinct type rather than a field on Element so
// that mixed content cannot be expressed by accident.
type TextNode string

func (TextNode) node() {}

// Text returns an escaped character-data node.
func Text(s string) Node { return TextNode(s) }

// Append adds children and returns the receiver, for chaining.
func (e *Element) Append(children ...Node) *Element {
	e.Children = append(e.Children, children...)
	return e
}

// Set adds or replaces an attribute, preserving position on replace.
func (e *Element) Set(name, value string) *Element {
	for i := range e.Attrs {
		if e.Attrs[i].Name == name {
			e.Attrs[i].Value = value
			return e
		}
	}
	e.Attrs = append(e.Attrs, Attr{name, value})
	return e
}

// Get returns an attribute value, or "" if absent.
func (e *Element) Get(name string) string {
	for i := range e.Attrs {
		if e.Attrs[i].Name == name {
			return e.Attrs[i].Value
		}
	}
	return ""
}

// Doc is a whole document.
type Doc struct {
	Root *Element
	// XMLDeclaration writes the <?xml ...?> prolog. Worth it: files without one
	// make some XML tooling guess the encoding, and the guess is sometimes wrong.
	XMLDeclaration bool
	// Indent pretty-prints. Off by default: whitespace inside <text> and
	// <tspan> is significant, and indentation costs bytes for nothing on the
	// wire once compressed.
	Indent int
}

// NewDoc returns a document with a root <svg> sized in viewBox units and
// carrying the same size in user units.
//
// Always emit both width/height and viewBox. width/height alone gives the
// intrinsic size and nothing else; viewBox alone makes the root scale to its
// container. Both is the only combination that is resolution-independent and
// still has a defined intrinsic size.
func NewDoc(w, h float64) *Doc {
	return &Doc{
		XMLDeclaration: true,
		Root: E("svg",
			A("xmlns", "http://www.w3.org/2000/svg"),
			AF("width", w, 3), AF("height", h, 3),
			A("viewBox", "0 0 "+num(w, 3)+" "+num(h, 3)),
		),
	}
}

// Encode serialises the document.
func (d *Doc) Encode() []byte {
	var b strings.Builder
	if d.XMLDeclaration {
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
		if d.Indent > 0 {
			b.WriteByte('\n')
		}
	}
	b.WriteString("<")
	b.WriteString(d.Root.Name)
	writeAttrs(&b, d.Root)
	if len(d.Root.Children) == 0 && d.Root.Text == "" {
		b.WriteString("/>")
	} else {
		b.WriteByte('>')
		if d.Indent > 0 {
			b.WriteByte('\n')
		}
		writeChildren(&b, d.Root, 1, d.Indent)
		if d.Indent > 0 {
			b.WriteString(strings.Repeat(" ", d.Indent*(depthOf(d.Root)-1)))
		}
		b.WriteString("</")
		b.WriteString(d.Root.Name)
		b.WriteByte('>')
	}
	b.WriteByte('\n')
	return []byte(b.String())
}

func writeChildren(b *strings.Builder, e *Element, depth, indent int) {
	for _, c := range e.Children {
		if indent > 0 {
			b.WriteString(strings.Repeat(" ", indent*depth))
		}
		switch n := c.(type) {
		case *Element:
			b.WriteByte('<')
			b.WriteString(n.Name)
			writeAttrs(b, n)
			if len(n.Children) == 0 && n.Text == "" {
				b.WriteString("/>")
			} else {
				b.WriteByte('>')
				if n.Text != "" {
					if n.Raw {
						b.WriteString(n.Text)
					} else {
						escape(b, n.Text)
					}
				}
				if len(n.Children) > 0 {
					if indent > 0 {
						b.WriteByte('\n')
					}
					writeChildren(b, n, depth+1, indent)
					if indent > 0 {
						b.WriteString(strings.Repeat(" ", indent*depth))
					}
				}
				b.WriteString("</")
				b.WriteString(n.Name)
				b.WriteByte('>')
			}
		case TextNode:
			if e.Raw {
				b.WriteString(string(n))
			} else {
				escape(b, string(n))
			}
		}
		if indent > 0 {
			b.WriteByte('\n')
		}
	}
}

func writeAttrs(b *strings.Builder, e *Element) {
	for _, a := range e.Attrs {
		if a.Value == "" {
			continue // drop empties: they are bytes and reviewer noise
		}
		b.WriteByte(' ')
		b.WriteString(a.Name)
		b.WriteString(`="`)
		escape(b, a.Value)
		b.WriteByte('"')
	}
}

func depthOf(e *Element) int {
	d := 1
	for _, c := range e.Children {
		if sub, ok := c.(*Element); ok {
			if s := depthOf(sub) + 1; s > d {
				d = s
			}
		}
	}
	return d
}

func escape(b *strings.Builder, s string) {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		default:
			b.WriteByte(c)
		}
	}
}

// num formats a coordinate: fixed precision, then no trailing zeros and no
// dangling decimal point. Whole numbers therefore cost their shortest form, which
// matters because most path coordinates in a real document are whole or half
// numbers.
func num(v float64, prec int) string {
	s := strconv.FormatFloat(v, 'f', prec, 64)
	if strings.ContainsRune(s, '.') {
		s = strings.TrimRight(s, "0")
		s = strings.TrimSuffix(s, ".")
	}
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

// Count is a structural census of a tree, used to check the node budget before
// anything is serialised. Cheaper to compute on the tree than to recover from
// the text, and it is where a budget violation is cheapest to diagnose.
type Count struct {
	Elements int
	// Shapes are the leaf paint elements: rect, path, circle, ellipse, polygon,
	// polyline, text, image. These are what a renderer actually rasterises.
	Shapes   int
	MaxDepth int
}

func (c *Count) Add(el *Element, depth int) {
	c.Elements++
	if depth > c.MaxDepth {
		c.MaxDepth = depth
	}
	switch el.Name {
	case "rect", "path", "circle", "ellipse", "polygon", "polyline", "text", "image", "line", "tspan":
		c.Shapes++
	}
	for _, ch := range el.Children {
		if sub, ok := ch.(*Element); ok {
			c.Add(sub, depth+1)
		}
	}
}

// Census returns the structural counts of the document.
func (d *Doc) Census() Count {
	var c Count
	c.Add(d.Root, 1)
	return c
}
