// Package tui is the live terminal view: the sessions that need you on top,
// the one under the cursor live in the tmux pane beside it, and the digests
// a key away.
package tui

import (
	"errors"
	"os"
	"os/exec"
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
		rows  []fleet.Row
		ttys  map[int]string
		hosts map[int]host
		err   error
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
	rowsErr error
	loaded  bool

	screen    screen
	sel       string // session id under the cursor, kept across refreshes
	listTop   int    // first list line shown, kept so clicks do not shift it
	filter    string
	filtering bool
	confirm   string // session id waiting on a yes to stop it
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
	// nothing is staged. The view runs in pane self of session, and stage
	// is the window self is in. exe starts the holder.
	mux                   multiplexer
	self, session         string
	exe                   string
	panes                 []tmux.Pane
	stage, holder, failed string
	staging, entering     bool
	spawningHolder        bool
	holderShown           string
	// attached maps a background session's ID to its attach pane. A map,
	// so copies of the model share it, and only Update writes to it.
	attached map[string]string
	// opened is the pane c started, shown until its session is listed.
	opened tmux.Pane
	// blurred is set while the focus is in another pane.
	blurred bool

	// The last click on a list row, to tell a double click.
	clickID string
	clickAt time.Time

	w, h int
}

// Run opens the view in the given theme and blocks until the user quits.
// It runs in the pane Launch made for it in overview's tmux.
func Run(home fleet.Home, llm string, theme Theme) error {
	useTheme(theme)
	m := newModel(home, llm)
	m.cwd, _ = os.Getwd()
	session, self := os.Getenv(envSession), os.Getenv("TMUX_PANE")
	if session == "" || self == "" {
		return errors.New("the view runs inside overview's tmux: start it with plain `overview`")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	srv := tmux.New("")
	if err := srv.MarkList(self); err != nil {
		return err
	}
	m.mux, m.self, m.session, m.exe = srv, self, session, exe
	m.holder = srv.Option(session, holderOption)
	if err := unpark(srv, session); err != nil {
		m = m.say("could not move the sessions from the parked session: " + err.Error())
	}
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
		attached: map[string]string{}, reveal: openApp}
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
		cmds = append(cmds, listPanes(m.mux), panesEveryTick())
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
	return rowsMsg{rows, ttys, hosts, err}
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
	return buildList(m.rows, m.filter, m.projects.name, m.hostOf)
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
func (m model) ordered() []fleet.Row { return flatten(m.sections()) }

// selected returns the row under the cursor and its place in rows, falling
// back to the first row when the selected session has gone.
func (m model) selected(rows []fleet.Row) (fleet.Row, int, bool) {
	for i, r := range rows {
		if r.SessionID == m.sel {
			return r, i, true
		}
	}
	if len(rows) == 0 {
		return fleet.Row{}, -1, false
	}
	return rows[0], 0, true
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

// keepSel puts the cursor on the first row if it is on none that is shown.
func (m model) keepSel() model {
	rows := m.ordered()
	if len(rows) == 0 {
		return m
	}
	for _, r := range rows {
		if r.SessionID == m.sel {
			return m
		}
	}
	m.sel, m.confirm = rows[0].SessionID, ""
	return m
}

// moveTo puts the cursor on the ith row. Moving it also stops showing a
// pane c started, so the stage follows the cursor again.
func (m model) moveTo(rows []fleet.Row, i int) model {
	if len(rows) == 0 {
		return m
	}
	i = max(0, min(i, len(rows)-1))
	if rows[i].SessionID != m.sel {
		m.sel, m.confirm, m.opened = rows[i].SessionID, "", tmux.Pane{}
	}
	return m
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	m = next.(model)
	// Keep the list where it is, scrolled only as far as the cursor needs.
	if m.w > 0 && m.screen == sessionsScreen {
		row, _, _ := m.selected(m.ordered())
		_, owners := m.listLines(m.sections(), row.SessionID, 1)
		m.listTop = m.listStart(owners, row.SessionID, m.boxHeight()-2)
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
	case panesTick, panesMsg, stagedMsg, spawnedMsg:
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
		// Keep the cursor on a visible row after sessions come and go.
		m = m.adopt().keepSel()
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
	rows := m.ordered()
	row, i, ok := m.selected(rows)
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
		id := m.confirm
		m.confirm = ""
		for _, r := range m.rows {
			if r.SessionID == id {
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
		return m.moveTo(rows, i+a.n), nil
	case moveTo:
		return m.moveTo(rows, a.n), nil
	case nextNeedy:
		// Wraps round the list.
		for d := 1; d <= len(rows); d++ {
			j := ((i+a.n*d)%len(rows) + len(rows)) % len(rows)
			if rows[j].NeedsYou() {
				return m.moveTo(rows, j), nil
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
		return m.act(row)
	case click:
		double := a.id == m.clickID && a.at.Sub(m.clickAt) < doubleClick
		m.clickID, m.clickAt = a.id, a.at
		for j, r := range rows {
			if r.SessionID == a.id {
				m = m.moveTo(rows, j)
			}
		}
		if double {
			m.clickID = ""
			return m.apply(action{do: open})
		}
	case stop:
		if app, away := m.hostOf(row); ok && away {
			return m.say(row.Name + " belongs to " + app + ". Close it there."), nil
		}
		if !ok || !row.Background() {
			return m.say("only background sessions can be stopped from here"), nil
		}
		m.confirm = row.SessionID
	case newSession:
		if !ok {
			// With nothing listed, start where overview itself was started.
			row = fleet.Row{Live: fleet.Live{Name: "overview", Cwd: m.cwd}}
		}
		return m.start(row)
	case copyID:
		if !ok {
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

// act does what enter means for a row: it moves the focus to the session
// on the stage. A background session gets an attach pane first. One that
// runs inside another app brings that app forward, since that is where it
// is used.
func (m model) act(row fleet.Row) (model, tea.Cmd) {
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
	env := []string{"CLAUDE_CONFIG_DIR=" + string(m.home)}
	return m.say("attaching to " + row.Name + "…"),
		spawnCmd(m.mux, spawnAttach, row.ID, row.Name, m.session, row.Cwd, env, attach.Args...)
}

// start runs a new claude at the root of row's project, in a window of its
// own that the stage shows until the session joins the list.
func (m model) start(row fleet.Row) (model, tea.Cmd) {
	dir, where := m.projects.root(row.Cwd), m.projects.name(row.Cwd)
	if dir == "" {
		return m.say(row.Name + " has no directory, so there is no project to start in"), nil
	}
	if m.mux == nil {
		return m.say("sessions start only inside overview's tmux"), nil
	}
	env := []string{"CLAUDE_CONFIG_DIR=" + string(m.home)}
	return m.say("starting a session in " + where + "…"),
		spawnCmd(m.mux, spawnSession, "", "a session in "+where, m.session, dir, env, m.home.NewCmd().Args...)
}

// awayNote says where a session that runs inside another app is.
func awayNote(name string, h host) string {
	if h.Name == "" {
		return name + " runs without a terminal, so there is nowhere to go."
	}
	return name + " runs inside " + h.Name + ". Switch to " + h.Name + " to use it."
}
