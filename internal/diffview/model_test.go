package diffview

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"overview/internal/diff"
	"overview/internal/review"
)

// twoFiles is jj's output for a changed file and a renamed one.
const twoFiles = `diff --git a/a.go b/a.go
index 1111111..2222222 100644
--- a/a.go
+++ b/a.go
@@ -1,5 +1,5 @@
 one
 two
-three
+THREE
 four
 five
diff --git a/old.go b/new.go
similarity index 80%
rename from old.go
rename to new.go
index 3333333..4444444 100644
--- a/old.go
+++ b/new.go
@@ -1,3 +1,3 @@
 a
-b
+B
 c
`

func newTest(t *testing.T, diffText string) (*fixture, Config) {
	t.Helper()
	f := newFixture(t.TempDir(), diffText)
	return f, baseConfig(testTheme(t), f)
}

// onlyNote is the single note event in the log.
func onlyNote(t *testing.T, f *fixture) review.Event {
	t.Helper()
	var notes []review.Event
	for _, e := range f.events(t) {
		if e.Op == review.OpNote {
			notes = append(notes, e)
		}
	}
	if len(notes) != 1 {
		t.Fatalf("want one note event, got %+v", f.events(t))
	}
	return notes[0]
}

func TestOpening(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	m := open(t, f, cfg, 100, 30)
	if !m.loaded || m.loadErr != nil || len(m.files) != 2 {
		t.Fatalf("loaded %v, err %v, %d files", m.loaded, m.loadErr, len(m.files))
	}
	if m.base != "parentcommit00" {
		t.Errorf("base %q, want @- since no start was given", m.base)
	}
	if got := cursorSpot(m); got != (spot{path: "a.go", side: review.New, line: 1}) {
		t.Errorf("cursor at %+v, want a.go's first line", got)
	}
	if s := screen(m); !strings.Contains(s, "THREE") || !strings.Contains(s, "uncommitted") {
		t.Errorf("screen:\n%s", s)
	}
}

func TestNoRepository(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	f.repo = false
	m := open(t, f, cfg, 80, 10)
	if m.loadErr == nil || !strings.Contains(screen(m), "in no git or jj repository") {
		t.Errorf("err %v, screen:\n%s", m.loadErr, screen(m))
	}
	if len(f.vcs.calls) != 0 {
		t.Errorf("ran %q with no repository", f.vcs.calls)
	}
}

func TestWritingANote(t *testing.T) {
	cases := []struct {
		name string
		keys []string
		want review.Anchor
	}{
		{"one line", []string{"j", "c"}, review.Anchor{Path: "a.go", Side: review.New, Start: 2, End: 2, Lines: []string{"two"}}},
		{"enter starts one too", []string{"enter"}, review.Anchor{Path: "a.go", Side: review.New, Start: 1, End: 1, Lines: []string{"one"}}},
		{"a range", []string{"j", "v", "j", "j", "enter"}, review.Anchor{Path: "a.go", Side: review.New, Start: 2, End: 3, Lines: []string{"two", "THREE"}}},
		{"a range made upwards", []string{"j", "j", "j", "v", "k", "k", "c"},
			review.Anchor{Path: "a.go", Side: review.New, Start: 2, End: 3, Lines: []string{"two", "THREE"}}},
		{"a removed line", []string{"j", "j", "c"}, review.Anchor{Path: "a.go", Side: review.Old, Start: 3, End: 3, Lines: []string{"three"}}},
		{"a removed line of a renamed file", []string{"]", "j", "c"}, review.Anchor{Path: "old.go", Side: review.Old, Start: 2, End: 2, Lines: []string{"b"}}},
		{"an added line of a renamed file", []string{"]", "j", "j", "c"}, review.Anchor{Path: "new.go", Side: review.New, Start: 2, End: 2, Lines: []string{"B"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, cfg := newTest(t, twoFiles)
			m := open(t, f, cfg, 100, 30)
			m = press(t, m, c.keys...)
			if !m.comp.open {
				t.Fatalf("no editor after %q; note %q", c.keys, m.note)
			}
			m = typeText(t, m, "fix this")
			m = press(t, m, "enter")
			e := onlyNote(t, f)
			if e.Author != review.User || e.Body != "fix this" || e.ID == "" || e.At.IsZero() {
				t.Errorf("event %+v", e)
			}
			if !reflect.DeepEqual(*e.Anchor, c.want) {
				t.Errorf("anchor %+v, want %+v", *e.Anchor, c.want)
			}
			if m.comp.open || m.mark != -1 {
				t.Errorf("editor open %v, mark %d after saving", m.comp.open, m.mark)
			}
			if len(m.rev.Notes) != 1 || !strings.Contains(screen(m), "fix this") {
				t.Errorf("the note is not shown:\n%s", screen(m))
			}
		})
	}
}

func TestARangeInSplitView(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	m := open(t, f, cfg, 160, 30)
	if !m.opts.split {
		t.Fatal("a wide pane is not split")
	}
	// Rows: one, two, three|THREE, four.
	m = press(t, m, "j", "v", "j", "j", "c")
	m = typeText(t, m, "x")
	m = press(t, m, "enter")
	want := review.Anchor{Path: "a.go", Side: review.New, Start: 2, End: 4, Lines: []string{"two", "THREE", "four"}}
	if a := *onlyNote(t, f).Anchor; !reflect.DeepEqual(a, want) {
		t.Errorf("anchor %+v, want %+v", a, want)
	}
}

func TestNoNoteOffALine(t *testing.T) {
	f, cfg := newTest(t, "")
	m := open(t, f, cfg, 80, 20)
	m = press(t, m, "c")
	if m.comp.open || !m.warn {
		t.Errorf("an editor on an empty diff: open %v, note %q", m.comp.open, m.note)
	}
	if !strings.Contains(screen(m), "No changes uncommitted.") {
		t.Errorf("screen:\n%s", screen(m))
	}
}

// noted is a viewer with one note on a.go's second line, the cursor on
// the note.
func noted(t *testing.T, f *fixture, cfg Config) model {
	t.Helper()
	m := open(t, f, cfg, 100, 30)
	m = press(t, m, "j", "c")
	m = typeText(t, m, "first")
	m = press(t, m, "enter", "n")
	if m.items[m.cur].kind != noteItem {
		t.Fatalf("n did not reach the note: %+v", m.items[m.cur])
	}
	return m
}

func TestReplyEditResolve(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	m := noted(t, f, cfg)
	id := m.items[m.cur].note

	m = press(t, m, "enter")
	if m.comp.reply != id {
		t.Fatalf("enter on a note replies to it, comp %+v", m.comp)
	}
	m = typeText(t, m, "more")
	m = press(t, m, "enter")
	es := f.events(t)
	last := es[len(es)-1]
	if last.Op != review.OpReply || last.Note != id || last.Body != "more" || last.Author != review.User || last.ID == id {
		t.Fatalf("reply event %+v", last)
	}
	replyID := last.ID

	// e edits the latest unsent entry, the reply, with its text.
	m = press(t, m, "e")
	if m.comp.edit != replyID || m.comp.ed.text != "more" || m.comp.ed.pos != len("more") {
		t.Fatalf("e opened %+v", m.comp)
	}
	m = typeText(t, m, " please")
	m = press(t, m, "enter")
	es = f.events(t)
	last = es[len(es)-1]
	if last.Op != review.OpEdit || last.ID != replyID || last.Body != "more please" {
		t.Fatalf("edit event %+v", last)
	}
	n, _ := m.rev.Find(id)
	if n.Thread[1].Body != "more please" {
		t.Errorf("thread %+v", n.Thread)
	}

	// x resolves, and the note leaves the view; h shows it and x reopens.
	m = press(t, m, "x")
	es = f.events(t)
	if last = es[len(es)-1]; last.Op != review.OpResolve || last.Note != id || last.Author != review.User {
		t.Fatalf("resolve event %+v", last)
	}
	if n, _ := m.rev.Find(id); !n.Resolved {
		t.Fatal("not resolved")
	}
	for _, it := range m.items {
		if it.kind == noteItem {
			t.Fatal("a resolved note is shown")
		}
	}
	m = press(t, m, "h", "g", "n")
	if m.items[m.cur].kind != noteItem {
		t.Fatal("h did not show the resolved note")
	}
	m = press(t, m, "x")
	es = f.events(t)
	if last = es[len(es)-1]; last.Op != review.OpReopen || last.Note != id {
		t.Fatalf("reopen event %+v", last)
	}
	if n, _ := m.rev.Find(id); n.Resolved {
		t.Fatal("still resolved")
	}
}

func TestEditingASentNote(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	m := noted(t, f, cfg)
	m = press(t, m, "s") // no target: copies, and marks it sent
	before := len(f.events(t))
	m = press(t, m, "e")
	if m.comp.open {
		t.Fatal("e opened the editor on a sent note")
	}
	if !strings.Contains(m.note, "not sent can be edited") {
		t.Errorf("note %q", m.note)
	}
	if len(f.events(t)) != before {
		t.Error("e on a sent note wrote")
	}
}

func TestKeysOnANoteOffANote(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	m := open(t, f, cfg, 100, 30)
	for _, k := range []string{"e", "x"} {
		m = press(t, m, k)
		if m.comp.open || !strings.Contains(m.note, "move to a note") {
			t.Errorf("%s off a note: note %q", k, m.note)
		}
	}
	if len(f.events(t)) != 0 {
		t.Error("wrote off a note")
	}
}

func TestEscAndEmptyText(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	m := open(t, f, cfg, 100, 30)

	m = press(t, m, "c")
	m = typeText(t, m, "draft")
	m = press(t, m, "esc")
	if !m.comp.open || !m.comp.discard || !strings.Contains(screen(m), "esc again") {
		t.Fatalf("one esc on text closed the editor: %+v", m.comp)
	}
	// Any other key takes the warning back.
	m = press(t, m, "left")
	if m.comp.discard {
		t.Error("the discard warning outlived a key")
	}
	m = press(t, m, "esc", "esc")
	if m.comp.open {
		t.Fatal("two escs left the editor open")
	}

	// esc on an empty editor closes at once.
	m = press(t, m, "c", "esc")
	if m.comp.open {
		t.Fatal("esc on an empty editor asked")
	}

	// enter on nothing, or only spaces, writes nothing.
	m = press(t, m, "c", "enter")
	m = press(t, m, "c", " ", " ", "enter")
	if m.comp.open || len(f.events(t)) != 0 {
		t.Errorf("open %v, events %+v", m.comp.open, f.events(t))
	}
	if m.note != "nothing written" {
		t.Errorf("note %q", m.note)
	}
}

func TestEditorKeys(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	m := open(t, f, cfg, 100, 30)
	m = press(t, m, "c")
	m = typeText(t, m, "ab")
	m = press(t, m, "ctrl+j")
	m = typeText(t, m, "cd ef")
	m = press(t, m, "ctrl+w", "up", "end", "backspace", "home", "delete")
	if m.comp.ed.text != "\ncd " {
		t.Errorf("text %q", m.comp.ed.text)
	}
	// A pasted newline is a newline, not a save.
	m, _ = step(m, tea.KeyMsg{Type: tea.KeyEnter, Paste: true})
	if !m.comp.open || !strings.HasPrefix(m.comp.ed.text, "\n\n") {
		t.Errorf("a pasted enter: open %v, %q", m.comp.open, m.comp.ed.text)
	}
	m = press(t, m, "down", "end", "ctrl+u")
	if m.comp.ed.text != "\n\n" {
		t.Errorf("ctrl+u left %q", m.comp.ed.text)
	}
}

func pendingIDs(t *testing.T, f *fixture) []string {
	t.Helper()
	var ids []string
	for _, it := range review.Pending(review.Fold(f.events(t))) {
		for _, e := range it.Entries {
			ids = append(ids, e.ID)
		}
	}
	return ids
}

// twoPending is a viewer with a note and a reply on it, and another note,
// none sent.
func twoPending(t *testing.T, f *fixture, cfg Config) model {
	t.Helper()
	m := noted(t, f, cfg)
	m = press(t, m, "enter")
	m = typeText(t, m, "and a reply")
	m = press(t, m, "enter", "]", "j", "c")
	m = typeText(t, m, "second")
	m = press(t, m, "enter")
	if n := len(pendingIDs(t, f)); n != 3 {
		t.Fatalf("%d pending, want 3", n)
	}
	return m
}

func TestSendToAnIdleSession(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	f.panes = &fakePanes{screen: readFixture(t, "idle-empty.txt")}
	cfg.Target = "%7"
	m := twoPending(t, f, cfg)
	ids := pendingIDs(t, f)
	before := len(f.events(t))

	m = press(t, m, "s")
	if len(f.panes.pastes) != 1 {
		t.Fatalf("pasted %d times", len(f.panes.pastes))
	}
	p := f.panes.pastes[0]
	if p.pane != "%7" || f.panes.captured[0] != "%7" || p.buffer == "" {
		t.Errorf("pasted %+v after capturing %q", p, f.panes.captured)
	}
	// A reply is listed under its note's id, so every pending note's id
	// is there.
	for _, n := range m.rev.Notes {
		if !strings.Contains(p.text, "["+n.ID+"]") {
			t.Errorf("the message lacks note %s:\n%s", n.ID, p.text)
		}
	}
	es := f.events(t)
	if len(es) != before+1 {
		t.Fatalf("%d events appended", len(es)-before)
	}
	sent := es[len(es)-1]
	if sent.Op != review.OpSent || sent.Author != review.User || !reflect.DeepEqual(sent.IDs, ids) {
		t.Errorf("sent event %+v, want ids %v", sent, ids)
	}
	if got := review.Pending(m.rev); len(got) != 0 {
		t.Errorf("still pending after the store reloaded: %+v", got)
	}
	if !strings.Contains(m.note, "sent 2 notes to the session") || m.warn {
		t.Errorf("note %q warn %v", m.note, m.warn)
	}
	if len(f.copied) != 0 {
		t.Error("copied as well as sent")
	}
}

func TestSendRefusesABusyPrompt(t *testing.T) {
	for _, fx := range []string{"multiline-draft.txt", "trust-dialog.txt", "bash-mode.txt"} {
		t.Run(fx, func(t *testing.T) {
			f, cfg := newTest(t, twoFiles)
			f.panes = &fakePanes{screen: readFixture(t, fx)}
			cfg.Target = "%7"
			m := twoPending(t, f, cfg)
			before := len(f.events(t))
			m = press(t, m, "s")
			if len(f.panes.pastes) != 0 || len(f.copied) != 0 {
				t.Errorf("pasted %d, copied %d", len(f.panes.pastes), len(f.copied))
			}
			if len(f.events(t)) != before {
				t.Error("appended to the log")
			}
			if !m.warn || !strings.HasPrefix(m.note, "not sent:") {
				t.Errorf("note %q warn %v", m.note, m.warn)
			}
			if len(review.Pending(m.rev)) != 2 {
				t.Error("the notes are no longer pending")
			}
		})
	}
}

func TestSendWithNoTargetCopies(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	f.panes = &fakePanes{screen: readFixture(t, "idle-empty.txt")}
	m := twoPending(t, f, cfg)
	ids := pendingIDs(t, f)
	m = press(t, m, "s")
	if len(f.copied) != 1 || len(f.panes.pastes) != 0 || len(f.panes.captured) != 0 {
		t.Fatalf("copied %d, pasted %d", len(f.copied), len(f.panes.pastes))
	}
	if want := review.Message(review.Pending(review.Fold(f.events(t)[:len(f.events(t))-1])), cfg.Store, cfg.Exe); f.copied[0] != want {
		t.Errorf("copied:\n%s\nwant:\n%s", f.copied[0], want)
	}
	es := f.events(t)
	if last := es[len(es)-1]; last.Op != review.OpSent || !reflect.DeepEqual(last.IDs, ids) {
		t.Errorf("last event %+v", last)
	}
	if len(review.Pending(m.rev)) != 0 || !strings.HasPrefix(m.note, "copied 2 notes") {
		t.Errorf("pending %v, note %q", review.Pending(m.rev), m.note)
	}
}

func TestSendNothing(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	f.panes = &fakePanes{screen: readFixture(t, "idle-empty.txt")}
	cfg.Target = "%7"
	m := open(t, f, cfg, 100, 30)
	m = press(t, m, "s")
	if len(f.panes.pastes) != 0 || len(f.copied) != 0 || len(f.panes.captured) != 0 || m.note != "nothing to send" {
		t.Errorf("pasted %d, copied %d, note %q", len(f.panes.pastes), len(f.copied), m.note)
	}
	// Once sent, the same notes are not pending either.
	m = noted(t, f, cfg)
	m = press(t, m, "s", "s")
	if len(f.panes.pastes) != 1 || m.note != "nothing to send" {
		t.Errorf("a second s pasted again: %d, note %q", len(f.panes.pastes), m.note)
	}
}

func TestAnAgentReplyAppears(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	m := noted(t, f, cfg)
	id := m.items[m.cur].note
	m = press(t, m, "s")
	if n, _ := m.rev.Find(id); n.Waiting() {
		t.Fatal("waiting before the agent wrote")
	}
	err := f.store.Append(review.Event{Op: review.OpReply, ID: "agent1", Note: id, Author: review.Agent, Body: "done that", At: f.now})
	if err != nil {
		t.Fatal(err)
	}
	m, cmd := step(m, stampsMsg{store: statFile(f.store.Path)})
	m = drain(t, m, cmd)
	n, _ := m.rev.Find(id)
	if len(n.Thread) != 2 || n.Thread[1].Body != "done that" || !n.Waiting() {
		t.Fatalf("thread %+v, waiting %v", n.Thread, n.Waiting())
	}
	if s := screen(m); !strings.Contains(s, "done that") || !strings.Contains(s, "reply waiting") || !strings.Contains(s, "1 waiting") {
		t.Errorf("screen:\n%s", s)
	}
	// The same stamps again change nothing.
	calls := len(f.vcs.calls)
	m, cmd = step(m, stampsMsg{store: m.storeAt})
	m = drain(t, m, cmd)
	if len(f.vcs.calls) != calls || len(m.rev.Notes) != 1 {
		t.Error("an unchanged stamp reloaded")
	}
}

func TestDue(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name                  string
		changed, pending, now time.Time
		want                  bool
	}{
		{"nothing owed", time.Time{}, time.Time{}, t0, false},
		{"just changed", t0, t0, t0, false},
		{"still settling", t0, t0, t0.Add(settled - 1), false},
		{"settled", t0, t0, t0.Add(settled), true},
		{"changing, not yet stale", t0.Add(3 * time.Second), t0, t0.Add(3*time.Second + settled/2), false},
		{"stale", t0.Add(stale), t0, t0.Add(stale), true},
		{"stale long ago", t0.Add(time.Hour), t0, t0.Add(time.Hour), true},
	}
	for _, c := range cases {
		if got := due(c.changed, c.pending, c.now); got != c.want {
			t.Errorf("%s: due = %v", c.name, got)
		}
	}
}

// stampAt is a transcript that looks as it did at version v.
func stampAt(v int) stamp {
	return stamp{mod: time.Unix(int64(1000+v), 0), size: int64(v), ok: true}
}

func TestReloadsFollowTheTranscript(t *testing.T) {
	type tick struct {
		after time.Duration // since the last
		trans int           // the transcript's version, 0 for missing
	}
	cases := []struct {
		name  string
		ticks []tick
		loads int
	}{
		{"the first look only records", []tick{{0, 1}, {time.Second, 1}, {time.Minute, 1}}, 0},
		{"a missing transcript appearing is a change", []tick{{0, 0}, {pollEvery, 1}, {settled, 1}}, 0},
		{"a change that settles loads once", []tick{{0, 1}, {pollEvery, 2}, {pollEvery, 2}, {pollEvery, 2}, {pollEvery, 2}, {time.Minute, 2}}, 1},
		{"not before it settles", []tick{{0, 1}, {pollEvery, 2}, {pollEvery, 2}}, 0},
		{"a burst is one load", []tick{{0, 1}, {pollEvery, 2}, {pollEvery, 3}, {pollEvery, 4}, {settled, 4}, {settled, 4}}, 1},
		{"constant change loads once it is stale", []tick{{0, 1}, {pollEvery, 2}, {pollEvery, 3}, {pollEvery, 4}, {pollEvery, 5},
			{pollEvery, 6}, {pollEvery, 7}, {pollEvery, 8}, {pollEvery, 9}, {pollEvery, 10}}, 1},
		{"and again after another stale wait", []tick{{0, 1}, {pollEvery, 2}, {stale, 3}, {pollEvery, 4}, {pollEvery, 5},
			{pollEvery, 6}, {pollEvery, 7}, {pollEvery, 8}, {pollEvery, 9}, {pollEvery, 10}, {pollEvery, 11}, {pollEvery, 12}}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, cfg := newTest(t, twoFiles)
			cfg.Transcript = "/fake/transcript.jsonl"
			m := open(t, f, cfg, 100, 30)
			start := f.vcs.count("diff")
			for _, tk := range c.ticks {
				f.now = f.now.Add(tk.after)
				tr := stamp{}
				if tk.trans > 0 {
					tr = stampAt(tk.trans)
				}
				var cmd tea.Cmd
				m, cmd = step(m, stampsMsg{store: m.storeAt, trans: tr})
				m = drain(t, m, cmd)
			}
			if got := f.vcs.count("diff") - start; got != c.loads {
				t.Errorf("%d loads, want %d", got, c.loads)
			}
		})
	}
}

func TestStaleResultsAreDropped(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	m := open(t, f, cfg, 100, 30)
	old := m.gen
	m, cmd := step(m, keyMsg("R"))
	if !m.loading || m.gen != old+1 || cmd == nil {
		t.Fatalf("R: loading %v gen %d", m.loading, m.gen)
	}
	m, _ = step(m, loadedMsg{gen: old, files: nil, base: "elsewhere"})
	if len(m.files) != 2 || m.base == "elsewhere" || !m.loading {
		t.Fatalf("an older load's result was taken: %d files, base %q", len(m.files), m.base)
	}

	// R while loading asks for another load once this one lands.
	m, again := step(m, keyMsg("R"))
	if !m.again || m.gen != old+1 {
		t.Fatalf("again %v gen %d", m.again, m.gen)
	}
	if again != nil {
		if msg := runCmd(t, again); msg != nil {
			if _, ok := msg.(loadedMsg); ok {
				t.Fatal("a second load ran at once")
			}
		}
	}
	before := f.vcs.count("diff")
	f.vcs.diff = twoFiles[:strings.Index(twoFiles, "diff --git a/old.go")]
	m = drain(t, m, cmd)
	if got := f.vcs.count("diff") - before; got != 2 {
		t.Errorf("%d loads ran, want the first and one more", got)
	}
	if m.loading || m.again || m.gen != old+2 || len(m.files) != 1 {
		t.Errorf("loading %v again %v gen %d files %d", m.loading, m.again, m.gen, len(m.files))
	}
}

func TestALoadErrorKeepsTheDiff(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	m := open(t, f, cfg, 100, 30)
	f.vcs.fail = errors.New("jj: \x1b[31mboom\x1b[0m\nmore")
	m = press(t, m, "R")
	if m.loadErr == nil || len(m.files) != 2 {
		t.Fatalf("err %v, %d files", m.loadErr, len(m.files))
	}
	f2, cfg2 := newTest(t, twoFiles)
	f2.vcs.fail = f.vcs.fail
	m2 := open(t, f2, cfg2, 40, 8)
	s := screen(m2)
	if !strings.Contains(s, "␛[31mboom") || strings.ContainsRune(s, 0x1b) {
		t.Errorf("screen:\n%q", s)
	}
}

func TestScope(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	cfg.Started = time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC)
	m := open(t, f, cfg, 100, 30)
	if m.scope != sinceStart || m.base != "startcommit0000" {
		t.Fatalf("scope %v base %q", m.scope, m.base)
	}
	m = press(t, m, "b")
	if m.scope != uncommitted || m.base != "parentcommit00" {
		t.Errorf("after b: scope %v base %q", m.scope, m.base)
	}
	m = press(t, m, "b")
	if m.scope != sinceStart || m.base != "startcommit0000" {
		t.Errorf("after b b: scope %v base %q", m.scope, m.base)
	}

	f, cfg = newTest(t, twoFiles)
	m = open(t, f, cfg, 100, 30)
	loads := f.vcs.count("diff")
	m = press(t, m, "b")
	if m.scope != uncommitted || f.vcs.count("diff") != loads || !strings.Contains(m.note, "no session start") {
		t.Errorf("b with no start: scope %v, note %q", m.scope, m.note)
	}
}

func TestResize(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	f.panes = &fakePanes{}
	cfg.Self = "%3"
	m := open(t, f, cfg, 100, 30)
	m = press(t, m, "z", "z", "z", "z")
	want := []string{"resize %3 80%", "zoom %3", "zoom %3", "resize %3 45%", "resize %3 80%"}
	if !slices.Equal(f.panes.calls, want) {
		t.Errorf("calls %q, want %q", f.panes.calls, want)
	}

	for _, c := range []struct {
		name  string
		panes *fakePanes
		self  string
	}{{"no tmux", nil, "%3"}, {"no pane of its own", &fakePanes{}, ""}} {
		f, cfg := newTest(t, twoFiles)
		f.panes = c.panes
		cfg.Self = c.self
		m := open(t, f, cfg, 100, 30)
		m = press(t, m, "z")
		if !strings.Contains(m.note, "not in a pane") || m.size != 0 {
			t.Errorf("%s: note %q size %d", c.name, m.note, m.size)
		}
		if c.panes != nil && len(c.panes.calls) != 0 {
			t.Errorf("%s: calls %q", c.name, c.panes.calls)
		}
	}
}

func TestSplitOrUnified(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	m := open(t, f, cfg, 100, 30)
	if m.opts.split {
		t.Fatal("a narrow pane is split")
	}
	m, _ = step(m, tea.WindowSizeMsg{Width: 200, Height: 30})
	if !m.opts.split {
		t.Fatal("a wide pane is not split")
	}
	m = press(t, m, "m")
	m, _ = step(m, tea.WindowSizeMsg{Width: 220, Height: 30})
	if m.opts.split {
		t.Error("width overrode the user's choice")
	}
}

func TestNavigation(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	m := open(t, f, cfg, 100, 30)
	at := func(m model) spot { return cursorSpot(m) }
	if s := at(press(t, m, "G")); s != (spot{path: "new.go", side: review.New, line: 3}) {
		t.Errorf("G: %+v", s)
	}
	if s := at(press(t, m, "]")); s != (spot{path: "new.go", side: review.New, line: 1}) {
		t.Errorf("]: %+v", s)
	}
	if s := at(press(t, m, "G", "[")); s != (spot{path: "new.go", side: review.New, line: 1}) {
		t.Errorf("G [: %+v, want the start of its own file", s)
	}
	if s := at(press(t, m, "G", "[", "[")); s != (spot{path: "a.go", side: review.New, line: 1}) {
		t.Errorf("G [ [: %+v", s)
	}
	if s := at(press(t, m, "k", "k")); s != (spot{path: "a.go", side: review.New, line: 1}) {
		t.Errorf("k at the top moved: %+v", s)
	}
	if m2 := press(t, m, "N"); m2.note != "no more notes that way" {
		t.Errorf("N with no notes: %q", m2.note)
	}
	if m2 := press(t, m, "?"); !m2.help || !strings.Contains(screen(m2), "next and previous hunk") || !strings.Contains(screen(m2), "close this help") {
		t.Error("? shows no help")
	} else if m3 := press(t, m2, "j"); m3.help || cursorSpot(m3) != cursorSpot(m) {
		t.Error("a key in the help did more than close it")
	}
}

func TestTheCursorStaysOnItsLine(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	m := open(t, f, cfg, 100, 30)
	m = press(t, m, "j", "j", "j", "j") // a.go new line 4
	want := cursorSpot(m)
	if want != (spot{path: "a.go", side: review.New, line: 4}) {
		t.Fatalf("at %+v", want)
	}
	// A file before it, and a removed line above it, so the new side's
	// numbers stay as they were.
	f.vcs.diff = `diff --git a/0.txt b/0.txt
new file mode 100644
index 0000000..5555555
--- /dev/null
+++ b/0.txt
@@ -0,0 +1,2 @@
+x
+y
` + strings.Replace(twoFiles, "@@ -1,5 +1,5 @@\n one\n", "@@ -1,6 +1,5 @@\n-zero\n one\n", 1)
	m = press(t, m, "R")
	if len(m.files) != 3 {
		t.Fatalf("%d files after the reload", len(m.files))
	}
	if got := cursorSpot(m); got != want {
		t.Errorf("cursor at %+v after a reload, want %+v", got, want)
	}
}

func TestWheelAndClick(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	m := open(t, f, cfg, 100, 6)
	m, _ = step(m, tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if m.top == 0 && m.off == 0 {
		t.Error("the wheel did not scroll")
	}
	_, owners := m.frame()
	if !slices.Contains(owners, m.cur) {
		t.Error("the cursor left the screen")
	}
	for y, o := range owners {
		if o >= 0 && m.items[o].selectable() && o != m.cur {
			m, _ = step(m, tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, Y: y})
			if m.cur != o {
				t.Errorf("a click on row %d put the cursor on %d, want %d", y, m.cur, o)
			}
			break
		}
	}
}

func TestHighlightArrives(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	cfg.Profile = termenv.TrueColor
	m := open(t, f, cfg, 100, 30)
	if cfg.Theme.Syntax == "" {
		t.Skip("the theme has no syntax style")
	}
	if _, ok := m.lit[0]; !ok {
		t.Error("a.go on screen was not highlighted")
	}
	// A highlight from an older load is dropped.
	m, _ = step(m, litMsg{gen: m.gen - 1, file: 7, lit: lit{}})
	if _, ok := m.lit[7]; ok {
		t.Error("a stale highlight was taken")
	}
	if !strings.Contains(m.View(), "\x1b[") {
		t.Error("no colour in a true-colour frame")
	}
}

// checkView fails unless the frame, as the terminal measures it, is
// exactly the pane.
func checkView(t *testing.T, m model) {
	t.Helper()
	lines := strings.Split(m.View(), "\n")
	if len(lines) != m.h {
		t.Fatalf("%d lines in a pane %d high", len(lines), m.h)
	}
	for i, l := range lines {
		plain := ansi.Strip(l)
		if w := ansi.StringWidth(plain); w != m.w {
			t.Errorf("line %d is %d cells in a pane %d wide: %q", i, w, m.w, plain)
		}
		if strings.ContainsFunc(plain, unicode.IsControl) {
			t.Errorf("line %d holds a control: %q", i, plain)
		}
	}
}

// Regression: a pane shorter than the header, a line and the status bar,
// or than the open editor, got a frame taller than itself.
func TestATinyPaneKeepsItsHeight(t *testing.T) {
	for _, h := range []int{1, 2, 3, 4} {
		f, cfg := newTest(t, twoFiles)
		m := open(t, f, cfg, 30, h)
		checkView(t, m)
		m = press(t, m, "c")
		m = typeText(t, m, "a long note that wraps over the editor's rows")
		checkView(t, m)
		if !strings.Contains(m.View(), "? keys") {
			t.Errorf("h %d: the status bar went: %q", h, m.View())
		}
	}
}

// Regression: an error's text reached the status bar uncleaned, so an
// escape or a newline in it went to the terminal.
func TestTheStatusBarCleansItsNote(t *testing.T) {
	f, cfg := newTest(t, twoFiles)
	m := open(t, f, cfg, 80, 10)
	m, _ = step(m, doneMsg{err: errors.New("tmux: \x1b[2Jgone\nfor good")})
	checkView(t, m)
	if !strings.Contains(m.View(), "␛[2Jgone␊for good") {
		t.Errorf("view:\n%s", m.View())
	}
	cfg.Title = "evil\x1b]0;x\x07"
	f.panes, cfg.Target = &fakePanes{screen: readFixture(t, "idle-empty.txt")}, "%1"
	m = noted(t, f, cfg)
	m = press(t, m, "s")
	checkView(t, m)
}

// Regression: a style change inside a grapheme cluster, from changed
// words or syntax tokens, split it into two segments that were measured
// as two clusters, so the row was narrower on screen than the pane. The
// editor's cursor did the same inside a cluster.
func TestAClusterIsNeverSplit(t *testing.T) {
	f, cfg := newTest(t, `diff --git a/f.txt b/f.txt
index 1111111..2222222 100644
--- a/f.txt
+++ b/f.txt
@@ -1,1 +1,1 @@
-x 👨‍👧 y
+x 👨‍👩 y
`)
	cfg.Profile = termenv.TrueColor
	m := open(t, f, cfg, 40, 8)
	checkView(t, m)
	m = press(t, m, "c")
	m = typeText(t, m, "👨‍👩‍👧 ☺️")
	for range 8 {
		m = press(t, m, "left")
		checkView(t, m)
	}
}

// Lines added above the cursor in its own file move its line down; the
// cursor goes with the text, not the number.
func TestTheCursorFollowsAMovedLine(t *testing.T) {
	before := []diff.File{{OldPath: "a.go", NewPath: "a.go", Hunks: []diff.Hunk{{OldStart: 1, OldCount: 2, NewStart: 1, NewCount: 2,
		Lines: []diff.Line{{Kind: diff.Context, Old: 1, New: 1, Text: "one"}, {Kind: diff.Context, Old: 2, New: 2, Text: "two"}}}}}}
	after := []diff.File{{OldPath: "a.go", NewPath: "a.go", Hunks: []diff.Hunk{{OldStart: 1, OldCount: 2, NewStart: 1, NewCount: 4,
		Lines: []diff.Line{{Kind: diff.Added, New: 1, Text: "new"}, {Kind: diff.Added, New: 2, Text: "lines"},
			{Kind: diff.Context, Old: 1, New: 3, Text: "one"}, {Kind: diff.Context, Old: 2, New: 4, Text: "two"}}}}}}
	items := layout(before, review.Review{}, nil, layoutOpts{})
	var at spot
	for _, it := range items {
		if s := spotOf(before, it); s.text == "two" {
			at = s
		}
	}
	items = layout(after, review.Review{}, nil, layoutOpts{})
	i, ok := find(items, after, at)
	if got := spotOf(after, items[i]); !ok || got.text != "two" || got.line != 4 {
		t.Fatalf("found %+v", got)
	}
}
