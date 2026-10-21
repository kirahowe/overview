package diffview

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
