package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"overview/internal/fleet"
)

func row(id, name, title string, pid int) fleet.Row {
	return fleet.Row{
		Live:  fleet.Live{SessionID: id, Name: name, PID: pid, Cwd: "/w/" + name, Kind: "interactive"},
		State: fleet.Idle, Title: title,
	}
}

// newTestModel returns a loaded model over rows, with ttys ttys<pid>, and
// no tmux.
func newTestModel(t *testing.T, rows ...fleet.Row) model {
	t.Helper()
	m := newModel(fleet.Home{Dir: t.TempDir()}, "")
	m.nav.inside = true // these tests drive the list; nav_test.go drives the panels
	m.deps = fakeDeps(nil)
	ttys := map[int]string{}
	for _, r := range rows {
		if r.PID > 0 && !r.Background() {
			ttys[r.PID] = "ttys" + string(rune('0'+r.PID%10))
		}
	}
	m = update(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	return update(m, rowsMsg{rows: rows, ttys: ttys})
}

// caseHome is a config directory of its own under root, for one case of a
// property test, which has no TempDir of its own.
func caseHome(t tester, root string) fleet.Home {
	dir, err := os.MkdirTemp(root, "case")
	if err != nil {
		t.Fatal(err)
	}
	return fleet.Home{Dir: dir}
}

func update(m model, msg tea.Msg) model {
	next, _ := m.Update(msg)
	return next.(model)
}

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// press sends keys in order and returns the model and the last Cmd.
func press(m model, keys ...string) (model, tea.Cmd) {
	var cmd tea.Cmd
	for _, k := range keys {
		var next tea.Model
		next, cmd = m.Update(keyMsg(k))
		m = next.(model)
	}
	return m, cmd
}

func typed(s string) []string {
	var keys []string
	for _, r := range s {
		keys = append(keys, string(r))
	}
	return keys
}

func TestFilterEnterWithoutMatchDoesNothing(t *testing.T) {
	m := newTestModel(t, row("s1", "red", "auth", 11))
	m, _ = press(m, "/", "z", "z")
	m, cmd := press(m, "enter")
	if cmd != nil || m.filtering || m.filter != "zz" {
		t.Fatalf("cmd=%v filtering=%v filter=%q", cmd != nil, m.filtering, m.filter)
	}
}

func TestArrowsMoveWhileFiltering(t *testing.T) {
	m := newTestModel(t, row("s1", "red", "work", 11), row("s2", "blue", "work", 12))
	m, _ = press(m, "/", "w")
	first := m.sel
	m, _ = press(m, "down")
	if m.sel == first || !m.filtering {
		t.Fatalf("down should move within the matches: %q", m.sel)
	}
	m, _ = press(m, "up")
	if m.sel != first {
		t.Fatalf("up should return: %q", m.sel)
	}
}

func TestNeedsYouIsPinnedAndNJumpsThere(t *testing.T) {
	asking := row("s3", "gold", "q", 13)
	asking.LastReply = "Shall I go on?"
	m := newTestModel(t, row("s1", "red", "a", 11), asking)
	if m.sel != "s3" {
		t.Fatalf("cursor should start on what needs you, got %q", m.sel)
	}
	m, _ = press(m, "j", "n")
	if m.sel != "s3" {
		t.Fatalf("n should return to the waiting session, got %q", m.sel)
	}
}

func TestUnhandledKeyWhileFilteringKeepsTheCursor(t *testing.T) {
	m := newTestModel(t, row("s1", "red", "work", 11), row("s2", "blue", "work", 12))
	m, _ = press(m, "/", "w", "down")
	moved := m.sel
	for _, k := range []tea.KeyMsg{{Type: tea.KeyLeft}, {Type: tea.KeyRight}, {Type: tea.KeyCtrlA}} {
		m = update(m, k)
		if m.sel != moved {
			t.Fatalf("%v moved the cursor from %q to %q", k, moved, m.sel)
		}
	}
	m, _ = press(m, "backspace") // the text changed: back to the first match
	if first := m.ordered()[0].key(); m.sel != first {
		t.Fatalf("new filter text should select the first match %q, got %q", first, m.sel)
	}
}

// A filter left in place by an enter with no match, then a refresh that
// brings rows the filter hides: esc must put the cursor on a shown row.
func TestEscOutsideTheFilterSelectsAShownRow(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(m, "/", "z", "z", "enter")
	m = update(m, rowsMsg{rows: []fleet.Row{row("s1", "red", "work", 11)}, ttys: map[int]string{11: "ttys1"}})
	m, _ = press(m, "esc")
	if m.filter != "" || m.sel != "s1" {
		t.Fatalf("filter %q sel %q", m.filter, m.sel)
	}
}

// A tab typed into the search box once overflowed the narrowest layout.
func TestSearchWithTabStaysWithinWidth(t *testing.T) {
	m := newTestModel(t, row("s1", "a", "auth", 11))
	m = update(m, tea.WindowSizeMsg{Width: 40, Height: 10})
	m, _ = press(m, "/")
	m, _ = press(m, typed("auth\tauth")...)
	if strings.ContainsRune(m.filter, '\t') {
		t.Fatalf("filter %q holds a tab", m.filter)
	}
	for i, line := range strings.Split(m.View(), "\n") {
		if w := ansi.StringWidth(line); w > 40 {
			t.Errorf("line %d is %d cells: %q", i, w, line)
		}
	}
}

func mouseClick(m model, x, y int) (model, tea.Cmd) {
	next, cmd := m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	return next.(model), cmd
}

func wheel(m model, x, y int, down bool) model {
	b := tea.MouseButtonWheelUp
	if down {
		b = tea.MouseButtonWheelDown
	}
	return update(m, tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: b})
}

// screenY is the screen line of a session's first list line, or -1.
func screenY(m model, id string) int {
	off := 0
	if m.cockpit.open {
		off = 1 // the cockpit draws the list's rule
	}
	for i, o := range m.shownColumn().owners {
		if o == id {
			return i + off
		}
	}
	return -1
}

func manyRows(n int) []fleet.Row {
	var rows []fleet.Row
	for i := 0; i < n; i++ {
		rows = append(rows, row(fmt.Sprintf("s%d", i), fmt.Sprintf("n%d", i), "work", 10+i))
	}
	return rows
}

func TestSlowSecondClickOnlySelects(t *testing.T) {
	m := newTestModel(t, row("s1", "red", "auth", 11))
	m, _ = mouseClick(m, 5, screenY(m, "s1"))
	m.clickAt = m.clickAt.Add(-time.Second)
	if _, cmd := mouseClick(m, 5, screenY(m, "s1")); cmd != nil {
		t.Fatal("two slow clicks acted")
	}
}

func TestClickOffARowDoesNothing(t *testing.T) {
	m := newTestModel(t, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	before := m.sel
	// A project's rule, the blank lines above the agents panel, its
	// rule, and the footer. The rule along the top is tmux's.
	for _, y := range []int{0, 3, 5, 20, 26, 29} {
		m, _ = mouseClick(m, 5, y)
		if m.sel != before {
			t.Fatalf("click at line %d moved the cursor to %q", y, m.sel)
		}
	}
}

// Clicking a row above the cursor once scrolled it to the bottom of the
// pane, out from under the pointer.
func TestClickDoesNotScrollTheList(t *testing.T) {
	m := newTestModel(t, manyRows(8)...)
	m = update(m, tea.WindowSizeMsg{Width: 120, Height: 12})
	m, _ = press(m, "G")
	for _, e := range m.ordered() {
		r := e.row
		y := screenY(m, r.SessionID)
		if y < 1 || y+1 > 9 {
			continue // not wholly shown
		}
		m, _ = mouseClick(m, 5, y)
		if m.sel != r.SessionID || screenY(m, r.SessionID) != y {
			t.Fatalf("clicked %s at line %d: sel %q, now at line %d", r.SessionID, y, m.sel, screenY(m, r.SessionID))
		}
		m.clickID = ""
	}
}

func TestWheel(t *testing.T) {
	m := newTestModel(t, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	first := m.sel
	m = wheel(m, 5, 5, true)
	if m.sel == first {
		t.Fatal("wheel down over the list did not move the cursor")
	}
	if m = wheel(m, 5, 5, false); m.sel != first {
		t.Fatal("wheel up did not move back")
	}
}

func TestClickClosesOverlays(t *testing.T) {
	m := newTestModel(t, row("s1", "red", "auth", 11))
	m, _ = press(m, "?")
	if m = wheel(m, 5, 5, true); !m.help {
		t.Fatal("wheel closed help")
	}
	if m, _ = mouseClick(m, 5, 5); m.help {
		t.Fatal("click left help open")
	}
	bg := fleet.Row{Live: fleet.Live{SessionID: "b1", ID: "b1", Name: "bg", Kind: "background"}, State: fleet.Idle}
	m = newTestModel(t, bg)
	m, _ = press(m, "x")
	if m.confirm == "" {
		t.Fatal("x did not ask")
	}
	if m, cmd := mouseClick(m, 5, screenY(m, "b1")); m.confirm != "" || cmd != nil {
		t.Fatal("click did not cancel the stop")
	}
}

func TestCopyRunsAsACommand(t *testing.T) {
	var got string
	m := newTestModel(t, row("s1", "red", "auth", 11))
	m.deps.copyText = func(s string) error { got = s; return nil }
	m, cmd := press(m, "y")
	if got != "" || cmd == nil {
		t.Fatal("y copied inside Update")
	}
	if m = update(m, cmd()); got != "s1" || m.note != "copied s1" {
		t.Fatalf("copied %q, note %q", got, m.note)
	}
}

func TestFilterEnterFocusesTheMatchAndClearsTheFilter(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth work", 11), row("s2", "blue", "billing work", 12))
	m = pressSettled(t, m, "/")
	m = pressSettled(t, m, typed("bill")...)
	if m.sel != "s2" || !m.filtering {
		t.Fatalf("cursor should sit on the first match: sel=%q filtering=%v", m.sel, m.filtering)
	}
	if want := paneWithTTY(f, "ttys012"); !f.staged(want) {
		t.Fatalf("the list is in %s, not with the match's %s", f.stage(), want)
	}
	m = pressSettled(t, m, "enter")
	if m.filter != "" || m.filtering || m.sel != "s2" {
		t.Fatalf("filter %q filtering %v sel %q", m.filter, m.filtering, m.sel)
	}
	if want := paneWithTTY(f, "ttys012"); len(f.selects) != 1 || f.selects[0] != want {
		t.Fatalf("selects %v, want [%s]", f.selects, want)
	}
	if got := len(ids(m.sections())); got != 2 {
		t.Fatalf("full list should be back, got %d rows", got)
	}
}

func TestClickSelectsAndDoubleClickFocuses(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	y := screenY(m, "s2")
	m, cmd := mouseClick(m, 5, y)
	m = settle(t, m, cmd)
	if m.sel != "s2" || len(f.selects) != 0 {
		t.Fatalf("sel %q selects %v", m.sel, f.selects)
	}
	m, cmd = mouseClick(m, 5, y)
	settle(t, m, cmd)
	if want := paneWithTTY(f, "ttys012"); len(f.selects) != 1 || f.selects[0] != want {
		t.Fatalf("double click should focus s2's pane: selects %v", f.selects)
	}
}

func TestEnterOutsideTmuxOnlySaysSo(t *testing.T) {
	m := newTestModel(t, row("s1", "red", "auth", 11))
	m, cmd := press(m, "enter")
	if cmd != nil || !strings.Contains(m.note, "inside overview's tmux") {
		t.Fatalf("cmd=%v note=%q", cmd != nil, m.note)
	}
	if m, cmd = press(m, "c"); cmd != nil || !strings.Contains(m.note, "inside overview's tmux") {
		t.Fatalf("c: cmd=%v note=%q", cmd != nil, m.note)
	}
}

func TestNewSessionNeedsADirectory(t *testing.T) {
	r := row("s1", "red", "auth", 11)
	r.Cwd = ""
	f := newFakeMux()
	m := stagedModel(t, f, r)
	if m = pressSettled(t, m, "c"); len(f.spawned) != 0 || !strings.Contains(m.note, "no directory") {
		t.Errorf("no cwd: spawned %v note %q", f.spawned, m.note)
	}
	// While searching, c is search text.
	if m = pressSettled(t, m, "/", "c"); len(f.spawned) != 0 || m.filter != "c" {
		t.Errorf("c while searching: filter %q", m.filter)
	}
}
