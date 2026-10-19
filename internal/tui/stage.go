package tui

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"

	"overview/internal/fleet"
	"overview/internal/tmux"
)

// The view runs in a pane of overview's own tmux server, and the window it
// is in is the stage. Every pane the view starts gets a window of its own
// in the view's session: the claude that c starts, `claude attach` for a
// background session once enter has opened one, and the holder, a pane the
// view draws a session's details into when it has neither, such as one
// inside another app. Moving the cursor moves the list into the window of
// the selected session's pane, as a full-height column on the left, so any
// splits the user made in that window come along and stay with it.

const (
	// panesEvery is how often the view rereads tmux's panes, so a session
	// that exits leaves the stage within about this long.
	panesEvery = 500 * time.Millisecond
	// listShare is the list's width when it has no width of its own to
	// keep, because it is alone in its window.
	listShare = "30%"
	// holderOption is the session option that remembers the holder pane.
	holderOption = "@overview-holder"
)

// multiplexer is the part of tmux.Server the view needs. Tests fake it.
type multiplexer interface {
	Panes() ([]tmux.Pane, error)
	Spawn(session, dir string, env []string, command ...string) (tmux.Pane, error)
	Move(list, target, size string) error
	Layout(window string) (string, error)
	SetLayout(window, layout string) error
	Select(pane string) error
	SelectBeside(pane string) error
	KillWindow(window string) error
	Detach() error
	SetOption(session, name, value string) error
	SetWindowOption(target, name, value string) error
	SetPaneOption(pane, name, value string) error
}

type spawnKind int

const (
	spawnHolder   spawnKind = iota
	spawnAttach             // claude attach for a background session
	spawnSession            // a new claude, from c
	spawnTerminal           // a shell, from t
)

// spawn is a pane to start in a window of its own.
type spawn struct {
	kind    spawnKind
	key     string // the background session's ID, for spawnAttach
	name    string // what to call it in a note
	session string
	dir     string // the project root, for spawnSession and spawnTerminal
	env     []string
	command []string
}

// marked is the spawned pane as the listing will show it once the Cmd has
// set its options: a session's or a terminal's window names its project,
// and an attach pane names its background session. It also says which
// option to set, as a pane or a window option.
func (sp spawn) marked(p tmux.Pane) (tmux.Pane, func(multiplexer) error) {
	switch sp.kind {
	case spawnSession, spawnTerminal:
		p.Project = sp.dir
		return p, func(mux multiplexer) error { return mux.SetWindowOption(p.ID, tmux.ProjectOption, sp.dir) }
	case spawnAttach:
		p.Attach = sp.key
		return p, func(mux multiplexer) error { return mux.SetPaneOption(p.ID, tmux.AttachOption, sp.key) }
	}
	return p, nil
}

type (
	panesTick time.Time
	// panesMsg is a listing of tmux's panes. gen is the model's spawns
	// when it was asked for: see model.spawns.
	panesMsg struct {
		panes []tmux.Pane
		gen   int
		err   error
	}
	// stagedMsg answers a stage Cmd with the panes as they are afterwards.
	stagedMsg struct {
		to    string
		panes []tmux.Pane
		gen   int
		err   error
	}
	spawnedMsg struct {
		spawn
		pane tmux.Pane
		err  error
	}
)

// SessionName is the tmux session the view for home runs in. tmux does not
// allow dots or colons in one.
func SessionName(home fleet.Home) string {
	return "overview-" + strings.NewReplacer(".", "_", ":", "_").Replace(home.Name())
}

func panesEveryTick() tea.Cmd {
	return tea.Tick(panesEvery, func(t time.Time) tea.Msg { return panesTick(t) })
}

func listPanes(mux multiplexer, gen int) tea.Cmd {
	return func() tea.Msg {
		panes, err := mux.Panes()
		return panesMsg{panes, gen, err}
	}
}

// shift is a step that puts the list beside the pane it wants: the list
// goes from window from into the window of pane to, size wide. fix says
// from keeps two or more panes, whose layout is then put right.
type shift struct {
	to, from, size string
	fix            bool
}

// plan decides how the list, pane self, gets beside pane want, given every
// pane on the server. It reports false when no move is needed, because want
// is already in the list's window, or is not listed.
func plan(self, want string, panes []tmux.Pane) (shift, bool) {
	var list, target tmux.Pane
	for _, p := range panes {
		switch p.ID {
		case self:
			list = p
		case want:
			target = p
		}
	}
	if list.ID == "" || target.ID == "" || target.Window == list.Window {
		return shift{}, false
	}
	n := 0
	for _, p := range panes {
		if p.Window == list.Window {
			n++
		}
	}
	// The list goes as wide as it is, so a width the user dragged it to is
	// kept, unless it is alone and so as wide as the whole window.
	size := listShare
	if n > 1 && list.W > 0 {
		size = strconv.Itoa(list.W)
	}
	return shift{to: want, from: list.Window, size: size, fix: n > 2}, true
}

// stageCmd carries out a shift. tmux gives the list's width to the column
// beside it, so a window left with two or more panes gets its old layout
// back with the list taken out and its space shared out. That fix-up is
// cosmetic, so it fails quietly.
func stageCmd(mux multiplexer, list string, mv shift, gen int) tea.Cmd {
	return func() tea.Msg {
		layout, lerr := "", errors.New("no fix-up")
		if mv.fix {
			layout, lerr = mux.Layout(mv.from)
		}
		err := mux.Move(list, mv.to, mv.size)
		if err == nil && lerr == nil {
			if fixed, err := tmux.Without(layout, list); err == nil {
				mux.SetLayout(mv.from, fixed) //nolint:errcheck
			}
		}
		panes, perr := mux.Panes()
		if err == nil {
			err = perr
		}
		return stagedMsg{mv.to, panes, gen, err}
	}
}

// spawnCmd starts sp's pane and sets its options straight after, so a
// listing taken later sees them. One taken in between is dropped: see
// model.spawns.
func spawnCmd(mux multiplexer, sp spawn) tea.Cmd {
	return func() tea.Msg {
		p, err := mux.Spawn(sp.session, sp.dir, sp.env, sp.command...)
		if err != nil {
			return spawnedMsg{sp, tmux.Pane{}, err}
		}
		p, mark := sp.marked(p)
		if mark != nil {
			err = mark(mux)
		}
		return spawnedMsg{sp, p, err}
	}
}

// writeTTY writes to a pane's terminal. Tests replace it.
var writeTTY = func(tty string, b []byte) error {
	f, err := os.OpenFile("/dev/"+tty, os.O_WRONLY|syscall.O_NOCTTY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(b)
	return err
}

func drawCmd(tty, text string) tea.Cmd {
	return func() tea.Msg {
		if err := writeTTY(tty, []byte(text)); err != nil {
			return noteMsg("could not draw the details: " + err.Error())
		}
		return nil
	}
}

func (m model) pane(id string) (tmux.Pane, bool) {
	for _, p := range m.panes {
		if p.ID == id && id != "" {
			return p, true
		}
	}
	return tmux.Pane{}, false
}

func (m model) alive(id string) bool {
	_, ok := m.pane(id)
	return ok
}

// stageOf is the window the list is in, "" when the listing lacks it.
func (m model) stageOf(panes []tmux.Pane) string {
	for _, p := range panes {
		if p.ID == m.self {
			return p.Window
		}
	}
	return ""
}

// paneOf is the pane that shows a session, "" when it has none: the pane
// its claude runs in, or the attach opened for a background session.
func (m model) paneOf(r fleet.Row) string {
	if r.Background() {
		for _, p := range m.panes {
			if r.ID != "" && p.Attach == r.ID && p.Session == m.session {
				return p.ID
			}
		}
		return ""
	}
	tty := m.ttys[r.PID]
	for _, p := range m.panes {
		if tty != "" && p.TTY == tty && p.ID != m.self {
			return p.ID
		}
	}
	return ""
}

// want is the pane whose window the list belongs in: a session just started with
// c until it shows up in the list, else the selected session's own pane or
// the selected terminal's first pane, else the holder.
func (m model) want() string {
	if m.alive(m.opened.ID) {
		return m.opened.ID
	}
	if e, _, ok := m.selected(m.ordered()); ok {
		switch e.kind {
		case sessionEntry:
			if id := m.paneOf(e.row); id != "" {
				return id
			}
		case terminalEntry:
			if m.alive(e.term.Pane) {
				return e.term.Pane
			}
		}
	}
	if m.alive(m.holder) {
		return m.holder
	}
	return ""
}

// besideTerminal says whether entering means the pane right of the list
// rather than the wanted pane: on a terminal, where tmux picks the split
// used last, so enter lands where the user was.
func (m model) besideTerminal(want string) bool {
	e, _, ok := m.selected(m.ordered())
	return ok && e.kind == terminalEntry && want != m.opened.ID
}

// setPanes takes a fresh listing of tmux's panes.
func (m model) setPanes(panes []tmux.Pane) model {
	m.panes = panes
	return m
}

// restage brings tmux in line with the model after every update: it moves
// the list to the wanted pane, one move at a time, then moves the focus
// there if enter asked for it, and redraws the holder when it shows and its
// text has changed.
func (m model) restage() (model, tea.Cmd) {
	want := m.want()
	if m.mux == nil || m.staging || want == "" || want == m.failed {
		return m, nil
	}
	if mv, ok := plan(m.self, want, m.panes); ok {
		m.staging = true
		return m, stageCmd(m.mux, m.self, mv, m.spawns)
	}
	var cmds []tea.Cmd
	if m.entering {
		m.entering = false
		mux, self, beside := m.mux, m.self, m.besideTerminal(want)
		cmds = append(cmds, func() tea.Msg {
			err := error(nil)
			if beside {
				err = mux.SelectBeside(self)
			} else {
				err = mux.Select(want)
			}
			if err != nil {
				return noteMsg(err.Error())
			}
			return nil
		})
	}
	if p, ok := m.pane(m.holder); ok && want == m.holder {
		if text := m.holderText(p.W, p.H); text != m.holderShown {
			m.holderShown = text
			cmds = append(cmds, drawCmd(p.TTY, text))
		}
	}
	return m, tea.Batch(cmds...)
}

// stageUpdate handles the messages that come back from tmux.
func (m model) stageUpdate(msg tea.Msg) (model, tea.Cmd) {
	switch msg := msg.(type) {
	case panesTick:
		return m, tea.Batch(listPanes(m.mux, m.spawns), panesEveryTick())
	case panesMsg:
		if msg.err != nil {
			return m.say(msg.err.Error()), nil
		}
		if msg.gen < m.spawns {
			return m, nil // it may show a spawned pane without its options
		}
		// A move that failed is tried again once tmux has been reread.
		m = m.setPanes(msg.panes)
		m.failed = ""
		if !m.alive(m.holder) && !m.spawningHolder {
			m.spawningHolder = true
			return m, spawnCmd(m.mux, spawn{kind: spawnHolder, session: m.session, command: []string{m.exe, "stage"}})
		}
	case stagedMsg:
		m.staging = false
		if msg.err != nil {
			m.failed = msg.to
			m = m.say("could not show the session: " + msg.err.Error())
		}
		if msg.panes != nil && msg.gen >= m.spawns {
			m = m.setPanes(msg.panes)
		}
		// The holder may have been resized, so draw it afresh.
		m.holderShown = ""
	case spawnedMsg:
		if msg.kind == spawnHolder {
			m.spawningHolder = false
		}
		if msg.pane.ID == "" {
			return m.say("could not start " + msg.name + ": " + msg.err.Error()), nil
		}
		if msg.err != nil {
			m = m.say("could not mark " + msg.name + ": " + msg.err.Error())
		}
		// Known now, though the next listing is yet to come. A listing
		// asked for before this point may lack it or its options.
		m.panes = append(m.panes, msg.pane)
		if msg.kind != spawnHolder {
			m.spawns++
		}
		switch msg.kind {
		case spawnHolder:
			m.holder, m.holderShown = msg.pane.ID, ""
			mux, session, id := m.mux, m.session, msg.pane.ID
			return m, func() tea.Msg {
				if err := mux.SetOption(session, holderOption, id); err != nil {
					return noteMsg(err.Error())
				}
				return nil
			}
		case spawnAttach:
			m.entering = true
		case spawnSession:
			m.opened, m.entering = msg.pane, true
			m = m.say("started " + msg.name)
		case spawnTerminal:
			// A terminal is listed as soon as its pane is, so the cursor
			// takes it at once.
			m.sel, m.confirm, m.opened, m.entering = msg.pane.Window, "", tmux.Pane{}, true
			if m.filter != "" && !m.shown(m.sel) {
				m.filter, m.filtering = "", false
			}
			m = m.say("opened " + msg.name)
		}
	case closedMsg:
		if msg.err != nil {
			return m.say(msg.err.Error()), nil
		}
		m = m.say(msg.note)
		if msg.gen >= m.spawns {
			m = m.setPanes(msg.panes)
		}
	}
	return m, nil
}

// adopt moves the cursor to the session started with c once the list has
// it, and stops showing that pane by itself.
func (m model) adopt() model {
	if m.opened.ID == "" {
		return m
	}
	for _, r := range m.rows {
		if !r.Background() && m.ttys[r.PID] == m.opened.TTY {
			m.sel, m.opened = r.SessionID, tmux.Pane{}
			if m.filter != "" && !m.shown(r.SessionID) {
				m.filter, m.filtering = "", false
			}
			return m
		}
	}
	return m
}

func (m model) shown(key string) bool {
	for _, e := range m.ordered() {
		if e.key() == key {
			return true
		}
	}
	return false
}

// closedMsg answers closeCmd with the panes after the window closed.
type closedMsg struct {
	note  string
	panes []tmux.Pane
	gen   int
	err   error
}

// closeCmd closes a terminal's window, and everything that runs in it.
// When the list is in that window it first goes beside the holder, since
// closing the window with the list in it would end the view.
func closeCmd(mux multiplexer, self, holder string, t terminal, panes []tmux.Pane, gen int) tea.Cmd {
	inside := false
	for _, p := range panes {
		inside = inside || (p.ID == self && p.Window == t.Window)
	}
	mv, canMove := plan(self, holder, panes)
	return func() tea.Msg {
		fail := func(err error) tea.Msg {
			return closedMsg{err: errors.New("could not close " + t.Title + ": " + err.Error())}
		}
		if inside {
			if !canMove {
				return fail(errors.New("the list has nowhere else to go"))
			}
			if err := mux.Move(self, mv.to, mv.size); err != nil {
				return fail(err)
			}
		}
		if err := mux.KillWindow(t.Window); err != nil {
			return fail(err)
		}
		panes, err := mux.Panes()
		return closedMsg{"closed " + t.Title, panes, gen, err}
	}
}

// holderText is the holder's whole screen at w by h: a line saying where
// the session is, then its details. When they do not fit, the head (state
// and what it waits on) and the newest turns are kept, and the older turns
// between them go.
func (m model) holderText(w, h int) string {
	cw := max(w-2, 1)
	var lines []string
	e, _, ok := m.selected(m.ordered())
	switch {
	case !m.loaded:
		lines = []string{dim.Render("reading sessions…")}
	case !ok:
		lines = wrap(dim.Render("Nothing is listed. c starts Claude where overview was started, t a terminal, and a adds a project."), cw)
	case e.kind == projectEntry:
		lines = wrap(dim.Render("No sessions in "+m.projects.name(e.project)+". c starts Claude here, t a terminal, x unpins it."), cw)
	case e.kind == terminalEntry:
		lines = wrap(dim.Render("The terminal "+e.term.Title+" has closed."), cw)
	default:
		if hint := m.stageHint(e.row); hint != "" {
			lines = append(wrap(dim.Render(hint), cw), "")
		}
		lines = append(lines, m.detail(e.row, cw)...)
	}
	if len(lines) > h {
		head := 0
		for i, l := range lines {
			if strings.Contains(ansi.Strip(l), "── last") {
				head = i + 1
				break
			}
		}
		head = min(head, h/2)
		lines = append(lines[:head:head], lines[len(lines)-(h-head):]...)
	}
	out := make([]string, h)
	for i := range out {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		out[i] = " " + fit(l, cw) + " "
	}
	// Home, clear, hide the cursor, then the lines. The pane is raw, so
	// each line ends in its own carriage return.
	return "\x1b[H\x1b[2J\x1b[?25l" + strings.Join(out, "\r\n")
}

// stageHint says why the holder shows a session instead of its terminal.
func (m model) stageHint(r fleet.Row) string {
	_, away := m.hosts[r.PID]
	switch {
	case r.Background():
		return "A background session. enter attaches to it here."
	case away:
		return "" // the details say where it is
	case m.ttys[r.PID] != "":
		return "This session runs in " + m.ttys[r.PID] + ", outside overview's tmux, so it cannot be shown here."
	}
	return "This session has no terminal to show."
}

// Stage is the holder pane's program: it keeps the pane open and quiet
// while the view draws into it, and ends when the pane closes.
func Stage() {
	fd := os.Stdin.Fd()
	if st, err := term.MakeRaw(fd); err == nil {
		defer term.Restore(fd, st) //nolint:errcheck
	}
	buf := make([]byte, 256)
	for {
		if _, err := os.Stdin.Read(buf); err != nil {
			return
		}
	}
}
