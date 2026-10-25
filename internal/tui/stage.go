package tui

import (
	"errors"
	"fmt"
	"os"
	"slices"
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
	// keep, because it has not yet shared a window.
	listShare = "30%"
	// listMin is the narrowest the list is kept while its window has room,
	// and besideMin the narrowest it leaves the panes beside it.
	listMin   = 24
	besideMin = 20
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
	KillPane(pane string) error
	Split(target, dir, size string, env []string, command ...string) (tmux.Pane, error)
	SetWidth(pane string, cols int) error
	Detach() error
	SetWindowOption(target, name, value string) error
	SetPaneOption(pane, name, value string) error
	SetPaneOptions(sets []tmux.PaneSet) error
	SetFrame(pane string, on bool) error
	SetZoom(pane string, on bool) error
	EnterPanels() error
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
	sel     string // the cursor's entry when it was asked for
	name    string // what to call it in a note
	session string
	dir     string // the project root, for spawnSession and spawnTerminal
	task    string // the workstream a spawnTerminal is for, "" for none
	env     []string
	command []string
}

// marked is the spawned pane as the listing will show it once the Cmd has
// set its options: a session's or a terminal's window names its project,
// a terminal's the workstream it is for, if any,
// an attach pane names its background session, and the holder is marked
// as such. It also says which option to set, as a pane or a window option.
func (sp spawn) marked(p tmux.Pane) (tmux.Pane, func(multiplexer) error) {
	switch sp.kind {
	case spawnHolder:
		p.Holder = true
		return p, func(mux multiplexer) error { return mux.SetPaneOption(p.ID, tmux.HolderOption, "1") }
	case spawnSession, spawnTerminal:
		p.Project, p.Task = sp.dir, sp.task
		return p, func(mux multiplexer) error {
			err := mux.SetWindowOption(p.ID, tmux.ProjectOption, sp.dir)
			if err == nil && sp.task != "" {
				err = mux.SetWindowOption(p.ID, tmux.TaskOption, sp.task)
			}
			return err
		}
	case spawnAttach:
		p.Attach = sp.key
		return p, func(mux multiplexer) error { return mux.SetPaneOption(p.ID, tmux.AttachOption, sp.key) }
	}
	return p, nil
}

type (
	panesTick time.Time
	// panesMsg is a listing of tmux's panes. gen is the model's landed
	// spawns when it was asked for: see model.spawns.
	panesMsg struct {
		panes []tmux.Pane
		roots roots // of the panes' directories
		gen   int
		err   error
	}
	// stagedMsg answers a stage Cmd with the panes as they are afterwards.
	stagedMsg struct {
		to    string
		panes []tmux.Pane
		roots roots
		gen   int
		err   error
	}
	// panelsMsg answers the Cmd that puts the client in the panels mode.
	panelsMsg  struct{ err error }
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

func listPanes(mux multiplexer, find func(...string) roots, gen int) tea.Cmd {
	return func() tea.Msg {
		panes, rs, err := readPanes(mux, find)
		return panesMsg{panes, rs, gen, err}
	}
}

// readPanes lists tmux's panes and finds the roots of their directories.
func readPanes(mux multiplexer, find func(...string) roots) ([]tmux.Pane, roots, error) {
	panes, err := mux.Panes()
	dirs := make([]string, len(panes))
	for i, p := range panes {
		dirs[i] = p.Path
	}
	return panes, find(dirs...), err
}

// shift is a step that puts the list beside the pane it wants: the list
// goes from window from into the window of pane to, size wide. fix says
// from keeps two or more panes, whose layout is then put right.
type shift struct {
	to, from, size string
	fix            bool
}

// plan decides how the list, pane self, gets beside pane want, given every
// pane on the server and the width the list keeps, 0 when it has none yet.
// It reports false when no move is needed, because want is already in the
// list's window, or is not listed.
func plan(self, want string, panes []tmux.Pane, width int) (shift, bool) {
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
	// The list keeps its width, fitted to the window it goes to. Until it
	// has one, it goes as wide as it is, unless it is alone and so as wide
	// as the whole window.
	size := listShare
	switch {
	case width > 0 && target.WinW > 0:
		size = strconv.Itoa(fitList(width, target.WinW))
	case width > 0:
		size = strconv.Itoa(width)
	case n > 1 && list.W > 0:
		size = strconv.Itoa(list.W)
	}
	return shift{to: want, from: list.Window, size: size, fix: n > 2}, true
}

// listWidth is the width the list keeps, and where it was last seen, which
// tells a width the user dragged it to from one tmux squeezed it to: tmux
// takes a narrowing window's columns from its leftmost pane, the list, and
// a window not shown since the terminal narrowed snaps to the terminal's
// width once the list moves into it.
type listWidth struct {
	want   int    // the width the list keeps, 0 until it has shared a window
	set    int    // the width it was left at when last seen
	window string // the list's window when last seen
	winW   int    // and that window's width
}

// fitList is want in a window win columns wide: no narrower than listMin
// and no wider than leaves besideMin and a border, or half the window each
// when it has room for neither, and never no width at all.
func fitList(want, win int) int {
	lo := max(min(listMin, max(win-besideMin-1, win/2)), 1)
	hi := max(win-besideMin-1, lo)
	return min(max(want, lo), hi)
}

// seen takes the list's pane from a fresh listing, and whether it is alone
// in its window, and says what width to resize it to, 0 for none. A change
// in the same window at the same window width is the user's, and becomes
// the width kept. Any other is tmux's, and the list gets its width back.
// A list alone is as wide as its window, which says nothing about the
// width it keeps.
func (k listWidth) seen(list tmux.Pane, alone bool) (listWidth, int) {
	if list.WinW <= 0 {
		return k, 0
	}
	same := list.Window == k.window && list.WinW == k.winW
	k.window, k.winW = list.Window, list.WinW
	if alone {
		k.set = list.W
		return k, 0
	}
	if k.want == 0 || (same && list.W != k.set) {
		k.want = list.W
	}
	k.set = fitList(k.want, list.WinW)
	if k.set == list.W {
		return k, 0
	}
	return k, k.set
}

// widthMsg answers a resize of the list.
type widthMsg struct{ err error }

// keepWidth reads the list's width off the listing just taken, which must
// be current, and resizes it when tmux has squeezed it. The resize counts
// as a spawn, so no listing asked for before it lands, with the old width,
// is taken for a drag.
func (m model) keepWidth() (model, tea.Cmd) {
	list, ok := m.pane(m.self)
	// A zoomed list is as wide as its window, which says nothing of the
	// width it keeps, and tmux puts that back when the zoom ends.
	if !ok || m.mux == nil || m.staging || m.zooming || m.zoomWant() || list.Zoomed {
		return m, nil
	}
	n := 0
	for _, p := range m.panes {
		if p.Window == list.Window {
			n++
		}
	}
	var to int
	m.width, to = m.width.seen(list, n == 1)
	if to == 0 {
		return m, nil
	}
	m.spawns++
	mux, self := m.mux, m.self
	return m, func() tea.Msg { return widthMsg{mux.SetWidth(self, to)} }
}

// stageCmd carries out a shift. tmux gives the list's width to the column
// beside it, so a window left with two or more panes gets its old layout
// back with the list taken out and its space shared out. That fix-up is
// cosmetic, so it fails quietly.
func stageCmd(mux multiplexer, find func(...string) roots, list string, mv shift, gen int) tea.Cmd {
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
		panes, rs, perr := readPanes(mux, find)
		if err == nil {
			err = perr
		}
		return stagedMsg{mv.to, panes, rs, gen, err}
	}
}

// startPane asks for sp's pane, which is pending until its Cmd lands.
func (m model) startPane(sp spawn) (model, tea.Cmd) {
	m.spawns++
	return m, spawnCmd(m.mux, sp)
}

// current says whether a listing asked for when gen spawns had landed
// can be taken: no spawn is pending, and none landed since.
func (m model) current(gen int) bool { return m.spawns == m.landed && gen == m.landed }

// spawnCmd starts sp's pane and sets its options straight after, so a
// listing taken later sees them. One answered in between may show the
// pane without them, and is dropped: see model.spawns.
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
		// An unmarked holder is a holder no listing finds, so the next one
		// would start another: it goes. Other panes run the user's work and
		// stay, unmarked.
		if err != nil && sp.kind == spawnHolder {
			return spawnedMsg{sp, tmux.Pane{}, errors.Join(err, mux.KillPane(p.ID))}
		}
		return spawnedMsg{sp, p, err}
	}
}

// writeTTY writes to a pane's terminal.
func writeTTY(tty string, b []byte) error {
	f, err := os.OpenFile("/dev/"+tty, os.O_WRONLY|syscall.O_NOCTTY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(b)
	return err
}

func drawCmd(write func(string, []byte) error, tty, text string) tea.Cmd {
	return func() tea.Msg {
		if err := write(tty, []byte(text)); err != nil {
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

// holder is the pane the view draws details into, "" when the listing has
// none in the view's session.
func (m model) holder() string {
	for _, p := range m.panes {
		if p.Holder && p.Session == m.session {
			return p.ID
		}
	}
	return ""
}

// windowOf is the window pane id is in, "" when it is not listed.
func (m model) windowOf(id string) string {
	p, _ := m.pane(id)
	return p.Window
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
		if tty != "" && p.TTY == tty && p.ID != m.self && p.Session == m.session {
			return p.ID
		}
	}
	return ""
}

// elsewhere says whether an interactive session runs in a pane of another
// session on this server, such as another home's view.
func (m model) elsewhere(r fleet.Row) bool {
	tty := m.ttys[r.PID]
	for _, p := range m.panes {
		if tty != "" && p.TTY == tty && p.Session != m.session {
			return true
		}
	}
	return false
}

// want is the pane whose window the list belongs in: a session just started with
// c until it shows up in the list, else the selected session's own pane or
// the selected terminal's first pane, else the holder.
func (m model) want() string {
	if m.alive(m.opened.ID) {
		return m.opened.ID
	}
	es := m.ordered()
	if e, _, ok := m.selected(es); ok {
		if id := m.entryPane(es, e); id != "" {
			return id
		}
	}
	if h := m.holder(); h != "" {
		return h
	}
	return ""
}

// entryPane is the pane that shows an entry, "" when it has none: a
// session's own pane, a terminal's first pane, or for a workstream its
// lead's.
func (m model) entryPane(es []entry, e entry) string {
	switch e.kind {
	case sessionEntry:
		return m.paneOf(e.row)
	case terminalEntry:
		if m.alive(e.term.Pane) {
			return e.term.Pane
		}
	case workstreamEntry:
		if c, ok := lead(children(es, e.task), func(c entry) bool { return m.entryPane(es, c) != "" }); ok {
			return m.entryPane(es, c)
		}
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

// setPanes takes a fresh listing of tmux's panes and their roots, which
// also say whether a client of the session is in the panels mode.
func (m model) setPanes(panes []tmux.Pane, rs roots) model {
	m.panes, m.paneRoots = panes, rs
	m.panels = slices.ContainsFunc(panes, func(p tmux.Pane) bool { return p.Panels && p.Session == m.session })
	return m
}

// restage brings tmux in line with the model after every update: it moves
// the list to the wanted pane, one move at a time, then moves the focus
// there if enter asked for it, and redraws the holder when it shows and its
// text has changed.
func (m model) restage() (model, tea.Cmd) {
	want := m.want()
	// The cockpit and the task picker hold the list in its window, zoomed,
	// until they close and the zoom has gone.
	if m.mux == nil || m.staging || m.zooming || m.zoomWant() || want == "" || want == m.failed {
		return m, nil
	}
	// With the keyboard outside the list, the list stays beside the pane
	// the user is in, unless an action asked to go somewhere.
	if mv, ok := plan(m.self, want, m.panes, m.width.want); ok && (!m.blurred || m.entering != "") {
		m.staging = true
		return m, stageCmd(m.mux, m.deps.roots, m.self, mv, m.landed)
	}
	var cmds []tea.Cmd
	// The focus moves only to the pane it was asked for, once the list is
	// beside it: the entry's own pane, or another in its window, such as
	// its review. A cursor that moved on has cleared it already.
	if target := m.entering; target != "" {
		m.entering = ""
		mux, self, beside := m.mux, m.self, target == want && m.besideTerminal(want)
		if m.windowOf(target) == m.windowOf(want) && m.stageOf(m.panes) == m.windowOf(want) {
			cmds = append(cmds, func() tea.Msg {
				err := error(nil)
				if beside {
					err = mux.SelectBeside(self)
				} else {
					err = mux.Select(target)
				}
				if err != nil {
					return noteMsg(err.Error())
				}
				return nil
			})
		}
	}
	if p, ok := m.pane(want); ok && want == m.holder() {
		if text := m.holderText(p.W, p.H); text != m.holderShown {
			m.holderShown = text
			cmds = append(cmds, drawCmd(m.deps.writeTTY, p.TTY, text))
		}
	}
	return m, tea.Batch(cmds...)
}

// movedSelf is panes with the list's own pane as listing has it.
func movedSelf(panes, listing []tmux.Pane, self string) []tmux.Pane {
	i := slices.IndexFunc(panes, func(p tmux.Pane) bool { return p.ID == self })
	j := slices.IndexFunc(listing, func(p tmux.Pane) bool { return p.ID == self })
	if i < 0 || j < 0 {
		return panes
	}
	out := slices.Clone(panes)
	out[i] = listing[j]
	return out
}

// stageUpdate handles the messages that come back from tmux.
func (m model) stageUpdate(msg tea.Msg) (model, tea.Cmd) {
	switch msg := msg.(type) {
	case panesTick:
		return m, tea.Batch(listPanes(m.mux, m.deps.roots, m.landed), panesEveryTick())
	case panesMsg:
		if msg.err != nil {
			return m.say(msg.err.Error()), nil
		}
		if !m.current(msg.gen) {
			return m, nil // it may show a spawned pane without its options
		}
		// A move that failed is tried again once tmux has been reread, and
		// so are the rules.
		m = m.setPanes(msg.panes, msg.roots)
		m.failed, m.frameStale = "", false
		m, zoom := m.zoomLost()
		var cmd tea.Cmd
		if m.holder() == "" && !m.spawningHolder {
			m.spawningHolder = true
			m, cmd = m.startPane(spawn{kind: spawnHolder, name: "the details pane", session: m.session, command: []string{m.exe, "stage"}})
		} else {
			m, cmd = m.keepWidth()
		}
		return m, tea.Batch(zoom, cmd)
	case panelsMsg:
		m.landed++
		if msg.err != nil {
			m.panels = false
			return m.say("could not enter the panels: " + msg.err.Error()), nil
		}
	case stagedMsg:
		m.staging = false
		if m.cockpitSoon {
			// o came while the list was moving: it opens where the list
			// went, and the cockpit holds it there.
			m, cmd := m.stageLanded(msg)
			m, open := m.openCockpit()
			return m, tea.Batch(cmd, open)
		}
		if m.taskPick.open {
			// s came while the list was moving: its zoom waited for this.
			m, cmd := m.stageLanded(msg)
			m.zoomPanes = m.inStage()
			m, zoom := m.syncZoom()
			return m, tea.Batch(cmd, zoom)
		}
		return m.stageLanded(msg)

	case spawnedMsg:
		m.landed++
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
		m.panes = slices.Concat(m.panes, []tmux.Pane{msg.pane})
		switch msg.kind {
		case spawnHolder:
			m.holderShown = ""
		case spawnAttach:
			// Only if the cursor is still on the session it is for.
			if m.sel == msg.sel {
				m.entering = msg.pane.ID
			}
		case spawnSession:
			m.opened, m.entering = msg.pane, msg.pane.ID
			m = m.say("started " + msg.name)
		case spawnTerminal:
			// A terminal is listed as soon as its pane is, so the cursor
			// takes it at once.
			m.sel, m.confirm, m.opened, m.entering = msg.pane.Window, "", tmux.Pane{}, msg.pane.ID
			if m.filter != "" && !m.shown(m.sel) {
				m.filter, m.filtering = "", false
			}
			m = m.say("opened " + msg.name)
		}
	case reviewOpenedMsg:
		m.landed++
		m.opening = withKey(m.opening, msg.id, false)
		if msg.pane.ID == "" {
			return m.say("could not open the review: " + msg.err.Error()), nil
		}
		if msg.err != nil {
			m = m.say("could not mark the review: " + msg.err.Error())
		}
		m.panes = slices.Concat(m.panes, []tmux.Pane{msg.pane})
		m.entering = msg.pane.ID
	case zoomedMsg:
		m.landed++
		m.zooming = false
		if msg.err != nil {
			m = m.say("could not zoom the list: " + msg.err.Error())
		}
		if m.rezoom {
			return m.syncZoom()
		}
	case framedMsg:
		m.framing = false
		if msg.err != nil {
			m.framed, m.frameStale = nil, true
		}
		if msg.window != "" && msg.err == nil {
			m.panes = slices.Clone(m.panes)
			for i, p := range m.panes {
				if p.Window == msg.window {
					m.panes[i].Framed = msg.on
				}
			}
		}
	case widthMsg:
		m.landed++
		if msg.err != nil {
			return m.say("could not resize the list: " + msg.err.Error()), nil
		}
	case closedMsg:
		if msg.err != nil {
			return m.say(msg.err.Error()), nil
		}
		m = m.say(msg.note)
		if m.current(msg.gen) {
			return m.setPanes(msg.panes, msg.roots).keepWidth()
		}
		// The rest may be stale, but the window is gone and the list is
		// where it went, so no move aims at a closed pane.
		gone := slices.DeleteFunc(slices.Clone(m.panes), func(p tmux.Pane) bool { return p.Window == msg.window && p.ID != m.self })
		m.panes = movedSelf(gone, msg.panes, m.self)
	}
	return m, nil
}

// stageLanded takes the answer to a stage Cmd.
func (m model) stageLanded(msg stagedMsg) (model, tea.Cmd) {
	if msg.err != nil {
		m.failed, m.entering = msg.to, ""
		m = m.say("could not show the session: " + msg.err.Error())
	}
	// The holder may have been resized, so draw it afresh.
	m.holderShown = ""
	switch {
	case msg.panes == nil:
	case m.current(msg.gen):
		return m.setPanes(msg.panes, msg.roots).keepWidth()
	default:
		// The rest may be stale, but where the list went is not.
		m.panes = movedSelf(m.panes, msg.panes, m.self)
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
	note   string
	window string // the window closed
	panes  []tmux.Pane
	roots  roots
	gen    int
	err    error
}

// closeCmd closes a terminal's window, and everything that runs in it.
// When the list is in that window it first goes beside the holder, since
// closing the window with the list in it would end the view.
func closeCmd(mux multiplexer, find func(...string) roots, self, holder string, t terminal, panes []tmux.Pane, width, gen int) tea.Cmd {
	inside := false
	for _, p := range panes {
		inside = inside || (p.ID == self && p.Window == t.Window)
	}
	mv, canMove := plan(self, holder, panes, width)
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
		panes, rs, err := readPanes(mux, find)
		return closedMsg{"closed " + t.Title, t.Window, panes, rs, gen, err}
	}
}

// holderText is the holder's whole screen at w by h: a line saying where
// the session is, then its details. When they do not fit, the head (state
// and what it waits on) and the newest turns are kept, and the older turns
// between them go.
func (m model) holderText(w, h int) string {
	cw := max(w-2, 1)
	var lines []string
	es := m.ordered()
	e, _, ok := m.selected(es)
	switch {
	case !m.loaded && m.rowsErr != nil:
		lines = wrap(red.Render(m.rowsErr.Error()), cw)
	case !m.loaded:
		lines = []string{dim.Render("reading sessions…")}
	case !ok:
		lines = wrap(dim.Render("Nothing is listed. c starts Claude where overview was started, t a terminal, and a adds a project."), cw)
	case e.kind == projectEntry:
		hint := ". c starts Claude here, t a terminal, x unpins it."
		if _, tracked := m.trackers[e.project]; tracked {
			hint = ". s starts one of its tasks, c Claude, t a terminal, and x unpins it."
		}
		lines = wrap(dim.Render("No sessions in "+m.name(e.project)+hint), cw)
	case e.kind == terminalEntry:
		lines = wrap(dim.Render("The terminal "+e.term.Title+" has closed."), cw)
	case idleStream(es, e):
		// No session to show: the cockpit's cards, where enter goes.
		cards, _, _ := layoutCards(m.cockpitRows(m.target()), w, h, "")
		return "\x1b[H\x1b[2J\x1b[?25l" + strings.Join(cards, "\r\n")
	case e.kind == workstreamEntry:
		lines = m.workstreamLines(e, cw)
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

// workstreamLines is what the holder shows for a workstream whose
// sessions have no pane to show: its issue, and where enter goes.
func (m model) workstreamLines(e entry, w int) []string {
	ref := issueRef(m.projectID(e.project), e.issue)
	out := wrap(heading.Render(ref)+" "+bold.Foreground(pal.Text).Render(e.issue.Title), w)
	out = append(out, wrap(dim.Render(e.issue.Status+" · "+e.issue.Priority+" · "+m.name(e.project)), w)...)
	out = append(out, "")
	n := 0
	for _, c := range children(m.ordered(), e.task) {
		if c.kind == sessionEntry {
			n++
		}
	}
	if n == 1 {
		return append(out, wrap(dim.Render("One session works on this task, and it has no pane here. enter goes to it."), w)...)
	}
	return append(out, wrap(dim.Render(fmt.Sprintf("%d sessions work on this task, and none has a pane here. enter goes to the first.", n)), w)...)
}

// stageHint says why the holder shows a session instead of its terminal.
func (m model) stageHint(r fleet.Row) string {
	_, away := m.hosts[r.PID]
	switch {
	case r.Background():
		return "A background session. enter attaches to it here."
	case away:
		return "" // the details say where it is
	case m.elsewhere(r):
		return "This session runs in another overview session."
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
