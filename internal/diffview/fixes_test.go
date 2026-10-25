package diffview

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rivo/uniseg"
)

// s pressed twice before the first send lands sends once: the notes are
// not sent until the log says so, and the second s must not paste them
// again.
func TestSendTwiceSendsOnce(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	f.panes = &fakePanes{screen: readFixture(t, "idle-empty.txt")}
	cfg.Target = "%7"
	m := twoPending(t, f, cfg)
	m, first := step(m, keyMsg("s"))
	m, second := step(m, keyMsg("s"))
	if second != nil {
		t.Fatal("the second s started another send")
	}
	m = drain(t, m, first)
	if len(f.panes.pastes) != 1 || m.sending {
		t.Fatalf("pasted %d times, sending %v", len(f.panes.pastes), m.sending)
	}
}

// s while a note is still being written waits for it, rather than
// sending without it.
func TestSendWaitsForASave(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	f.panes = &fakePanes{screen: readFixture(t, "idle-empty.txt")}
	cfg.Target = "%7"
	m := open(t, f, cfg, 100, 30)
	m = press(t, m, "c")
	m = typeText(t, m, "late note")
	m, save := step(m, keyMsg("enter"))
	m, send := step(m, keyMsg("s"))
	if send != nil || !strings.Contains(m.note, "still saving") {
		t.Fatalf("sent while saving: note %q", m.note)
	}
	m = drain(t, m, save)
	m = press(t, m, "s")
	if len(f.panes.pastes) != 1 || !strings.Contains(f.panes.pastes[0].text, "late note") {
		t.Fatalf("pastes %+v", f.panes.pastes)
	}
}

// A pane whose foreground program is a shell has lost its Claude Code,
// whatever the screen still shows.
func TestSendRefusesAShell(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	f.panes = &fakePanes{command: "-zsh", screen: readFixture(t, "idle-empty.txt")}
	cfg.Target = "%7"
	m := twoPending(t, f, cfg)
	m = press(t, m, "s")
	if len(f.panes.pastes) != 0 || !m.warn || !strings.Contains(m.note, "runs -zsh") {
		t.Fatalf("pastes %d note %q", len(f.panes.pastes), m.note)
	}
}

// A read of the log that began before a write and lands after it is
// stale, and must not bring back what the write changed.
func TestStaleStoreReadIsDropped(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	m := twoPending(t, f, cfg)
	old := m.events[:len(m.events)-1]
	m, _ = step(m, storeMsg{events: old})
	if len(m.events) == len(old) {
		t.Fatal("a shorter, older read replaced the log")
	}
}

// ctrl+c in the editor asks before throwing a note away, as esc does.
func TestCtrlCInTheEditorAsksFirst(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	m := open(t, f, cfg, 100, 30)
	m = press(t, m, "c")
	m = typeText(t, m, "keep me")
	m, cmd := step(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil || !m.comp.open || !m.comp.discard {
		t.Fatalf("ctrl+c quit or discarded: open %v discard %v", m.comp.open, m.comp.discard)
	}
}

// A line of megabytes is drawn up to maxDrawn bytes, and says how much is
// left out.
func TestALongLineIsCut(t *testing.T) {
	long := strings.Repeat("x", 3*maxDrawn)
	src := "diff --git a/min.js b/min.js\nindex 1..2 100644\n--- a/min.js\n+++ b/min.js\n@@ -1,1 +1,1 @@\n-a\n+" + long + "\n"
	f, cfg := newTest(t, src)
	m := open(t, f, cfg, 100, 30)
	marked := false
	for i := range m.items {
		if m.items[i].kind != rowItem {
			continue
		}
		rows := m.draw(i)
		if len(rows) > maxDrawn/50+2 {
			t.Fatalf("item %d draws %d rows", i, len(rows))
		}
		for _, r := range rows {
			for _, s := range r {
				marked = marked || strings.Contains(s.Text, "more bytes")
			}
		}
	}
	if !marked {
		t.Fatal("no marker for the bytes left out")
	}
}

// The session's start is looked up once: the operation log it comes from
// grows while the session works.
func TestTheStartIsFoundOnce(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	cfg.Started = time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	m := open(t, f, cfg, 100, 30)
	if m.scope != sinceStart {
		t.Fatal("not since the start")
	}
	m = press(t, m, "R")
	m = press(t, m, "R")
	if n := f.vcs.count("op", "log"); n != 1 {
		t.Fatalf("op log read %d times", n)
	}
	if n := f.vcs.count("diff"); n != 3 {
		t.Fatalf("diff read %d times", n)
	}
}

// The cursor's mark takes one cell in split view as in unified, so the
// code of the line under the cursor stays where the other lines' is.
func TestTheCursorDoesNotShiftItsLine(t *testing.T) {
	for _, split := range []bool{true, false} {
		f, cfg := newTest(t, twoFiles)
		m := open(t, f, cfg, 160, 30)
		if m.opts.split != split {
			m = press(t, m, "m")
		}
		i := m.cur
		on := m.draw(i)[0]
		m = press(t, m, "j")
		off := m.draw(i)[0]
		text := func(r []Seg) string {
			var b strings.Builder
			for _, s := range r[1:] { // after the mark's cell
				b.WriteString(s.Text)
			}
			return b.String()
		}
		if text(on) != text(off) {
			t.Fatalf("split %v: the line moved under the cursor:\n%q\n%q", split, text(on), text(off))
		}
	}
}

// } and ] put the header they jump to at the top of the screen, with the
// cursor on its first line.
func TestJumpsPutTheHeaderOnTop(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	m := open(t, f, cfg, 100, 8)
	for _, k := range []string{"}", "]"} {
		m = press(t, m, k)
		if top := m.items[m.top].kind; top != hunkItem && top != fileItem {
			t.Fatalf("%s: the screen starts at a %v", k, top)
		}
		if m.cur != nextSelectable(m.items, m.top, 1) {
			t.Fatalf("%s: cursor %d, top %d", k, m.cur, m.top)
		}
	}
}

// Neighbours in one style are drawn as one run, so a joiner that ends one
// fuses its emoji with the next one's: the pair takes two cells, not four,
// and the row is filled to the width around it. An empty segment between
// them in another style draws nothing and does not keep them apart.
func TestAJoinerAcrossSegmentsIsMeasuredJoined(t *testing.T) {
	bold := Style{Fg: "2", Bold: true}
	segs := []Seg{{"😀‍", bold}, {"", Style{Fg: "1"}}, {"😀a", bold}}
	rows := wrap(segs, 4, fill)
	if len(rows) != 1 || width(rows[0]) != 3 {
		t.Fatalf("rows %+v, want one row of 3 cells", rows)
	}
}

const withNewFile = twoFiles + `diff --git a/NOTES.md b/NOTES.md
new file mode 100644
index 0000000..5555555
--- /dev/null
+++ b/NOTES.md
@@ -0,0 +1,1 @@
+the only line
`

// A note saved on the first line of a new file keeps the file's hunk
// header on screen: the header at the top of the screen is found again
// as itself after the notes change, not as the nearest line.
func TestANoteKeepsTheHunkHeader(t *testing.T) {
	for _, w := range []int{120, 160} {
		f, cfg := newTest(t, withNewFile)
		m := open(t, f, cfg, w, 30)
		m = press(t, m, "]", "]")
		if p := cursorSpot(m); p.path != "NOTES.md" || p.line != 1 {
			t.Fatalf("cursor at %+v", p)
		}
		before := screen(m)
		m = press(t, m, "c")
		m = typeText(t, m, "a note")
		m = press(t, m, "enter")
		after := screen(m)
		if !strings.Contains(before, "@@ -0,0 +1,1 @@") || !strings.Contains(after, "@@ -0,0 +1,1 @@") {
			t.Fatalf("w %d: the hunk header went:\nbefore\n%s\nafter\n%s", w, before, after)
		}
	}
}

// rowText is a row's text as drawn.
func rowText(r []Seg) string {
	var b strings.Builder
	for _, s := range r {
		b.WriteString(s.Text)
	}
	return b.String()
}

// A long note in the status line is cut before the counts, with room
// between them, and the counts stay whole.
func TestALongNoteLeavesTheCountsAlone(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	m := open(t, f, cfg, 120, 30)
	m = press(t, m, "c")
	m = typeText(t, m, "a note")
	m = press(t, m, "enter")
	m = m.complain(errors.New("not sent: the session is not at its prompt: " + strings.Repeat("it is busy with something else ", 4)))
	row := rowText(m.statusRow())
	if !strings.Contains(row, "…  1 open · 1 to send  ? keys ") || uniseg.StringWidth(row) != 120 {
		t.Fatalf("status line %q", row)
	}
	// Too narrow for the keys hint, the counts keep their room.
	m.w = 25
	if row := rowText(m.statusRow()); !strings.HasSuffix(row, "…  1 open · 1 to send ") {
		t.Fatalf("narrow status line %q", row)
	}
}

// The editor wraps a note between words, with no space opening a row, and
// the cursor stays on the grapheme it is on.
func TestTheEditorWrapsAtWords(t *testing.T) {
	e := editor{}.insert("we wrap across words")
	rows, cur := e.rows(12, Style{})
	var got []string
	for _, r := range rows {
		got = append(got, strings.TrimRight(rowText(r), " "))
	}
	if strings.Join(got, "|") != "we wrap|across words|" || cur != 2 {
		t.Fatalf("rows %q, cursor on %d", got, cur)
	}
	// On the a of across, the cursor is on the second row.
	e.pos = strings.Index(e.text, "across")
	rows, cur = e.rows(12, Style{})
	if cur != 1 || len(rows) != 2 {
		t.Fatalf("cursor on row %d of %d", cur, len(rows))
	}
	for _, s := range rows[1] {
		if s.St.Reverse && s.Text != "a" {
			t.Fatalf("cursor drawn on %q", s.Text)
		}
	}
	// On the space the row breaks at, it is drawn where the space fits,
	// at the end of the first row.
	e.pos = strings.Index(e.text, " across")
	rows, cur = e.rows(12, Style{})
	if cur != 0 || !rows[0][1].St.Reverse || rows[0][1].Text != " " {
		t.Fatalf("cursor on row %d: %+v", cur, rows)
	}
	// On a space that does not fit the row, it opens the next one.
	e = editor{}.insert("we wrap more  words")
	e.pos = strings.Index(e.text, "  ") + 1
	rows, cur = e.rows(12, Style{})
	if cur != 1 || !rows[1][0].St.Reverse || rows[1][0].Text != " " {
		t.Fatalf("cursor on row %d: %+v", cur, rows)
	}
}

// A saved note wraps at words, and its continuation rows start at the
// text, not a space.
func TestASavedNoteWrapsAtWords(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	m := open(t, f, cfg, 40, 30)
	m = press(t, m, "c")
	const text = "the editor should wrap across words when the note is long"
	m = typeText(t, m, text)
	m = press(t, m, "enter")
	var body []string
	for _, line := range strings.Split(screen(m), "\n") {
		if i := strings.Index(line, "│ "); i >= 0 && !strings.Contains(line, "╭") {
			body = append(body, strings.TrimRight(line[i+len("│ "):], " "))
		}
	}
	if len(body) < 2 || strings.Join(body, " ") != text {
		t.Fatalf("note body %q", body)
	}
	for _, b := range body {
		if strings.HasPrefix(b, " ") {
			t.Fatalf("a row starts with a space: %q", body)
		}
	}
}
