package tui

import (
	"errors"
	"fmt"
	"maps"
	"path"
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
	besides []string // the panes SelectBeside went right from
	killed  []string // the windows KillWindow closed
	spawned []spawnCall
	moves   []string          // the size each move asked for
	layouts []string          // the windows whose layout was read
	set     map[string]string // the layouts set, by window
	detach  int

	drawn map[string]string // what the view wrote to each tty
	focus string            // the active pane of the window shown

	moveErr error
}

type spawnCall struct {
	session, dir string
	env, command []string
}

// newFakeMux holds the view's pane %0, alone in its window, and a holder
// %1 in a window of its own, both in session ov.
func newFakeMux() *fakeMux {
	f := &fakeMux{drawn: map[string]string{}, set: map[string]string{},
		current: map[string]string{"ov": "@0"}, next: 2}
	f.panes = []tmux.Pane{
		{ID: "%0", TTY: "ttys900", Session: "ov", Window: "@0", W: 160, H: 40, List: true},
		{ID: "%1", TTY: "ttys901", Session: "ov", Window: "@1", W: 160, H: 40, Holder: true},
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
// It reads the window's options, and has none of its own.
func (f *fakeMux) split(id string) tmux.Pane {
	beside := f.panes[f.find(id)]
	p := tmux.Pane{ID: fmt.Sprintf("%%%d", f.next), TTY: fmt.Sprintf("ttys%d", 300+f.next), Session: beside.Session, Window: beside.Window, W: 80, H: 40,
		Command: "zsh", Path: beside.Path, Project: beside.Project}
	f.next++
	f.panes = append(f.panes, p)
	return p
}

func (f *fakeMux) Spawn(session, dir string, env []string, command ...string) (tmux.Pane, error) {
	f.spawned = append(f.spawned, spawnCall{session, dir, env, command})
	p := f.add(session, fmt.Sprintf("ttys%d", 500+f.next))
	cmd := "zsh"
	if len(command) > 0 {
		cmd = path.Base(command[0])
	}
	i := f.find(p.ID)
	f.panes[i].Command, f.panes[i].Path = cmd, dir
	return f.panes[i], nil
}

func (f *fakeMux) SetWindowOption(target, name, value string) error {
	if name != tmux.ProjectOption {
		return fmt.Errorf("unexpected window option %s", name)
	}
	w := f.window(target)
	if strings.HasPrefix(target, "@") {
		w = target
	}
	if f.inWindow(w) == 0 {
		return errors.New("can't find window " + target)
	}
	for i, p := range f.panes {
		if p.Window == w {
			f.panes[i].Project = value
		}
	}
	return nil
}

func (f *fakeMux) SetPaneOption(pane, name, value string) error {
	i := f.find(pane)
	switch {
	case i < 0:
		return errors.New("can't find pane " + pane)
	case name == tmux.AttachOption:
		f.panes[i].Attach = value
	case name == tmux.HolderOption:
		f.panes[i].Holder = value == "1"
	default:
		return fmt.Errorf("unexpected pane option %s on %s", name, pane)
	}
	return nil
}

func (f *fakeMux) KillWindow(window string) error {
	if f.inWindow(window) == 0 {
		return errors.New("can't find window " + window)
	}
	// Real tmux would end the view with it.
	if f.window("%0") == window {
		return errors.New("killed the list's window " + window)
	}
	f.killed = append(f.killed, window)
	f.panes = slices.DeleteFunc(f.panes, func(p tmux.Pane) bool { return p.Window == window })
	return nil
}

// SelectBeside focuses a pane right of pane in its window: the first one
// listed, which stands in for the one used last.
func (f *fakeMux) SelectBeside(pane string) error {
	f.besides = append(f.besides, pane)
	for _, p := range f.panes {
		if p.Window == f.window(pane) && p.ID != pane {
			f.focus = p.ID
			return nil
		}
	}
	return errors.New("no pane right of " + pane)
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
	f.focus = list // join-pane makes the list the active pane
	return nil
}

// Layout is the window's panes side by side, in tmux's classic form, so
// that tmux.Without reads it as it would a real one.
func (f *fakeMux) Layout(window string) (string, error) {
	f.layouts = append(f.layouts, window)
	return fakeLayout(f.panes, window), nil
}

// fakeLayout lays out a 160x40 window's panes in columns.
func fakeLayout(panes []tmux.Pane, window string) string {
	var ids []string
	for _, p := range panes {
		if p.Window == window {
			ids = append(ids, strings.TrimPrefix(p.ID, "%"))
		}
	}
	body := "160x40,0,0"
	if len(ids) == 1 {
		body += "," + ids[0]
	} else {
		w := (160 - (len(ids) - 1)) / len(ids)
		var cells []string
		for i, id := range ids {
			cw := w
			if i == len(ids)-1 {
				cw = 160 - (len(ids)-1)*(w+1)
			}
			cells = append(cells, fmt.Sprintf("%dx40,%d,0,%s", cw, i*(w+1), id))
		}
		body += "{" + strings.Join(cells, ",") + "}"
	}
	var sum uint16
	for i := 0; i < len(body); i++ {
		sum = sum>>1 + (sum&1)<<15
		sum += uint16(body[i])
	}
	return fmt.Sprintf("%04x,%s", sum, body)
}

func (f *fakeMux) SetLayout(window, layout string) error {
	f.set[window] = layout
	return nil
}

func (f *fakeMux) Select(pane string) error {
	if f.find(pane) < 0 {
		return errors.New("can't find pane " + pane)
	}
	f.selects = append(f.selects, pane)
	f.focus = pane
	return nil
}

func (f *fakeMux) Detach() error { f.detach++; return nil }

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

// draw records what the view writes to a tty, as the model's writeTTY.
func (f *fakeMux) draw(tty string, b []byte) error {
	f.drawn[tty] = string(b)
	return nil
}

// fakeDeps stand in for the system in a test model: nothing is running,
// and the holder's text goes to f.
func fakeDeps(f *fakeMux) deps {
	d := systemDeps()
	d.psTable = func() ([]byte, error) { return nil, nil }
	d.copyText = func(string) error { return errors.New("no clipboard in tests") }
	d.reveal = func(string) error { return errors.New("no apps in tests") }
	d.readDirs = func(dir string) ([]string, error) { return nil, errors.New(dir + ": no directories in tests") }
	d.exists = func(string) bool { return false }
	d.resolve = path.Clean
	if f != nil {
		d.writeTTY = f.draw
	} else {
		d.writeTTY = func(string, []byte) error { return errors.New("no ttys in tests") }
	}
	return d
}

// stagedModel is newTestModel inside the fake tmux: each interactive row's
// claude runs in a window of its own, on the row's tty.
func stagedModel(t testing.TB, f *fakeMux, rows ...fleet.Row) model {
	m := newModel(fleet.Home{Dir: t.TempDir()}, "")
	m.mux, m.self, m.session, m.exe = f, "%0", "ov", "overview"
	m.deps = fakeDeps(f)
	ttys := map[int]string{}
	for _, r := range rows {
		if r.PID > 0 && !r.Background() {
			ttys[r.PID] = fmt.Sprintf("ttys%03d", r.PID)
			f.add("ov", ttys[r.PID])
		}
	}
	m = m.setPanes(f.panes, nil)
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
	want := paneWithTTY(f, m.ttys[m.ordered()[0].row.PID])
	if !f.staged(want) || m.stageOf(m.panes) != f.stage() {
		t.Fatalf("the list is in %s (model %s), want the selected session's %s", f.stage(), m.stageOf(m.panes), want)
	}
	m = pressSettled(t, m, "j")
	want = paneWithTTY(f, m.ttys[m.ordered()[1].row.PID])
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
	if want := paneWithTTY(f, "ttys011"); len(f.selects) != 1 || f.selects[0] != want || f.focus != want {
		t.Fatalf("selects %v, focus %s, want %s", f.selects, f.focus, want)
	}
	// Twice is fine too, and nothing else moves.
	moves := len(f.moves)
	pressSettled(t, m, "enter")
	if len(f.selects) != 2 || len(f.moves) != moves {
		t.Fatalf("selects %v, moves %d then %d", f.selects, moves, len(f.moves))
	}
}

func TestSessionOutsideTmuxGetsTheHolder(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f)
	// A session in some other terminal: it has a tty, but no pane here.
	m, cmd := step(m, rowsMsg{rows: []fleet.Row{row("s1", "red", "auth", 11)}, ttys: map[int]string{11: "ttys777"}})
	m = settle(t, m, cmd)
	if !f.staged("%1") {
		t.Fatalf("the list is in %s, want the holder's window", f.stage())
	}
	text := ansi.Strip(f.drawn["ttys901"])
	if !strings.Contains(text, "ttys777") || !strings.Contains(text, "outside overview's tmux") {
		t.Fatalf("holder shows %q", text)
	}
	m = pressSettled(t, m, "enter")
	if len(f.selects) != 0 || !strings.Contains(m.note, "outside overview's tmux") {
		t.Fatalf("enter: selects %v, note %q", f.selects, m.note)
	}
	// Drawn once, and again only when what it shows changes.
	delete(f.drawn, "ttys901")
	m, cmd = step(m, tea.FocusMsg{})
	settle(t, m, cmd)
	if _, again := f.drawn["ttys901"]; again {
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
	attach := m.paneOf(bg)
	if p, ok := m.pane(attach); !ok || p.Attach != "abc123" || f.panes[f.find(attach)].Attach != "abc123" {
		t.Fatalf("the attach pane %s is not marked: %+v", attach, f.panes)
	}
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
	m, cmd := step(m, panesMsg{panes: slices.Clone(f.panes), gen: m.spawns})
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
	m.deps.exists = func(p string) bool { return p == "/code/app/.jj" }
	m = refreshed(t, m)
	m = pressSettled(t, m, "c")
	if len(f.spawned) != 1 || f.spawned[0].dir != "/code/app" || f.spawned[0].session != "ov" {
		t.Fatalf("spawned %+v", f.spawned)
	}
	opened := m.opened
	if got := f.panes[f.find(opened.ID)].Project; got != "/code/app" {
		t.Fatalf("the new window's project is %q", got)
	}
	if !f.staged(opened.ID) || len(f.selects) != 1 || f.selects[0] != opened.ID {
		t.Fatalf("list in %s, selects %v, want the new pane %s", f.stage(), f.selects, opened.ID)
	}
	// Once claude lists it, the cursor moves onto it.
	n := row("s9", "new", "", 19)
	m, cmd := step(m, m.rowsMsg([]fleet.Row{r, n}, map[int]string{11: "ttys011", 19: opened.TTY}, nil, nil))
	m = settle(t, m, cmd)
	if m.sel != "s9" || m.opened.ID != "" || !f.staged(opened.ID) {
		t.Fatalf("sel %s, opened %v, list in %s", m.sel, m.opened, f.stage())
	}
}

// A listing tmux answered between creating the attach pane and marking it
// shows the pane unmarked. It is dropped rather than taken to mean the
// attach has gone.
func TestAListingFromBeforeASpawnIsDropped(t *testing.T) {
	f := newFakeMux()
	bg := row("b1", "bgwork", "nightly", 0)
	bg.Kind, bg.ID = "background", "abc123"
	m := stagedModel(t, f, bg)
	gen := m.spawns
	m = pressSettled(t, m, "enter")
	attach := m.paneOf(bg)
	if attach == "" || m.spawns != gen+1 {
		t.Fatalf("attach %q, spawns %d", attach, m.spawns)
	}
	unmarked := slices.Clone(f.panes)
	unmarked[f.find(attach)].Attach = ""
	moves := len(f.moves)
	m, cmd := step(m, panesMsg{panes: unmarked, gen: gen})
	m = settle(t, m, cmd)
	if m.paneOf(bg) != attach || len(f.moves) != moves {
		t.Fatalf("a stale listing lost the attach: pane %q, moves %d then %d", m.paneOf(bg), moves, len(f.moves))
	}
	// A current listing is taken: the window, unmarked, is now a plain
	// terminal, and the cursor stays in it.
	f.panes = unmarked
	m, cmd = step(m, panesMsg{panes: unmarked, gen: m.spawns})
	m = settle(t, m, cmd)
	if m.paneOf(bg) != "" || m.sel != f.window(attach) || !f.staged(attach) {
		t.Fatalf("a current listing was not taken: pane %q, sel %s, list in %s", m.paneOf(bg), m.sel, f.stage())
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
	m, cmd := step(m, panesMsg{panes: slices.Clone(f.panes), gen: m.spawns})
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
	m, cmd := step(m, panesMsg{panes: slices.Clone(f.panes), gen: m.spawns})
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
	m, cmd := step(m, panesMsg{panes: slices.Clone(f.panes), gen: m.spawns})
	m = settle(t, m, cmd)
	moves := len(f.moves)
	pressSettled(t, m, "j")
	if len(f.moves) != moves+1 || f.moves[moves] != "42" {
		t.Fatalf("moves %v, want the dragged 42", f.moves)
	}
}

func TestASplitStaysWithItsSession(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	blue := paneWithTTY(f, "ttys012")
	split := f.split(blue)
	moves := len(f.moves)
	m, cmd := step(m, panesMsg{panes: slices.Clone(f.panes), gen: m.spawns})
	m = settle(t, m, cmd)
	if len(f.moves) != moves {
		t.Fatalf("a split in the list's window moved the list: %v", f.moves)
	}
	win := f.window(blue)
	before := fakeLayout(f.panes, win)
	m = pressSettled(t, m, "j")
	if f.window(split.ID) != win || f.staged(blue) || f.inWindow(win) != 2 {
		t.Fatalf("the split is in %s, blue in %s with %d panes", f.window(split.ID), win, f.inWindow(win))
	}
	// Leaving three panes, the list read the layout and put it back
	// without itself.
	want, err := tmux.Without(before, "%0")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.layouts, []string{win}) || f.set[win] != want {
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

func TestAHolderThatClosedIsStartedAgain(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11))
	f.exit("%1")
	m, cmd := step(m, panesMsg{panes: slices.Clone(f.panes), gen: m.spawns})
	m = settle(t, m, cmd)
	if len(f.spawned) != 1 || strings.Join(f.spawned[0].command, " ") != "overview stage" || f.spawned[0].session != "ov" {
		t.Fatalf("spawned %+v", f.spawned)
	}
	if h := m.holder(); h == "%1" || h == "" || !f.panes[f.find(h)].Holder || !f.staged(paneWithTTY(f, "ttys011")) {
		t.Fatalf("holder %s, panes %+v, list in %s", h, f.panes, f.stage())
	}
	// One holder, however many listings arrive before it is up.
	m.spawningHolder = true
	m, cmd = step(m, panesMsg{panes: f.panes[:len(f.panes)-1], gen: m.spawns})
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
// do, once the view settles with the keyboard in the list, the list is in
// the wanted pane's window, and every other pane stays in the window it
// started in. While the keyboard is in another pane the list never moves,
// and when an entry's pane is in the list's window the cursor is on such
// an entry.
func TestPropTheStageSettlesOnTheWantedPane(t *testing.T) {
	root := t.TempDir()
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
		m := newModel(caseHome(t, root), "")
		m.mux, m.self, m.session, m.exe = f, "%0", "ov", "overview"
		m.deps = fakeDeps(f)
		m = m.setPanes(f.panes, nil)
		m, cmd := step(m, tea.WindowSizeMsg{Width: 50, Height: 30})
		m = settle(t, m, cmd)
		m, cmd = step(m, rowsMsg{rows: rows, ttys: ttys})
		m = settle(t, m, cmd)
		// home is the window each pane other than the list started in.
		home := map[string]string{}
		// moves is how many moves there had been when the keyboard left.
		moves := 0
		// owns says whether an entry's pane is in the list's window, read
		// from the fake tmux.
		owns := func(e entry) bool {
			for _, p := range f.panes {
				if p.Session != "ov" || p.Window != f.stage() || p.ID == "%0" {
					continue
				}
				switch {
				case e.kind == terminalEntry && e.term.Window == p.Window,
					e.kind == sessionEntry && e.row.Background() && p.Attach == e.row.ID,
					e.kind == sessionEntry && !e.row.Background() && p.TTY == m.ttys[e.row.PID]:
					return true
				}
			}
			return false
		}

		t.Repeat(map[string]func(*rapid.T){
			"": func(t *rapid.T) {
				if m.blurred {
					if len(f.moves) != moves {
						t.Fatalf("the list moved while the keyboard was elsewhere: %v", f.moves[moves:])
					}
					cur, _, _ := m.selected(m.ordered())
					for _, e := range m.ordered() {
						if owns(e) && !owns(cur) {
							t.Fatalf("%s is in the list's window %s, yet the cursor is on %s", e.key(), f.stage(), m.sel)
						}
					}
				} else if want := m.want(); want != "" && !f.staged(want) {
					t.Fatalf("the list is in %s, the wanted %s in %s", f.stage(), want, f.window(want))
				}
				if m.stageOf(m.panes) != f.stage() {
					t.Fatalf("the list is in %s, the model thinks %s", f.stage(), m.stageOf(m.panes))
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
				if m.blurred {
					t.Skip("the keys go to another pane")
				}
				m = pressSettled(t, m, rapid.SampledFrom([]string{"j", "k", "g", "G", "n", "enter", "c", "t", "esc"}).Draw(t, "key"))
			},
			"blur": func(t *rapid.T) {
				if !m.blurred {
					m, moves = blurred(t, m), len(f.moves)
				}
			},
			"focus": func(t *rapid.T) {
				var cmd tea.Cmd
				m, cmd = step(m, tea.FocusMsg{})
				m = settle(t, m, cmd)
			},
			"close": func(t *rapid.T) {
				if m.blurred {
					t.Skip("the keys go to another pane")
				}
				// x and y on a terminal close its window. (On a session
				// they would run claude stop.)
				if e, _, ok := m.selected(m.ordered()); ok && e.kind == terminalEntry {
					m = pressSettled(t, m, "x", "y")
					if f.inWindow(e.term.Window) != 0 && f.find("%0") >= 0 && f.window("%0") != e.term.Window {
						t.Fatalf("%s is still open", e.term.Window)
					}
				}
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
				m, cmd = step(m, panesMsg{panes: slices.Clone(f.panes), gen: m.spawns})
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
				m, cmd = step(m, panesMsg{panes: slices.Clone(f.panes), gen: m.spawns})
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
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		rows, ttys := genStateRows(t)
		m := newModel(caseHome(t, root), "")
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

// repoModel is stagedModel with red's directory inside the repo /code/app.
func repoModel(t *testing.T, f *fakeMux) model {
	t.Helper()
	r := row("s1", "red", "auth", 11)
	r.Cwd = "/code/app/internal/x"
	m := stagedModel(t, f, r)
	m.deps.exists = func(p string) bool { return p == "/code/app/.jj" }
	return refreshed(t, m)
}

// refreshed is m after a refresh that finds what it has, so its roots are
// found again with the model's deps.
func refreshed(t tester, m model) model {
	t.Helper()
	m, cmd := step(m, m.rowsMsg(m.rows, m.ttys, m.hosts, m.pinned))
	return settle(t, m, cmd)
}

func TestTOpensATerminalThatTheCursorTakes(t *testing.T) {
	f := newFakeMux()
	m := repoModel(t, f)
	m = pressSettled(t, m, "t")
	if len(f.spawned) != 1 {
		t.Fatalf("spawned %+v", f.spawned)
	}
	call := f.spawned[0]
	if call.dir != "/code/app" || call.session != "ov" || len(call.command) != 0 ||
		!slices.Equal(call.env, []string{m.home.ClaudeEnv()}) {
		t.Fatalf("spawned %+v", call)
	}
	shell := f.panes[len(f.panes)-1]
	if shell.Project != "/code/app" {
		t.Fatalf("the terminal's window has project %q", shell.Project)
	}
	e, _, _ := m.selected(m.ordered())
	if m.sel != shell.Window || e.kind != terminalEntry || e.term.Title != "zsh" {
		t.Fatalf("sel %s (%+v), want the terminal %s", m.sel, e, shell.Window)
	}
	if !f.staged(shell.ID) || !slices.Equal(f.besides, []string{"%0"}) || len(f.selects) != 0 || f.focus != shell.ID {
		t.Fatalf("list in %s, besides %v, selects %v, focus %s", f.stage(), f.besides, f.selects, f.focus)
	}
	// It is listed under its project, after the session.
	secs := m.sections()
	if len(secs) != 1 || len(secs[0].Entries) != 2 || secs[0].Entries[1].key() != shell.Window {
		t.Fatalf("sections %+v", secs)
	}
	// Back to it later, enter goes to the split used last, beside the list.
	m = pressSettled(t, m, "k", "j", "enter")
	if !f.staged(shell.ID) || len(f.besides) != 2 {
		t.Fatalf("list in %s, besides %v", f.stage(), f.besides)
	}
}

func TestTWithASearchHidingItClearsTheSearch(t *testing.T) {
	f := newFakeMux()
	m := repoModel(t, f)
	m = pressSettled(t, m, "/", "a", "u", "t", "h", "enter")
	m.filter = "auth" // left in place, as an enter with no match leaves it
	m = pressSettled(t, m, "t")
	if m.filter != "" || m.sel != f.panes[len(f.panes)-1].Window {
		t.Fatalf("filter %q, sel %s", m.filter, m.sel)
	}
}

func TestXClosesATerminalAfterAYes(t *testing.T) {
	f := newFakeMux()
	m := repoModel(t, f)
	m = pressSettled(t, m, "t")
	win := m.sel
	m = pressSettled(t, m, "x")
	if m.confirm != win || !strings.Contains(ansi.Strip(m.View()), "Close terminal zsh?") {
		t.Fatalf("confirm %q", m.confirm)
	}
	m = pressSettled(t, m, "n")
	if m.confirm != "" || len(f.killed) != 0 {
		t.Fatalf("a no closed it: confirm %q killed %v", m.confirm, f.killed)
	}
	m = pressSettled(t, m, "x", "y")
	if !slices.Equal(f.killed, []string{win}) || f.find("%0") < 0 {
		t.Fatalf("killed %v, list %d", f.killed, f.find("%0"))
	}
	// The list left the window before it closed, and the cursor is on
	// the entry before it, the last in the list.
	if m.sel != "s1" || m.shown(win) || !strings.Contains(m.note, "closed zsh") || !f.staged(paneWithTTY(f, "ttys011")) {
		t.Fatalf("sel %s, note %q, list in %s", m.sel, m.note, f.stage())
	}
}

// Closing a terminal puts the cursor on the entry after it.
func TestClosingATerminalSelectsTheNextEntry(t *testing.T) {
	f := newFakeMux()
	m := repoModel(t, f)
	m = pressSettled(t, m, "t")
	first := m.sel
	m = pressSettled(t, m, "t")
	second := m.sel
	m = pressSettled(t, m, "k")
	if m.sel != first {
		t.Fatalf("sel %s, want the first terminal %s", m.sel, first)
	}
	m = pressSettled(t, m, "x", "y")
	if m.sel != second || !f.staged(f.panes[len(f.panes)-1].ID) {
		t.Fatalf("sel %s, want the next terminal %s; list in %s", m.sel, second, f.stage())
	}
}

// A session that ends leaves the cursor on its neighbour, not the top.
func TestAnEndedSessionSelectsTheNextEntry(t *testing.T) {
	f := newFakeMux()
	a, b, c := row("s1", "a", "", 11), row("s2", "b", "", 12), row("s3", "c", "", 13)
	m := stagedModel(t, f, a, b, c)
	m = pressSettled(t, m, "j")
	if m.sel != "s2" {
		t.Fatalf("sel %s", m.sel)
	}
	// Its claude exits and its pane closes, and then claude drops it.
	f.exit(paneWithTTY(f, "ttys012"))
	m, cmd := step(m, panesMsg{panes: slices.Clone(f.panes), gen: m.spawns})
	m = settle(t, m, cmd)
	m, cmd = step(m, rowsMsg{rows: []fleet.Row{a, c}, ttys: map[int]string{11: "ttys011", 13: "ttys013"}})
	m = settle(t, m, cmd)
	if m.sel != "s3" || !f.staged(paneWithTTY(f, "ttys013")) {
		t.Fatalf("sel %s, want s3", m.sel)
	}
}

// claude exits, and the split the user made beside it stays: the cursor
// stays in that window, now a terminal, so the list does not hop away from
// where the user is typing.
func TestTheCursorStaysInAWindowWhoseSessionEnded(t *testing.T) {
	f := newFakeMux()
	red, blue := row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12)
	m := stagedModel(t, f, red, blue)
	pane := paneWithTTY(f, "ttys012")
	if m.sel != "s2" || !f.staged(pane) {
		t.Fatalf("sel %s, list in %s", m.sel, f.stage())
	}
	split := f.split(pane)
	m, cmd := step(m, panesMsg{panes: slices.Clone(f.panes), gen: m.spawns})
	m = settle(t, m, cmd)
	moves := len(f.moves)
	f.exit(pane)
	m, cmd = step(m, panesMsg{panes: slices.Clone(f.panes), gen: m.spawns})
	m = settle(t, m, cmd)
	if m.sel != split.Window || !f.staged(split.ID) || len(f.moves) != moves {
		t.Fatalf("sel %s, list in %s, moves %d then %d", m.sel, f.stage(), moves, len(f.moves))
	}
	// claude stops listing it, and nothing moves.
	m, cmd = step(m, rowsMsg{rows: []fleet.Row{red}, ttys: map[int]string{11: "ttys011"}})
	m = settle(t, m, cmd)
	if m.sel != split.Window || len(f.moves) != moves {
		t.Fatalf("sel %s, moves %d then %d", m.sel, moves, len(f.moves))
	}
}

// A claude started by hand in a terminal exits back to its shell: the row
// goes, the pane stays, and so does the cursor.
func TestTheCursorStaysWhenClaudeExitsToItsShell(t *testing.T) {
	f := newFakeMux()
	red, blue := row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12)
	m := stagedModel(t, f, red, blue)
	pane := paneWithTTY(f, "ttys012")
	m, cmd := step(m, rowsMsg{rows: []fleet.Row{red}, ttys: map[int]string{11: "ttys011"}})
	m = settle(t, m, cmd)
	if m.sel != f.window(pane) || !f.staged(pane) {
		t.Fatalf("sel %s, list in %s", m.sel, f.stage())
	}
}

func TestAnEmptyPinnedProjectHasAPlaceholder(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f)
	if err := m.home.PinProject("/code/empty"); err != nil {
		t.Fatal(err)
	}
	m, cmd := step(m, rowsMsg{pinned: m.home.Projects()})
	m = settle(t, m, cmd)
	e, _, ok := m.selected(m.ordered())
	if !ok || e.kind != projectEntry || m.sel != "project:/code/empty" || !f.staged("%1") {
		t.Fatalf("sel %s, entry %+v, list in %s", m.sel, e, f.stage())
	}
	if text := ansi.Strip(f.drawn["ttys901"]); !strings.Contains(text, "No sessions in /code/empty. c starts Claude here") {
		t.Fatalf("holder shows %q", text)
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "no sessions") || !strings.Contains(view, "x unpin") {
		t.Fatalf("view %s", view)
	}
	need, working, idle := m.counts()
	if need+working+idle != 0 {
		t.Fatal("the placeholder counts as a session")
	}
	// c starts claude there.
	m = pressSettled(t, m, "c")
	if len(f.spawned) != 1 || f.spawned[0].dir != "/code/empty" || f.panes[f.find(m.opened.ID)].Project != "/code/empty" {
		t.Fatalf("spawned %+v", f.spawned)
	}
	// x unpins it at once.
	m, cmd = press(m, "esc", "g", "x")
	if cmd == nil || m.confirm != "" {
		t.Fatalf("x on the placeholder: confirm %q", m.confirm)
	}
	msg := cmd().(pinnedMsg)
	if msg.err != nil || !msg.unpin || msg.root != "/code/empty" || len(m.home.Projects()) != 0 {
		t.Fatalf("done %+v, pinned %v", msg, m.home.Projects())
	}
	if m = update(m, msg); m.shown("project:/code/empty") || m.note != "unpinned /code/empty" {
		t.Fatalf("after the unpin: note %q, entries %v", m.note, m.ordered())
	}
}

func TestTInAPinnedProjectAdoptsItsTerminal(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f)
	m, cmd := step(m, rowsMsg{pinned: []string{"/code/empty"}})
	m = settle(t, m, cmd)
	m = pressSettled(t, m, "t")
	e, _, _ := m.selected(m.ordered())
	if len(f.spawned) != 1 || f.spawned[0].dir != "/code/empty" || e.kind != terminalEntry || e.term.Project != "/code/empty" {
		t.Fatalf("spawned %+v, sel %+v", f.spawned, e)
	}
	if m.shown("project:/code/empty") {
		t.Fatal("the placeholder stayed beside the terminal")
	}
}

// One failed refresh keeps what the last one found: the cursor stays and
// the list does not move, then or when the next refresh brings it all back.
func TestAFailedRefreshKeepsTheList(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	m = pressSettled(t, m, "t", "k")
	sel, cur, rows, ttys := m.sel, f.stage(), m.rows, m.ttys
	m, cmd := step(m, rowsMsg{err: errors.New("claude agents: timeout")})
	m = settle(t, m, cmd)
	if m.sel != sel || f.stage() != cur || len(m.rows) != 2 || m.rowsErr == nil {
		t.Fatalf("after a failed refresh: sel %s, list in %s, rows %d, err %v", m.sel, f.stage(), len(m.rows), m.rowsErr)
	}
	m, cmd = step(m, rowsMsg{rows: rows, ttys: ttys})
	m = settle(t, m, cmd)
	if m.sel != sel || f.stage() != cur || m.rowsErr != nil {
		t.Fatalf("after the next refresh: sel %s, list in %s", m.sel, f.stage())
	}
}

// blurred is m with the keyboard in another pane, as tmux reports it.
func blurred(t tester, m model) model {
	t.Helper()
	m, cmd := step(m, tea.BlurMsg{})
	return settle(t, m, cmd)
}

// claude typed into a terminal turns its window into a session's. The
// terminal's entry goes and the session's comes, and since the user is
// typing there, the cursor goes to the session and the list stays.
func TestClaudeTypedInATerminalKeepsTheList(t *testing.T) {
	f := newFakeMux()
	m := repoModel(t, f)
	m = pressSettled(t, m, "t")
	shell := f.panes[len(f.panes)-1]
	if !f.staged(shell.ID) {
		t.Fatalf("setup: list in %s", f.stage())
	}
	m = blurred(t, m)
	moves, selects := len(f.moves), len(f.selects)
	r := row("new", "typed", "typed", 44)
	r.Cwd = "/code/app"
	ttys := maps.Clone(m.ttys)
	ttys[44] = shell.TTY
	m, cmd := step(m, m.rowsMsg(append(slices.Clone(m.rows), r), ttys, nil, nil))
	m = settle(t, m, cmd)
	if f.stage() != shell.Window || len(f.moves) != moves || len(f.selects) != selects || m.sel != "new" {
		t.Fatalf("list in %s (was %s), moves %d then %d, sel %s", f.stage(), shell.Window, moves, len(f.moves), m.sel)
	}
}

// A session whose id changes under the same pane, as /clear does, keeps
// the list while the user types in it.
func TestANewSessionIDKeepsTheListWhileTyping(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	m = blurred(t, pressSettled(t, m, "j"))
	cur, moves := f.stage(), len(f.moves)
	rows := slices.Clone(m.rows)
	for i := range rows {
		if rows[i].SessionID == m.sel {
			rows[i].SessionID = "s-new"
		}
	}
	m, cmd := step(m, rowsMsg{rows: rows, ttys: m.ttys})
	m = settle(t, m, cmd)
	if f.stage() != cur || len(f.moves) != moves || m.sel != "s-new" {
		t.Fatalf("list in %s (was %s), sel %s", f.stage(), cur, m.sel)
	}
	// Back in the list, the cursor rules apply again, and nothing moves
	// since the cursor is where the list is.
	m, cmd = step(m, tea.FocusMsg{})
	settle(t, m, cmd)
	if f.stage() != cur || len(f.moves) != moves {
		t.Fatalf("focus moved the list to %s", f.stage())
	}
}

// A search the selected session stops matching, while the user types in
// it, moves the cursor but not the list.
func TestASearchThatLosesTheSessionKeepsTheList(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "auth", 12))
	m = pressSettled(t, m, "/", "a", "u", "t", "h", "esc", "j")
	m.filter = "auth"
	m = blurred(t, m)
	cur, moves := f.stage(), len(f.moves)
	rows := slices.Clone(m.rows)
	for i := range rows {
		if rows[i].SessionID == m.sel {
			rows[i].Title = "other"
		}
	}
	m, cmd := step(m, rowsMsg{rows: rows, ttys: m.ttys})
	m = settle(t, m, cmd)
	if f.stage() != cur || len(f.moves) != moves {
		t.Fatalf("list in %s (was %s)", f.stage(), cur)
	}
}

// Both homes' views share one server. A session of this home started in
// the other home's view gets the holder, which says so, and the list never
// moves into the other view's session.
func TestASessionInAnotherOverviewSessionGetsTheHolder(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11))
	f.add("overview-other", "ttys777")
	ttys := map[int]string{11: m.ttys[11], 22: "ttys777"}
	m, cmd := step(m, rowsMsg{rows: append(slices.Clone(m.rows), row("s2", "zzz", "x", 22)), ttys: ttys})
	m = settle(t, m, cmd)
	m = pressSettled(t, m, "j")
	if m.sel != "s2" || f.panes[f.find("%0")].Session != "ov" || !f.staged("%1") {
		t.Fatalf("sel %s, list in session %s, window %s", m.sel, f.panes[f.find("%0")].Session, f.stage())
	}
	if text := ansi.Strip(f.drawn["ttys901"]); !strings.Contains(text, "runs in another overview session") {
		t.Fatalf("holder shows %q", text)
	}
}

// An attach that lands after the cursor has moved on does not take the
// keyboard there.
func TestALateAttachDoesNotTakeTheKeyboard(t *testing.T) {
	f := newFakeMux()
	bg := row("b1", "bgwork", "nightly", 0)
	bg.Kind, bg.ID = "background", "abc123"
	m := stagedModel(t, f, bg, row("s1", "red", "auth", 11))
	m = pressSettled(t, m, "g")
	m, spawned := press(m, "enter")
	m = pressSettled(t, m, "j")
	m = settle(t, m, spawned)
	if len(f.spawned) != 1 || len(f.selects) != 0 || !f.staged(paneWithTTY(f, "ttys011")) {
		t.Fatalf("spawned %d, selects %v, list in %s", len(f.spawned), f.selects, f.stage())
	}
}

// A move that fails forgets the focus it was for, so the retry after the
// next listing does not take the keyboard.
func TestAFailedMoveForgetsTheFocus(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11))
	f.moveErr = errors.New("tmux join-pane: boom")
	m = pressSettled(t, m, "c")
	if m.entering != "" || len(f.selects) != 0 {
		t.Fatalf("entering %q, selects %v", m.entering, f.selects)
	}
	f.moveErr = nil
	m, cmd := step(m, panesMsg{panes: slices.Clone(f.panes), gen: m.landed})
	settle(t, m, cmd)
	if !f.staged(m.opened.ID) || len(f.selects) != 0 {
		t.Fatalf("list in %s, selects %v", f.stage(), f.selects)
	}
}

// A listing answered while a spawn is in flight may show its pane without
// its options, so it is dropped even though it was asked for after the
// last spawn landed.
func TestAListingDuringASpawnIsDropped(t *testing.T) {
	f := newFakeMux()
	bg := row("b1", "bgwork", "nightly", 0)
	bg.Kind, bg.ID = "background", "abc123"
	m := stagedModel(t, f, bg)
	m, spawned := press(m, "enter")
	gen := m.landed
	p, _ := f.Spawn("ov", "", nil, "claude", "attach", "abc123") // not yet marked
	m, _ = step(m, panesMsg{panes: slices.Clone(f.panes), gen: gen})
	if m.alive(p.ID) {
		t.Fatalf("a listing taken mid-spawn shows %s", p.ID)
	}
	settle(t, m, spawned)
}

// A refresh that read the pinned projects before a pin landed does not
// take the pin away again.
func TestARefreshFromBeforeAPinKeepsThePin(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f)
	stale := m.rowsMsg(nil, nil, nil, nil)
	m.pins++ // as pressing enter in the picker does
	m, _ = step(m, pinnedMsg{root: "/code/new"})
	if m.sel != "project:/code/new" {
		t.Fatalf("sel %s after the pin", m.sel)
	}
	m, cmd := step(m, stale)
	m = settle(t, m, cmd)
	if !slices.Contains(m.pinned, "/code/new") || m.sel != "project:/code/new" {
		t.Fatalf("a stale refresh: pinned %v, sel %s", m.pinned, m.sel)
	}
}
