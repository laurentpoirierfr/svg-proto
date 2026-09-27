// Package svgstat measures an SVG document.
//
// This is the project's instrument, not a product. Nothing is written without a
// corresponding metric, because the only meaningful reference for a
// raster-to-SVG converter is the trivial baseline: <image href="data:..."/>,
// which is already valid SVG at ~1.33x the raster size. If we cannot beat that
// on a category, we must not claim the category.
//
// Everything reported here is a *cost* (bytes, nodes, commands) or a
// *description* of the output. Fidelity metrics live in package imcompare,
// because they need a rasterised render and a reference image.
package svgstat

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Stats is a flat, comparable description of an SVG document.
type Stats struct {
	// Cost
	Bytes     int `json:"bytes"`
	GzipBytes int `json:"gzipBytes"`
	Elements  int `json:"elements"`
	Nodes     int `json:"nodes"`
	MaxDepth  int `json:"maxDepth"`
	TextNodes int `json:"textNodes"`
	UseNodes  int `json:"useNodes"`

	// Geometry
	Paths           int `json:"paths"`
	PathCommands    int `json:"pathCommands"`
	PathDataBytes   int `json:"pathDataBytes"`
	ArcCommands     int `json:"arcCommands"`
	Numbers         int `json:"pathNumbers"`
	TransformAttrs  int `json:"transformAttrs"`
	PrecisionLe0    int `json:"numbers_0dp"`
	PrecisionLe1    int `json:"numbers_le1dp"`
	PrecisionLe2    int `json:"numbers_le2dp"`
	PrecisionLe3    int `json:"numbers_le3dp"`
	PrecisionGt3    int `json:"numbers_gt3dp"`
	RelativeCmds    int `json:"relativeCommands"`
	AbsoluteCmds    int `json:"absoluteCommands"`
	PolylinePoints  int `json:"approxPolylinePoints"`
	DistinctFills   int `json:"distinctFills"`
	DistinctStrokes int `json:"distinctStrokes"`
	Groups          int `json:"groups"`

	// Portability
	Width     string   `json:"width"`
	Height    string   `json:"height"`
	ViewBox   string   `json:"viewBox"`
	HasImage  bool     `json:"hasImage"`
	HasFilter bool     `json:"hasFilter"`
	HasMask   bool     `json:"hasMask"`
	HasClip   bool     `json:"hasClipPath"`
	HasUse    bool     `json:"hasUse"`
	Fonts     []string `json:"fonts,omitempty"`
	HasScript bool     `json:"hasScript"`

	// Filled in by CompareAgainstRasters, when a reference raster is available.
	RatioPNG float64 `json:"ratioVsPng,omitempty"`
	RatioJPG float64 `json:"ratioVsJpg,omitempty"`

	// Filled in by imcompare, when --ref is passed. Score is the ranking
	// metric: gradient-weighted OKLab RMSE, lower is better.
	PSNR    float64 `json:"psnr,omitempty"`
	SSIM    float64 `json:"ssim,omitempty"`
	DeltaE  float64 `json:"deltaEOKLab,omitempty"`
	Score   float64 `json:"score,omitempty"`
	Renders bool    `json:"hasRender"`
}

type analyzer struct {
	st       Stats
	elemByID map[string]int
	fills    map[string]struct{}
	strokes  map[string]struct{}
	fonts    map[string]struct{}
	elems    map[string]int
	depth    int
}

// Analyze reads an SVG from r. rawSize, when non-zero, overrides the reported
// byte count (used when the document was produced on the fly).
func Analyze(r io.Reader) (*Stats, error) {
	a := &analyzer{
		elemByID: map[string]int{},
		fills:    map[string]struct{}{},
		strokes:  map[string]struct{}{},
		fonts:    map[string]struct{}{},
		elems:    map[string]int{},
	}
	dec := xml.NewDecoder(r)
	dec.Strict = false

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			a.depth++
			if a.depth > a.st.MaxDepth {
				a.st.MaxDepth = a.depth
			}
			a.onStart(t)
		case xml.EndElement:
			a.depth--
		}
	}
	a.st.DistinctFills = len(a.fills)
	a.st.DistinctStrokes = len(a.strokes)
	a.st.Fonts = sortedKeys(a.fonts)
	if n, ok := a.elems["g"]; ok {
		a.st.Groups = n
	}
	return &a.st, nil
}

// AnalyzeBytes measures an in-memory SVG document.
func AnalyzeBytes(b []byte) (*Stats, error) {
	s, err := Analyze(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	s.Bytes = len(b)
	if gz, err := gzipBytes(b); err == nil {
		s.GzipBytes = gz
	}
	return s, nil
}

// AnalyzeFile measures a file on disk.
func AnalyzeFile(path string) (*Stats, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return AnalyzeBytes(b)
}

func gzipBytes(b []byte) (int, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return 0, err
	}
	if _, err := zw.Write(b); err != nil {
		return 0, err
	}
	if err := zw.Close(); err != nil {
		return 0, err
	}
	return buf.Len(), nil
}

func (a *analyzer) onStart(e xml.StartElement) {
	name := localName(e.Name)
	a.st.Elements++
	a.st.Nodes += len(e.Attr)
	a.elems[name]++

	// Grouping by presentation attribute rather than per element is the single
	// biggest text-size lever in SVG; counting distinct values is how we tell
	// whether a producer is doing it.
	for _, at := range e.Attr {
		an, av := localName(at.Name), at.Value
		switch an {
		case "d":
			pi := analyzePathData(av)
			a.st.Paths++
			a.st.PathCommands += pi.Commands
			a.st.PathDataBytes += len(av)
			a.st.ArcCommands += pi.ByCmd['A'] + pi.ByCmd['a']
			a.st.Numbers += pi.Numbers
			a.st.RelativeCmds += pi.Relative
			a.st.AbsoluteCmds += pi.Absolute
			a.st.PolylinePoints += pi.PolylinePoints
			for d, n := range pi.Digits {
				switch {
				case d <= 0:
					a.st.PrecisionLe0 += n
				case d == 1:
					a.st.PrecisionLe1 += n
				case d == 2:
					a.st.PrecisionLe2 += n
				case d == 3:
					a.st.PrecisionLe3 += n
				default:
					a.st.PrecisionGt3 += n
				}
			}
		case "transform":
			a.st.TransformAttrs++
		case "fill":
			if av != "none" {
				a.fills[av] = struct{}{}
			}
		case "stroke":
			if av != "none" {
				a.strokes[av] = struct{}{}
			}
		case "font-family":
			a.fonts[av] = struct{}{}
		case "id":
			a.elemByID[av] = a.st.Elements
		}
	}

	switch name {
	case "svg":
		if v := attr(e, "width"); v != "" {
			a.st.Width = v
		}
		if v := attr(e, "height"); v != "" {
			a.st.Height = v
		}
		a.st.ViewBox = attr(e, "viewBox")
	case "text", "tspan":
		a.st.TextNodes++
	case "use":
		a.st.UseNodes++
		a.st.HasUse = true
	case "image":
		a.st.HasImage = true
	case "filter":
		a.st.HasFilter = true
	case "mask":
		a.st.HasMask = true
	case "clipPath":
		a.st.HasClip = true
	case "script":
		a.st.HasScript = true
	}
}

func localName(n xml.Name) string {
	s := n.Local
	if s == "" {
		s = n.Space
	}
	return s
}

func attr(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func sortedKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// path data
// ---------------------------------------------------------------------------

// PathInfo describes the cost profile of a single `d` attribute.
type PathInfo struct {
	Commands int
	ByCmd    map[byte]int
	Numbers  int
	Digits   map[int]int
	Relative int
	Absolute int
	// PolylinePoints estimates the point count this path would have if the
	// curves were flattened. Lower is better for the DOM; it is the number the
	// browser's renderer actually feels.
	PolylinePoints int
}

var cmdArity = map[byte]int{
	'M': 2, 'L': 2, 'H': 1, 'V': 1, 'C': 6, 'S': 4, 'Q': 4, 'T': 2, 'A': 7, 'Z': 0,
}

// arity is the number of parameters a command takes. Case does not affect it.
func arity(c byte) int {
	if n, ok := cmdArity[upper(c)]; ok {
		return n
	}
	return 0
}

// analyzePathData tokenises SVG path data. The arc flag parameters are the
// reason this cannot be a regexp: large-arc-flag and sweep-flag are single
// digits that may be written without any separator ("a1 1 0 011 1"), so they
// have to be tracked positionally to avoid merging them with the following
// coordinate.
//
// Decimals are counted on the mantissa; scientific notation (which SVG permits
// but producers never emit) will be under-counted, which only ever makes a
// producer look tidier than it is.
func analyzePathData(d string) PathInfo {
	pi := PathInfo{ByCmd: map[byte]int{}, Digits: map[int]int{}}
	n := len(d)
	param := 0
	curCmd := byte(0)
	implicit := false

	for i := 0; i < n; {
		c := d[i]
		if isCmd(c) {
			curCmd = c
			implicit = false
			pi.ByCmd[upper(c)]++
			pi.Commands++
			pi.PolylinePoints += flattenedPoints(curCmd)
			if isLower(c) {
				pi.Relative++
			} else {
				pi.Absolute++
			}
			param = 0
			i++
			continue
		}
		if c == ',' || c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			i++
			continue
		}

		// A number where a new command letter was expected is an implicit
		// segment: "M0 0 5 5" draws two lines, and "L1 1 2 2 3 3" draws three.
		// Those repeats cost a command's worth of rendering each, so they must
		// be counted even though they add no letter.
		if implicit && arity(curCmd) > 0 {
			rep := curCmd
			if upper(curCmd) == 'M' {
				rep = 'L' // after a moveto, implicit repeats are linetos
			}
			pi.PolylinePoints += flattenedPoints(rep)
			param = 0
		}
		implicit = false

		isArc := curCmd == 'A' || curCmd == 'a'
		arcFlag := isArc && (param == 3 || param == 4)

		dec, next, ok := scanNumber(d, i, arcFlag)
		if !ok {
			// Unparseable: bail out rather than loop forever. Real-world files do
			// contain junk, and a stats tool that hangs is worse than one that
			// undercounts.
			break
		}
		pi.Numbers++
		pi.Digits[dec]++
		i = next
		param++

		if a := arity(curCmd); a == 0 || param >= a {
			param = 0
			implicit = a > 0
		}
	}
	return pi
}

// flattenedPoints is a rough estimate of how many points this command becomes
// once a renderer flattens it. It is the number the renderer actually feels, so
// it is the number worth optimising, but it is an estimate: an arc covering a
// full circle flattens to far more than 16 points.
func flattenedPoints(c byte) int {
	switch upper(c) {
	case 'Z':
		return 1
	case 'A':
		return 16
	case 'C':
		return 8
	case 'S', 'Q':
		return 4
	default:
		return 1
	}
}

func isCmd(c byte) bool {
	switch c {
	case 'M', 'm', 'L', 'l', 'H', 'h', 'V', 'v', 'C', 'c', 'S', 's',
		'Q', 'q', 'T', 't', 'A', 'a', 'Z', 'z':
		return true
	}
	return false
}

func upper(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - 32
	}
	return c
}

func isLower(c byte) bool { return c >= 'a' && c <= 'z' }

// scanNumber parses one number starting at i and reports how many digits follow
// the decimal point. When arcFlag is set a single '0'/'1' is consumed.
func scanNumber(s string, i int, arcFlag bool) (decimals, next int, ok bool) {
	n := len(s)
	if i >= n {
		return 0, i, false
	}
	if arcFlag {
		if s[i] == '0' || s[i] == '1' {
			return 0, i + 1, true
		}
	}
	j := i
	if s[j] == '+' || s[j] == '-' {
		j++
	}
	intDigits := 0
	for j < n && s[j] >= '0' && s[j] <= '9' {
		j++
		intDigits++
	}
	fracDigits := 0
	if j < n && s[j] == '.' {
		j++
		for j < n && s[j] >= '0' && s[j] <= '9' {
			j++
			fracDigits++
		}
	}
	if intDigits+fracDigits == 0 {
		return 0, i, false
	}
	if j < n && (s[j] == 'e' || s[j] == 'E') {
		k := j + 1
		if k < n && (s[k] == '+' || s[k] == '-') {
			k++
		}
		exp := 0
		for k < n && s[k] >= '0' && s[k] <= '9' {
			k++
			exp++
		}
		if exp > 0 {
			j = k
		}
	}
	return fracDigits, j, true
}

// ---------------------------------------------------------------------------
// comparison
// ---------------------------------------------------------------------------

// Delta is the signed change of every cost metric, for --diff.
type Delta struct {
	Field string  `json:"field"`
	A     float64 `json:"a"`
	B     float64 `json:"b"`
	Delta float64 `json:"delta"`
	Unit  string  `json:"unit,omitempty"`
}

// Compare returns the cost-metric deltas between two measurements.
func Compare(a, b *Stats) []Delta {
	type row struct {
		name   string
		ga, gb float64
		unit   string
	}
	rows := []row{
		{"bytes", float64(a.Bytes), float64(b.Bytes), "B"},
		{"gzipBytes", float64(a.GzipBytes), float64(b.GzipBytes), "B"},
		{"elements", float64(a.Elements), float64(b.Elements), ""},
		{"nodes", float64(a.Nodes), float64(b.Nodes), ""},
		{"paths", float64(a.Paths), float64(b.Paths), ""},
		{"pathCommands", float64(a.PathCommands), float64(b.PathCommands), ""},
		{"pathDataBytes", float64(a.PathDataBytes), float64(b.PathDataBytes), "B"},
		{"approxPolylinePoints", float64(a.PolylinePoints), float64(b.PolylinePoints), ""},
		{"distinctFills", float64(a.DistinctFills), float64(b.DistinctFills), ""},
		{"maxDepth", float64(a.MaxDepth), float64(b.MaxDepth), ""},
		{"numbers_gt3dp", float64(a.PrecisionGt3), float64(b.PrecisionGt3), ""},
	}
	out := make([]Delta, 0, len(rows))
	for _, r := range rows {
		out = append(out, Delta{Field: r.name, A: r.ga, B: r.gb, Delta: r.gb - r.ga, Unit: r.unit})
	}
	return out
}

// RatiosAgainstRasters divides the SVG byte count by the byte count of raster
// references. A ratio above 1 means the SVG is bigger than the raster it came
// from, which for a vectorisation is a failure regardless of quality.
func (s *Stats) RatiosAgainstRasters(pngBytes, jpgBytes int) {
	if pngBytes > 0 {
		s.RatioPNG = round3(float64(s.Bytes) / float64(pngBytes))
	}
	if jpgBytes > 0 {
		s.RatioJPG = round3(float64(s.Bytes) / float64(jpgBytes))
	}
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// JSON renders the measurement as stable, indented JSON.
func (s *Stats) JSON() ([]byte, error) {
	return json.MarshalIndent(s, "", "  ")
}

// String renders a compact human-readable report.
func (s *Stats) String() string {
	var b strings.Builder
	pct := func(part, whole int) string {
		if whole == 0 {
			return "-"
		}
		return strconv.Itoa(int(math.Round(100*float64(part)/float64(whole)))) + "%"
	}
	fmt.Fprintf(&b, "cost\n")
	fmt.Fprintf(&b, "  bytes            %8d  (gzip %d)\n", s.Bytes, s.GzipBytes)
	fmt.Fprintf(&b, "  elements         %8d  (max depth %d)\n", s.Elements, s.MaxDepth)
	fmt.Fprintf(&b, "  attribute nodes  %8d\n", s.Nodes)
	fmt.Fprintf(&b, "  groups           %8d\n", s.Groups)
	fmt.Fprintf(&b, "  <text> / <use>   %8d / %d\n", s.TextNodes, s.UseNodes)
	fmt.Fprintf(&b, "geometry\n")
	fmt.Fprintf(&b, "  paths            %8d  (%d commands, %d B of d)\n", s.Paths, s.PathCommands, s.PathDataBytes)
	fmt.Fprintf(&b, "  approx points    %8d  <- what the renderer feels\n", s.PolylinePoints)
	fmt.Fprintf(&b, "  rel / abs cmds   %8d / %d\n", s.RelativeCmds, s.AbsoluteCmds)
	fmt.Fprintf(&b, "  arc commands     %8d\n", s.ArcCommands)
	fmt.Fprintf(&b, "  numbers <=1dp    %8d  (%s of all numbers)\n", s.PrecisionLe0+s.PrecisionLe1, pct(s.PrecisionLe0+s.PrecisionLe1, s.Numbers))
	fmt.Fprintf(&b, "  numbers >3dp     %8d  (%s of all numbers)\n", s.PrecisionGt3, pct(s.PrecisionGt3, s.Numbers))
	fmt.Fprintf(&b, "  distinct fills   %8d  (strokes %d)\n", s.DistinctFills, s.DistinctStrokes)
	if s.RatioPNG > 0 || s.RatioJPG > 0 {
		fmt.Fprintf(&b, "versus raster\n")
		if s.RatioPNG > 0 {
			fmt.Fprintf(&b, "  vs png           %6.3fx %s\n", s.RatioPNG, verdict(s.RatioPNG))
		}
		if s.RatioJPG > 0 {
			fmt.Fprintf(&b, "  vs jpg           %6.3fx %s\n", s.RatioJPG, verdict(s.RatioJPG))
		}
	}
	if s.Renders {
		fmt.Fprintf(&b, "fidelity (vs source raster)\n")
		fmt.Fprintf(&b, "  ssim             %8.4f\n", s.SSIM)
		fmt.Fprintf(&b, "  psnr (dB)        %8.2f\n", s.PSNR)
		fmt.Fprintf(&b, "  deltaE OKLab     %8.4f  (JND ~%.3f)\n", s.DeltaE, jnd())
	}
	fmt.Fprintf(&b, "portability\n")
	fmt.Fprintf(&b, "  root             %s x %s  viewBox=%q\n", orDash(s.Width), orDash(s.Height), s.ViewBox)
	flags := []string{
		boolFlag("image", s.HasImage), boolFlag("filter", s.HasFilter),
		boolFlag("mask", s.HasMask), boolFlag("clipPath", s.HasClip),
		boolFlag("use", s.HasUse), boolFlag("script", s.HasScript),
	}
	fmt.Fprintf(&b, "  features         %s\n", strings.Join(flags, " "))
	if len(s.Fonts) > 0 {
		fmt.Fprintf(&b, "  fonts            %s\n", strings.Join(s.Fonts, ", "))
	}
	return b.String()
}

func jnd() float64 { return 0.02 }

func boolFlag(name string, on bool) string {
	if on {
		return "+" + name
	}
	return "-" + name
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func verdict(ratio float64) string {
	switch {
	case ratio > 1:
		return "BIGGER THAN THE RASTER"
	case ratio > 0.5:
		return "worse than embedding"
	case ratio > 0.15:
		return "comparable to embedding"
	default:
		return "clearly better than embedding"
	}
}

// FormatDeltaTable renders a comparison as an aligned table.
func FormatDeltaTable(deltas []Delta) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-22s %14s %14s %14s\n", "metric", "A", "B", "delta")
	for _, d := range deltas {
		suffix := ""
		if d.Unit != "" {
			suffix = d.Unit
		}
		fmt.Fprintf(&b, "%-22s %13.0f%-6s %13.0f%-6s %+13.0f%-6s\n",
			d.Field, d.A, suffix, d.B, suffix, d.Delta, suffix)
	}
	return b.String()
}
