// Package tui is the live terminal view: the sessions that need you on top,
// a detail pane for the one under the cursor, and the digests a key away.
package tui

import (
	"os"
	"os/exec"
	"strconv"
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
		rows []fleet.Row
		ttys map[int]string
		err  error
	}
	digestMsg struct {
		d   fleet.Digest
		err error
	}
	// jumpMsg reports how a jump to a Ghostty terminal went. The Cmd that
	// ran it does not touch the model; Update applies this.
	jumpMsg struct {
		key  string // cache key
		id   string // terminal id to remember, if the jump worked
		drop bool   // forget the cached id
		note string // what to tell the user when it failed
	}
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
	rowsErr error
	loaded  bool

	screen    screen
	focus     pane
	sel       string // session id under the cursor, kept across refreshes
	scroll    int    // detail pane
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

	w, h int
}

// Run opens the view and blocks until the user quits.
func Run(home fleet.Home, llm string) error {
	m := newModel(home, llm)
	m.jump = ghostty.New()
	m = m.loadDigests()
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func newModel(home fleet.Home, llm string) model {
	return model{home: home, llm: llm, cache: &detailCache{}, projects: newProjects(),
		terms: map[string]string{}}
}

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
	return rowsMsg{rows, terminals(rows), err}
}

// terminals finds the tty each interactive session runs in, which is the
// only way to tell which terminal tab to go to.
func terminals(rows []fleet.Row) map[int]string {
	var pids []string
	for _, r := range rows {
		if !r.Background() && r.PID > 0 {
			pids = append(pids, strconv.Itoa(r.PID))
		}
	}
	out := map[int]string{}
	if len(pids) == 0 {
		return out
	}
	b, _ := exec.Command("ps", "-o", "pid=,tty=", "-p", strings.Join(pids, ",")).Output()
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			if pid, err := strconv.Atoi(f[0]); err == nil && f[1] != "??" {
				out[pid] = f[1]
			}
		}
	}
	return out
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
	return buildList(m.rows, m.filter, m.projects.name)
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
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tea.KeyMsg:
		return m.key(msg)
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
		m.rows, m.ttys, m.rowsErr, m.loaded = msg.rows, msg.ttys, msg.err, true
		// Keep the cursor on a visible row after sessions come and go.
		m = m.keepSel()
	case jumpMsg:
		if msg.drop {
			delete(m.terms, msg.key)
		}
		if msg.id != "" {
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

func (m model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if m.filtering {
		before := m.filter
		switch msg.Type {
		case tea.KeyEsc:
			// The cursor's row is still shown once the filter is gone.
			m.filter, m.filtering = "", false
			return m.keepSel(), nil
		case tea.KeyEnter:
			m.filtering = false
			rows := m.ordered()
			if row, _, ok := m.selected(rows); ok && len(rows) > 0 {
				// Act on the match under the cursor, then show everything
				// again so coming back finds the full list.
				m.filter = ""
				m.sel = row.SessionID
				return m.act(row, true)
			}
			return m, nil
		case tea.KeyUp, tea.KeyCtrlP, tea.KeyDown, tea.KeyCtrlN:
			rows := m.ordered()
			_, i, _ := m.selected(rows)
			if msg.Type == tea.KeyUp || msg.Type == tea.KeyCtrlP {
				return m.moveTo(rows, i-1), nil
			}
			return m.moveTo(rows, i+1), nil
		case tea.KeyBackspace:
			if r := []rune(m.filter); len(r) > 0 {
				m.filter = string(r[:len(r)-1])
			}
		case tea.KeyRunes, tea.KeySpace:
			m.filter += printable(string(msg.Runes))
		case tea.KeyCtrlC:
			return m, tea.Quit
		}
		// New filter text puts the cursor on the first match. Keys that left
		// the text alone must not undo where the user arrowed to.
		if m.filter != before {
			return m.moveTo(m.ordered(), 0), nil
		}
		return m.keepSel(), nil
	}
	if m.confirm != "" {
		id := m.confirm
		m.confirm = ""
		if k == "y" {
			for _, r := range m.rows {
				if r.SessionID == id {
					home, l := m.home, r.Live
					return m.say("stopping " + r.Name + "…"), func() tea.Msg {
						return doneMsg{"stopped " + l.Name, home.Stop(l)}
					}
				}
			}
		}
		return m.say("left it running"), nil
	}
	if m.help {
		m.help = false
		return m, nil
	}
	// Several runes in one message are a paste or a fast burst, not
	// keystrokes. They can start a filter but never trigger an action.
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 {
		if msg.Runes[0] == '/' && m.screen == sessionsScreen {
			m.filter, m.filtering, m.focus = printable(string(msg.Runes[1:])), true, listPane
			return m.moveTo(m.ordered(), 0), nil
		}
		return m, nil
	}

	switch k {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.help = true
		return m, nil
	case "d":
		m.screen = 1 - m.screen
		return m, nil
	case "1":
		m.screen = sessionsScreen
		return m, nil
	case "2":
		m.screen = digestScreen
		return m, nil
	case "D":
		if m.digesting {
			return m, nil
		}
		m.digesting = true
		return m.say("writing a digest…"), m.runDigest
	case "r":
		return m, m.refresh
	}
	if m.screen == digestScreen {
		return m.digestKey(k), nil
	}

	rows := m.ordered()
	row, i, ok := m.selected(rows)
	page := max(m.h/2, 1)
	if m.focus == detailPane {
		switch k {
		case "j", "down":
			m.scroll++
		case "k", "up":
			m.scroll--
		case "g", "home":
			m.scroll = 0
		case "G", "end":
			m.scroll = 1 << 30
		case "esc", "h", "left", "tab", "shift+tab":
			m.focus = listPane
			return m, nil
		}
	} else {
		switch k {
		case "j", "down":
			return m.moveTo(rows, i+1), nil
		case "k", "up":
			return m.moveTo(rows, i-1), nil
		case "g", "home":
			return m.moveTo(rows, 0), nil
		case "G", "end":
			return m.moveTo(rows, len(rows)-1), nil
		case "l", "right", "tab", "shift+tab":
			m.focus = detailPane
			return m, nil
		case "esc":
			m.filter = ""
			return m.keepSel(), nil
		}
	}

	switch k {
	case "n", "N":
		// Jump to the next (or previous) session that needs you, wrapping.
		step := 1
		if k == "N" {
			step = -1
		}
		for d := 1; d <= len(rows); d++ {
			j := ((i+step*d)%len(rows) + len(rows)) % len(rows)
			if rows[j].NeedsYou() {
				m.focus = listPane
				return m.moveTo(rows, j), nil
			}
		}
		return m.say("nothing needs you"), nil
	case "J", "ctrl+e":
		m.scroll++
	case "K", "ctrl+y":
		m.scroll--
	case "ctrl+d", "pgdown", " ":
		m.scroll += page
	case "ctrl+u", "pgup":
		m.scroll -= page
	case "/":
		m.filtering, m.focus = true, listPane
	case "enter", "a":
		if !ok {
			return m, nil
		}
		return m.act(row, k == "enter")
	case "x":
		if !ok || !row.Background() {
			return m.say("only background sessions can be stopped from here"), nil
		}
		m.confirm = row.SessionID
	case "y":
		if !ok {
			return m, nil
		}
		text := row.SessionID
		if row.Background() {
			text = "claude attach " + row.ID
		}
		cmd := exec.Command("pbcopy")
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err != nil {
			return m.say("copy failed: " + err.Error()), nil
		}
		return m.say("copied " + text), nil
	}
	m.scroll = m.clampScroll()
	return m, nil
}

// act does what enter means for a row. A background session is attached. An
// interactive one is jumped to in Ghostty when jump is set (enter), and
// otherwise only reported with its tty (the `a` key).
func (m model) act(row fleet.Row, jump bool) (tea.Model, tea.Cmd) {
	if row.Background() {
		return m, tea.ExecProcess(m.home.AttachCmd(row.Live), func(err error) tea.Msg {
			return doneMsg{"back from " + row.Name, err}
		})
	}
	tty := m.ttys[row.PID]
	if !jump || m.jump == nil || tty == "" {
		return m.say(ttyNote(row.Name, tty, "")), nil
	}
	key := row.SessionID + "\x00" + tty
	return m, jumpCmd(m.jump, row.Name, key, tty, row.PID, m.terms[key])
}

func (m model) digestKey(k string) model {
	page := max(m.h/2, 1)
	switch k {
	case "j", "down", "J":
		m.digestScroll++
	case "k", "up", "K":
		m.digestScroll--
	case "ctrl+d", "pgdown", " ":
		m.digestScroll += page
	case "ctrl+u", "pgup":
		m.digestScroll -= page
	case "g", "home":
		m.digestScroll = 0
	case "G", "end":
		m.digestScroll = 1 << 30
	case "[", "h", "left":
		m = m.showDigest(m.digestIdx - 1)
	case "]", "l", "right":
		m = m.showDigest(m.digestIdx + 1)
	case "esc", "tab":
		m.screen = sessionsScreen
	}
	m.digestScroll = max(0, min(m.digestScroll, len(m.digestLines())-m.innerHeight()))
	return m
}
