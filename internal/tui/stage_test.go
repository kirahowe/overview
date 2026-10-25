package tui

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"pgregory.net/rapid"

	"overview/internal/fleet"
	"overview/internal/tmux"
	"overview/internal/vcs"
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
	widths  []int             // the widths SetWidth gave the list
	layouts []string          // the windows whose layout was read
	set     map[string]string // the layouts set, by window
	detach  int
	splits  []spawnCall // the reviews Split opened, dir and command

	drawn map[string]string // what the view wrote to each tty
	focus string            // the active pane of the window shown

	zooms []string // the zooms SetZoom was asked for, "%0 true"

	// The rules' titles, by pane, as SetPaneOptions set them.
	titles map[string]string

	// panels says a client of the session is in the panels mode, as the
	// listing shows it, and entered counts the times EnterPanels was asked.
	panels    bool
	entered   int
	panelsErr error

	moveErr error
}

type spawnCall struct {
	session, dir string
	env, command []string
}

// newFakeMux holds the view's pane %0, alone in its window, and a holder
// %1 in a window of its own, both in session ov.
func newFakeMux() *fakeMux {
	f := &fakeMux{drawn: map[string]string{}, set: map[string]string{}, titles: map[string]string{},
		current: map[string]string{"ov": "@0"}, next: 2}
	f.panes = []tmux.Pane{
		{ID: "%0", TTY: "ttys900", Session: "ov", Window: "@0", W: 160, H: 40, List: true},
		{ID: "%1", TTY: "ttys901", Session: "ov", Window: "@1", W: 160, H: 40, Holder: true},
	}
	return f
}

// Panes lists the panes, the one the keyboard is in active.
func (f *fakeMux) Panes() ([]tmux.Pane, error) {
	out := slices.Clone(f.panes)
	for i := range out {
		out[i].Active = out[i].ID == f.focus
		out[i].Panels = f.panels && out[i].Session == "ov"
	}
	return out, nil
}

// lay places the panes of window w as tmux would: the list down the left,
// and the others beside it, one above another in the order they came.
func (f *fakeMux) lay(w string) {
	lw, n := 0, 0
	for _, p := range f.panes {
		if p.Window == w && p.List {
			lw = p.W + 1
		} else if p.Window == w {
			n++
		}
	}
	k := 0
	for i, p := range f.panes {
		switch {
		case p.Window != w:
		case p.List:
			f.panes[i].X, f.panes[i].Y = 0, 0
		default:
			f.panes[i].X, f.panes[i].Y, f.panes[i].H = lw, k*(40/n), 40/n-1
			k++
		}
	}
}

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
		Command: "zsh", Path: beside.Path, Project: beside.Project, Task: beside.Task}
	f.next++
	f.panes = append(f.panes, p)
	f.lay(beside.Window)
	return f.panes[len(f.panes)-1]
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
	if name != tmux.ProjectOption && name != tmux.TaskOption {
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
		if p.Window == w && name == tmux.ProjectOption {
			f.panes[i].Project = value
		} else if p.Window == w {
			f.panes[i].Task = value
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

// SetPaneOptions sets the rules' options in order, stopping at the first
// pane that is not there, as tmux does.
func (f *fakeMux) SetPaneOptions(sets []tmux.PaneSet) error {
	for _, ps := range sets {
		switch {
		case f.find(ps.Pane) < 0:
			return errors.New("can't find pane " + ps.Pane)
		case ps.Name == tmux.TitleOption:
			f.titles[ps.Pane] = ps.Value
		default:
			return fmt.Errorf("unexpected pane option %s on %s", ps.Name, ps.Pane)
		}
	}
	return nil
}

// EnterPanels puts the client in the panels mode.
func (f *fakeMux) EnterPanels() error {
	f.entered++
	if f.panelsErr == nil {
		f.panels = true
	}
	return f.panelsErr
}

// SetFrame turns the rules of pane's window on or off.
func (f *fakeMux) SetFrame(pane string, on bool) error {
	if f.find(pane) < 0 {
		return errors.New("can't find pane " + pane)
	}
	f.setFrame(f.window(pane), on)
	return nil
}

func (f *fakeMux) setFrame(w string, on bool) {
	for i, p := range f.panes {
		if p.Window == w {
			f.panes[i].Framed = on
		}
	}
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

func (f *fakeMux) KillPane(pane string) error {
	if f.find(pane) < 0 {
		return errors.New("can't find pane " + pane)
	}
	f.exit(pane)
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
	// Moving a pane ends the zoom of both windows, as tmux does.
	f.setZoom(f.panes[i].Window, false)
	f.setZoom(f.panes[j].Window, false)
	w, err := strconv.Atoi(size)
	if err != nil {
		w = 48 // a share of a window 160 wide
	}
	f.panes[i].Session, f.panes[i].Window, f.panes[i].W, f.panes[i].WinW = f.panes[j].Session, f.panes[j].Window, w, f.panes[j].WinW
	f.current[f.panes[j].Session] = f.panes[j].Window
	f.focus = list // join-pane makes the list the active pane
	f.lay(f.panes[j].Window)
	return nil
}

func (f *fakeMux) SetWidth(pane string, cols int) error {
	i := f.find(pane)
	if i < 0 {
		return errors.New("can't find pane")
	}
	f.widths = append(f.widths, cols)
	f.panes[i].W = cols
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

// SetZoom zooms pane's window or ends its zoom, as the one tmux command
// does: idempotently, and not at all for a pane alone in its window.
func (f *fakeMux) SetZoom(pane string, on bool) error {
	if f.find(pane) < 0 {
		return errors.New("can't find pane " + pane)
	}
	f.zooms = append(f.zooms, fmt.Sprint(pane, " ", on))
	if w := f.window(pane); f.inWindow(w) > 1 {
		f.setZoom(w, on)
	}
	f.setFrame(f.window(pane), !on)
	return nil
}

// zoomed says whether pane's window is zoomed.
func (f *fakeMux) zoomed(pane string) bool {
	i := f.find(pane)
	return i >= 0 && f.panes[i].Zoomed
}

// setZoom zooms window w or ends its zoom, which tmux shows on every pane
// of the window.
func (f *fakeMux) setZoom(w string, on bool) {
	for i, p := range f.panes {
		if p.Window == w {
			f.panes[i].Zoomed = on
		}
	}
}

// Split puts a new pane in target's window, which ends its zoom, as
// tmux's split-window does.
func (f *fakeMux) Split(target, dir, size string, env []string, command ...string) (tmux.Pane, error) {
	f.setZoom(f.window(target), false)
	p := f.split(target)
	f.splits = append(f.splits, spawnCall{dir: dir, env: env, command: command})
	f.focus = p.ID
	return p, nil
}

// exit ends a pane's process, which closes it.
func (f *fakeMux) exit(id string) {
	if i := f.find(id); i >= 0 {
		// tmux ends the zoom of a window that loses a pane.
		f.setZoom(f.panes[i].Window, false)
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
	d.modTime = func(string) time.Time { return time.Time{} }
	d.recentTurns = func(string, int) []fleet.Turn { return nil }
	d.glob = func(string) []string { return nil }
	d.readFile = func(p string) ([]byte, error) { return nil, errors.New(p + ": no files in tests") }
	d.vcs = func(dir string, args ...string) ([]byte, error) { return nil, errors.New("no git or jj in tests") }
	d.untracked = func(p string) (vcs.Untracked, error) { return vcs.Untracked{}, errors.New(p + ": no files in tests") }
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
	want := paneWithTTY(f, m.in.TTYs[m.ordered()[0].sess.PID])
	if !f.staged(want) || m.windowOf(m.self) != f.stage() {
		t.Fatalf("the list is in %s (model %s), want the selected session's %s", f.stage(), m.windowOf(m.self), want)
	}
	m = pressSettled(t, m, "j")
	want = paneWithTTY(f, m.in.TTYs[m.ordered()[1].sess.PID])
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
	attach := paneOf(m, bg)
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
	attach := paneOf(m, bg)
	if attach == "" || m.spawns != gen+1 {
		t.Fatalf("attach %q, spawns %d", attach, m.spawns)
	}
	unmarked := slices.Clone(f.panes)
	unmarked[f.find(attach)].Attach = ""
	moves := len(f.moves)
	m, cmd := step(m, panesMsg{panes: unmarked, gen: gen})
	m = settle(t, m, cmd)
	if paneOf(m, bg) != attach || len(f.moves) != moves {
		t.Fatalf("a stale listing lost the attach: pane %q, moves %d then %d", paneOf(m, bg), moves, len(f.moves))
	}
	// A current listing is taken: the window, unmarked, is now a plain
	// terminal, and the cursor stays in it.
	f.panes = unmarked
	m, cmd = step(m, panesMsg{panes: unmarked, gen: m.spawns})
	m = settle(t, m, cmd)
	if paneOf(m, bg) != "" || m.sel != f.window(attach) || !f.staged(attach) {
		t.Fatalf("a current listing was not taken: pane %q, sel %s, list in %s", paneOf(m, bg), m.sel, f.stage())
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

// setWinW makes every pane in window win's window w wide.
func (f *fakeMux) setWinW(win string, w int) {
	for i := range f.panes {
		if f.panes[i].Window == win {
			f.panes[i].WinW = w
		}
	}
}

// Browsing moves the list into windows left at the width the terminal had
// when they were last shown. tmux snaps each to the terminal's width, which
// takes the difference out of the list, and before the view kept its width
// a few moves took it to a column, where the view panicked and its pane
// closed. The next listing gives the list its width back.
func TestASqueezedListGetsItsWidthBack(t *testing.T) {
	f := newFakeMux()
	for _, p := range f.panes {
		f.setWinW(p.Window, 283)
	}
	m := stagedModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	// The terminal is 229 wide, and the user dragged the list to 60.
	f.setWinW(f.stage(), 229)
	f.panes[f.find("%0")].W = 60
	m, cmd := step(m, panesMsg{panes: slices.Clone(f.panes), gen: m.spawns})
	m = settle(t, m, cmd)
	if m.width.want != 60 || len(f.widths) != 0 {
		t.Fatalf("kept %d, resized to %v, after a drag to 60", m.width.want, f.widths)
	}
	for _, key := range []string{"j", "k", "j"} {
		// Every other window was last shown when the terminal was 283 wide.
		for _, p := range f.panes {
			if p.Window != f.stage() {
				f.setWinW(p.Window, 283)
			}
		}
		m = pressSettled(t, m, key)
		if got := f.moves[len(f.moves)-1]; got != "60" {
			t.Fatalf("moved %s wide, want 60", got)
		}
		// The window snaps from 283 to 229, and tmux takes 54 from the list.
		f.setWinW(f.stage(), 229)
		f.panes[f.find("%0")].W = 6
		m, cmd = step(m, panesMsg{panes: slices.Clone(f.panes), gen: m.spawns})
		m = settle(t, m, cmd)
		if w := f.panes[f.find("%0")].W; w != 60 || m.width.want != 60 {
			t.Fatalf("the list is %d wide, keeping %d, want 60", w, m.width.want)
		}
	}
}

// A list squeezed past listMin by a terminal that narrowed is put back to
// it, and when the terminal widens again it gets the width it had.
func TestANarrowedTerminalKeepsTheListUsable(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	f.setWinW(f.stage(), 160)
	f.panes[f.find("%0")].W = 50
	m, cmd := step(m, panesMsg{panes: slices.Clone(f.panes), gen: m.spawns})
	m = settle(t, m, cmd)
	f.setWinW(f.stage(), 45)
	f.panes[f.find("%0")].W = 1
	m, cmd = step(m, panesMsg{panes: slices.Clone(f.panes), gen: m.spawns})
	m = settle(t, m, cmd)
	if w := f.panes[f.find("%0")].W; w != listMin {
		t.Fatalf("the list is %d wide in 45, want %d", w, listMin)
	}
	f.setWinW(f.stage(), 160)
	m, cmd = step(m, panesMsg{panes: slices.Clone(f.panes), gen: m.spawns})
	settle(t, m, cmd)
	if w := f.panes[f.find("%0")].W; w != 50 {
		t.Fatalf("the list is %d wide in 160 again, want the 50 it had", w)
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

// q detaches and leaves the view running. No key quits the view, which is
// the point of the app: Q, typed by mistake for q, does nothing.
func TestQDetachesAndNothingQuits(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11))
	pressSettled(t, m, "q")
	if f.detach != 1 {
		t.Fatalf("detached %d times", f.detach)
	}
	if _, cmd := step(m, keyMsg("Q")); cmd != nil {
		if msg := cmd(); msg != nil {
			if _, ok := msg.(tea.QuitMsg); ok {
				t.Fatal("Q quit the view")
			}
		}
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
					e.kind == sessionEntry && e.sess.Background() && p.Attach == e.sess.ID,
					e.kind == sessionEntry && !e.sess.Background() && p.TTY == m.in.TTYs[e.sess.PID]:
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
				if m.windowOf(m.self) != f.stage() {
					t.Fatalf("the list is in %s, the model thinks %s", f.stage(), m.windowOf(m.self))
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
	m, cmd := step(m, m.rowsMsg(m.in.Rows, m.in.TTYs, m.in.Hosts, m.in.Pinned))
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
	// It is listed under its project, after the session, and not in the
	// agents list.
	secs := m.sections()
	if len(secs) != 2 || len(secs[0].Entries) != 2 || secs[0].Entries[1].key() != shell.Window || len(secs[1].Entries) != 1 {
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
	if view := ansi.Strip(m.View()); !strings.Contains(view, "+ c claude · t terminal") || !strings.Contains(view, "x unpin") {
		t.Fatalf("view %s", view)
	}
	need, working, idle := counts(m)
	if need+working+idle != 0 {
		t.Fatal("the placeholder counts as a session")
	}
	// c starts claude there.
	m = pressSettled(t, m, "c")
	if len(f.spawned) != 1 || f.spawned[0].dir != "/code/empty" || f.panes[f.find(m.opened.ID)].Project != "/code/empty" {
		t.Fatalf("spawned %+v", f.spawned)
	}
	// x asks first, and anything but y leaves it pinned.
	m, cmd = press(m, "esc", "g", "x")
	if m.confirm != "project:/code/empty" || !strings.Contains(ansi.Strip(m.View()), "Unpin /code/empty?") {
		t.Fatalf("x on the placeholder: confirm %q", m.confirm)
	}
	if m, _ = press(m, "n"); m.confirm != "" || m.note != "left it pinned" || len(m.home.Projects()) != 1 {
		t.Fatalf("n: confirm %q note %q pinned %v", m.confirm, m.note, m.home.Projects())
	}
	// y unpins it.
	m, cmd = press(m, "x", "y")
	if cmd == nil || m.confirm != "" {
		t.Fatalf("x y on the placeholder: confirm %q", m.confirm)
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
	if len(f.spawned) != 1 || f.spawned[0].dir != "/code/empty" || e.kind != terminalEntry || e.term.Root != "/code/empty" {
		t.Fatalf("spawned %+v, sel %+v", f.spawned, e)
	}
	if m.shown("project:/code/empty") {
		t.Fatal("the placeholder stayed beside the terminal")
	}
}

// Closing the only terminal of a pinned project leaves the cursor on the
// placeholder that takes its place, not on another project's entry, so
// a second x does not reach into that project.
func TestClosingAPinnedProjectsLastTerminalKeepsTheCursorThere(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f)
	m, cmd := step(m, rowsMsg{pinned: []string{"/code/beta", "/code/gamma"}})
	m = settle(t, m, cmd)
	m = pressSettled(t, selectKey(t, m, "project:/code/beta"), "t")
	beta := m.sel
	m = pressSettled(t, selectKey(t, m, "project:/code/gamma"), "t")
	gamma := m.sel
	if gamma == beta || m.shown("project:/code/gamma") {
		t.Fatalf("sel %s, beta's terminal %s", gamma, beta)
	}
	m = pressSettled(t, m, "x", "y")
	if m.sel != "project:/code/gamma" {
		t.Fatalf("sel %s after closing %s, want gamma's placeholder", m.sel, gamma)
	}
	if m = pressSettled(t, m, "x"); m.confirm != "project:/code/gamma" || !strings.Contains(ansi.Strip(m.View()), "Unpin /code/gamma?") {
		t.Fatalf("a second x asks about %q, want to unpin gamma", m.confirm)
	}
}

// A session that starts in a pinned project with only its placeholder
// takes the cursor from the placeholder, so it stays in that project.
func TestASessionStartingInAPinnedProjectTakesItsPlaceholdersCursor(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f)
	m, cmd := step(m, rowsMsg{pinned: []string{"/code/beta", "/code/gamma"}})
	m = settle(t, m, cmd)
	m = selectKey(t, m, "project:/code/beta")
	s := row("s1", "red", "auth", 11)
	s.Cwd = "/code/beta"
	f.add("ov", "ttys011")
	m, cmd = step(m, rowsMsg{rows: []fleet.Row{s}, ttys: map[int]string{11: "ttys011"}, pinned: m.in.Pinned})
	m = settle(t, m, cmd)
	if m.sel != "s1" {
		t.Fatalf("sel %s, want the session that started in beta", m.sel)
	}
}

// One failed refresh keeps what the last one found: the cursor stays and
// the list does not move, then or when the next refresh brings it all back.
func TestAFailedRefreshKeepsTheList(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	m = pressSettled(t, m, "t", "k")
	sel, cur, rows, ttys := m.sel, f.stage(), m.in.Rows, m.in.TTYs
	m, cmd := step(m, rowsMsg{err: errors.New("claude agents: timeout")})
	m = settle(t, m, cmd)
	if m.sel != sel || f.stage() != cur || len(m.in.Rows) != 2 || m.world().RefreshErr == nil {
		t.Fatalf("after a failed refresh: sel %s, list in %s, rows %d, err %v", m.sel, f.stage(), len(m.in.Rows), m.world().RefreshErr)
	}
	m, cmd = step(m, rowsMsg{rows: rows, ttys: ttys})
	m = settle(t, m, cmd)
	if m.sel != sel || f.stage() != cur || m.world().RefreshErr != nil {
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
	ttys := maps.Clone(m.in.TTYs)
	ttys[44] = shell.TTY
	m, cmd := step(m, m.rowsMsg(append(slices.Clone(m.in.Rows), r), ttys, nil, nil))
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
	rows := slices.Clone(m.in.Rows)
	for i := range rows {
		if rows[i].SessionID == m.sel {
			rows[i].SessionID = "s-new"
		}
	}
	m, cmd := step(m, rowsMsg{rows: rows, ttys: m.in.TTYs})
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

// Both homes' views share one server. A session of this home started in
// the other home's view gets the holder, which says so, and the list never
// moves into the other view's session.
func TestASessionInAnotherOverviewSessionGetsTheHolder(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11))
	f.add("overview-other", "ttys777")
	ttys := map[int]string{11: m.in.TTYs[11], 22: "ttys777"}
	m, cmd := step(m, rowsMsg{rows: append(slices.Clone(m.in.Rows), row("s2", "zzz", "x", 22)), ttys: ttys})
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
	if !slices.Contains(m.in.Pinned, "/code/new") || m.sel != "project:/code/new" {
		t.Fatalf("a stale refresh: pinned %v, sel %s", m.in.Pinned, m.sel)
	}
}

// Keys that reach the list mean the user is there, even if tmux never said
// the focus came back, so they move the cursor as ever.
func TestKeysWhileMarkedBlurredMoveTheCursor(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	sel := m.sel
	m = pressSettled(t, blurred(t, m), "j")
	if m.sel == sel || m.blurred || !f.staged(m.want()) {
		t.Fatalf("j while marked blurred: sel %s (was %s), blurred %v, list in %s", m.sel, sel, m.blurred, f.stage())
	}
}

// The wheel over the list leaves tmux's focus in the session, and still
// moves the cursor.
func TestTheWheelOverTheListWhileBlurredMovesTheCursor(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	sel := m.sel
	m = blurred(t, m)
	m, cmd := step(m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown, X: 2, Y: 3})
	m = settle(t, m, cmd)
	if m.sel == sel {
		t.Fatalf("the wheel left the cursor on %s", m.sel)
	}
}

// A click on a row selects it, though no focus event came first.
func TestAClickWhileBlurredSelects(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	m = blurred(t, m)
	other := "s1"
	if m.sel == "s1" {
		other = "s2"
	}
	m, cmd := step(m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 2, Y: screenY(m, other)})
	m = settle(t, m, cmd)
	if m.sel != other || !f.staged(m.want()) {
		t.Fatalf("sel %s, want %s; list in %s", m.sel, other, f.stage())
	}
}

// A search that hides the session the user types in moves nothing, then
// or when the keyboard comes back to the list, which shows it again.
func TestASearchLostWhileTypingKeepsTheListOnReturn(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "auth", 12))
	m = pressSettled(t, m, "/", "a", "u", "t", "h", "esc", "j")
	m.filter = "auth"
	m = blurred(t, m)
	cur, sel := f.stage(), m.sel
	rows := slices.Clone(m.in.Rows)
	for i := range rows {
		if rows[i].SessionID == m.sel {
			rows[i].Title = "other"
		}
	}
	m, cmd := step(m, rowsMsg{rows: rows, ttys: m.in.TTYs})
	m = settle(t, m, cmd)
	if m.sel != sel || f.stage() != cur {
		t.Fatalf("while typing: sel %s (was %s), list in %s (was %s)", m.sel, sel, f.stage(), cur)
	}
	m, cmd = step(m, tea.FocusMsg{})
	m = settle(t, m, cmd)
	if m.sel != sel || f.stage() != cur || m.filter != "" {
		t.Fatalf("back in the list: sel %s, list in %s, filter %q", m.sel, f.stage(), m.filter)
	}
}

// failMark is a tmux on which marking the holder fails.
type failMark struct{ *fakeMux }

func (f failMark) SetPaneOption(pane, name, value string) error {
	if name == tmux.HolderOption {
		return errors.New("set-option: boom")
	}
	return f.fakeMux.SetPaneOption(pane, name, value)
}

// A holder that cannot be marked is closed again, so listing after listing
// does not leave a window of unmarked holders behind.
func TestAHolderThatCannotBeMarkedIsClosed(t *testing.T) {
	f := newFakeMux()
	f.exit("%1")
	m := stagedModel(t, f, row("s1", "red", "auth", 11))
	m.mux = failMark{f}
	for range 5 {
		m = settle(t, m, listPanes(m.mux, m.deps.roots, m.landed))
	}
	for _, p := range f.panes {
		if p.Command == "overview" {
			t.Fatalf("a holder was left behind: %+v", f.panes)
		}
	}
	if len(f.spawned) < 2 || !strings.Contains(m.note, "could not start the details pane") {
		t.Fatalf("spawned %d, note %q", len(f.spawned), m.note)
	}
}

// A spawn that fails lands all the same, so later listings are taken.
func TestAFailedSpawnLetsListingsThrough(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11))
	m.mux = failSpawn{f}
	m = pressSettled(t, m, "t")
	extra := f.add("ov", "ttys444")
	m = settle(t, m, listPanes(m.mux, m.deps.roots, m.landed))
	if m.spawns != m.landed || !m.alive(extra.ID) {
		t.Fatalf("spawns %d, landed %d, listing taken %v", m.spawns, m.landed, m.alive(extra.ID))
	}
}

type failSpawn struct{ *fakeMux }

func (f failSpawn) Spawn(string, string, []string, ...string) (tmux.Pane, error) {
	return tmux.Pane{}, errors.New("boom")
}

// A terminal closed while a spawn is in flight: the listing that comes
// back is not taken, but the closed window's panes go all the same, so no
// move aims at them.
func TestATerminalClosedMidSpawnLeavesNoDeadPanes(t *testing.T) {
	f := newFakeMux()
	m := repoModel(t, f)
	m = pressSettled(t, m, "t")
	win := m.sel
	m, cmd := press(m, "x", "y")
	m.spawns++ // a spawn asked for meanwhile
	m = settle(t, m, cmd)
	for _, p := range m.in.Panes {
		if p.Window == win {
			t.Fatalf("%s of the closed window %s is still listed", p.ID, win)
		}
	}
	if m.windowOf(m.self) != f.stage() {
		t.Fatalf("the model has the list in %s, tmux in %s", m.windowOf(m.self), f.stage())
	}
}

// When the first refresh fails, the holder says why instead of waiting.
func TestTheHolderShowsAFirstRefreshThatFailed(t *testing.T) {
	m := newModel(fleet.Home{Dir: t.TempDir()}, "")
	m.deps = fakeDeps(nil)
	m = update(m, rowsMsg{err: errors.New("claude agents --json: not found")})
	text := ansi.Strip(m.holderText(60, 10))
	if !strings.Contains(text, "claude agents --json: not found") || strings.Contains(text, "reading sessions") {
		t.Fatalf("holder shows %q", text)
	}
}

// Digests are listed and read in Cmds: the newest shows first, [ reads an
// older one, and a new digest arriving leaves a reader of an older one
// where they are.
func TestDigestsAreReadInCmds(t *testing.T) {
	m := newTestModel(t)
	dir := filepath.Join(m.home.Dir, "overview", "digests")
	write := func(name, text string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("1.md", "first")
	write("2.md", "second")
	m = settle(t, m, m.listDigests(true))
	if m.digest != "second" || m.digestIdx != 1 {
		t.Fatalf("digest %q at %d", m.digest, m.digestIdx)
	}
	m, cmd := press(m, "d", "[")
	if m.digest != "second" {
		t.Fatal("[ read the file inside Update")
	}
	m = settle(t, m, cmd)
	if m.digest != "first" || m.digestIdx != 0 {
		t.Fatalf("after [: digest %q at %d", m.digest, m.digestIdx)
	}
	write("3.md", "third")
	m = settle(t, m, m.listDigests(false))
	if m.digest != "first" || len(m.digests) != 3 {
		t.Fatalf("a new digest moved the reader: %q of %d", m.digest, len(m.digests))
	}
}

// The selected session's last turns are read in a Cmd when its transcript
// changes, as the refresh found it, and never while drawing.
func TestTurnsAreReadWhenTheTranscriptChanges(t *testing.T) {
	f := newFakeMux()
	r := row("s1", "red", "auth", 11)
	r.Transcript = "/t/s1.jsonl"
	m := stagedModel(t, f, r)
	at, reads := time.Unix(100, 0), 0
	m.deps.modTime = func(string) time.Time { return at }
	m.deps.recentTurns = func(path string, n int) []fleet.Turn {
		reads++
		return []fleet.Turn{{Prompt: "fix the login page", Reply: "done"}}
	}
	m = refreshed(t, m)
	if reads != 1 || !strings.Contains(ansi.Strip(m.holderText(80, 40)), "fix the login page") {
		t.Fatalf("reads %d, holder %q", reads, ansi.Strip(m.holderText(80, 40)))
	}
	m = refreshed(t, m)
	_ = m.View()
	m.holderText(80, 40)
	if reads != 1 {
		t.Fatalf("an unchanged transcript was read again: %d reads", reads)
	}
	at = time.Unix(200, 0)
	m = refreshed(t, m)
	if reads != 2 {
		t.Fatalf("a changed transcript was not read: %d reads", reads)
	}
}

// A pane's rule names the session the World says it shows. The
// orchestrator is not in the World, so its pane is titled as a shell.
func TestAPanesRuleNamesTheWorldsSession(t *testing.T) {
	f := newFakeMux()
	orch := row("o", fleet.OrchestratorName, "", 13)
	m := stagedModel(t, f, row("s1", "red", "auth", 11), orch)
	byTTY := map[string]string{}
	for _, p := range f.panes {
		byTTY[p.TTY] = p.ID
	}
	if got := f.titles[byTTY["ttys011"]]; !strings.HasPrefix(got, "red") {
		t.Fatalf("red's pane is titled %q", got)
	}
	if got := f.titles[byTTY["ttys013"]]; strings.Contains(got, fleet.OrchestratorName) || !strings.HasPrefix(got, "$ ") {
		t.Fatalf("the orchestrator's pane is titled %q", got)
	}
	if _, ok := m.world().Sessions["o"]; ok {
		t.Fatal("the orchestrator is in the World")
	}
}

// The unpin box names a long project by the end of its path, which tells
// it from its neighbours.
func TestTheUnpinBoxKeepsTheEndOfAPath(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f)
	long := "/private/tmp/qa2/work/a-long-project-name"
	if err := m.home.PinProject(long); err != nil {
		t.Fatal(err)
	}
	m, cmd := step(m, rowsMsg{pinned: m.home.Projects()})
	m = settle(t, m, cmd)
	m = resized(t, m, 40, 20)
	m = selectKey(t, m, "project:"+long)
	m, _ = press(m, "x")
	if box := ansi.Strip(strings.Join(m.confirmBox(), "\n")); !strings.Contains(box, "Unpin …work/a-long-project-name?") {
		t.Fatalf("the unpin box:\n%s", box)
	}
}
