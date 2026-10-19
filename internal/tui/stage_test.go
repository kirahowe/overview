package tui

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"pgregory.net/rapid"

	"overview/internal/fleet"
	"overview/internal/tmux"
)

// fakeMux is a tmux small enough to reason about: panes sit in windows of
// sessions, and moving the list takes it out of one window and into
// another, as tmux does. A window is gone once no pane has its id.
type fakeMux struct {
	panes   []tmux.Pane
	next    int
	current map[string]string // each session's current window
	selects []string
	spawned []spawnCall
	moves   []string          // the size each move asked for
	layouts []string          // the windows whose layout was read
	set     map[string]string // the layouts set, by window
	detach  int
	options map[string]string

	moveErr error
}

type spawnCall struct {
	session, dir string
	env, command []string
}

// newFakeMux holds the view's pane %0, alone in its window, and a holder
// %1 in a window of its own, both in session ov.
func newFakeMux() *fakeMux {
	f := &fakeMux{options: map[string]string{}, set: map[string]string{},
		current: map[string]string{"ov": "@0"}, next: 2}
	f.panes = []tmux.Pane{
		{ID: "%0", TTY: "ttys900", Session: "ov", Window: "@0", W: 160, H: 40, List: true},
		{ID: "%1", TTY: "ttys901", Session: "ov", Window: "@1", W: 160, H: 40},
	}
	return f
}

func (f *fakeMux) Panes() ([]tmux.Pane, error) { return slices.Clone(f.panes), nil }

func (f *fakeMux) find(id string) int {
	for i, p := range f.panes {
		if p.ID == id {
			return i
		}
	}
	return -1
}

// add starts a pane in a background window of its own in session.
func (f *fakeMux) add(session, tty string) tmux.Pane {
	p := tmux.Pane{ID: fmt.Sprintf("%%%d", f.next), TTY: tty, Session: session, Window: fmt.Sprintf("@%d", f.next), W: 160, H: 40}
	f.next++
	f.panes = append(f.panes, p)
	return p
}

// split adds a pane beside pane id, in its window, as a user's split does.
func (f *fakeMux) split(id string) tmux.Pane {
	beside := f.panes[f.find(id)]
	p := tmux.Pane{ID: fmt.Sprintf("%%%d", f.next), TTY: fmt.Sprintf("ttys%d", 300+f.next), Session: beside.Session, Window: beside.Window, W: 80, H: 40}
	f.next++
	f.panes = append(f.panes, p)
	return p
}

func (f *fakeMux) Spawn(session, dir string, env []string, command ...string) (tmux.Pane, error) {
	f.spawned = append(f.spawned, spawnCall{session, dir, env, command})
	return f.add(session, fmt.Sprintf("ttys%d", 500+f.next)), nil
}

func (f *fakeMux) Move(list, target, size string) error {
	if f.moveErr != nil {
		return f.moveErr
	}
	i, j := f.find(list), f.find(target)
	if i < 0 || j < 0 || i == j {
		return errors.New("can't find pane")
	}
	f.moves = append(f.moves, size)
	w, err := strconv.Atoi(size)
	if err != nil {
		w = 48 // a share of a window 160 wide
	}
	f.panes[i].Session, f.panes[i].Window, f.panes[i].W = f.panes[j].Session, f.panes[j].Window, w
	f.current[f.panes[j].Session] = f.panes[j].Window
	return nil
}

func (f *fakeMux) Layout(window string) (string, error) {
	f.layouts = append(f.layouts, window)
	return "layout of " + window, nil
}

func (f *fakeMux) SetLayout(window, layout string) error {
	f.set[window] = layout
	return nil
}

func (f *fakeMux) MoveWindow(window, session string) error {
	for i, p := range f.panes {
		if p.Window == window {
			f.panes[i].Session = session
		}
	}
	return nil
}

func (f *fakeMux) Select(pane string) error {
	f.selects = append(f.selects, pane)
	return nil
}

func (f *fakeMux) Detach() error { f.detach++; return nil }

func (f *fakeMux) SetOption(_, name, value string) error {
	f.options[name] = value
	return nil
}

// exit ends a pane's process, which closes it.
func (f *fakeMux) exit(id string) {
	if i := f.find(id); i >= 0 {
		f.panes = slices.Delete(f.panes, i, i+1)
	}
}

// stage is the window the list is in.
func (f *fakeMux) stage() string { return f.window("%0") }

func (f *fakeMux) window(id string) string {
	if i := f.find(id); i >= 0 {
		return f.panes[i].Window
	}
	return ""
}

// staged reports whether pane id is in the list's window, and that window
// is the one shown.
func (f *fakeMux) staged(id string) bool {
	return id != "" && f.window(id) == f.stage() && f.current["ov"] == f.stage()
}

// inWindow counts the panes in a window.
func (f *fakeMux) inWindow(w string) int {
	n := 0
	for _, p := range f.panes {
		if p.Window == w {
			n++
		}
	}
	return n
}

// drawn records what the view wrote to each tty.
type drawn map[string]string

func recordDraws(t testing.TB) drawn {
	d := drawn{}
	old := writeTTY
	writeTTY = func(tty string, b []byte) error { d[tty] = string(b); return nil }
	t.Cleanup(func() { writeTTY = old })
	return d
}

// stagedModel is newTestModel inside the fake tmux: each interactive row's
// claude runs in a window of its own, on the row's tty.
func stagedModel(t testing.TB, f *fakeMux, rows ...fleet.Row) model {
	m := newModel(fleet.Home{Dir: t.TempDir()}, "")
	m.projects.exists = func(string) bool { return false }
	m.mux, m.self, m.session, m.holder, m.exe = f, "%0", "ov", "%1", "overview"
	ttys := map[int]string{}
	for _, r := range rows {
		if r.PID > 0 && !r.Background() {
			ttys[r.PID] = fmt.Sprintf("ttys%03d", r.PID)
			f.add("ov", ttys[r.PID])
		}
	}
	m = m.setPanes(f.panes)
	m, cmd := step(m, tea.WindowSizeMsg{Width: 50, Height: 30})
	m = settle(t, m, cmd)
	m, cmd = step(m, rowsMsg{rows: rows, ttys: ttys})
	return settle(t, m, cmd)
}

// tester is what settle needs from *testing.T and *rapid.T alike.
type tester interface {
	Helper()
	Fatal(args ...any)
}

func step(m model, msg tea.Msg) (model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(model), cmd
}

// settle runs commands and feeds their messages back, as the runtime would,
// until nothing is left to do. A view that never settles fails the test.
func settle(t tester, m model, cmd tea.Cmd) model {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for n := 0; len(queue) > 0; n++ {
		if n > 200 {
			t.Fatal("the view never settled")
		}
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, msg...)
		default:
			var next tea.Cmd
			m, next = step(m, msg)
			queue = append(queue, next)
		}
	}
	return m
}

func pressSettled(t tester, m model, keys ...string) model {
	t.Helper()
	for _, k := range keys {
		var cmd tea.Cmd
		m, cmd = step(m, keyMsg(k))
		m = settle(t, m, cmd)
	}
	return m
}

func paneWithTTY(f *fakeMux, tty string) string {
	for _, p := range f.panes {
		if p.TTY == tty {
			return p.ID
		}
	}
	return ""
}

func TestTheStageFollowsTheCursor(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	want := paneWithTTY(f, m.ttys[m.ordered()[0].PID])
	if !f.staged(want) || m.stage != f.stage() {
		t.Fatalf("the list is in %s (model %s), want the selected session's %s", f.stage(), m.stage, want)
	}
	m = pressSettled(t, m, "j")
	want = paneWithTTY(f, m.ttys[m.ordered()[1].PID])
	if !f.staged(want) || f.inWindow(f.stage()) != 2 {
		t.Fatalf("after j the list is in %s, want %s's window alone", f.stage(), want)
	}
	if len(f.selects) != 0 {
		t.Fatalf("moving the cursor took the focus: %v", f.selects)
	}
}

func TestEnterFocusesTheSession(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11))
	m = pressSettled(t, m, "enter")
	if want := paneWithTTY(f, "ttys011"); len(f.selects) != 1 || f.selects[0] != want {
		t.Fatalf("selects %v, want [%s]", f.selects, want)
	}
	// Twice is fine too, and nothing else moves.
	moves := len(f.moves)
	pressSettled(t, m, "enter")
	if len(f.selects) != 2 || len(f.moves) != moves {
		t.Fatalf("selects %v, moves %d then %d", f.selects, moves, len(f.moves))
	}
}

func TestSessionOutsideTmuxGetsTheHolder(t *testing.T) {
	d := recordDraws(t)
	f := newFakeMux()
	m := stagedModel(t, f)
	// A session in some other terminal: it has a tty, but no pane here.
	m, cmd := step(m, rowsMsg{rows: []fleet.Row{row("s1", "red", "auth", 11)}, ttys: map[int]string{11: "ttys777"}})
	m = settle(t, m, cmd)
	if !f.staged("%1") {
		t.Fatalf("the list is in %s, want the holder's window", f.stage())
	}
	text := ansi.Strip(d["ttys901"])
	if !strings.Contains(text, "ttys777") || !strings.Contains(text, "outside overview's tmux") {
		t.Fatalf("holder shows %q", text)
	}
	m = pressSettled(t, m, "enter")
	if len(f.selects) != 0 || !strings.Contains(m.note, "outside overview's tmux") {
		t.Fatalf("enter: selects %v, note %q", f.selects, m.note)
	}
	// Drawn once, and again only when what it shows changes.
	delete(d, "ttys901")
	m, cmd = step(m, tea.FocusMsg{})
	settle(t, m, cmd)
	if _, again := d["ttys901"]; again {
		t.Fatal("the holder was redrawn with nothing changed")
	}
}

func TestEnterOnABackgroundSessionAttachesBesideTheList(t *testing.T) {
	f := newFakeMux()
	bg := row("b1", "bgwork", "nightly", 0)
	bg.Kind, bg.ID = "background", "abc123"
	m := stagedModel(t, f, bg, row("s1", "red", "auth", 11))
	m = pressSettled(t, m, "g")
	if m.sel != "b1" || !f.staged("%1") {
		t.Fatalf("sel %s, list in %s: want the holder for the background session", m.sel, f.stage())
	}
	m = pressSettled(t, m, "enter")
	if len(f.spawned) != 1 {
		t.Fatalf("spawned %v", f.spawned)
	}
	call := f.spawned[0]
	if got := strings.Join(call.command, " "); !strings.HasSuffix(got, "attach abc123") || call.session != "ov" {
		t.Fatalf("spawned %q in %s", got, call.session)
	}
	if !slices.Contains(call.env, m.home.ClaudeEnv()) {
		t.Fatalf("env %v", call.env)
	}
	attach := m.attached["abc123"]
	if !f.staged(attach) || len(f.selects) != 1 || f.selects[0] != attach {
		t.Fatalf("list in %s, selects %v, attach pane %s", f.stage(), f.selects, attach)
	}
	// Away and back: the same attach pane comes back.
	m = pressSettled(t, m, "j", "k", "enter")
	if len(f.spawned) != 1 || !f.staged(attach) {
		t.Fatalf("spawned %d, list in %s", len(f.spawned), f.stage())
	}
	// Detaching ends the attach, and the list goes to the holder.
	f.exit(attach)
	m, cmd := step(m, panesMsg{panes: f.panes})
	settle(t, m, cmd)
	if !f.staged("%1") {
		t.Fatalf("list in %s after the attach ended", f.stage())
	}
}

func TestNewSessionStartsAtTheProjectRootAndIsAdopted(t *testing.T) {
	f := newFakeMux()
	r := row("s1", "red", "auth", 11)
	r.Cwd = "/code/app/internal/x"
	m := stagedModel(t, f, r)
	m.projects.exists = func(p string) bool { return p == "/code/app/.jj" }
	m.projects.roots = map[string]string{}
	m = pressSettled(t, m, "c")
	if len(f.spawned) != 1 || f.spawned[0].dir != "/code/app" || f.spawned[0].session != "ov" {
		t.Fatalf("spawned %+v", f.spawned)
	}
	opened := m.opened
	if !f.staged(opened.ID) || len(f.selects) != 1 || f.selects[0] != opened.ID {
		t.Fatalf("list in %s, selects %v, want the new pane %s", f.stage(), f.selects, opened.ID)
	}
	// Once claude lists it, the cursor moves onto it.
	n := row("s9", "new", "", 19)
	m, cmd := step(m, rowsMsg{rows: []fleet.Row{r, n}, ttys: map[int]string{11: "ttys011", 19: opened.TTY}})
	m = settle(t, m, cmd)
	if m.sel != "s9" || m.opened.ID != "" || !f.staged(opened.ID) {
		t.Fatalf("sel %s, opened %v, list in %s", m.sel, m.opened, f.stage())
	}
}

func TestNewSessionWithNothingListedStartsWhereOverviewDid(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f)
	m.cwd = "/home/me/proj"
	pressSettled(t, m, "c")
	if len(f.spawned) != 1 || f.spawned[0].dir != "/home/me/proj" {
		t.Fatalf("spawned %+v", f.spawned)
	}
}

func TestAFailedMoveWaitsForTheNextListing(t *testing.T) {
	f := newFakeMux()
	f.moveErr = errors.New("tmux join-pane: boom")
	m := stagedModel(t, f, row("s1", "red", "auth", 11))
	if !strings.Contains(m.note, "boom") || m.failed == "" {
		t.Fatalf("note %q, failed %q", m.note, m.failed)
	}
	f.moveErr = nil
	m, cmd := step(m, panesMsg{panes: f.panes})
	settle(t, m, cmd)
	if !f.staged(paneWithTTY(f, "ttys011")) {
		t.Fatalf("list in %s after the retry", f.stage())
	}
}

func TestAListLeftAloneMovesOn(t *testing.T) {
	f := newFakeMux()
	red, blue := row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12)
	m := stagedModel(t, f, red, blue)
	// blue sorts first, so the list is in its window. Its claude exits,
	// and the pane closes. Until the list loses blue, the holder stands in.
	moves, left := len(f.moves), f.stage()
	f.exit(paneWithTTY(f, "ttys012"))
	m, cmd := step(m, panesMsg{panes: f.panes})
	m = settle(t, m, cmd)
	if len(f.moves) != moves+1 || !f.staged("%1") || f.inWindow(left) != 0 {
		t.Fatalf("moves %d then %d, list in %s, %d panes left in %s", moves, len(f.moves), f.stage(), f.inWindow(left), left)
	}
	// Alone in its window the list is as wide as it, so it takes a share.
	if f.moves[len(f.moves)-1] != listShare {
		t.Fatalf("moved %s wide", f.moves[len(f.moves)-1])
	}
	m, cmd = step(m, rowsMsg{rows: []fleet.Row{red}, ttys: map[int]string{11: "ttys011"}})
	settle(t, m, cmd)
	if !f.staged(paneWithTTY(f, "ttys011")) {
		t.Fatalf("list in %s, not with red", f.stage())
	}
}

func TestTheListKeepsItsWidth(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	// The first move came from a window the list had to itself.
	if len(f.moves) == 0 || f.moves[0] != listShare {
		t.Fatalf("moves %v", f.moves)
	}
	// The user drags the border, and the next listing has the new width.
	f.panes[f.find("%0")].W = 42
	m, cmd := step(m, panesMsg{panes: f.panes})
	m = settle(t, m, cmd)
	moves := len(f.moves)
	pressSettled(t, m, "j")
	if len(f.moves) != moves+1 || f.moves[moves] != "42" {
		t.Fatalf("moves %v, want the dragged 42", f.moves)
	}
}

func TestASplitStaysWithItsSession(t *testing.T) {
	old := without
	without = func(layout, pane string) (string, error) { return layout + " without " + pane, nil }
	t.Cleanup(func() { without = old })
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	blue := paneWithTTY(f, "ttys012")
	split := f.split(blue)
	moves := len(f.moves)
	m, cmd := step(m, panesMsg{panes: f.panes})
	m = settle(t, m, cmd)
	if len(f.moves) != moves {
		t.Fatalf("a split in the list's window moved the list: %v", f.moves)
	}
	m = pressSettled(t, m, "j")
	win := f.window(blue)
	if f.window(split.ID) != win || f.staged(blue) || f.inWindow(win) != 2 {
		t.Fatalf("the split is in %s, blue in %s with %d panes", f.window(split.ID), win, f.inWindow(win))
	}
	// Leaving three panes, the list read the layout and put it back
	// without itself.
	if !slices.Equal(f.layouts, []string{win}) || f.set[win] != "layout of "+win+" without %0" {
		t.Fatalf("layouts read %v, set %v", f.layouts, f.set)
	}
	m = pressSettled(t, m, "k")
	if !f.staged(blue) || !f.staged(split.ID) {
		t.Fatalf("back on blue, the list is in %s and the split in %s", f.stage(), f.window(split.ID))
	}
	// red's window had only red and the list, so it needed no fix-up.
	if len(f.layouts) != 1 {
		t.Fatalf("layouts read %v", f.layouts)
	}
}

func TestUnparkKeepsTheParkedSessions(t *testing.T) {
	f := newFakeMux()
	a := f.add("ov-parked", "ttys050")
	f.split(a.ID)
	b := f.add("ov-parked", "ttys051")
	f.add("ov-parkedx", "ttys052")
	if err := unpark(f, "ov"); err != nil {
		t.Fatal(err)
	}
	for _, p := range f.panes {
		if p.Session == "ov-parked" {
			t.Fatalf("%s is still parked", p.ID)
		}
	}
	if f.inWindow(a.Window) != 2 || f.inWindow(b.Window) != 1 || f.panes[len(f.panes)-1].Session != "ov-parkedx" {
		t.Fatalf("panes %+v", f.panes)
	}
}

func TestAHolderThatClosedIsStartedAgain(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11))
	f.exit("%1")
	m, cmd := step(m, panesMsg{panes: f.panes})
	m = settle(t, m, cmd)
	if len(f.spawned) != 1 || strings.Join(f.spawned[0].command, " ") != "overview stage" || f.spawned[0].session != "ov" {
		t.Fatalf("spawned %+v", f.spawned)
	}
	if m.holder == "%1" || f.options[holderOption] != m.holder || !f.staged(paneWithTTY(f, "ttys011")) {
		t.Fatalf("holder %s, option %q, list in %s", m.holder, f.options[holderOption], f.stage())
	}
	// One holder, however many listings arrive before it is up.
	m.spawningHolder = true
	m, cmd = step(m, panesMsg{panes: f.panes[:len(f.panes)-1]})
	settle(t, m, cmd)
	if len(f.spawned) != 1 {
		t.Fatalf("a second holder was started: %+v", f.spawned)
	}
}

func TestQDetachesAndShiftQQuits(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11))
	pressSettled(t, m, "q")
	if f.detach != 1 {
		t.Fatalf("detached %d times", f.detach)
	}
	if _, cmd := step(m, keyMsg("Q")); cmd == nil {
		t.Fatal("Q did nothing")
	} else if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("Q did not quit")
	}
}

func TestFocusDimsTheCursor(t *testing.T) {
	m := newTestModel(t, row("s1", "red", "auth", 11))
	m = update(m, tea.BlurMsg{})
	if !m.blurred {
		t.Fatal("blur not recorded")
	}
	if m = update(m, tea.FocusMsg{}); m.blurred {
		t.Fatal("focus not recorded")
	}
}

// Whatever the cursor, the sessions, their panes and the user's splits
// do, once the view settles the list is in the wanted pane's window, and
// every other pane stays in the window it started in.
func TestPropTheStageSettlesOnTheWantedPane(t *testing.T) {
	old := writeTTY
	writeTTY = func(string, []byte) error { return nil }
	defer func() { writeTTY = old }()
	rapid.Check(t, func(t *rapid.T) {
		f := newFakeMux()
		var rows []fleet.Row
		ttys := map[int]string{}
		for i := range rapid.IntRange(0, 6).Draw(t, "n") {
			r := row(fmt.Sprintf("s%d", i), fmt.Sprintf("n%d", i), "work", 10+i)
			switch rapid.IntRange(0, 2).Draw(t, "kind") {
			case 0: // in overview's tmux
				ttys[r.PID] = fmt.Sprintf("ttys%03d", r.PID)
				f.add("ov", ttys[r.PID])
			case 1: // in some other terminal
				ttys[r.PID] = fmt.Sprintf("ttys%03d", 600+r.PID)
			case 2:
				r.Kind, r.ID, r.PID = "background", fmt.Sprintf("bg%d", i), 0
			}
			rows = append(rows, r)
		}
		m := newModel(fleet.Home{Dir: t.Name()}, "")
		m.projects.exists = func(string) bool { return false }
		m.mux, m.self, m.session, m.holder, m.exe = f, "%0", "ov", "%1", "overview"
		m = m.setPanes(f.panes)
		m, cmd := step(m, tea.WindowSizeMsg{Width: 50, Height: 30})
		m = settle(t, m, cmd)
		m, cmd = step(m, rowsMsg{rows: rows, ttys: ttys})
		m = settle(t, m, cmd)
		// home is the window each pane other than the list started in.
		home := map[string]string{}

		t.Repeat(map[string]func(*rapid.T){
			"": func(t *rapid.T) {
				if want := m.want(); want != "" && !f.staged(want) {
					t.Fatalf("the list is in %s, the wanted %s in %s", f.stage(), want, f.window(want))
				}
				if m.stage != f.stage() {
					t.Fatalf("the list is in %s, the model thinks %s", f.stage(), m.stage)
				}
				seen := map[string]bool{}
				for _, p := range f.panes {
					if seen[p.ID] {
						t.Fatalf("pane %s twice", p.ID)
					}
					seen[p.ID] = true
					if p.ID == "%0" {
						continue
					}
					if w, ok := home[p.ID]; ok && w != p.Window {
						t.Fatalf("%s moved from %s to %s", p.ID, w, p.Window)
					}
					home[p.ID] = p.Window
				}
				if !seen["%0"] {
					t.Fatal("the list is gone")
				}
			},
			"key": func(t *rapid.T) {
				m = pressSettled(t, m, rapid.SampledFrom([]string{"j", "k", "g", "G", "n", "enter", "c", "esc"}).Draw(t, "key"))
			},
			"split": func(t *rapid.T) {
				// The user splits a session's window.
				var ids []string
				for _, p := range f.panes {
					if p.ID != "%0" {
						ids = append(ids, p.ID)
					}
				}
				if len(ids) == 0 {
					return
				}
				f.split(rapid.SampledFrom(ids).Draw(t, "beside"))
				var cmd tea.Cmd
				m, cmd = step(m, panesMsg{panes: f.panes})
				m = settle(t, m, cmd)
			},
			"exit": func(t *rapid.T) {
				// Some pane's process ends: a session, an attach, a split,
				// or the holder.
				var ids []string
				for _, p := range f.panes {
					if p.ID != "%0" {
						ids = append(ids, p.ID)
					}
				}
				if len(ids) == 0 {
					return
				}
				f.exit(rapid.SampledFrom(ids).Draw(t, "pane"))
				var cmd tea.Cmd
				m, cmd = step(m, panesMsg{panes: f.panes})
				m = settle(t, m, cmd)
			},
			"refresh": func(t *rapid.T) {
				// claude lists the sessions again, with any c started.
				all := slices.Clone(rows)
				tt := map[int]string{}
				for k, v := range ttys {
					tt[k] = v
				}
				if m.opened.ID != "" && rapid.Bool().Draw(t, "listed") {
					n := row("new", "new", "", 99)
					all, tt[99] = append(all, n), m.opened.TTY
				}
				var cmd tea.Cmd
				m, cmd = step(m, rowsMsg{rows: all, ttys: tt})
				m = settle(t, m, cmd)
			},
		})
	})
}

func TestPropHolderTextFillsThePane(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rows, ttys := genStateRows(t)
		m := newModel(fleet.Home{Dir: t.Name()}, "")
		m.projects.exists = func(string) bool { return false }
		m = update(m, rowsMsg{rows: rows, ttys: ttys})
		w := rapid.IntRange(3, 200).Draw(t, "w")
		h := rapid.IntRange(1, 60).Draw(t, "h")
		body := strings.TrimPrefix(m.holderText(w, h), "\x1b[H\x1b[2J\x1b[?25l")
		lines := strings.Split(body, "\r\n")
		if len(lines) != h {
			t.Fatalf("%d lines for a pane %d high", len(lines), h)
		}
		for i, l := range lines {
			if got := ansi.StringWidth(l); got != w {
				t.Fatalf("line %d is %d cells in a pane %d wide: %q", i, got, w, l)
			}
		}
	})
}

func TestSessionNameIsSafeForTmux(t *testing.T) {
	for dir, want := range map[string]string{
		"/u/.claude-work": "overview-work",
		"/u/.claude":      "overview-_claude",
		"/u/a:b.c":        "overview-a_b_c",
	} {
		if got := SessionName(fleet.Home{Dir: dir}); got != want {
			t.Errorf("%s: %q, want %q", dir, got, want)
		}
	}
}
