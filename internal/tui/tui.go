// Package tui is the live terminal view: the sessions that need you on top,
// the one under the cursor live in the tmux pane beside it, and the digests
// a key away.
package tui

import (
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"overview/internal/fleet"
	"overview/internal/tmux"
)

const (
	refreshEvery = 2 * time.Second
	frameEvery   = 120 * time.Millisecond
	noteFor      = 6 * time.Second
	recentTurns  = 6
	// doubleClick is how close two clicks on a row must be to act on it.
	doubleClick = 400 * time.Millisecond
	// wheelLines is how far one wheel notch scrolls a pane.
	wheelLines = 3
)

type screen int

const (
	sessionsScreen screen = iota
	digestScreen
)

type (
	tickMsg  time.Time
	frameMsg time.Time
	rowsMsg  struct {
		rows   []fleet.Row
		ttys   map[int]string
		hosts  map[int]host
		pinned []string
		err    error
	}
	digestMsg struct {
		d   fleet.Digest
		err error
	}
	// noteMsg is something to tell the user.
	noteMsg string
	// doneMsg reports how an action on a session went.
	doneMsg struct {
		note string
		err  error
	}
)

type model struct {
	home fleet.Home
	llm  string

	rows    []fleet.Row
	ttys    map[int]string
	hosts   map[int]host // interactive sessions with no terminal, by pid
	pinned  []string     // the pinned projects' roots
	rowsErr error
	loaded  bool

	screen    screen
	sel       string // the key of the entry under the cursor, kept across refreshes
	listTop   int    // first list line shown, kept so clicks do not shift it
	filter    string
	filtering bool
	confirm   string // the key of a session to stop or a terminal to close, waiting on a yes
	help      bool
	frame     int

	digests      []string
	digestIdx    int
	digest       string
	digestAt     time.Time
	digesting    bool
	digestScroll int

	note   string
	noteAt time.Time

	// The details are rebuilt only when what they show changes, not on
	// every spinner frame. A pointer, because the model is passed by value.
	cache *detailCache

	projects *projects

	// reveal brings an app to the front. Tests replace it.
	reveal func(path string) error
	// cwd is where c starts a session when the list is empty.
	cwd string

	// tmux, as stage.go describes. mux is nil outside tmux, and then
	// nothing is staged. The view runs in pane self of session. exe starts
	// the holder.
	mux               multiplexer
	self, session     string
	exe               string
	panes             []tmux.Pane
	failed            string
	staging, entering bool
	spawningHolder    bool
	holderShown       string
	// spawns counts the panes started with options to set. A listing asked
	// for before the latest of them came back is dropped, since it may show
	// that pane before its options were set.
	spawns int
	// opened is the pane c started, shown until its session is listed.
	opened tmux.Pane
	// blurred is set while the focus is in another pane.
	blurred bool

	// pick is the add-project picker, open while it takes the list pane.
	pick picker

	// The last click on a list row, to tell a double click.
	clickID string
	clickAt time.Time

	w, h int
}

// Run opens the view in the given theme and blocks until the user quits.
// It runs in the pane Launch made for it in session, on the tmux server at
// the socket path, and acts on no other server.
func Run(home fleet.Home, llm string, theme Theme, socket, session string) error {
	useTheme(theme)
	m := newModel(home, llm)
	m.cwd, _ = os.Getwd()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	srv := tmux.At(socket, "")
	self, ok, err := srv.Self(os.Getpid())
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("the view runs inside overview's tmux: start it with plain `overview`")
	}
	if err := srv.MarkList(self); err != nil {
		return err
	}
	m.mux, m.self, m.session, m.exe = srv, self, session, exe
	panes, err := srv.Panes()
	if err != nil {
		return err
	}
	m = m.setPanes(panes).loadDigests()
	_, err = tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithReportFocus()).Run()
	return err
}

func newModel(home fleet.Home, llm string) model {
	return model{home: home, llm: llm, cache: &detailCache{}, projects: newProjects(),
		reveal: openApp}
}

func openApp(path string) error { return exec.Command("open", path).Run() }

// loadDigests picks up the digest list and shows the newest one.
func (m model) loadDigests() model {
	m.digests = m.home.DigestFiles()
	return m.showDigest(len(m.digests) - 1)
}

func (m model) showDigest(i int) model {
	if i < 0 || i >= len(m.digests) {
		return m
	}
	b, err := os.ReadFile(m.digests[i])
	if err != nil {
		return m
	}
	info, _ := os.Stat(m.digests[i])
	m.digestIdx, m.digest, m.digestAt, m.digestScroll = i, string(b), info.ModTime(), 0
	return m
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.refresh, tick(), nextFrame()}
	if m.mux != nil {
		// The first listing also starts a holder when there is none.
		cmds = append(cmds, listPanes(m.mux, m.spawns), panesEveryTick())
	}
	return tea.Batch(cmds...)
}

func tick() tea.Cmd {
	return tea.Tick(refreshEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func nextFrame() tea.Cmd {
	return tea.Tick(frameEvery, func(t time.Time) tea.Msg { return frameMsg(t) })
}

func (m model) refresh() tea.Msg {
	rows, err := m.home.Rows()
	out, _ := psTable()
	ttys, hosts := places(parseProcs(string(out)), rows)
	return rowsMsg{rows, ttys, hosts, m.home.Projects(), err}
}

func (m model) runDigest() tea.Msg {
	d, err := m.home.RunDigest(m.llm)
	return digestMsg{d, err}
}

func (m model) say(note string) model {
	m.note, m.noteAt = note, time.Now()
	return m
}

// sections is the list as shown, with the filter applied.
func (m model) sections() []section {
	return buildList(m.rows, m.terminals(), m.pinned, m.filter, m.projects.name, m.hostOf)
}

// terminals are the windows of the view's session that run no session.
func (m model) terminals() []terminal {
	claude := map[string]bool{}
	for _, r := range m.rows {
		if tty := m.ttys[r.PID]; tty != "" && !r.Background() {
			claude[tty] = true
		}
	}
	return terminals(m.panes, m.session, m.self, m.opened.ID, claude, m.projects.root)
}

// hostOf names the app a session runs inside, for one that has no terminal.
func (m model) hostOf(r fleet.Row) (string, bool) {
	if r.Background() {
		return "", false
	}
	h, ok := m.hosts[r.PID]
	return h.label(), ok
}

// ordered is the list flattened in the order the cursor walks it.
func (m model) ordered() []entry { return flatten(m.sections()) }

// selected returns the entry under the cursor and its place in es, falling
// back to the first entry when the selected one has gone.
func (m model) selected(es []entry) (entry, int, bool) {
	for i, e := range es {
		if e.key() == m.sel {
			return e, i, true
		}
	}
	if len(es) == 0 {
		return entry{}, -1, false
	}
	return es[0], 0, true
}

// printable cleans typed or pasted filter text: tabs become spaces and other
// control characters, which would have no width and break the layout, go.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
}

// keepSel puts the cursor on the first entry if it is on none that is
// shown.
func (m model) keepSel() model {
	es := m.ordered()
	if len(es) == 0 || m.shown(m.sel) {
		return m
	}
	m.sel, m.confirm = es[0].key(), ""
	return m
}

// moveTo puts the cursor on the ith entry. Moving it also stops showing a
// pane c started, so the stage follows the cursor again.
func (m model) moveTo(es []entry, i int) model {
	if len(es) == 0 {
		return m
	}
	i = max(0, min(i, len(es)-1))
	if k := es[i].key(); k != m.sel {
		m.sel, m.confirm, m.opened = k, "", tmux.Pane{}
	}
	return m
}

// stuck is where the cursor goes when the session it was on had a pane in
// window win and has none there now: to that window's terminal, if the
// window is listed as one. Claude may have exited and left a split behind,
// and the user may be typing in it, so the list must not hop to another
// window. ok is false when the cursor should stay where it is.
func stuck(win string, stillThere bool, es []entry) (string, bool) {
	if win == "" || stillThere {
		return "", false
	}
	for _, e := range es {
		if e.kind == terminalEntry && e.term.Window == win {
			return e.key(), true
		}
	}
	return "", false
}

// nearest is where the cursor goes when the entry it was on, sel, has gone:
// to the first entry after sel in the old order that is still listed, or
// else to the last one before it. ok is false when sel was not in the old
// order, or nothing from it is left.
func nearest(old []string, sel string, listed func(string) bool) (string, bool) {
	i := slices.Index(old, sel)
	if i < 0 {
		return "", false
	}
	for _, k := range old[i+1:] {
		if listed(k) {
			return k, true
		}
	}
	for j := i - 1; j >= 0; j-- {
		if listed(old[j]) {
			return old[j], true
		}
	}
	return "", false
}

// keepNear applies nearest across one update, from the model before it,
// when the entry under the cursor is no longer shown.
func (m model) keepNear(before model) model {
	if m.shown(m.sel) {
		return m
	}
	var old []string
	for _, e := range before.ordered() {
		old = append(old, e.key())
	}
	now := map[string]bool{}
	for _, e := range m.ordered() {
		now[e.key()] = true
	}
	if key, ok := nearest(old, m.sel, func(k string) bool { return now[k] }); ok {
		m.sel, m.confirm = key, ""
	}
	return m
}

// stick applies stuck across one update, from the model before it.
func (m model) stick(before model) model {
	e, _, ok := before.selected(before.ordered())
	r, isSession := e.session()
	if !ok || !isSession {
		return m
	}
	was, _ := before.pane(before.paneOf(r))
	now, _ := m.pane(m.paneOf(r))
	if key, ok := stuck(was.Window, now.Window == was.Window, m.ordered()); ok {
		m.sel = key
	}
	return m
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	before := m
	next, cmd := m.update(msg)
	m = next.(model)
	// Rows and panes come and go: keep the cursor on a shown entry, and in
	// the window it was in when its session ended there.
	m = m.stick(before).keepNear(before).keepSel()
	// Keep the list where it is, scrolled only as far as the cursor needs.
	if m.w > 0 && m.screen == sessionsScreen {
		e, _, _ := m.selected(m.ordered())
		_, owners := m.listLines(m.sections(), e.key(), 1)
		m.listTop = m.listStart(owners, e.key(), m.boxHeight()-2)
	}
	m, stage := m.restage()
	return m, tea.Batch(cmd, stage)
}

func (m model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tea.FocusMsg:
		m.blurred = false
	case tea.BlurMsg:
		m.blurred = true
	case panesTick, panesMsg, stagedMsg, spawnedMsg, closedMsg:
		return m.stageUpdate(msg)
	case tea.KeyMsg:
		return m.handle(m.keyActions(msg))
	case tea.MouseMsg:
		// The input's time is read here, at the door, so apply stays pure.
		return m.handle(m.mouseActions(msg, now()))
	case noteMsg:
		m = m.say(string(msg))
	case frameMsg:
		m.frame++
		return m, nextFrame()
	case tickMsg:
		if !m.noteAt.IsZero() && time.Since(m.noteAt) > noteFor {
			m.note = ""
		}
		// The orchestrator publishes digests on its own schedule. Follow the
		// newest one unless the reader has gone back to an older one.
		if files := m.home.DigestFiles(); len(files) != len(m.digests) {
			atNewest := m.digestIdx == len(m.digests)-1
			m.digests = files
			if atNewest {
				m = m.showDigest(len(files) - 1)
			}
		}
		return m, tea.Batch(m.refresh, tick())
	case rowsMsg:
		m.rows, m.ttys, m.hosts, m.rowsErr, m.loaded = msg.rows, msg.ttys, msg.hosts, msg.err, true
		if msg.err == nil {
			m.pinned = msg.pinned
		}
		// Keep the cursor on a visible row after sessions come and go.
		m = m.adopt()
	case digestMsg:
		m.digesting = false
		switch {
		case msg.err != nil:
			m = m.say("digest failed: " + msg.err.Error())
		case msg.d.Quiet:
			m = m.say("no activity since " + msg.d.Since.Local().Format("15:04"))
		default:
			m = m.loadDigests().say("new digest written")
		}
	case pickedMsg:
		if m.pick.open && msg.source == m.pick.from {
			m.pick.paths, m.pick.read = msg.paths, true
			m.pick.sel = max(0, min(m.pick.sel, len(m.pickMatches())-1))
		}
	case pinnedMsg:
		if msg.err != nil {
			return m.say("could not add the project: " + msg.err.Error()), nil
		}
		if !slices.Contains(m.pinned, msg.root) {
			m.pinned = append(slices.Clone(m.pinned), msg.root)
		}
		return m.toProject(msg.root).say("pinned " + m.projects.name(msg.root)), nil
	case doneMsg:
		if msg.err != nil {
			m = m.say(msg.err.Error())
		} else if msg.note != "" {
			m = m.say(msg.note)
		}
		return m, m.refresh
	}
	return m, nil
}

// handle translates one input into actions and applies them in order.
func (m model) handle(actions []action) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	for _, a := range actions {
		var cmd tea.Cmd
		m, cmd = m.apply(a)
		cmds = append(cmds, cmd)
	}
	// Whatever happened, the cursor stays on a row that is shown.
	return m.keepSel(), tea.Batch(cmds...)
}

// apply carries out one action. It is the only place input changes the
// model, and it runs nothing itself: any work comes back as a Cmd.
func (m model) apply(a action) (model, tea.Cmd) {
	es := m.ordered()
	e, i, ok := m.selected(es)
	row, isSession := e.session()
	switch a.do {
	case quit:
		return m, tea.Quit
	case detach:
		if m.mux == nil {
			return m, tea.Quit
		}
		mux := m.mux
		return m, func() tea.Msg {
			if err := mux.Detach(); err != nil {
				return noteMsg(err.Error())
			}
			return nil
		}
	case openHelp:
		m.help = true
	case dismiss:
		if m.confirm != "" {
			m.confirm = ""
			return m.say("left it running"), nil
		}
		m.help = false
	case confirmStop:
		key := m.confirm
		m.confirm = ""
		for _, c := range es {
			if c.key() != key {
				continue
			}
			if c.kind == terminalEntry && m.mux != nil {
				return m.say("closing " + c.term.Title + "…"), closeCmd(m.mux, m.self, m.holder(), c.term, m.panes, m.spawns)
			}
		}
		for _, r := range m.rows {
			if r.SessionID == key {
				home, l := m.home, r.Live
				return m.say("stopping " + r.Name + "…"), func() tea.Msg {
					return doneMsg{"stopped " + l.Name, home.Stop(l)}
				}
			}
		}
		return m.say("left it running"), nil
	case showScreen:
		m.screen = screen(a.n)
	case toggleScreen:
		m.screen = 1 - m.screen
	case writeDigest:
		if m.digesting {
			return m, nil
		}
		m.digesting = true
		return m.say("writing a digest…"), m.runDigest
	case refresh:
		return m, m.refresh
	case move:
		return m.moveTo(es, i+a.n), nil
	case moveTo:
		return m.moveTo(es, a.n), nil
	case nextNeedy:
		// Wraps round the list.
		for d := 1; d <= len(es); d++ {
			j := ((i+a.n*d)%len(es) + len(es)) % len(es)
			if r, ok := es[j].session(); ok && r.NeedsYou() {
				return m.moveTo(es, j), nil
			}
		}
		return m.say("nothing needs you"), nil
	case scrollDigest, scrollDigestTo:
		if a.do == scrollDigest {
			a.n += m.digestScroll
		}
		m.digestScroll = max(0, min(a.n, len(m.digestLines())-m.innerHeight()))
	case stepDigest:
		m = m.showDigest(m.digestIdx + a.n)
	case search:
		m.filtering = true
	case typeText, backspace:
		before := m.filter
		if a.do == typeText {
			m.filter += a.text
		} else if r := []rune(m.filter); len(r) > 0 {
			m.filter = string(r[:len(r)-1])
		}
		// New search text puts the cursor on the first match.
		if m.filter != before {
			return m.moveTo(m.ordered(), 0), nil
		}
	case cancelSearch:
		m.filter, m.filtering = "", false
	case clearFilter:
		m.filter = ""
	case open:
		if m.filtering {
			// Act on the match under the cursor, then show everything
			// again so coming back finds the full list.
			m.filtering = false
			if ok {
				m.filter = ""
			}
		}
		if !ok {
			return m, nil
		}
		return m.act(e)
	case click:
		double := a.id == m.clickID && a.at.Sub(m.clickAt) < doubleClick
		m.clickID, m.clickAt = a.id, a.at
		for j, c := range es {
			if c.key() == a.id {
				m = m.moveTo(es, j)
			}
		}
		if double {
			m.clickID = ""
			return m.apply(action{do: open})
		}
	case stop:
		switch {
		case ok && e.kind == terminalEntry:
			m.confirm = e.key()
		case ok && e.kind == projectEntry:
			home, root, name := m.home, e.project, m.projects.name(e.project)
			return m, func() tea.Msg { return doneMsg{"unpinned " + name, home.UnpinProject(root)} }
		case !isSession:
			return m.say("nothing to stop"), nil
		default:
			if app, away := m.hostOf(row); away {
				return m.say(row.Name + " belongs to " + app + ". Close it there."), nil
			}
			if !row.Background() {
				return m.say("only background sessions can be stopped from here"), nil
			}
			m.confirm = row.SessionID
		}
	case newSession:
		return m.start(m.rootOf(e, ok))
	case newTerminal:
		return m.openTerminal(m.rootOf(e, ok))
	case addProject:
		m.pick = picker{open: true}
		return m.readPicks()
	case pickType, pickBack:
		q := m.pick.query
		if a.do == pickType {
			q += a.text
		} else if r := []rune(q); len(r) > 0 {
			q = string(r[:len(r)-1])
		}
		return m.setPickQuery(q)
	case pickMove:
		n := len(m.pickMatches())
		m.pick.sel = max(0, min(m.pick.sel+a.n, n-1))
	case pickTab:
		if ms := m.pickMatches(); m.pick.sel < len(ms) {
			return m.setPickQuery(pickComplete(m.pick.query, m.projects.home, ms[m.pick.sel]))
		}
	case pickEnter:
		path, typed := pickTarget(m.pick.query, m.projects.home, m.pickMatches(), m.pick.sel)
		if path == "" {
			return m.say("nothing to add"), nil
		}
		m.pick = picker{}
		return m.say("adding " + path + "…"), pinCmd(m.home.PinProject, path, typed, m.projects.resolve, m.projects.exists)
	case pickCancel:
		m.pick = picker{}
	case copyID:
		if !isSession {
			return m, nil
		}
		text := row.SessionID
		if row.Background() {
			text = "claude attach " + row.ID
		}
		return m, func() tea.Msg { return copied(text, copyText(text)) }
	}
	return m, nil
}

// knownRoots are the roots of the projects in the list: those of the
// sessions and terminals, and the pinned ones.
func (m model) knownRoots() []string {
	var roots []string
	for _, r := range m.rows {
		if r.Name != fleet.OrchestratorName {
			roots = append(roots, m.projects.root(r.Cwd))
		}
	}
	for _, t := range m.terminals() {
		roots = append(roots, t.Project)
	}
	return append(roots, m.pinned...)
}

// pickMatches is what the picker offers now.
func (m model) pickMatches() []string {
	shown := map[string]bool{}
	for _, r := range m.knownRoots() {
		shown[r] = true
	}
	return pickMatches(m.pick.query, m.projects.home, m.pick.paths, shown, fleet.ShortDir)
}

// setPickQuery changes the picker's query, and reads the directories it
// needs when they are not the ones read already.
func (m model) setPickQuery(q string) (model, tea.Cmd) {
	m.pick.query, m.pick.sel = q, 0
	if src := pickSource(q, m.projects.home); src != m.pick.from {
		m.pick.from, m.pick.paths, m.pick.read = src, nil, false
		return m.readPicks()
	}
	return m, nil
}

// readPicks reads what the picker's source needs: the folders beside the
// known projects and the one overview was started in, or a path query's
// directory.
func (m model) readPicks() (model, tea.Cmd) {
	dirs := []string{m.pick.from}
	if m.pick.from == "" {
		dirs = pickParents(append(m.knownRoots(), m.projects.root(m.cwd)))
	}
	return m, readPathsCmd(m.pick.from, dirs)
}

// toProject puts the cursor on a project's first entry, clearing a search
// that hides it.
func (m model) toProject(root string) model {
	name := m.projects.name(root)
	find := func() (string, bool) {
		for _, s := range m.sections() {
			if !s.NeedsYou && !s.Away && s.Title == name && len(s.Entries) > 0 {
				return s.Entries[0].key(), true
			}
		}
		return "", false
	}
	key, ok := find()
	if !ok && m.filter != "" {
		m.filter, m.filtering = "", false
		key, ok = find()
	}
	if ok {
		m.sel, m.confirm, m.opened = key, "", tmux.Pane{}
	}
	return m
}

// rootOf is the root of the project an entry belongs to, where c and t
// start: a session's, a terminal's, or a pinned project's. With nothing
// selected it is the project overview was started in.
func (m model) rootOf(e entry, ok bool) string {
	switch {
	case !ok:
		return m.projects.root(m.cwd)
	case e.kind == terminalEntry:
		return e.term.Project
	case e.kind == projectEntry:
		return e.project
	}
	return m.projects.root(e.row.Cwd)
}

// now is the clock that stamps mouse input. Tests replace it.
var now = time.Now

// copyText puts text on the clipboard. Tests replace it.
var copyText = func(text string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

func copied(text string, err error) tea.Msg {
	if err != nil {
		return noteMsg("copy failed: " + err.Error())
	}
	return noteMsg("copied " + text)
}

// act does what enter means for an entry: it moves the focus beside the
// list. A background session gets an attach pane first. One that runs
// inside another app brings that app forward, since that is where it is
// used. A pinned project with nothing in it only says how to start one.
func (m model) act(e entry) (model, tea.Cmd) {
	switch e.kind {
	case projectEntry:
		return m.say("Nothing runs in " + m.projects.name(e.project) + " yet. c starts Claude here, t a terminal."), nil
	case terminalEntry:
		if m.mux == nil {
			return m.say("terminals open only inside overview's tmux"), nil
		}
		m.entering = true
		return m, nil
	}
	row := e.row
	if h, away := m.hosts[row.PID]; away && !row.Background() {
		if h.Path == "" || m.reveal == nil {
			return m.say(awayNote(row.Name, h)), nil
		}
		reveal := m.reveal
		return m.say("opening " + h.Name + "…"), func() tea.Msg {
			return doneMsg{row.Name + " is in " + h.Name, reveal(h.Path)}
		}
	}
	if m.mux == nil {
		return m.say("sessions open only inside overview's tmux"), nil
	}
	if m.paneOf(row) != "" {
		m.entering = true
		return m, nil
	}
	if !row.Background() {
		return m.say(m.stageHint(row)), nil
	}
	attach := m.home.AttachCmd(row.Live)
	return m.say("attaching to " + row.Name + "…"), spawnCmd(m.mux, spawn{kind: spawnAttach, key: row.ID, name: row.Name,
		session: m.session, dir: row.Cwd, env: m.claudeEnv(), command: attach.Args})
}

// claudeEnv is what a pane overview starts is given, so a claude in it,
// started by overview or typed by the user, joins this config directory.
func (m model) claudeEnv() []string { return []string{m.home.ClaudeEnv()} }

// start runs a new claude at dir, a project's root, in a window of its own
// that the stage shows until the session joins the list.
func (m model) start(dir string) (model, tea.Cmd) {
	if dir == "" {
		return m.say("there is no directory to start a session in"), nil
	}
	if m.mux == nil {
		return m.say("sessions start only inside overview's tmux"), nil
	}
	where := m.projects.name(dir)
	return m.say("starting a session in " + where + "…"), spawnCmd(m.mux, spawn{kind: spawnSession, name: "a session in " + where,
		session: m.session, dir: dir, env: m.claudeEnv(), command: m.home.NewCmd().Args})
}

// openTerminal starts tmux's default shell at dir, a project's root, in a
// window of its own, and the cursor goes to it.
func (m model) openTerminal(dir string) (model, tea.Cmd) {
	if dir == "" {
		return m.say("there is no directory to open a terminal in"), nil
	}
	if m.mux == nil {
		return m.say("terminals open only inside overview's tmux"), nil
	}
	where := m.projects.name(dir)
	return m.say("opening a terminal in " + where + "…"), spawnCmd(m.mux, spawn{kind: spawnTerminal, name: "a terminal in " + where,
		session: m.session, dir: dir, env: m.claudeEnv()})
}

// awayNote says where a session that runs inside another app is.
func awayNote(name string, h host) string {
	if h.Name == "" {
		return name + " runs without a terminal, so there is nowhere to go."
	}
	return name + " runs inside " + h.Name + ". Switch to " + h.Name + " to use it."
}
