// Package tui is the live terminal view: every running session on top, the
// latest digest underneath.
package tui

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"overview/internal/fleet"
)

const refreshEvery = 2 * time.Second

var (
	bold    = lipgloss.NewStyle().Bold(true)
	dim     = lipgloss.NewStyle().Faint(true)
	red     = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	green   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	heading = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
)

type (
	tickMsg time.Time
	rowsMsg struct {
		rows []fleet.Row
		err  error
	}
	digestMsg struct {
		d   fleet.Digest
		err error
	}
)

type model struct {
	home  fleet.Home
	every time.Duration
	llm   string

	rows    []fleet.Row
	rowsErr error

	digest    string
	digestAt  time.Time
	attemptAt time.Time
	running   bool
	note      string
	scroll    int

	w, h int
}

// Run opens the view and blocks until the user quits.
func Run(home fleet.Home, every time.Duration, llm string) error {
	m := model{home: home, every: every, llm: llm}
	m.digest, m.digestAt, _ = home.LatestDigest()
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func (m model) Init() tea.Cmd { return tea.Batch(m.refresh, tick()) }

func tick() tea.Cmd {
	return tea.Tick(refreshEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) refresh() tea.Msg {
	rows, err := m.home.Rows()
	return rowsMsg{rows, err}
}

func (m model) runDigest() tea.Msg {
	d, err := m.home.RunDigest(m.llm)
	return digestMsg{d, err}
}

// digestDue reports whether the configured interval has passed since the
// last digest, or since the last attempt when that one failed.
func (m model) digestDue(now time.Time) bool {
	if m.every <= 0 || m.running {
		return false
	}
	last := m.home.LastDigestAt()
	if m.attemptAt.After(last) {
		last = m.attemptAt
	}
	return now.Sub(last) >= m.every
}

func (m model) startDigest() (model, tea.Cmd) {
	m.running, m.attemptAt, m.note = true, time.Now(), ""
	return m, m.runDigest
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "d":
			if !m.running {
				return m.startDigest()
			}
		case "j", "down":
			m.scroll++
		case "k", "up":
			m.scroll = max(0, m.scroll-1)
		case "g":
			m.scroll = 0
		}
	case tickMsg:
		cmds := []tea.Cmd{m.refresh, tick()}
		if m.digestDue(time.Time(msg)) {
			var c tea.Cmd
			m, c = m.startDigest()
			cmds = append(cmds, c)
		}
		return m, tea.Batch(cmds...)
	case rowsMsg:
		m.rows, m.rowsErr = msg.rows, msg.err
	case digestMsg:
		m.running = false
		switch {
		case msg.err != nil:
			m.note = "digest failed: " + msg.err.Error()
		case msg.d.Quiet:
			m.note = "no activity since " + msg.d.Since.Local().Format("15:04")
		default:
			m.digest, m.digestAt, m.scroll = msg.d.Text, time.Now(), 0
		}
	}
	return m, nil
}

func (m model) View() string {
	if m.w == 0 {
		return ""
	}
	fit := lipgloss.NewStyle().MaxWidth(m.w)
	var top []string
	top = append(top, fit.Render(m.header()), "")
	if m.rowsErr != nil {
		top = append(top, red.Render(m.rowsErr.Error()))
	}
	dir := ""
	for _, r := range m.rows {
		if r.Cwd != dir {
			dir = r.Cwd
			top = append(top, fit.Render(heading.Render(fleet.ShortDir(dir))))
		}
		top = append(top, fit.Render(rowLine(r)))
	}

	// The digest gets whatever the session list leaves, and never less than
	// a few lines.
	const minDigest = 6
	if room := m.h - minDigest - 2; len(top) > room && room > 3 {
		hidden := len(top) - room + 1
		top = append(top[:room-1], dim.Render(fmt.Sprintf("  … %d more lines", hidden)))
	}
	out := append(top, "", fit.Render(m.digestRule()))

	body := lipgloss.NewStyle().Width(m.w).Render(styleDigest(m.digest))
	lines := strings.Split(body, "\n")
	room := max(m.h-len(out), 1)
	start := min(m.scroll, max(len(lines)-room, 0))
	lines = lines[start:min(start+room, len(lines))]
	return strings.Join(append(out, lines...), "\n")
}

func (m model) header() string {
	count := map[fleet.State]int{}
	for _, r := range m.rows {
		count[r.State]++
	}
	parts := []string{bold.Render("overview " + m.home.Name()), fmt.Sprintf("%d sessions", len(m.rows))}
	if n := count[fleet.Blocked]; n > 0 {
		parts = append(parts, red.Render(fmt.Sprintf("%d blocked", n)))
	}
	if n := count[fleet.Working]; n > 0 {
		parts = append(parts, green.Render(fmt.Sprintf("%d working", n)))
	}
	parts = append(parts, fmt.Sprintf("%d idle", count[fleet.Idle]))
	return strings.Join(parts, dim.Render(" · "))
}

func (m model) digestRule() string {
	var parts []string
	switch {
	case m.running:
		parts = append(parts, "writing digest…")
	case m.digestAt.IsZero():
		parts = append(parts, "no digest yet")
	default:
		parts = append(parts, "digest "+m.digestAt.Format("15:04"))
	}
	if m.every > 0 && !m.running {
		last := m.home.LastDigestAt()
		if m.attemptAt.After(last) {
			last = m.attemptAt
		}
		if !last.IsZero() {
			parts = append(parts, "next "+last.Add(m.every).Local().Format("15:04"))
		}
	}
	if m.note != "" {
		parts = append(parts, m.note)
	}
	parts = append(parts, "d digest now", "j/k scroll", "q quit")
	return dim.Render("── " + strings.Join(parts, " · ") + " ──")
}

func rowLine(r fleet.Row) string {
	mark, state := dim.Render("○"), dim.Render(fmt.Sprintf("%-8s", r.State))
	switch r.State {
	case fleet.Blocked:
		mark, state = red.Render("!"), red.Render(fmt.Sprintf("%-8s", r.State))
	case fleet.Working:
		mark, state = green.Render("●"), green.Render(fmt.Sprintf("%-8s", r.State))
	}
	return fmt.Sprintf("  %s %-22s %s %4s  %s", mark, r.Name, state, Ago(r.Since), Subject(r))
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

var strong = regexp.MustCompile(`\*\*(.+?)\*\*`)

// styleDigest renders the little Markdown a digest uses: headings and bold.
func styleDigest(md string) string {
	lines := strings.Split(strings.TrimSpace(md), "\n")
	for i, l := range lines {
		l = strong.ReplaceAllStringFunc(l, func(s string) string { return bold.Render(s[2 : len(s)-2]) })
		lines[i] = l
		switch {
		case strings.HasPrefix(l, "## Needs"):
			lines[i] = red.Render(strings.TrimPrefix(l, "## "))
		case strings.HasPrefix(l, "#"):
			lines[i] = heading.Render(strings.TrimLeft(l, "# "))
		}
	}
	return strings.Join(lines, "\n")
}
