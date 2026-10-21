package diff

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"pgregory.net/rapid"
)

var textRunes = []rune{'a', 'b', 'Z', '0', ' ', '\t', '\r', '-', '+', '@', '\\', '_', '(', 'é', '日', '😀'}

func genText(t *rapid.T, label string) string {
	return string(rapid.SliceOf(rapid.SampledFrom(textRunes)).Draw(t, label))
}

var pathRunes = []rune{'a', 'b', 'x', 'Z', '1', ' ', '.', '/', '"', '\t', '\\', '\n', 'ä', '日', '-', '_'}

// genPath is a non-empty relative path without NUL.
func genPath(t *rapid.T, label string) string {
	first := rapid.SampledFrom([]rune{'a', 'b', 'x', 'ä', '"', ' '}).Draw(t, label+"-first")
	rest := rapid.SliceOf(rapid.SampledFrom(pathRunes)).Draw(t, label)
	return string(first) + string(rest)
}

var modes = []string{"100644", "100755", "120000"}

func genHunk(t *rapid.T, allowNoEOL bool) Hunk {
	kinds := rapid.SliceOfN(rapid.SampledFrom([]Kind{Context, Added, Removed}), 1, 12).Draw(t, "kinds")
	h := Hunk{
		OldStart: rapid.IntRange(1, 500).Draw(t, "oldStart"),
		NewStart: rapid.IntRange(1, 500).Draw(t, "newStart"),
	}
	if rapid.Bool().Draw(t, "section") {
		h.Section = genText(t, "sectionText")
	}
	oldNo, newNo := h.OldStart, h.NewStart
	lastOld, lastNew := -1, -1
	for i, k := range kinds {
		l := Line{Kind: k, Text: genText(t, "text")}
		if k != Added {
			l.Old = oldNo
			oldNo++
			h.OldCount++
			lastOld = i
		}
		if k != Removed {
			l.New = newNo
			newNo++
			h.NewCount++
			lastNew = i
		}
		h.Lines = append(h.Lines, l)
	}
	if allowNoEOL {
		last := len(kinds) - 1
		for _, i := range []int{lastOld, lastNew, last} {
			ok := i >= 0 && (kinds[i] != Context || i == last)
			if ok && rapid.Bool().Draw(t, "noeol") {
				h.Lines[i].NoEOL = true
			}
		}
	}
	return h
}

func genFile(t *rapid.T) File {
	var f File
	switch rapid.IntRange(0, 4).Draw(t, "status") {
	case 0:
		f.Status = Changed
		f.OldPath = genPath(t, "path")
		f.NewPath = f.OldPath
		if rapid.Bool().Draw(t, "modeChange") {
			f.OldMode, f.NewMode = modes[0], modes[1]
		}
	case 1:
		f.Status = Created
		f.NewPath = genPath(t, "path")
		f.NewMode = rapid.SampledFrom(modes).Draw(t, "mode")
	case 2:
		f.Status = Deleted
		f.OldPath = genPath(t, "path")
		f.OldMode = rapid.SampledFrom(modes).Draw(t, "mode")
	default:
		f.Status = Renamed
		if rapid.Bool().Draw(t, "copy") {
			f.Status = Copied
		}
		f.OldPath = genPath(t, "from")
		f.NewPath = genPath(t, "to")
		if f.OldPath == f.NewPath {
			f.NewPath += "2"
		}
	}
	if rapid.IntRange(0, 5).Draw(t, "binary") == 0 {
		f.Binary = true
		return f
	}
	n := rapid.IntRange(0, 3).Draw(t, "hunks")
	for i := 0; i < n; i++ {
		f.Hunks = append(f.Hunks, genHunk(t, true))
	}
	return f
}

func TestRoundTripProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		files := rapid.SliceOfN(rapid.Custom(genFile), 0, 4).Draw(t, "files")
		out := Format(files)
		got, err := Parse(out)
		if err != nil {
			t.Fatalf("Parse(Format) failed: %v\n%s", err, out)
		}
		if len(files) == 0 {
			files = nil
		}
		if !reflect.DeepEqual(got, files) {
			t.Fatalf("round trip differs\ngot  %+v\nwant %+v\ntext:\n%s", got, files, out)
		}
	})
}

func TestParseNeverPanicsProperty(t *testing.T) {
	pieces := []string{
		"diff --git ", "a/", "b/", " ", "\n", "@@ -1,2 +3 @@", "@@ ", "--- ", "+++ ", "+", "-", " ", "\\ No newline",
		"\"", "\\", "\\3", "rename from ", "Binary files ", "GIT binary patch", "/dev/null", "x", "é", "\r", "0", ",",
	}
	rapid.Check(t, func(t *rapid.T) {
		src := strings.Join(rapid.SliceOf(rapid.SampledFrom(pieces)).Draw(t, "pieces"), "")
		_, _ = Parse(src)
		raw := rapid.String().Draw(t, "raw")
		_, _ = Parse(raw)
		_ = Format(nil)
	})
}

func TestUnquoteQuoteProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.String().Draw(t, "s")
		q := quote(s)
		if !needsQuote(s) {
			if q != s {
				t.Fatalf("plain path changed: %q", q)
			}
			return
		}
		got, rest, ok := unquote(q + " tail")
		if !ok || got != s || rest != " tail" {
			t.Fatalf("unquote(%q) = %q %q %v", q, got, rest, ok)
		}
	})
}

func genLine(t *rapid.T, label string) string {
	words := rapid.SliceOf(rapid.SampledFrom([]string{"foo", "bar", " ", "  ", "(", ")", ",", "x1", "_y", "é", "日本", "=", ".", "\t"})).Draw(t, label)
	return strings.Join(words, "")
}

func TestWordDiffProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		old, new := genLine(t, "old"), genLine(t, "new")
		if rapid.Bool().Draw(t, "related") {
			new = old + new
		}
		oldSpans, newSpans, ok := WordDiff(old, new)
		if !ok {
			if oldSpans != nil || newSpans != nil {
				t.Fatal("spans returned with ok false")
			}
			return
		}
		a, b := cut(t, old, oldSpans), cut(t, new, newSpans)
		if a != b {
			t.Fatalf("remainders differ: %q vs %q (spans %v %v)", a, b, oldSpans, newSpans)
		}
		if old == new && (len(oldSpans) != 0 || len(newSpans) != 0) {
			t.Fatal("identical lines have spans")
		}
	})
}

// cut checks the spans are sorted, disjoint, in bounds and on rune
// boundaries, and returns s with them deleted.
func cut(t *rapid.T, s string, spans []Span) string {
	var b strings.Builder
	at := 0
	for _, sp := range spans {
		if sp.Start < at || sp.End <= sp.Start || sp.End > len(s) {
			t.Fatalf("bad span %v in %q (after %d)", sp, s, at)
		}
		if at > 0 && sp.Start == at {
			t.Fatalf("spans not merged at %d", at)
		}
		if !utf8.RuneStart(s[sp.Start]) || (sp.End < len(s) && !utf8.RuneStart(s[sp.End])) {
			t.Fatalf("span %v splits a rune in %q", sp, s)
		}
		b.WriteString(s[at:sp.Start])
		at = sp.End
	}
	b.WriteString(s[at:])
	return b.String()
}

func TestWordDiffArbitraryProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		old, new := rapid.String().Draw(t, "old"), rapid.String().Draw(t, "new")
		o, n, ok := WordDiff(old, new)
		if ok && cut(t, old, o) != cut(t, new, n) {
			t.Fatalf("remainders differ for %q %q", old, new)
		}
	})
}

func TestPairsProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		h := genHunk(t, false)
		rows := Pairs(h)
		var oldSeen, newSeen []int
		for _, r := range rows {
			if r.Old < 0 && r.New < 0 {
				t.Fatalf("empty row in %v", rows)
			}
			if r.Old >= 0 {
				oldSeen = append(oldSeen, r.Old)
				if h.Lines[r.Old].Kind == Added {
					t.Fatalf("added line %d on the left", r.Old)
				}
			}
			if r.New >= 0 {
				newSeen = append(newSeen, r.New)
				if h.Lines[r.New].Kind == Removed {
					t.Fatalf("removed line %d on the right", r.New)
				}
			}
			if (r.Old >= 0 && h.Lines[r.Old].Kind == Context) != (r.Old == r.New) {
				t.Fatalf("context row %v is not on both sides", r)
			}
		}
		var wantOld, wantNew []int
		for i, l := range h.Lines {
			if l.Kind != Added {
				wantOld = append(wantOld, i)
			}
			if l.Kind != Removed {
				wantNew = append(wantNew, i)
			}
		}
		if !reflect.DeepEqual(oldSeen, wantOld) || !reflect.DeepEqual(newSeen, wantNew) {
			t.Fatalf("sides wrong: %v %v for %v", oldSeen, newSeen, rows)
		}
	})
}

func TestUntrackedProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		content := rapid.SliceOf(rapid.SampledFrom([]byte("ab\n\r \x00"))).Draw(t, "content")
		f := Untracked("p", content)
		out := Format([]File{f})
		got, err := Parse(out)
		if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], f) {
			t.Fatalf("round trip of %q: %v %+v", content, err, got)
		}
		if f.Binary || len(content) == 0 {
			return
		}
		var b strings.Builder
		for i, l := range f.Hunks[0].Lines {
			b.WriteString(l.Text)
			if !l.NoEOL || i < len(f.Hunks[0].Lines)-1 {
				b.WriteByte('\n')
			}
		}
		if b.String() != string(content) {
			t.Fatalf("content %q came back as %q", content, b.String())
		}
	})
}
