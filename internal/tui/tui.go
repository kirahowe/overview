// Package tui is the live terminal view: the sessions that need you on top,
// a detail pane for the one under the cursor, and the digests a tab away.
package tui

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"overview/internal/fleet"
)

const (
	refreshEvery = 2 * time.Second
	noteFor      = 6 * time.Second
	recentTurns  = 6
)

var (
	bold    = lipgloss.NewStyle().Bold(true)
	dim     = lipgloss.NewStyle().Faint(true)
	italic  = lipgloss.NewStyle().Italic(true)
	red     = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	amber   = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
	green   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	heading = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	codeFg  = lipgloss.NewStyle().Foreground(lipgloss.Color("180"))
	bar     = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("252"))
	key     = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
)

func pill(text string, bg lipgloss.Color) string {
	return lipgloss.NewStyle().Background(bg).Foreground(lipgloss.Color("0")).Bold(true).Padding(0, 1).Render(text)
}

type tab int

const (
	sessionsTab tab = iota
	digestTab
)

type (
	tickMsg time.Time
	rowsMsg struct {
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

	tab       tab
	sel       string // session id under the cursor, kept across refreshes
	scroll    int    // detail pane
	filter    string
	filtering bool
	confirm   string // session id waiting on a yes to stop it
	help      bool

	digests      []string
	digestIdx    int
	digest       string
	digestAt     time.Time
	digesting    bool
	digestScroll int

	note   string
	noteAt time.Time

	w, h int
}

// Run opens the view and blocks until the user quits.
func Run(home fleet.Home, llm string) error {
	m := model{home: home, llm: llm}
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

func (m model) Init() tea.Cmd { return tea.Batch(m.refresh, tick()) }

func tick() tea.Cmd {
	return tea.Tick(refreshEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
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

var groupNames = []string{"NEEDS YOU", "WORKING", "IDLE"}

// ordered is the list as shown: filtered, grouped, the longest wait first
// among what needs you and the latest activity first elsewhere. The
// orchestrator is left out, since the header already reports on it.
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
		if msg.Runes[0] == '/' && m.tab == sessionsTab {
			m.filter, m.filtering = string(msg.Runes[1:]), true
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
	case "tab", "shift+tab":
		m.tab = 1 - m.tab
		return m, nil
	case "1":
		m.tab = sessionsTab
		return m, nil
	case "2":
		m.tab = digestTab
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
	if m.tab == digestTab {
		return m.digestKey(k), nil
	}

	rows := m.ordered()
	row, i, ok := m.selected(rows)
	page := max(m.h/2, 1)
	switch k {
	case "j", "down":
		return m.moveTo(rows, i+1), nil
	case "k", "up":
		return m.moveTo(rows, i-1), nil
	case "g", "home":
		return m.moveTo(rows, 0), nil
	case "G", "end":
		return m.moveTo(rows, len(rows)-1), nil
	case "n", "N":
		// Jump to the next (or previous) session that needs you, wrapping.
		step := 1
		if k == "N" {
			step = -1
		}
		for d := 1; d <= len(rows); d++ {
			j := ((i+step*d)%len(rows) + len(rows)) % len(rows)
			if rows[j].NeedsYou() {
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
		m.filtering = true
	case "esc":
		m.filter = ""
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
	switch k {
	case "j", "down", "J":
		m.digestScroll++
	case "k", "up", "K":
		m.digestScroll = max(0, m.digestScroll-1)
	case "ctrl+d", "pgdown", " ":
		m.digestScroll += max(m.h/2, 1)
	case "ctrl+u", "pgup":
		m.digestScroll = max(0, m.digestScroll-max(m.h/2, 1))
	case "g":
		m.digestScroll = 0
	case "[", "h", "left":
		m = m.showDigest(m.digestIdx - 1)
	case "]", "l", "right":
		m = m.showDigest(m.digestIdx + 1)
	}
	return m
}

// Layout.

func (m model) bodyHeight() int { return max(m.h-2, 1) }

// panes returns the widths of the list and the detail pane. A narrow
// terminal stacks them, which the zero detail width signals.
func (m model) panes() (list, detail int) {
	if m.w < 90 {
		return m.w, 0
	}
	list = max(36, min(m.w*2/5, 60))
	return list, m.w - list - 3
}

func (m model) View() string {
	if m.w == 0 {
		return ""
	}
	var body []string
	switch {
	case m.help:
		body = m.helpView()
	case m.tab == digestTab:
		body = m.digestView()
	default:
		body = m.sessionsView()
	}
	h := m.bodyHeight()
	body = pad(body, h)
	for i, l := range body {
		body[i] = fit(l, m.w)
	}
	return strings.Join(append(append([]string{m.header()}, body...), m.footer()), "\n")
}

func (m model) sessionsView() []string {
	rows := m.ordered()
	row, _, ok := m.selected(rows)
	lw, dw := m.panes()
	h := m.bodyHeight()

	if dw == 0 {
		top := h / 2
		out := pad(m.listLines(rows, row.SessionID, lw, top), top)
		out = append(out, dim.Render(strings.Repeat("─", m.w)))
		if ok {
			out = append(out, m.window(m.detailLines(row, m.w), h-top-1)...)
		}
		return out
	}
	list := pad(m.listLines(rows, row.SessionID, lw, h), h)
	var detail []string
	if ok {
		detail = m.window(m.detailLines(row, dw), h)
	}
	detail = pad(detail, h)
	sep := dim.Render("│")
	out := make([]string, h)
	for i := range out {
		out[i] = fit(list[i], lw) + " " + sep + " " + fit(detail[i], dw)
	}
	return out
}

// listLines renders the grouped list and scrolls it so the cursor stays in
// view.
func (m model) listLines(rows []fleet.Row, sel string, w, h int) []string {
	if !m.loaded {
		return []string{dim.Render(" reading sessions…")}
	}
	var lines []string
	if m.rowsErr != nil {
		lines = append(lines, red.Render(" "+m.rowsErr.Error()), "")
	}
	if len(rows) == 0 {
		msg := " no sessions running"
		if m.filter != "" {
			msg = " nothing matches /" + m.filter
		}
		return append(lines, dim.Render(msg))
	}
	counts := map[int]int{}
	for _, r := range rows {
		counts[group(r)]++
	}
	selTop, selBottom, last := 0, 0, -1
	for _, r := range rows {
		if g := group(r); g != last {
			if last != -1 {
				lines = append(lines, "")
			}
			style := dim
			switch g {
			case 0:
				style = red
			case 1:
				style = green
			}
			lines = append(lines, " "+style.Render(groupNames[g])+dim.Render(fmt.Sprintf(" %d", counts[g])))
			last = g
		}
		if r.SessionID == sel {
			selTop = len(lines)
		}
		lines = append(lines, rowLines(r, w, r.SessionID == sel)...)
		if r.SessionID == sel {
			selBottom = len(lines)
		}
	}
	top := 0
	if selBottom > h {
		top = selBottom - h
	}
	return lines[min(top, selTop):]
}

func rowLines(r fleet.Row, w int, selected bool) []string {
	mark, when := dim.Render("○"), dim.Render(Ago(r.Since))
	sub := Subject(r)
	switch {
	case r.State == fleet.Blocked:
		mark, when = red.Render("!"), red.Render("waiting "+Ago(r.Since))
		if sub == "" {
			sub = "blocked"
		}
		sub = red.Render(sub)
	case r.Asking():
		mark, when = amber.Render("?"), amber.Render("waiting "+Ago(r.LastReplyAt))
		sub = amber.Render(question(r.LastReply))
	case r.State == fleet.Working:
		mark, when = green.Render("●"), green.Render(Ago(r.Since))
		sub = dim.Render(sub)
	default:
		sub = dim.Render(sub)
	}
	gutter, name := " ", r.Name
	if selected {
		gutter, name = heading.Render("▌"), bold.Render(name)
	}
	right := dim.Render(project(r)+" · ") + when
	left := fit(gutter+mark+" "+name, max(w-ansi.StringWidth(right)-1, 4))
	return []string{fit(left+" "+right, w), fit(gutter+"   "+sub, w)}
}

// project is the short directory a session runs in, for the list.
func project(r fleet.Row) string {
	if user, _ := os.UserHomeDir(); r.Cwd == user {
		return "~"
	}
	return fleet.ShortDir(r.Cwd)
}

// detailLines renders everything known about one session: where it is, what
// it is waiting on, and its last few turns.
func (m model) detailLines(r fleet.Row, w int) []string {
	var out []string
	add := func(s string) { out = append(out, wrap(s, w)...) }

	var state string
	switch {
	case r.State == fleet.Blocked:
		state = pill("BLOCKED "+Ago(r.Since), "9")
	case r.Asking():
		state = pill("ASKING "+Ago(r.LastReplyAt), "11")
	case r.State == fleet.Working:
		state = pill("WORKING "+Ago(r.Since), "10")
	default:
		state = pill("IDLE "+Ago(r.Since), "245")
	}
	add(bold.Render(r.Name) + "  " + state)

	where := []string{fleet.ShortDir(r.Cwd)}
	if r.Background() {
		where = append(where, "background "+r.ID)
	} else {
		kind := "interactive"
		if tty := m.ttys[r.PID]; tty != "" {
			kind += " in " + tty
		}
		where = append(where, kind)
	}
	if r.Label != "" {
		where = append(where, r.Label)
	}
	where = append(where, "started "+Ago(time.UnixMilli(r.StartedAt))+" ago")
	if !r.Hooked {
		where = append(where, "no hook")
	}
	add(dim.Render(strings.Join(where, " · ")))
	if r.Title != "" {
		add(italic.Render(r.Title))
	}
	out = append(out, "")

	switch {
	case r.State == fleet.Blocked:
		reason := r.Reason
		if reason == "" {
			reason = "Claude Code marks it as waiting on you. Attach to see what for."
		}
		add(red.Render("Blocked: ") + reason)
		out = append(out, "")
	case r.Asking():
		add(amber.Render("Asking: ") + question(r.LastReply))
		out = append(out, "")
	}

	turns := fleet.RecentTurns(r.Transcript, recentTurns)
	if len(turns) == 0 {
		if r.LastPrompt != "" {
			add(heading.Render("› ") + r.LastPrompt)
		}
		return out
	}
	add(heading.Render("Recent") + dim.Render(fmt.Sprintf("  last %d turns", len(turns))))
	for i, t := range turns {
		out = append(out, "")
		prompt := wrap(heading.Render("› ")+bold.Render(oneLine(t.Prompt)), w)
		if len(prompt) > 3 {
			prompt = append(prompt[:3], dim.Render("  …"))
		}
		out = append(out, prompt...)
		if t.Reply == "" {
			out = append(out, dim.Render("  (no reply yet)"))
			continue
		}
		lines := wrap(styleMarkdown(t.Reply), w)
		// Earlier turns are context. Only the last one is shown in full.
		if i < len(turns)-1 && len(lines) > 8 {
			lines = append(lines[:8], dim.Render(fmt.Sprintf("  … %d more lines", len(lines)-8)))
		}
		out = append(out, lines...)
	}
	return out
}

// window cuts the detail pane to its scroll position and height.
func (m model) window(lines []string, h int) []string {
	start := max(0, min(m.scroll, len(lines)-h))
	return lines[start:min(start+h, len(lines))]
}

// clampScroll keeps the detail scroll inside its content, so scrolling back
// up after overshooting the end responds at once.
func (m model) clampScroll() int {
	row, _, ok := m.selected(m.ordered())
	if !ok {
		return 0
	}
	_, dw := m.panes()
	h := m.bodyHeight()
	if dw == 0 {
		dw, h = m.w, h-h/2-1
	}
	return max(0, min(m.scroll, len(m.detailLines(row, dw))-h))
}

func (m model) digestView() []string {
	if m.digest == "" {
		return []string{"", dim.Render("  No digest yet. Press D to write one, or start the orchestrator.")}
	}
	w := min(m.w-4, 100)
	lines := wrap(styleDigest(m.digest), w)
	h := m.bodyHeight()
	start := max(0, min(m.digestScroll, len(lines)-h))
	lines = lines[start:min(start+h, len(lines))]
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return lines
}

func (m model) helpView() []string {
	keys := [][2]string{
		{"j k ↑ ↓", "move between sessions"},
		{"n N", "next / previous session that needs you"},
		{"g G", "first / last session"},
		{"enter a", "attach to a background session"},
		{"x", "stop a background session (asks first)"},
		{"y", "copy the attach command, or an interactive session's id"},
		{"J K ctrl+d ctrl+u", "scroll the detail pane"},
		{"/", "filter by name, project, title or prompt; esc clears"},
		{"tab 1 2", "sessions / digest"},
		{"[ ]", "older / newer digest (digest tab)"},
		{"D", "write a digest now"},
		{"r", "refresh now"},
		{"q", "quit"},
	}
	out := []string{"", "  " + heading.Render("Keys"), ""}
	for _, k := range keys {
		out = append(out, fmt.Sprintf("  %s %s", key.Render(fmt.Sprintf("%-18s", k[0])), k[1]))
	}
	return append(out, "",
		"  "+red.Render("!")+" blocked on a prompt    "+amber.Render("?")+" idle, last reply asks you something",
		"  "+green.Render("●")+" working                "+dim.Render("○")+" idle",
		"", dim.Render("  any key closes this"))
}

func (m model) header() string {
	count := map[int]int{}
	n := 0
	for _, r := range m.rows {
		if r.Name != fleet.OrchestratorName {
			count[group(r)]++
			n++
		}
	}
	tabs := []string{" sessions ", " digest "}
	tabs[m.tab] = lipgloss.NewStyle().Reverse(true).Bold(true).Render(tabs[m.tab])
	left := bold.Render(" ◆ overview "+m.home.Name()) + "  " + tabs[0] + tabs[1]

	parts := []string{fmt.Sprintf("%d sessions", n)}
	if c := count[0]; c > 0 {
		parts = append(parts, red.Render(fmt.Sprintf("%d need you", c)))
	}
	if c := count[1]; c > 0 {
		parts = append(parts, green.Render(fmt.Sprintf("%d working", c)))
	}
	parts = append(parts, fmt.Sprintf("%d idle", count[2]))
	if m.loaded && m.rowsErr == nil {
		switch h := m.home.HealthOf(m.rows); {
		case !h.Running:
			parts = append(parts, red.Render("orchestrator off"))
		case h.Stale:
			parts = append(parts, red.Render("orchestrator silent "+Ago(h.LastCheck)))
		default:
			parts = append(parts, dim.Render("orchestrator ok"))
		}
	}
	right := strings.Join(parts, dim.Render(" · ")) + " "
	gap := max(m.w-ansi.StringWidth(left)-ansi.StringWidth(right), 1)
	return bar.Render(fit(left+strings.Repeat(" ", gap)+right, m.w))
}

func (m model) footer() string {
	hint := func(k, what string) string { return key.Render(k) + " " + dim.Render(what) }
	var parts []string
	switch {
	case m.filtering:
		parts = []string{"/" + m.filter + "▏", hint("enter", "keep"), hint("esc", "clear")}
	case m.confirm != "":
		name := m.confirm
		for _, r := range m.rows {
			if r.SessionID == m.confirm {
				name = r.Name
			}
		}
		parts = []string{red.Render("stop " + name + "?"), hint("y", "yes"), dim.Render("any other key cancels")}
	case m.tab == digestTab:
		pos := "no digest"
		if len(m.digests) > 0 {
			pos = fmt.Sprintf("digest %d/%d · %s", m.digestIdx+1, len(m.digests), m.digestAt.Format("Mon 15:04"))
		}
		parts = []string{dim.Render(pos), hint("[ ]", "older/newer"), hint("j/k", "scroll"), hint("D", "digest now"), hint("tab", "sessions"), hint("?", "help")}
	default:
		parts = []string{hint("j/k", "move"), hint("n", "next waiting"), hint("↵", "attach"),
			hint("x", "stop"), hint("y", "copy"), hint("/", "filter"), hint("tab", "digest"), hint("?", "help")}
		if m.filter != "" {
			parts = append([]string{heading.Render("/" + m.filter)}, parts...)
		}
	}
	if m.note != "" && !m.filtering && m.confirm == "" {
		parts = append([]string{amber.Render(m.note)}, parts...)
	}
	return fit(" "+strings.Join(parts, "  "), m.w)
}

// Text helpers.

// fit cuts s to w cells and pads it to exactly w, so panes line up.
func fit(s string, w int) string {
	s = ansi.Truncate(s, w, "…")
	return s + strings.Repeat(" ", max(w-ansi.StringWidth(s), 0))
}

func pad(lines []string, h int) []string {
	for len(lines) < h {
		lines = append(lines, "")
	}
	return lines[:h]
}

func wrap(s string, w int) []string {
	if w <= 0 {
		return nil
	}
	return strings.Split(lipgloss.NewStyle().Width(w).Render(s), "\n")
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// question pulls the asking part out of a reply: its last paragraph.
func question(reply string) string {
	paras := strings.Split(strings.TrimSpace(reply), "\n\n")
	return oneLine(paras[len(paras)-1])
}

// Subject is the one line that says what a session is about: why it is
// blocked, or else its title and last prompt.
func Subject(r fleet.Row) string {
	var s string
	switch {
	case r.Reason != "":
		s = r.Reason
	case r.Title != "" && r.LastPrompt != "":
		s = r.Title + ": " + r.LastPrompt
	default:
		s = r.Title + r.LastPrompt
	}
	if r.Label != "" {
		s = "[" + r.Label + "] " + s
	}
	return s
}

// Ago renders how long ago t was, in one unit.
func Ago(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

var (
	strong     = regexp.MustCompile(`\*\*(.+?)\*\*`)
	inlineCode = regexp.MustCompile("`([^`]+)`")
)

// styleMarkdown renders the little Markdown that replies lean on: headings,
// bold, inline code and bullets. Fenced code is coloured rather than parsed.
func styleMarkdown(md string) string {
	lines := strings.Split(strings.TrimSpace(md), "\n")
	fenced := false
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			fenced = !fenced
			lines[i] = dim.Render(l)
			continue
		}
		if fenced {
			lines[i] = codeFg.Render(l)
			continue
		}
		t := strings.TrimLeft(l, " ")
		indent := l[:len(l)-len(t)]
		switch {
		case strings.HasPrefix(t, "#"):
			lines[i] = heading.Render(strings.TrimLeft(t, "# "))
			continue
		case strings.HasPrefix(t, "- "), strings.HasPrefix(t, "* "):
			t = "• " + t[2:]
		}
		t = strong.ReplaceAllStringFunc(t, func(s string) string { return bold.Render(s[2 : len(s)-2]) })
		t = inlineCode.ReplaceAllStringFunc(t, func(s string) string { return codeFg.Render(s[1 : len(s)-1]) })
		lines[i] = indent + t
	}
	return strings.Join(lines, "\n")
}

// styleDigest renders a digest: the shared Markdown, with the section that
// needs attention in red.
func styleDigest(md string) string {
	lines := strings.Split(strings.TrimSpace(md), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "## Needs") {
			lines[i] = red.Render(strings.TrimPrefix(l, "## "))
		} else {
			lines[i] = styleMarkdown(l)
		}
	}
	return strings.Join(lines, "\n")
}
