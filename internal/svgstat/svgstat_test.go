package svgstat

import (
	"strings"
	"testing"
)

const sample = `<svg xmlns="http://www.w3.org/2000/svg" width="100" height="50" viewBox="0 0 100 50">
  <g fill="#ff0000">
    <path d="M 0 0 L 10.5 0.25 L 10 10 Z"/>
    <path d="m0 0 l.5 .5"/>
  </g>
  <rect x="0" y="0" width="10" height="10" fill="#00ff00"/>
  <text x="5" y="20" font-family="Inter, sans-serif">hi</text>
</svg>`

func TestAnalyzeSample(t *testing.T) {
	s, err := AnalyzeBytes([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if s.Width != "100" || s.Height != "50" {
		t.Errorf("size = %sx%s", s.Width, s.Height)
	}
	if s.ViewBox != "0 0 100 50" {
		t.Errorf("viewBox = %q", s.ViewBox)
	}
	if s.Elements != 6 {
		t.Errorf("elements = %d, want 6 (svg, g, 2 paths, rect, text)", s.Elements)
	}
	if s.Paths != 2 {
		t.Errorf("paths = %d, want 2", s.Paths)
	}
	if s.DistinctFills != 2 {
		t.Errorf("distinct fills = %d, want 2 (#ff0000 group + #00ff00 rect)", s.DistinctFills)
	}
	if s.TextNodes != 1 {
		t.Errorf("text nodes = %d, want 1", s.TextNodes)
	}
	if len(s.Fonts) != 1 || s.Fonts[0] != "Inter, sans-serif" {
		t.Errorf("fonts = %v", s.Fonts)
	}
	if s.MaxDepth != 3 {
		t.Errorf("max depth = %d, want 3", s.MaxDepth)
	}
	if s.GzipBytes == 0 || s.GzipBytes >= s.Bytes {
		t.Errorf("gzip %d not smaller than %d", s.GzipBytes, s.Bytes)
	}
}

func TestPathTokenizer(t *testing.T) {
	cases := []struct {
		name      string
		d         string
		commands  int
		numbers   int
		decimals  map[int]int
		arcs      int
		relatives int
	}{
		{
			name: "absolute moveto and linetos", d: "M0 0L10.5 0.25L10 10Z",
			commands: 4, numbers: 6, decimals: map[int]int{0: 4, 1: 1, 2: 1}, arcs: 0, relatives: 0,
		},
		{
			name: "relative shorthand", d: "m0 0l.5 .5",
			commands: 2, numbers: 4, decimals: map[int]int{0: 2, 1: 2}, arcs: 0, relatives: 2,
		},
		{
			// The reason this is a tokeniser and not a regexp: the two arc flags
			// are single digits written with no separator before the x coordinate,
			// so a naive number scanner swallows "011" as one number.
			name: "arc flags run together", d: "M0 0a5 5 0 0110 0",
			commands: 2, numbers: 9, decimals: map[int]int{0: 9}, arcs: 1, relatives: 1,
		},
		{
			name: "arc flags with separators", d: "M0 0 A5 5 0 1 0 10 0",
			commands: 2, numbers: 9, decimals: map[int]int{0: 9}, arcs: 1, relatives: 0,
		},
		{
			// 1e-5 has five effective decimals but none in its mantissa, so the
			// precision bucket under-counts it. Documented, and harmless: the
			// effect is to make a producer look tidier than it is.
			name: "exponents", d: "M0 0L1e-5 2.5E2",
			commands: 2, numbers: 4, decimals: map[int]int{0: 3, 1: 1}, arcs: 0, relatives: 0,
		},
		{
			name: "one letter each", d: "M0 0h10v10h-10z",
			commands: 5, numbers: 5, decimals: map[int]int{0: 5}, arcs: 0, relatives: 4,
		},
		{
			// Implicit repeats draw real segments but add no letter, so they are
			// invisible to a command count and must be tracked separately.
			name: "implicit lineto after moveto", d: "M0 0 5 5 10 0",
			commands: 1, numbers: 6, decimals: map[int]int{0: 6}, arcs: 0, relatives: 0,
		},
		{
			name: "implicit repeated lineto", d: "L1 1 2 2 3 3",
			commands: 1, numbers: 6, decimals: map[int]int{0: 6}, arcs: 0, relatives: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pi := analyzePathData(c.d)
			if pi.Commands != c.commands {
				t.Errorf("commands = %d, want %d (%v)", pi.Commands, c.commands, pi.ByCmd)
			}
			if pi.Numbers != c.numbers {
				t.Errorf("numbers = %d, want %d", pi.Numbers, c.numbers)
			}
			for dec, want := range c.decimals {
				if pi.Digits[dec] != want {
					t.Errorf("numbers with %d decimals = %d, want %d (all: %v)", dec, pi.Digits[dec], want, pi.Digits)
				}
			}
			if pi.ByCmd['A'] != c.arcs {
				t.Errorf("arcs = %d, want %d", pi.ByCmd['A'], c.arcs)
			}
			if pi.Relative != c.relatives {
				t.Errorf("relative = %d, want %d", pi.Relative, c.relatives)
			}
		})
	}
}

func TestFlattenedPointCount(t *testing.T) {
	cases := []struct {
		d    string
		pts  int
		note string
	}{
		{"M0 0 5 5 10 0", 3, "moveto plus two implicit linetos"},
		{"L1 1 2 2", 2, "one letter, two segments"},
		{"M0 0L1 1Z", 3, "moveto, lineto, implicit closing point"},
		{"M0 0h1h1h1", 4, "three relative horizontal runs"},
	}
	for _, c := range cases {
		if got := analyzePathData(c.d).PolylinePoints; got != c.pts {
			t.Errorf("%q: points = %d, want %d (%s)", c.d, got, c.pts, c.note)
		}
	}
}

// A stats tool that hangs on malformed input is worse than useless: SVG in the
// wild contains junk, and the corpus will eventually contain a file that breaks
// a naive parser.
func TestPathTokenizerTerminatesOnJunk(t *testing.T) {
	for _, d := range []string{
		"M0 0L", "M", "L L L", "M0 0 L $$ $$", "a", "M0 0A", "zzz", "M0 0L1 1Q", "",
		"M0 0C1 1 1", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = analyzePathData(d)
		}()
		<-done
	}
}

func TestAnalyzeJunkDoesNotPanic(t *testing.T) {
	junk := `<?xml version="1.0"?><svg width="1" height="1"><path d="M????"/><g><path/></svg>`
	if _, err := AnalyzeBytes([]byte(junk)); err != nil && !strings.Contains(err.Error(), "parse") {
		t.Fatalf("unexpected error kind: %v", err)
	}
}

func TestCompare(t *testing.T) {
	a, err := AnalyzeBytes([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	b, err := AnalyzeBytes([]byte(strings.Replace(sample, "M 0 0 L 10.5 0.25 L 10 10 Z", "M0,0 10,0", 1)))
	if err != nil {
		t.Fatal(err)
	}
	deltas := Compare(a, b)
	if len(deltas) == 0 {
		t.Fatal("no deltas")
	}
	var bytesDelta float64
	for _, d := range deltas {
		if d.Field == "pathDataBytes" {
			bytesDelta = d.Delta
		}
	}
	if bytesDelta >= 0 {
		t.Errorf("expected the shortened path to be smaller, delta = %v", bytesDelta)
	}
	if FormatDeltaTable(deltas) == "" {
		t.Error("empty table")
	}
}

func TestPrecisionBucketsFeedThrough(t *testing.T) {
	s, err := AnalyzeBytes([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	total := s.PrecisionLe0 + s.PrecisionLe1 + s.PrecisionLe2 + s.PrecisionLe3 + s.PrecisionGt3
	if total != s.Numbers {
		t.Errorf("precision buckets sum to %d, want %d", total, s.Numbers)
	}
}

// FuzzAnalyze is the safety net for the whole project. Every corpus image, and
// every SVG any user will ever hand us, goes through this parser first. A panic
// or a hang here is a crash in the encode path, so both are treated as failures
// rather than as curiosity.
func FuzzAnalyze(f *testing.F) {
	seeds := []string{
		sample,
		`<svg/>`,
		`<svg width="1" height="1" viewBox="0 0 1 1"><path d="M0 0A1 1 0 119 9z"/></svg>`,
		`<svg><path d="M0 0L1e-5 2.5E-3 1e9999 -1e-9999 z"/></svg>`,
		`<svg><path d="M0 0 5 5 10 0 15 5"/></svg>`,
		`<svg><path d="a5 5 0 0110 0a5 5 0 1130 0"/></svg>`,
		`<svg><path d="M????"/><g><path/></svg>`,
		`<?xml version="1.0"?><!DOCTYPE svg [<!ENTITY x "y">]><svg>&x;</svg>`,
		"<svg><![CDATA[]]></svg>",
		"<svg><path d=\"\"/></svg>",
		"\x00\x01\x02",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		st, err := AnalyzeBytes(b)
		if err != nil {
			return // malformed input is a valid outcome, not a bug
		}
		// Whatever comes out has to be self-consistent, and String/JSON must
		// never panic on whatever the input managed to produce.
		if st.Elements > 0 {
			if st.MaxDepth < 1 {
				t.Fatalf("elements=%d but MaxDepth=%d", st.Elements, st.MaxDepth)
			}
		}
		_ = st.String()
		if _, err := st.JSON(); err != nil {
			t.Fatalf("JSON: %v", err)
		}
		_ = FormatDeltaTable(Compare(st, st))
	})
}
