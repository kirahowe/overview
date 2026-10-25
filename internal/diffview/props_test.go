package diffview

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/rivo/uniseg"
	"pgregory.net/rapid"

	"overview/internal/diff"
	"overview/internal/review"
)

// pieces are what generated text is made of: the code a diff can carry,
// hostile parts included.
var pieces = []string{
	"a", "b", "x", "if", " ", "  ", "_", "(", "}", "\t", "a\tb",
	"\x1b[31m", "\x1b[2J", "\x1b]0;title\x07", "\x1b", "\x00", "\x07", "\x08", "\x7f",
	"\u0085", "\u009b", "\r", "\xff", "\xc3", "\xe6\x97",
	"日", "本語", "😀", "👨‍👩‍👧", "☺️", "️", "é", "́", "̈",
	"‮", "⁦", "‏", "‍", "­", " ",
	strings.Repeat("long", 40),
}

func genPiecesText(t *rapid.T, label string) string {
	parts := rapid.SliceOfN(rapid.SampledFrom(pieces), 0, 10).Draw(t, label)
	s := strings.Join(parts, "")
	if rapid.IntRange(0, 4).Draw(t, label+"-cr") == 0 {
		s += "\r"
	}
	return s
}

// genLineText is a line's text, sometimes one of a few plain words so
// that lines repeat, as they do in code.
func genLineText(t *rapid.T, label string) string {
	if rapid.Bool().Draw(t, label+"-plain") {
		return rapid.SampledFrom([]string{"", "x", "y := 1", "}", "return nil"}).Draw(t, label+"-word")
	}
	return genPiecesText(t, label)
}

var paths = []string{"a.go", "b/c.py", "日本.txt", "sp ace.md", "weird\x1b[2J.go", "é.rs", "z.js", "tab\there.c"}

// genHunks makes hunks numbered from start on both sides, with gaps
// between them, each line's numbers right for its side. only limits the
// kinds: Added for a new file, Removed for a deleted one, else any.
func genHunks(t *rapid.T, start int, only []diff.Kind) []diff.Hunk {
	n := rapid.IntRange(0, 3).Draw(t, "hunks")
	oldNo, newNo := start, start
	if only[0] == diff.Added && len(only) == 1 {
		oldNo = 0
	}
	if only[0] == diff.Removed && len(only) == 1 {
		newNo = 0
	}
	var hs []diff.Hunk
	for range n {
		kinds := rapid.SliceOfN(rapid.SampledFrom(only), 1, 10).Draw(t, "kinds")
		h := diff.Hunk{OldStart: oldNo, NewStart: newNo}
		if rapid.Bool().Draw(t, "section") {
			h.Section = genPiecesText(t, "section")
		}
		for _, k := range kinds {
			l := diff.Line{Kind: k, Text: genLineText(t, "text")}
			if k != diff.Added {
				l.Old = oldNo
				oldNo++
				h.OldCount++
			}
			if k != diff.Removed {
				l.New = newNo
				newNo++
				h.NewCount++
			}
			h.Lines = append(h.Lines, l)
		}
		hs = append(hs, h)
		gap := rapid.IntRange(1, 20).Draw(t, "gap")
		if oldNo > 0 {
			oldNo += gap
		}
		if newNo > 0 {
			newNo += gap
		}
	}
	return hs
}

func genFile(t *rapid.T, path string, start int) diff.File {
	f := diff.File{OldPath: path, NewPath: path, OldMode: "100644", NewMode: "100644"}
	only := []diff.Kind{diff.Context, diff.Added, diff.Removed}
	switch rapid.IntRange(0, 5).Draw(t, "status") {
	case 0:
		f.Status, f.OldPath, f.OldMode = diff.Created, "", ""
		only = []diff.Kind{diff.Added}
	case 1:
		f.Status, f.NewPath, f.NewMode = diff.Deleted, "", ""
		only = []diff.Kind{diff.Removed}
	case 2:
		f.Status, f.OldPath = diff.Renamed, "old/"+path
	case 3:
		if rapid.Bool().Draw(t, "binary") {
			f.Binary = true
			return f
		}
		f.NewMode = "100755"
	}
	f.Hunks = genHunks(t, start, only)
	return f
}

func genFiles(t *rapid.T) []diff.File {
	ps := rapid.Permutation(paths).Draw(t, "paths")
	n := rapid.IntRange(0, 4).Draw(t, "files")
	var files []diff.File
	for i := range n {
		files = append(files, genFile(t, ps[i], rapid.IntRange(1, 60).Draw(t, "start")))
	}
	return files
}

type sideLine struct {
	num  int
	text string
}

// linesOn is the lines of h on one side, in order.
func linesOn(h diff.Hunk, side review.Side) []sideLine {
	var out []sideLine
	for _, l := range h.Lines {
		switch {
		case side == review.New && l.Kind != diff.Removed:
			out = append(out, sideLine{l.New, l.Text})
		case side == review.Old && l.Kind != diff.Added:
			out = append(out, sideLine{l.Old, l.Text})
		}
	}
	return out
}

// genEvents is a review of files: notes on lines in the diff, some on
// lines since changed or on files not in it, replies by both, some
// resolved and some sent.
func genEvents(t *rapid.T, files []diff.File) []review.Event {
	n := rapid.IntRange(0, 6).Draw(t, "notes")
	var es []review.Event
	var ids []string
	authors := []review.Author{review.User, review.Agent}
	for i := range n {
		id := fmt.Sprintf("n%d", i)
		body := genPiecesText(t, "body") + "x" + rapid.SampledFrom([]string{"", "\nline two", "\n\nthree\n"}).Draw(t, "more")
		var a review.Anchor
		kind := rapid.IntRange(0, 4).Draw(t, "where")
		var hunks []diff.Hunk
		var fi int
		if len(files) > 0 {
			fi = rapid.IntRange(0, len(files)-1).Draw(t, "file")
			hunks = files[fi].Hunks
		}
		if kind == 0 || len(hunks) == 0 {
			a = review.Anchor{Path: "gone.go", Side: review.New, Start: 3, End: 3, Lines: []string{"gone"}}
		} else {
			f := files[fi]
			h := hunks[rapid.IntRange(0, len(hunks)-1).Draw(t, "hunk")]
			side := review.New
			ls := linesOn(h, side)
			if len(ls) == 0 || (rapid.Bool().Draw(t, "old") && len(linesOn(h, review.Old)) > 0) {
				side = review.Old
				ls = linesOn(h, side)
			}
			from := rapid.IntRange(0, len(ls)-1).Draw(t, "from")
			to := rapid.IntRange(from, min(len(ls)-1, from+3)).Draw(t, "to")
			a = review.Anchor{Path: f.NewPath, Side: side, Start: ls[from].num, End: ls[to].num}
			if side == review.Old {
				a.Path = f.OldPath
			}
			for _, l := range ls[from : to+1] {
				a.Lines = append(a.Lines, l.text)
			}
			if kind == 1 {
				a.Lines[0] += " since changed"
			}
		}
		es = append(es, review.Event{Op: review.OpNote, ID: id, Author: rapid.SampledFrom(authors).Draw(t, "author"), Body: body, Anchor: &a})
		ids = append(ids, id)
		for j := range rapid.IntRange(0, 3).Draw(t, "replies") {
			rid := fmt.Sprintf("%s-r%d", id, j)
			es = append(es, review.Event{Op: review.OpReply, ID: rid, Note: id, Author: rapid.SampledFrom(authors).Draw(t, "replier"), Body: genPiecesText(t, "reply") + "y"})
			ids = append(ids, rid)
		}
		switch rapid.IntRange(0, 3).Draw(t, "state") {
		case 0:
			es = append(es, review.Event{Op: review.OpResolve, Note: id, Author: review.User})
		case 1:
			es = append(es, review.Event{Op: review.OpAddressed, Note: id, Author: review.Agent})
		}
	}
	if len(ids) > 0 && rapid.Bool().Draw(t, "sent") {
		k := rapid.IntRange(1, len(ids)).Draw(t, "sentCount")
		es = append(es, review.Event{Op: review.OpSent, Author: review.User, IDs: ids[:k]})
	}
	return es
}

// checkFrame says the frame is exactly the pane's size and holds nothing
// a terminal would act on.
func checkFrame(t *rapid.T, m model, after string) {
	t.Helper()
	rows, owners := m.frame()
	if len(rows) != m.h || len(owners) != len(rows) {
		t.Fatalf("after %s: %d rows, %d owners, want %d", after, len(rows), len(owners), m.h)
	}
	for i, r := range rows {
		w := 0
		for _, s := range r {
			w += uniseg.StringWidth(s.Text)
			if !utf8.ValidString(s.Text) {
				t.Fatalf("after %s: row %d holds invalid UTF-8 %q", after, i, s.Text)
			}
			for _, c := range s.Text {
				if unicode.IsControl(c) {
					t.Fatalf("after %s: row %d holds control %U in %q", after, i, c, s.Text)
				}
			}
		}
		if w != m.w {
			t.Fatalf("after %s: row %d is %d cells, want %d: %+v", after, i, w, m.w, r)
		}
	}
	lines := strings.Split(m.View(), "\n")
	if len(lines) != m.h {
		t.Fatalf("after %s: View has %d lines, want %d", after, len(lines), m.h)
	}
	for i, l := range lines {
		if w := ansi.StringWidth(ansi.Strip(l)); w != m.w {
			t.Fatalf("after %s: View line %d is %d cells, want %d: %q", after, i, w, m.w, ansi.Strip(l))
		}
	}
}

var propKeys = []string{
	"j", "k", "j", "k", "ctrl+d", "ctrl+u", "g", "G", "]", "[", "}", "{", "n", "N",
	"m", "h", "v", "c", "esc", "enter", "e", "x", "s", "b", "R", "z", "?", " ", "pgdown",
}

var editKeys = []string{"backspace", "delete", "left", "right", "up", "down", "home", "end", "ctrl+w", "ctrl+j", "ctrl+u", "esc"}

func TestFrameShapeProperty(t *testing.T) {
	base := t.TempDir()
	theme := testTheme(t)
	rapid.Check(t, func(t *rapid.T) {
		dir, err := os.MkdirTemp(base, "case")
		if err != nil {
			t.Fatal(err)
		}
		files := genFiles(t)
		f := newFixture(dir, diff.Format(files))
		if es := genEvents(t, files); len(es) > 0 {
			if err := f.store.Append(es...); err != nil {
				t.Fatal(err)
			}
		}
		cfg := baseConfig(theme, f)
		cfg.Title = rapid.SampledFrom([]string{"", "plain", "evil\x1b[2J\ntitle\t日本"}).Draw(t, "title")
		if rapid.Bool().Draw(t, "colour") {
			cfg.Profile = termenv.TrueColor
		}
		if rapid.Bool().Draw(t, "started") {
			cfg.Started = f.now.Add(-1e12)
		}
		if rapid.Bool().Draw(t, "tmux") {
			f.panes = &fakePanes{screen: rapid.SampledFrom([]string{"", "\x1b[2J", "❯ \n"}).Draw(t, "screen")}
			cfg.Target, cfg.Self = "%1", "%2"
		}
		m := newModel(cfg, f.deps())
		w, h := rapid.IntRange(1, 250).Draw(t, "w"), rapid.IntRange(1, 80).Draw(t, "h")
		m, cmd := step(m, tea.WindowSizeMsg{Width: w, Height: h})
		m = drain(t, m, cmd)
		checkFrame(t, m, "opening")
		m, cmd = step(m, loadedMsg{gen: m.gen, base: "base0123456789abcdef", files: files})
		m = drain(t, m, tea.Batch(cmd, m.loadStore()))
		if rapid.Bool().Draw(t, "flip") {
			m = press(t, m, "m")
		}
		checkFrame(t, m, "loading")

		for i := range rapid.IntRange(0, 40).Draw(t, "steps") {
			var msg tea.Msg
			var what string
			action := rapid.IntRange(0, 9).Draw(t, "action")
			if m.comp.open && action >= 4 && rapid.Bool().Draw(t, "editing") {
				// Mostly edit while the editor is open.
				action = rapid.SampledFrom([]int{0, 0, 10}).Draw(t, "edit")
			}
			switch action {
			case 10:
				k := rapid.SampledFrom(editKeys).Draw(t, "editKey")
				msg, what = keyMsg(k), "key "+k
			case 0:
				s := genPiecesText(t, "typed")
				msg, what = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s), Paste: rapid.Bool().Draw(t, "paste")}, fmt.Sprintf("typing %q", s)
			case 1:
				nw, nh := rapid.IntRange(1, 250).Draw(t, "w"), rapid.IntRange(1, 80).Draw(t, "h")
				msg, what = tea.WindowSizeMsg{Width: nw, Height: nh}, fmt.Sprintf("resizing to %dx%d", nw, nh)
			case 2:
				b := rapid.SampledFrom([]tea.MouseButton{tea.MouseButtonWheelDown, tea.MouseButtonWheelUp, tea.MouseButtonLeft}).Draw(t, "button")
				y := rapid.IntRange(-1, 85).Draw(t, "y")
				msg, what = tea.MouseMsg{Button: b, Action: tea.MouseActionPress, Y: y}, fmt.Sprintf("mouse %v at %d", b, y)
			case 3:
				e := genPiecesText(t, "err")
				msg, what = doneMsg{err: fmt.Errorf("failed: %s", e)}, fmt.Sprintf("an error %q", e)
			default:
				k := rapid.SampledFrom(propKeys).Draw(t, "key")
				msg, what = keyMsg(k), "key "+k
			}
			m, cmd = step(m, msg)
			m = drain(t, m, cmd)
			checkFrame(t, m, fmt.Sprintf("step %d, %s", i, what))
			if m.cur < 0 || (len(m.items) > 0 && m.cur >= len(m.items)) || m.top < 0 || (len(m.items) > 0 && m.top >= len(m.items)) {
				t.Fatalf("after %s: cursor %d, top %d of %d items", what, m.cur, m.top, len(m.items))
			}
		}
	})
}

// isControlFree says s holds nothing a terminal acts on.
func isControlFree(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || bidi(r) {
			return false
		}
	}
	return utf8.ValidString(s)
}

func TestCleanProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.OneOf(
			rapid.Custom(func(t *rapid.T) string { return genPiecesText(t, "s") }),
			rapid.StringOf(rapid.Rune()),
			rapid.Map(rapid.SliceOf(rapid.Byte()), func(b []byte) string { return string(b) }),
		).Draw(t, "s")
		out, at := clean(s)
		if !isControlFree(out) {
			t.Fatalf("clean(%q) = %q", s, out)
		}
		if len(at) != len(s)+1 || at[len(s)] != len(out) || at[0] != 0 {
			t.Fatalf("at %v for %q -> %q", at, s, out)
		}
		for i := range at {
			if i > 0 && at[i] < at[i-1] {
				t.Fatalf("at decreases at %d: %v", i, at)
			}
			if at[i] < len(out) && !utf8.RuneStart(out[at[i]]) {
				t.Fatalf("at[%d] = %d is inside a rune of %q", i, at[i], out)
			}
		}
	})
}

// safe are the runes clean leaves alone.
var safe = []rune{'a', 'Z', '0', ' ', '_', '{', 'é', '日', '😀', 0x200d, 0xfe0f, 0x0301, 0x00ad, 0x2028, 'ß'}

func TestCleanKeepsSafeTextProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := string(rapid.SliceOf(rapid.SampledFrom(safe)).Draw(t, "s"))
		out, at := clean(s)
		if out != s {
			t.Fatalf("clean(%q) = %q", s, out)
		}
		for i, a := range at {
			if (i == len(s) || utf8.RuneStart(s[i])) && a != i {
				t.Fatalf("at[%d] = %d", i, a)
			}
		}
	})
}

func TestCleanCases(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"a\tb", "a   b"},
		{"abcd\te", "abcd    e"},
		{"日\tx", "日  x"},
		{"\x1b[31mred", "␛[31mred"},
		{"crlf\r", "crlf"},
		{"mid\rdle", "mid␍dle"},
		{"two\r\r", "two␍"},
		{"\x00\x7f", "␀␡"},
		{"c1\u009b", "c1�"},
		{"bad\xffbyte", "bad�byte"},
		{"rtl‮evil", "rtl�evil"},
	}
	for _, c := range cases {
		if got, _ := clean(c.in); got != c.want {
			t.Errorf("clean(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

var segStyles = []Style{{Fg: "1"}, {Fg: "2", Bold: true}, {Bg: "3"}}
var fill = Style{Fg: "fill", Reverse: true}

func genSegs(t *rapid.T) []Seg {
	var segs []Seg
	for range rapid.IntRange(0, 6).Draw(t, "segs") {
		text, _ := clean(genPiecesText(t, "seg"))
		segs = append(segs, Seg{text, rapid.SampledFrom(segStyles).Draw(t, "style")})
	}
	return segs
}

func TestWrapProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		segs := genSegs(t)
		w := rapid.IntRange(1, 40).Draw(t, "w")
		rows := wrap(segs, w, fill)
		if len(rows) == 0 {
			t.Fatal("no rows")
		}
		// The graphemes the rows must hold, in order: each segment's own,
		// one too wide for any row drawn as "?". Neighbours in one style
		// are drawn joined, so their graphemes are read joined.
		var want []string
		for _, s := range join(segs) {
			g := uniseg.NewGraphemes(s.Text)
			for g.Next() {
				if g.Width() > w {
					want = append(want, "?")
				} else {
					want = append(want, g.Str())
				}
			}
		}
		next := 0
		for i, r := range rows {
			if rw := width(r); rw > w || (i < len(rows)-1 && rw != w) {
				t.Fatalf("row %d of %d is %d cells at w %d: %+v", i, len(rows), rw, w, r)
			}
			var text string
			for k, s := range r {
				if s.St == fill {
					if k != len(r)-1 || strings.Trim(s.Text, " ") != "" {
						t.Fatalf("fill inside row %d: %+v", i, r)
					}
					continue
				}
				text += s.Text
			}
			// The row is a run of whole graphemes.
			acc := ""
			for acc != text && len(acc) < len(text) && next < len(want) {
				acc += want[next]
				next++
			}
			if acc != text {
				t.Fatalf("row %d holds %q, not whole graphemes (got to %q)", i, text, acc)
			}
		}
		if next != len(want) {
			t.Fatalf("the rows hold %d of %d graphemes", next, len(want))
		}
	})
}

// wrapWords breaks prose between words: it keeps every grapheme but the
// spaces a break stands for, draws no blank row, indents the rows after
// the first as the first is when there is room, and splits only a word
// wider than the room after that indentation. A reversed space, the
// editor's cursor, is never dropped.
func TestWrapWordsProperty(t *testing.T) {
	styles := append(append([]Style{}, segStyles...), Style{Fg: "4", Reverse: true})
	rapid.Check(t, func(t *rapid.T) {
		var segs []Seg
		if indent := rapid.IntRange(0, 12).Draw(t, "indent"); indent > 0 {
			segs = append(segs, Seg{strings.Repeat(" ", indent), rapid.SampledFrom(styles).Draw(t, "indentStyle")})
		}
		for range rapid.IntRange(0, 6).Draw(t, "segs") {
			text, _ := clean(genPiecesText(t, "seg") + rapid.SampledFrom([]string{"", " ", "  ", " word "}).Draw(t, "space"))
			segs = append(segs, Seg{text, rapid.SampledFrom(styles).Draw(t, "style")})
		}
		w := rapid.IntRange(1, 40).Draw(t, "w")
		rows := wrapWords(segs, w, fill)
		if len(rows) == 0 {
			t.Fatal("no rows")
		}
		type gr struct {
			text string
			wd   int
			rev  bool
		}
		graphemes := func(segs []Seg) []gr {
			var out []gr
			for _, s := range join(segs) {
				g := uniseg.NewGraphemes(s.Text)
				for g.Next() {
					text, gw := g.Str(), g.Width()
					if gw > w {
						text, gw = "?", 1
					}
					out = append(out, gr{text, gw, s.St.Reverse})
				}
			}
			return out
		}
		droppable := func(g gr) bool { return g.text == " " && !g.rev }
		var want []string
		for _, g := range graphemes(segs) {
			if !droppable(g) {
				want = append(want, g.text)
			}
		}
		var got []string
		var drawn [][]gr // each row's graphemes, the fill left out
		for i, r := range rows {
			if rw := width(r); rw > w || (i < len(rows)-1 && rw != w) {
				t.Fatalf("row %d of %d is %d cells at w %d: %+v", i, len(rows), rw, w, r)
			}
			var content []Seg
			for k, s := range r {
				if s.St == fill {
					if k != len(r)-1 || strings.Trim(s.Text, " ") != "" {
						t.Fatalf("fill inside row %d: %+v", i, r)
					}
					continue
				}
				content = append(content, s)
			}
			gs := graphemes(content)
			drawn = append(drawn, gs)
			for _, g := range gs {
				if !droppable(g) {
					got = append(got, g.text)
				}
			}
		}
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("the rows hold %q, want %q", got, want)
		}
		// Every row holds something but spaces, unless the input does not.
		blank := len(want) == 0
		for i, gs := range drawn {
			if !slices.ContainsFunc(gs, func(g gr) bool { return !droppable(g) }) && !blank {
				t.Fatalf("row %d is blank: %+v", i, rows)
			}
		}
		if blank && len(rows) != 1 {
			t.Fatalf("blank input drawn on %d rows", len(rows))
		}
		// lead is how far the first row is indented. The rows after it
		// start with that many spaces when that leaves minHang cells, and
		// at once otherwise, then go straight on to something drawn.
		lead := 0
		for _, g := range drawn[0] {
			if g.text != " " {
				break
			}
			lead += g.wd
		}
		hang := 0
		if w-lead >= minHang && slices.ContainsFunc(drawn[0], func(g gr) bool { return g.text != " " }) {
			hang = lead
		}
		for i := 1; i < len(drawn); i++ {
			k, n := 0, 0
			for k < len(drawn[i]) && droppable(drawn[i][k]) {
				n += drawn[i][k].wd
				k++
			}
			// A grapheme of no width starts its row bare, so it does not
			// fuse with the indent.
			if n != hang && (n != 0 || k == len(drawn[i]) || drawn[i][k].wd != 0) {
				t.Fatalf("row %d starts with %d spaces, want %d: %+v", i, n, hang, rows)
			}
		}
		// A word that fits after the first row's indentation is drawn on
		// one row: match each input grapheme to where it is drawn, if it
		// is.
		type at struct{ row, k int }
		var flat []at
		for r, gs := range drawn {
			for k := range gs {
				flat = append(flat, at{r, k})
			}
		}
		get := func(a at) gr { return drawn[a.row][a.k] }
		in := graphemes(segs)
		rowOf := make([]int, len(in))
		next := 0
		for i, g := range in {
			rowOf[i] = -1
			if droppable(g) {
				if next < len(flat) && droppable(get(flat[next])) {
					next++
				}
				continue
			}
			for next < len(flat) && droppable(get(flat[next])) {
				next++
			}
			if next == len(flat) || get(flat[next]) != g {
				t.Fatalf("grapheme %d %q is not drawn in order", i, g.text)
			}
			rowOf[i] = flat[next].row
			next++
		}
		for i := 0; i < len(in); {
			if in[i].text == " " {
				i++
				continue
			}
			j, wd := i, 0
			for j < len(in) && in[j].text != " " {
				wd += in[j].wd
				j++
			}
			if wd <= w-lead && rowOf[i] != rowOf[j-1] {
				t.Fatalf("a word of %d cells is split at w %d: %+v", wd, w, rows)
			}
			i = j
		}
	})
}

func TestPadProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		segs := genSegs(t)
		w := rapid.IntRange(0, 40).Draw(t, "w")
		if got := width(pad(segs, w, fill)); got != w {
			t.Fatalf("pad to %d is %d cells", w, got)
		}
		tr := truncate(strings.Repeat("日a", rapid.IntRange(0, 30).Draw(t, "n")), w)
		if uniseg.StringWidth(tr) > w {
			t.Fatalf("truncate to %d is %q", w, tr)
		}
	})
}

type editOp struct {
	name string
	text string
}

func (o editOp) apply(e editor) editor {
	switch o.name {
	case "insert":
		return e.insert(o.text)
	case "backspace":
		return e.backspace()
	case "del":
		return e.del()
	case "left":
		return e.left()
	case "right":
		return e.right()
	case "up":
		return e.up()
	case "down":
		return e.down()
	case "home":
		return e.home()
	case "end":
		return e.end()
	case "deleteWord":
		return e.deleteWord()
	case "killLine":
		return e.killLine()
	}
	panic(o.name)
}

var editPieces = []string{"a", "b c", "\n", "\r\n", "\r", "\t", "日本", "😀", "é", "👨‍👩", "\xff", "  ", "word"}

func genEditOp(t *rapid.T) editOp {
	names := []string{"insert", "insert", "insert", "backspace", "del", "left", "right", "up", "down", "home", "end", "deleteWord", "killLine"}
	o := editOp{name: rapid.SampledFrom(names).Draw(t, "op")}
	if o.name == "insert" {
		o.text = strings.Join(rapid.SliceOfN(rapid.SampledFrom(editPieces), 1, 4).Draw(t, "parts"), "")
	}
	return o
}

func checkEditor(t *rapid.T, e editor, after string) {
	t.Helper()
	if e.pos < 0 || e.pos > len(e.text) || (e.pos < len(e.text) && !utf8.RuneStart(e.text[e.pos])) {
		t.Fatalf("after %s: pos %d in %q", after, e.pos, e.text)
	}
	if !utf8.ValidString(e.text) || strings.ContainsAny(e.text, "\r\t") {
		t.Fatalf("after %s: text %q", after, e.text)
	}
}

func TestEditorProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var e editor
		for _, o := range rapid.SliceOf(rapid.Custom(genEditOp)).Draw(t, "ops") {
			e = o.apply(e)
			checkEditor(t, e, fmt.Sprintf("%+v", o))
		}
		w := rapid.IntRange(1, 30).Draw(t, "w")
		rows, cur := e.rows(w, Style{})
		if cur < 0 || cur >= len(rows) {
			t.Fatalf("cursor row %d of %d", cur, len(rows))
		}
		// The cursor is drawn once, on its row, over the grapheme at pos,
		// or a space at the end of a line.
		// The grapheme the cursor is in, which left and right, moving by
		// runes, can put it inside of.
		under := " "
		start := strings.LastIndexByte(e.text[:e.pos], '\n') + 1
		line := e.text[start:]
		if i := strings.IndexByte(line, '\n'); i >= 0 {
			line = line[:i]
		}
		g := uniseg.NewGraphemes(line)
		for g.Next() {
			if from, to := g.Positions(); start+from <= e.pos && e.pos < start+to {
				under = g.Str()
			}
		}
		under, _ = clean(under)
		if uniseg.StringWidth(under) > w {
			under = "?"
		}
		drawnAt := -1
		for i, r := range rows {
			for _, sg := range r {
				if !sg.St.Reverse {
					continue
				}
				if drawnAt >= 0 || sg.Text != under {
					t.Fatalf("w %d: cursor drawn as %q on row %d (and row %d), want %q once in %q at %d", w, sg.Text, i, drawnAt, under, e.text, e.pos)
				}
				drawnAt = i
			}
		}
		if drawnAt != cur {
			t.Fatalf("w %d: cursor drawn on row %d, reported on %d", w, drawnAt, cur)
		}
		for _, r := range rows {
			if width(r) > w {
				t.Fatalf("an editor row is %d cells at w %d", width(r), w)
			}
			// As the terminal measures it, the cursor's segment included.
			if got := ansi.StringWidth(ansi.Strip(paint(pad(r, w, Style{}), termenv.Ascii))); got != w {
				t.Fatalf("an editor row padded to %d is %d cells on screen: %+v", w, got, r)
			}
		}
		// Typing then deleting as many runes leaves it as it was.
		s := strings.Join(rapid.SliceOf(rapid.SampledFrom([]string{"a", " ", "\n", "日", "😀", "é", "👨‍👩"})).Draw(t, "typed"), "")
		after := e.insert(s)
		for range utf8.RuneCountInString(s) {
			after = after.backspace()
		}
		if after != e {
			t.Fatalf("insert %q then backspaces: %+v, want %+v", s, after, e)
		}
	})
}

func TestEditorCases(t *testing.T) {
	e := editor{}.insert("one\r\ntwo\rthree\tx")
	if e.text != "one\ntwo\nthree x" || e.pos != len(e.text) {
		t.Errorf("insert: %+v", e)
	}
	e = e.up()
	if e.pos != len("one\ntwo") {
		t.Errorf("up from a longer line: pos %d", e.pos)
	}
	e = e.up().home().right().down()
	if e.pos != len("one\nt") {
		t.Errorf("down keeps the column: pos %d", e.pos)
	}
	e = editor{text: "foo bar  ", pos: 9}.deleteWord()
	if e.text != "foo " || e.pos != 4 {
		t.Errorf("deleteWord: %+v", e)
	}
	if e := (editor{text: "日本", pos: 6}).left(); e.pos != 3 {
		t.Errorf("left over a wide rune: %d", e.pos)
	}
	if e := (editor{}).left().backspace().up().right().del().down(); e != (editor{}) {
		t.Errorf("an empty editor moved: %+v", e)
	}
}

// layoutCase is a generated diff and review, laid out.
func layoutCase(t *rapid.T) ([]diff.File, review.Review, map[string]review.Place, layoutOpts, []item) {
	files := genFiles(t)
	rev := review.Fold(genEvents(t, files))
	places := review.Places(rev, files)
	o := layoutOpts{split: rapid.Bool().Draw(t, "split"), showResolved: rapid.Bool().Draw(t, "resolved")}
	return files, rev, places, o, layout(files, rev, places, o)
}

func TestLayoutRowsProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		files, _, _, o, items := layoutCase(t)
		for fi, f := range files {
			for hi, h := range f.Hunks {
				for li, l := range h.Lines {
					var olds, news, any int
					for _, it := range items {
						if it.kind != rowItem || it.file != fi || it.hunk != hi {
							continue
						}
						if it.old == li {
							olds++
						}
						if it.new == li {
							news++
						}
						if it.old == li || it.new == li {
							any++
						}
					}
					wantOld, wantNew := 0, 0
					if l.Kind != diff.Added {
						wantOld = 1
					}
					if l.Kind != diff.Removed {
						wantNew = 1
					}
					if olds != wantOld || news != wantNew || any != 1 {
						t.Fatalf("split %v: line %d of hunk %d of file %d (%v) is on %d old, %d new, %d rows", o.split, li, hi, fi, l.Kind, olds, news, any)
					}
				}
			}
		}
		for _, it := range items {
			if it.kind != rowItem {
				continue
			}
			if !o.split && it.old >= 0 && it.new >= 0 && it.old != it.new {
				t.Fatalf("a unified row on two lines: %+v", it)
			}
		}
	})
}

func TestLayoutNotesProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		files, rev, places, o, items := layoutCase(t)
		for _, n := range rev.Notes {
			count := 0
			for i, it := range items {
				if it.kind != noteItem || it.note != n.ID {
					continue
				}
				count++
				p := places[n.ID]
				if it.outdated != (p.Outdated || p.File < 0) {
					t.Fatalf("note %s outdated %v, placed %+v", n.ID, it.outdated, p)
				}
				// The item before the run of notes it is in.
				j := i - 1
				for j >= 0 && items[j].kind == noteItem {
					j--
				}
				if j < 0 {
					t.Fatalf("note %s is first", n.ID)
				}
				prev := items[j]
				switch {
				case p.File < 0:
					if prev.kind != strayItem {
						t.Fatalf("stray note %s after %+v", n.ID, prev)
					}
				case p.Outdated:
					if prev.kind != fileItem || prev.file != p.File {
						t.Fatalf("outdated note %s after %+v, not its file's header", n.ID, prev)
					}
				default:
					if prev.kind != rowItem || prev.file != p.File {
						t.Fatalf("note %s after %+v", n.ID, prev)
					}
					lines := files[prev.file].Hunks[prev.hunk].Lines
					idx, num := prev.new, 0
					if n.Anchor.Side == review.Old {
						idx = prev.old
					}
					if idx >= 0 {
						num = lines[idx].New
						if n.Anchor.Side == review.Old {
							num = lines[idx].Old
						}
					}
					if idx < 0 || num != p.End {
						t.Fatalf("note %s ending on %s line %d is after %+v", n.ID, n.Anchor.Side, p.End, prev)
					}
				}
			}
			want := 1
			if n.Resolved && !o.showResolved {
				want = 0
			}
			if count != want {
				t.Fatalf("note %s (resolved %v) is shown %d times", n.ID, n.Resolved, count)
			}
		}
		noteCount := 0
		for _, it := range items {
			if it.kind == noteItem {
				noteCount++
			}
		}
		shown := 0
		for _, n := range rev.Notes {
			if !n.Resolved || o.showResolved {
				shown++
			}
		}
		if noteCount != shown {
			t.Fatalf("%d note items for %d notes", noteCount, shown)
		}
	})
}

func TestCursorStaysOnItsSpotProperty(t *testing.T) {
	base := t.TempDir()
	theme := testTheme(t)
	rapid.Check(t, func(t *rapid.T) {
		dir, err := os.MkdirTemp(base, "case")
		if err != nil {
			t.Fatal(err)
		}
		ps := rapid.Permutation(paths).Draw(t, "paths")
		n := rapid.IntRange(1, 3).Draw(t, "files")
		var files []diff.File
		for i := range n {
			// Numbered from 30, so a hunk fits above the first.
			files = append(files, genFile(t, ps[i], 30))
		}
		f := newFixture(dir, "")
		if es := genEvents(t, files); len(es) > 0 {
			if err := f.store.Append(es...); err != nil {
				t.Fatal(err)
			}
		}
		m := newModel(baseConfig(theme, f), f.deps())
		m, cmd := step(m, tea.WindowSizeMsg{Width: rapid.IntRange(20, 200).Draw(t, "w"), Height: rapid.IntRange(3, 50).Draw(t, "h")})
		m = drain(t, m, cmd)
		m, cmd = step(m, loadedMsg{gen: m.gen, base: "b", files: files})
		m = drain(t, m, tea.Batch(cmd, m.loadStore()))
		if rapid.Bool().Draw(t, "flip") {
			m = press(t, m, "m")
		}
		var rowsAt []int
		for i, it := range m.items {
			if it.kind == rowItem {
				rowsAt = append(rowsAt, i)
			}
		}
		if len(rowsAt) == 0 {
			t.Skip("no lines")
		}
		m = m.moveTo(rapid.SampledFrom(rowsAt).Draw(t, "cursor"))
		before := cursorSpot(m)
		fi := m.items[m.cur].file

		// The reload adds files with other names before and after it, and
		// a hunk above its lines.
		var next []diff.File
		for i := range rapid.IntRange(0, 2).Draw(t, "before") {
			next = append(next, genFile(t, fmt.Sprintf("0-%d.txt", i), 1))
		}
		for i, g := range files {
			if i == fi && g.Hunks != nil {
				above := genHunks(t, 1, []diff.Kind{diff.Context, diff.Added, diff.Removed})
				if len(above) > 0 {
					g.Hunks = append([]diff.Hunk{above[0]}, g.Hunks...)
				}
			}
			next = append(next, g)
		}
		for i := range rapid.IntRange(0, 2).Draw(t, "after") {
			next = append(next, genFile(t, fmt.Sprintf("~%d.txt", i), 1))
		}
		m, _ = step(m, keyMsg("R"))
		m, cmd = step(m, loadedMsg{gen: m.gen, base: "b", files: next})
		m = drain(t, m, cmd)
		if got := cursorSpot(m); got != before {
			t.Fatalf("cursor at %+v after the reload, want %+v", got, before)
		}
		if m.items[m.cur].kind != rowItem {
			t.Fatalf("cursor on %+v", m.items[m.cur])
		}
	})
}

// Whatever notes arrive, and whatever hunks a reload adds or drops around
// it, the hunk header over the cursor stays drawn when a jump put it at
// the top of the screen: notes go under lines, and the header the screen
// starts at is found again as itself, by where it starts.
func TestNotesKeepTheHunkHeaderProperty(t *testing.T) {
	base := t.TempDir()
	theme := testTheme(t)
	rapid.Check(t, func(t *rapid.T) {
		dir, err := os.MkdirTemp(base, "case")
		if err != nil {
			t.Fatal(err)
		}
		ps := rapid.Permutation(paths).Draw(t, "paths")
		var files []diff.File
		for i := range rapid.IntRange(1, 3).Draw(t, "files") {
			// Numbered from 30, so a hunk fits above the first.
			files = append(files, genFile(t, ps[i], 30))
		}
		f := newFixture(dir, "")
		m := newModel(baseConfig(theme, f), f.deps())
		m, cmd := step(m, tea.WindowSizeMsg{Width: rapid.IntRange(40, 200).Draw(t, "w"), Height: rapid.IntRange(14, 50).Draw(t, "h")})
		m = drain(t, m, cmd)
		m, cmd = step(m, loadedMsg{gen: m.gen, base: "b", files: files})
		m = drain(t, m, cmd)
		if rapid.Bool().Draw(t, "flip") {
			m = press(t, m, "m")
		}
		for range rapid.IntRange(0, 6).Draw(t, "jumps") {
			m = press(t, m, "}")
		}
		if m.top >= len(m.items) || m.items[m.top].kind != hunkItem {
			t.Skip("no hunk at the top")
		}
		header := spotOf(m.laidOut, m.items[m.top])
		same := func(a spot) bool {
			return a.path == header.path && a.oldStart == header.oldStart && a.newStart == header.newStart && a.section == header.section
		}
		drawn := func(m model) bool {
			_, owners := m.frame()
			for _, o := range owners {
				if o >= 0 && m.items[o].kind == hunkItem && same(spotOf(m.laidOut, m.items[o])) {
					return true
				}
			}
			return false
		}
		if !drawn(m) {
			t.Skip("the header does not fit")
		}
		for k := range rapid.IntRange(1, 3).Draw(t, "rounds") {
			m, _ = step(m, storeMsg{events: genEvents(t, files)})
			m.events = nil // each round is a log of its own
			if !drawn(m) {
				t.Fatalf("round %d: the hunk header %+v is gone:\n%s", k, header, screen(m))
			}
		}

		// A reload adds a hunk at the top of the header's file, or drops
		// another of its hunks.
		fi, hi := m.items[m.top].file, m.items[m.top].hunk
		next := append([]diff.File{}, files...)
		g := next[fi]
		g.Hunks = append([]diff.Hunk{}, g.Hunks...)
		if rapid.Bool().Draw(t, "add") {
			only := []diff.Kind{diff.Context}
			if g.Hunks[0].OldStart == 0 {
				only = []diff.Kind{diff.Added}
			} else if g.Hunks[0].NewStart == 0 {
				only = []diff.Kind{diff.Removed}
			}
			if above := genHunks(t, 1, only); len(above) > 0 {
				g.Hunks = append([]diff.Hunk{above[0]}, g.Hunks...)
			}
		} else if len(g.Hunks) > 1 {
			drop := rapid.IntRange(0, len(g.Hunks)-2).Draw(t, "drop")
			if drop >= hi {
				drop++
			}
			g.Hunks = append(g.Hunks[:drop], g.Hunks[drop+1:]...)
		}
		next[fi] = g
		m, _ = step(m, keyMsg("R"))
		m, cmd = step(m, loadedMsg{gen: m.gen, base: "b", files: next})
		m = drain(t, m, cmd)
		if !drawn(m) {
			t.Fatalf("after the reload the hunk header %+v is gone:\n%s", header, screen(m))
		}
	})
}

// The status line's counts are always whole, with a gap before them and
// only the keys hint or a space after, whatever note is shown, as long as
// the counts and the gap fit the width at all.
func TestStatusCountsProperty(t *testing.T) {
	theme := testTheme(t)
	rapid.Check(t, func(t *rapid.T) {
		files := genFiles(t)
		m := model{pal: newPalette(theme), rev: review.Fold(genEvents(t, files)), mark: -1}
		m.w = rapid.IntRange(1, 250).Draw(t, "w")
		m.opts.split = rapid.Bool().Draw(t, "split")
		if rapid.Bool().Draw(t, "noted") {
			m.note, m.warn = genPiecesText(t, "note"), rapid.Bool().Draw(t, "warn")
		}
		sum := review.Summarise(m.rev)
		var parts []string
		for _, p := range []struct {
			n    int
			what string
		}{{sum.Open, "open"}, {sum.Unsent, "to send"}, {sum.Waiting, "waiting"}} {
			if p.n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", p.n, p.what))
			}
		}
		row := m.statusRow()
		text := ""
		for _, s := range row {
			text += s.Text
		}
		if got := width(row); got != m.w {
			t.Fatalf("status line is %d cells at %d: %q", got, m.w, text)
		}
		counts := strings.Join(parts, " · ")
		if counts == "" || uniseg.StringWidth(counts)+1+statusGap > m.w {
			return
		}
		var rest string
		switch {
		case strings.HasSuffix(text, counts+"  ? keys "):
			rest = strings.TrimSuffix(text, counts+"  ? keys ")
		case strings.HasSuffix(text, counts+" "):
			rest = strings.TrimSuffix(text, counts+" ")
		default:
			t.Fatalf("the counts %q are not whole at the end of %q", counts, text)
		}
		if !strings.HasSuffix(rest, strings.Repeat(" ", statusGap)) {
			t.Fatalf("no gap before the counts in %q", text)
		}
	})
}
