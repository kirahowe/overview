// Package tui is the live terminal view: the sessions that need you on top,
// a detail pane for the one under the cursor, and the digests a key away.
package tui

import (
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"overview/internal/fleet"
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

	w, h int
}

// Run opens the view and blocks until the user quits.
func Run(home fleet.Home, llm string) error {
	m := model{home: home, llm: llm, cache: &detailCache{}}
	m = m.loadDigests()
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
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

// group places a session in the list: what needs you, then what is working,
// then the rest.
func group(r fleet.Row) int {
	switch {
	case r.NeedsYou():
		return 0
	case r.State == fleet.Working:
		return 1
	}
	return 2
}

// ordered is the list as shown: filtered, grouped, the longest wait first
// among what needs you and the latest activity first elsewhere. The
// orchestrator is left out, since the status bar already reports on it.
func (m model) ordered() []fleet.Row {
	var out []fleet.Row
	q := strings.ToLower(m.filter)
	for _, r := range m.rows {
		if r.Name == fleet.OrchestratorName {
			continue
		}
		hay := strings.ToLower(strings.Join([]string{r.Name, r.Cwd, r.Title, r.Label, r.LastPrompt}, " "))
		if q != "" && !strings.Contains(hay, q) {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		gi, gj := group(out[i]), group(out[j])
		if gi != gj {
			return gi < gj
		}
		if gi == 0 {
			return out[i].Since.Before(out[j].Since)
		}
		return out[i].Since.After(out[j].Since)
	})
	return out
}

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
		if rows := m.ordered(); m.sel == "" && len(rows) > 0 {
			m.sel = rows[0].SessionID
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
		switch msg.Type {
		case tea.KeyEsc:
			m.filter, m.filtering = "", false
		case tea.KeyEnter:
			m.filtering = false
		case tea.KeyBackspace:
			if r := []rune(m.filter); len(r) > 0 {
				m.filter = string(r[:len(r)-1])
			}
		case tea.KeyRunes, tea.KeySpace:
			m.filter += string(msg.Runes)
		case tea.KeyCtrlC:
			return m, tea.Quit
		}
		return m.moveTo(m.ordered(), 0), nil
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
			m.filter, m.filtering, m.focus = string(msg.Runes[1:]), true, listPane
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
			return m, nil
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
		if !row.Background() {
			where := "its own terminal"
			if tty := m.ttys[row.PID]; tty != "" {
				where = tty
			}
			return m.say(row.Name + " is interactive, in " + where + ". Switch to that tab to answer it."), nil
		}
		return m, tea.ExecProcess(m.home.AttachCmd(row.Live), func(err error) tea.Msg {
			return doneMsg{"back from " + row.Name, err}
		})
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
