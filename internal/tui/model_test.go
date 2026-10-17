package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"overview/internal/fleet"
	"overview/internal/ghostty"
)

// fakeJumper records what the view asked Ghostty to do.
type fakeJumper struct {
	checks  []int // pids asked about
	locates []string
	focuses []string

	outside  map[int]bool // pids not under Ghostty
	checkErr error

	next      int
	locateErr error
	focusErr  map[string]error // by terminal id
}

func (f *fakeJumper) InGhostty(_ context.Context, pid int) (bool, error) {
	f.checks = append(f.checks, pid)
	return !f.outside[pid], f.checkErr
}

func (f *fakeJumper) Locate(_ context.Context, tty string) (string, error) {
	f.locates = append(f.locates, tty)
	if f.locateErr != nil {
		return "", f.locateErr
	}
	f.next++
	return "T" + string(rune('0'+f.next)), nil
}

func (f *fakeJumper) Focus(_ context.Context, id string) error {
	f.focuses = append(f.focuses, id)
	return f.focusErr[id]
}

func row(id, name, title string, pid int) fleet.Row {
	return fleet.Row{
		Live:  fleet.Live{SessionID: id, Name: name, PID: pid, Cwd: "/w/" + name, Kind: "interactive"},
		State: fleet.Idle, Title: title,
	}
}

// newTestModel returns a loaded model over rows, with ttys ttys<pid>.
func newTestModel(t *testing.T, j jumper, rows ...fleet.Row) model {
	t.Helper()
	m := newModel(fleet.Home(t.TempDir()), "")
	m.projects.exists = func(string) bool { return false }
	m.jump = j
	ttys := map[int]string{}
	for _, r := range rows {
		if r.PID > 0 && !r.Background() {
			ttys[r.PID] = "ttys" + string(rune('0'+r.PID%10))
		}
	}
	m = update(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	return update(m, rowsMsg{rows: rows, ttys: ttys})
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

// settle runs a jump Cmd and feeds its result back, as the runtime would.
func settle(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a command")
	}
	msg, ok := cmd().(jumpMsg)
	if !ok {
		t.Fatalf("command did not return a jumpMsg")
	}
	return update(m, msg)
}

func TestFilterEnterLocatesThenFocusesAndClearsFilter(t *testing.T) {
	j := &fakeJumper{}
	m := newTestModel(t, j, row("s1", "red", "auth work", 11), row("s2", "blue", "billing work", 12))
	m, _ = press(m, "/")
	m, _ = press(m, typed("bill")...)
	if m.sel != "s2" || !m.filtering {
		t.Fatalf("cursor should sit on the first match: sel=%q filtering=%v", m.sel, m.filtering)
	}
	m, cmd := press(m, "enter")
	if m.filter != "" || m.filtering {
		t.Fatalf("filter should be cleared: %q %v", m.filter, m.filtering)
	}
	if m.sel != "s2" {
		t.Fatalf("cursor left the row: %q", m.sel)
	}
	if len(j.locates) != 0 {
		t.Fatal("Update must not block on Ghostty")
	}
	m = settle(t, m, cmd)
	if len(j.locates) != 1 || j.locates[0] != "ttys2" || len(j.focuses) != 1 || j.focuses[0] != "T1" {
		t.Fatalf("locates %v focuses %v", j.locates, j.focuses)
	}
	if got := len(m.ordered()); got != 2 {
		t.Fatalf("full list should be back, got %d rows", got)
	}
}

func TestCachedJumpSkipsLocate(t *testing.T) {
	j := &fakeJumper{}
	m := newTestModel(t, j, row("s1", "red", "auth", 11))
	m, cmd := press(m, "enter")
	m = settle(t, m, cmd)
	m, cmd = press(m, "enter")
	settle(t, m, cmd)
	if len(j.locates) != 1 || len(j.focuses) != 2 {
		t.Fatalf("locates %v focuses %v", j.locates, j.focuses)
	}
}

func TestStaleIDLocatesOnce(t *testing.T) {
	j := &fakeJumper{focusErr: map[string]error{}}
	m := newTestModel(t, j, row("s1", "red", "auth", 11))
	m, cmd := press(m, "enter")
	m = settle(t, m, cmd) // caches T1
	j.focusErr["T1"] = ghostty.ErrStale
	m, cmd = press(m, "enter")
	m = settle(t, m, cmd)
	if strings.Join(j.locates, ",") != "ttys1,ttys1" || strings.Join(j.focuses, ",") != "T1,T1,T2" {
		t.Fatalf("locates %v focuses %v", j.locates, j.focuses)
	}
	if got := m.terms["s1\x00ttys1"]; got != "T2" {
		t.Fatalf("cache holds %q, want T2", got)
	}
	// Still stale after a fresh locate: give up after one more locate.
	j.focusErr["T2"], j.focusErr["T3"] = ghostty.ErrStale, ghostty.ErrStale
	m, cmd = press(m, "enter")
	m = settle(t, m, cmd)
	if len(j.locates) != 3 {
		t.Fatalf("locates %v", j.locates)
	}
	if !strings.Contains(m.note, "ttys1") || len(m.terms) != 0 {
		t.Fatalf("note %q cache %v", m.note, m.terms)
	}
}

func TestFailureFallsBackToTTYNote(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{ghostty.ErrNotRunning, "not running"},
		{ghostty.ErrNotFound, "not found"},
		{fmt.Errorf("ghostty: find: %w", context.DeadlineExceeded), "did not answer"},
		{errors.New("boom"), "jump failed"},
	} {
		j := &fakeJumper{locateErr: tc.err}
		m := newTestModel(t, j, row("s1", "red", "auth", 11))
		m, cmd := press(m, "enter")
		m = settle(t, m, cmd)
		if !strings.Contains(m.note, "ttys1") || !strings.Contains(m.note, tc.want) {
			t.Errorf("%v: note %q", tc.err, m.note)
		}
		if len(j.focuses) != 0 || len(m.terms) != 0 {
			t.Errorf("%v: focuses %v cache %v", tc.err, j.focuses, m.terms)
		}
	}
}

func TestBackgroundRowAttaches(t *testing.T) {
	j := &fakeJumper{}
	bg := row("s1", "red", "auth", 0)
	bg.Kind, bg.ID = "background", "abc123"
	m := newTestModel(t, j, bg)
	_, cmd := press(m, "enter")
	if cmd == nil {
		t.Fatal("enter on a background session should attach")
	}
	m2, cmd := press(m, "/", "a", "u", "enter")
	if cmd == nil || m2.filter != "" {
		t.Fatalf("filter-enter should attach and clear: cmd=%v filter=%q", cmd != nil, m2.filter)
	}
	if len(j.locates)+len(j.focuses) != 0 {
		t.Fatal("background sessions must not touch Ghostty")
	}
}

func TestAKeyKeepsReportingTheTTY(t *testing.T) {
	j := &fakeJumper{}
	m := newTestModel(t, j, row("s1", "red", "auth", 11))
	m, cmd := press(m, "a")
	if cmd != nil || !strings.Contains(m.note, "ttys1") || len(j.locates) != 0 {
		t.Fatalf("cmd=%v note=%q", cmd != nil, m.note)
	}
}

func TestNoJumperOrNoTTYFallsBack(t *testing.T) {
	m := newTestModel(t, nil, row("s1", "red", "auth", 11))
	m, cmd := press(m, "enter")
	if cmd != nil || !strings.Contains(m.note, "ttys1") {
		t.Fatalf("no jumper: cmd=%v note=%q", cmd != nil, m.note)
	}
	j := &fakeJumper{}
	m = newTestModel(t, j, row("s1", "red", "auth", 0))
	m, cmd = press(m, "enter")
	if cmd != nil || !strings.Contains(m.note, "its own terminal") {
		t.Fatalf("no tty: cmd=%v note=%q", cmd != nil, m.note)
	}
}

func TestFilterEnterWithoutMatchDoesNothing(t *testing.T) {
	j := &fakeJumper{}
	m := newTestModel(t, j, row("s1", "red", "auth", 11))
	m, _ = press(m, "/", "z", "z")
	m, cmd := press(m, "enter")
	if cmd != nil || m.filtering || m.filter != "zz" || len(j.locates) != 0 {
		t.Fatalf("cmd=%v filtering=%v filter=%q", cmd != nil, m.filtering, m.filter)
	}
}

func TestArrowsMoveWhileFiltering(t *testing.T) {
	j := &fakeJumper{}
	m := newTestModel(t, j, row("s1", "red", "work", 11), row("s2", "blue", "work", 12))
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
	m := newTestModel(t, nil, row("s1", "red", "a", 11), asking)
	if m.sel != "s3" {
		t.Fatalf("cursor should start on what needs you, got %q", m.sel)
	}
	m, _ = press(m, "j", "n")
	if m.sel != "s3" {
		t.Fatalf("n should return to the waiting session, got %q", m.sel)
	}
}

func TestUnhandledKeyWhileFilteringKeepsTheCursor(t *testing.T) {
	m := newTestModel(t, nil, row("s1", "red", "work", 11), row("s2", "blue", "work", 12))
	m, _ = press(m, "/", "w", "down")
	moved := m.sel
	for _, k := range []tea.KeyMsg{{Type: tea.KeyLeft}, {Type: tea.KeyRight}, {Type: tea.KeyCtrlA}} {
		m = update(m, k)
		if m.sel != moved {
			t.Fatalf("%v moved the cursor from %q to %q", k, moved, m.sel)
		}
	}
	m, _ = press(m, "backspace") // the text changed: back to the first match
	if first := m.ordered()[0].SessionID; m.sel != first {
		t.Fatalf("new filter text should select the first match %q, got %q", first, m.sel)
	}
}

// A filter left in place by an enter with no match, then a refresh that
// brings rows the filter hides: esc must put the cursor on a shown row.
func TestEscOutsideTheFilterSelectsAShownRow(t *testing.T) {
	m := newTestModel(t, nil)
	m, _ = press(m, "/", "z", "z", "enter")
	m = update(m, rowsMsg{rows: []fleet.Row{row("s1", "red", "work", 11)}, ttys: map[int]string{11: "ttys1"}})
	m, _ = press(m, "esc")
	if m.filter != "" || m.sel != "s1" {
		t.Fatalf("filter %q sel %q", m.filter, m.sel)
	}
}

// A session outside Ghostty (tmux, another terminal app) has a tty too, but
// Locate must never write to it.
func TestNoLocateOutsideGhostty(t *testing.T) {
	for _, j := range []*fakeJumper{
		{outside: map[int]bool{11: true}},
		{checkErr: errors.New("ps: boom")},
	} {
		m := newTestModel(t, j, row("s1", "red", "auth", 11))
		m, cmd := press(m, "enter")
		m = settle(t, m, cmd)
		if len(j.checks) != 1 || j.checks[0] != 11 || len(j.locates) != 0 || len(j.focuses) != 0 {
			t.Fatalf("checks %v locates %v focuses %v", j.checks, j.locates, j.focuses)
		}
		if !strings.Contains(m.note, "ttys1") {
			t.Fatalf("note %q", m.note)
		}
		if j.checkErr == nil && !strings.Contains(m.note, "not in Ghostty") {
			t.Fatalf("note %q", m.note)
		}
	}
}

func TestSecondEnterWaitsForTheJumpInFlight(t *testing.T) {
	j := &fakeJumper{}
	m := newTestModel(t, j, row("s1", "red", "auth", 11))
	m, first := press(m, "enter")
	m, second := press(m, "enter")
	if first == nil || second != nil {
		t.Fatalf("first cmd %v, second cmd %v: want one jump", first != nil, second != nil)
	}
	if !strings.Contains(m.note, "finding red") {
		t.Fatalf("no progress note while the jump runs: %q", m.note)
	}
	m = settle(t, m, first)
	if len(j.locates) != 1 || m.jumping != 0 {
		t.Fatalf("locates %v, in flight %d", j.locates, m.jumping)
	}
	if _, again := press(m, "enter"); again == nil {
		t.Fatal("enter after the jump finished should jump again")
	}
}

// The answer to a jump can arrive after a refresh: the session may be gone,
// or now run on another tty. The view keeps going and caches nothing stale.
func TestJumpAnswerAfterTheRowChanged(t *testing.T) {
	for name, after := range map[string]rowsMsg{
		"vanished":    {rows: []fleet.Row{row("s2", "blue", "b", 12)}, ttys: map[int]string{12: "ttys2"}},
		"tty changed": {rows: []fleet.Row{row("s1", "red", "auth", 11)}, ttys: map[int]string{11: "ttys8"}},
	} {
		j := &fakeJumper{}
		m := newTestModel(t, j, row("s1", "red", "auth", 11))
		m, cmd := press(m, "enter")
		m = update(m, after)
		m = settle(t, m, cmd)
		if m.jumping != 0 || len(m.terms) != 0 {
			t.Errorf("%s: in flight %d, cache %v", name, m.jumping, m.terms)
		}
	}
}

func TestJumpShowsProgressNote(t *testing.T) {
	j := &fakeJumper{}
	m := newTestModel(t, j, row("s1", "red", "auth", 11))
	m, first := press(m, "enter")
	if m.note != "finding red's terminal…" {
		t.Fatalf("note %q", m.note)
	}
	m.note = ""
	m, second := press(m, "enter")
	if second != nil || !strings.Contains(m.note, "finding red") {
		t.Fatalf("repeat enter: cmd %v, note %q", second != nil, m.note)
	}
	m = settle(t, m, first)
	if m.jumping != 0 {
		t.Fatal("jump still in flight")
	}
}

// A timeout from the real runner wraps the context error and reaches the
// user as "Ghostty did not answer".
func TestJumpTimeoutFromRunnerShowsDidNotAnswer(t *testing.T) {
	killed := fmt.Errorf("osascript: %w: %w: ", context.DeadlineExceeded, errors.New("signal: killed"))
	c := ghostty.Client{
		Run:   func(context.Context, string, ...string) ([]byte, error) { return nil, killed },
		Write: func(string, []byte) error { return nil },
		Sleep: func(context.Context, time.Duration) error { return nil },
		Token: func() string { return "tok" },
	}
	m := newTestModel(t, c, row("s1", "red", "auth", 11))
	m, cmd := press(m, "enter")
	m = settle(t, m, cmd)
	if !strings.Contains(m.note, "did not answer") {
		t.Fatalf("note %q", m.note)
	}
}

// A tab typed into the search box once overflowed the narrowest layout.
func TestSearchWithTabStaysWithinWidth(t *testing.T) {
	m := newTestModel(t, &fakeJumper{}, row("s1", "a", "auth", 11))
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
	cur, _, _ := m.selected(m.ordered())
	_, owners := m.listView(m.sections(), cur.SessionID)
	for i, o := range owners {
		if o == id {
			return i + 1
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

func TestClickSelectsAndDoubleClickJumps(t *testing.T) {
	j := &fakeJumper{}
	m := newTestModel(t, j, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	m, _ = press(m, "tab")
	y := screenY(m, "s2")
	m, cmd := mouseClick(m, 5, y+1) // the row's second line
	if m.sel != "s2" || m.focus != listPane || cmd != nil {
		t.Fatalf("sel %q focus %v cmd %v", m.sel, m.focus, cmd != nil)
	}
	m, cmd = mouseClick(m, 5, y)
	m = settle(t, m, cmd)
	if len(j.locates) != 1 || j.locates[0] != "ttys2" {
		t.Fatalf("double click should jump to s2: locates %v", j.locates)
	}
}

func TestSlowSecondClickOnlySelects(t *testing.T) {
	j := &fakeJumper{}
	m := newTestModel(t, j, row("s1", "red", "auth", 11))
	m, _ = mouseClick(m, 5, screenY(m, "s1"))
	m.clickAt = m.clickAt.Add(-time.Second)
	if _, cmd := mouseClick(m, 5, screenY(m, "s1")); cmd != nil {
		t.Fatal("two slow clicks acted")
	}
}

func TestClickOffARowDoesNothing(t *testing.T) {
	m := newTestModel(t, &fakeJumper{}, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	before := m.sel
	for _, y := range []int{0, 1, 28, 29} { // top edge, a heading, bottom edge, status bar
		m, _ = mouseClick(m, 5, y)
		if m.sel != before {
			t.Fatalf("click at line %d moved the cursor to %q", y, m.sel)
		}
	}
}

// Clicking a row above the cursor once scrolled it to the bottom of the
// pane, out from under the pointer.
func TestClickDoesNotScrollTheList(t *testing.T) {
	m := newTestModel(t, &fakeJumper{}, manyRows(8)...)
	m = update(m, tea.WindowSizeMsg{Width: 120, Height: 12})
	m, _ = press(m, "G")
	rows := m.ordered()
	for _, r := range rows {
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
	m := newTestModel(t, &fakeJumper{}, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	first := m.sel
	m = wheel(m, 5, 5, true)
	if m.sel == first {
		t.Fatal("wheel down over the list did not move the cursor")
	}
	if m = wheel(m, 5, 5, false); m.sel != first {
		t.Fatal("wheel up did not move back")
	}
	m, _ = mouseClick(m, 80, 5)
	if m.focus != detailPane {
		t.Fatal("click on the detail pane did not focus it")
	}
	m = wheel(m, 80, 5, false)
	if m.scroll != 0 || m.sel != first {
		t.Fatalf("wheel up at the top: scroll %d sel %q", m.scroll, m.sel)
	}
}

func TestClickClosesOverlays(t *testing.T) {
	m := newTestModel(t, &fakeJumper{}, row("s1", "red", "auth", 11))
	m, _ = press(m, "?")
	if m = wheel(m, 5, 5, true); !m.help {
		t.Fatal("wheel closed help")
	}
	if m, _ = mouseClick(m, 5, 5); m.help {
		t.Fatal("click left help open")
	}
	bg := fleet.Row{Live: fleet.Live{SessionID: "b1", ID: "b1", Name: "bg", Kind: "background"}, State: fleet.Idle}
	m = newTestModel(t, &fakeJumper{}, bg)
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
	defer func(old func(string) error) { copyText = old }(copyText)
	copyText = func(s string) error { got = s; return nil }
	m := newTestModel(t, &fakeJumper{}, row("s1", "red", "auth", 11))
	m, cmd := press(m, "y")
	if got != "" || cmd == nil {
		t.Fatal("y copied inside Update")
	}
	if m = update(m, cmd()); got != "s1" || m.note != "copied s1" {
		t.Fatalf("copied %q, note %q", got, m.note)
	}
}
