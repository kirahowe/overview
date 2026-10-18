// Package tui is the live terminal view: the sessions that need you on top,
// a detail pane for the one under the cursor, and the digests a key away.
package tui

import (
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"overview/internal/fleet"
	"overview/internal/ghostty"
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

type pane int

const (
	listPane pane = iota
	detailPane
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
	// jumpMsg reports how a jump to a Ghostty terminal went. The Cmd that
	// ran it does not touch the model; Update applies this.
	jumpMsg struct {
		seq  int    // which jump this answers
		key  string // cache key
		id   string // terminal id to remember, if the jump worked
		drop bool   // forget the cached id
		note string // what to tell the user when it failed
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
	focus     pane
	sel       string // session id under the cursor, kept across refreshes
	scroll    int    // detail pane
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

	// The detail pane is rebuilt only when what it shows changes, not on
	// every spinner frame. A pointer, because the model is passed by value.
	cache *detailCache

	projects *projects

	// jump finds and focuses a session's Ghostty terminal; nil means no
	// jumping. terms caches session+tty -> terminal id. It is a map, so
	// copies of the model share it, and only Update writes to it.
	jump  jumper
	terms map[string]string
	// tabs opens new Ghostty tabs; nil means it cannot.
	tabs tabber
	// reveal brings an app to the front. Tests replace it.
	reveal func(path string) error
	// jumping is the seq of the jump in flight, 0 if none. Only one runs at
	// a time: two Locates on one tty would each take the other's token for
	// the terminal's real title.
	jumping, jumpSeq int

	// The last click on a list row, to tell a double click.
	clickID string
	clickAt time.Time

	w, h int
}

// Run opens the view in the given theme and blocks until the user quits.
func Run(home fleet.Home, llm string, theme Theme) error {
	useTheme(theme)
	m := newModel(home, llm)
	gh := ghostty.New()
	m.jump, m.tabs = gh, gh
	m = m.loadDigests()
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}

func newModel(home fleet.Home, llm string) model {
	return model{home: home, llm: llm, cache: &detailCache{}, projects: newProjects(),
		terms: map[string]string{}, reveal: openApp}
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

func (m model) Init() tea.Cmd { return tea.Batch(m.refresh, tick(), nextFrame()) }

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
	m.sel, m.scroll, m.confirm = rows[0].SessionID, 0, ""
	return m
}

func (m model) moveTo(rows []fleet.Row, i int) model {
	if len(rows) == 0 {
		return m
	}
	i = max(0, min(i, len(rows)-1))
	if rows[i].SessionID != m.sel {
		m.sel, m.scroll, m.confirm = rows[i].SessionID, 0, ""
	}
	return m
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	m = next.(model)
	// Keep the list where it is, scrolled only as far as the cursor needs.
	if m.w > 0 && m.screen == sessionsScreen {
		row, _, _ := m.selected(m.ordered())
		_, lh, _, _ := m.split()
		_, owners := m.listLines(m.sections(), row.SessionID, 1)
		m.listTop = m.listStart(owners, row.SessionID, lh-2)
	}
	return m, cmd
}

func (m model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
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
		m = m.keepSel()
	case jumpMsg:
		if msg.drop {
			delete(m.terms, msg.key)
		}
		if msg.seq == 0 || msg.seq != m.jumping {
			return m, nil // not the jump in flight
		}
		m.jumping = 0
		// Remember the terminal only while a shown session still has that
		// tty; the row may have gone, or its session moved, meanwhile.
		if msg.id != "" && m.hasJumpKey(msg.key) {
			m.terms[msg.key] = msg.id
		}
		if msg.note != "" {
			m = m.say(msg.note)
		}
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
				m.focus = listPane
				return m.moveTo(rows, j), nil
			}
		}
		return m.say("nothing needs you"), nil
	case focus:
		m.focus = pane(a.n)
	case scroll, scrollTo:
		if a.do == scroll {
			a.n += m.scroll
		}
		m.scroll = a.n
		m.scroll = m.clampScroll()
	case scrollDigest, scrollDigestTo:
		if a.do == scrollDigest {
			a.n += m.digestScroll
		}
		m.digestScroll = max(0, min(a.n, len(m.digestLines())-m.innerHeight()))
	case stepDigest:
		m = m.showDigest(m.digestIdx + a.n)
	case search:
		m.filtering, m.focus = true, listPane
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
	case open, attach:
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
		return m.act(row, a.do == open)
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
	case newSession, newTab:
		if !ok {
			return m.say("no session selected, so no project to open"), nil
		}
		return m.openTab(row, a.do == newSession)
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

// act does what enter means for a row. A background session is attached. An
// interactive one is jumped to in Ghostty when jump is set (enter), and
// otherwise only reported with its tty (the `a` key). One that runs inside
// another app brings that app forward, since that is where it is used.
func (m model) act(row fleet.Row, jump bool) (model, tea.Cmd) {
	if row.Background() {
		return m, tea.ExecProcess(m.home.AttachCmd(row.Live), func(err error) tea.Msg {
			return doneMsg{"back from " + row.Name, err}
		})
	}
	if h, away := m.hosts[row.PID]; away {
		if !jump || h.Path == "" || m.reveal == nil {
			return m.say(awayNote(row.Name, h)), nil
		}
		reveal := m.reveal
		return m.say("opening " + h.Name + "…"), func() tea.Msg {
			return doneMsg{row.Name + " is in " + h.Name, reveal(h.Path)}
		}
	}
	tty := m.ttys[row.PID]
	if !jump || m.jump == nil || tty == "" {
		return m.say(ttyNote(row.Name, tty, "")), nil
	}
	if m.jumping != 0 {
		// A second enter while the first jump runs. Keep the note up.
		return m.say("finding " + row.Name + "'s terminal…"), nil
	}
	key := jumpKey(row.SessionID, tty)
	m.jumpSeq++
	m.jumping = m.jumpSeq
	m = m.say("finding " + row.Name + "'s terminal…")
	return m, jumpCmd(m.jump, m.jumping, row.Name, key, tty, row.PID, m.terms[key])
}

// openTab opens a Ghostty tab at the root of row's project, and starts
// claude there when session is set.
func (m model) openTab(row fleet.Row, session bool) (model, tea.Cmd) {
	dir, where := m.projects.root(row.Cwd), m.projects.name(row.Cwd)
	if dir == "" {
		return m.say(row.Name + " has no directory, so there is no project to open"), nil
	}
	if m.tabs == nil {
		return m.say("opening a tab needs Ghostty"), nil
	}
	what, input := "a tab", ""
	if session {
		what, input = "a session", "claude\n"
	}
	return m.say("opening " + what + " in " + where + "…"), tabCmd(m.tabs, m.home, dir, input, what, where)
}

// jumpKey is the terms cache key: a terminal id is only good for the same
// session on the same tty.
func jumpKey(session, tty string) string { return session + "\x00" + tty }

func (m model) hasJumpKey(key string) bool {
	for _, r := range m.rows {
		if tty := m.ttys[r.PID]; tty != "" && !r.Background() && jumpKey(r.SessionID, tty) == key {
			return true
		}
	}
	return false
}
