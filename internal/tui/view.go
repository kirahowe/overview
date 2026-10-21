package tui

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"overview/internal/config"
	"overview/internal/fleet"
)

// pal is the palette in use, and the styles below are built from it.
// useTheme sets both, once, before the view starts.
var (
	pal                                             Theme
	bold                                            = lipgloss.NewStyle().Bold(true)
	dim, italic, red, amber, green, heading, codeFg lipgloss.Style
)

func fg(c lipgloss.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }

func useTheme(t Theme) {
	pal = t
	dim = fg(t.Muted)
	italic = lipgloss.NewStyle().Italic(true).Foreground(t.Text)
	red = fg(t.Blocked).Bold(true)
	amber = fg(t.Asking).Bold(true)
	green = fg(t.Working)
	heading = fg(t.Accent).Bold(true)
	codeFg = fg(t.Code)
}

func init() {
	t, err := builtinTheme(config.DefaultTheme)
	if err != nil {
		panic(err)
	}
	useTheme(t)
}

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func pill(text string, bg lipgloss.Color) string {
	return lipgloss.NewStyle().Background(bg).Foreground(pal.PillFg).Bold(true).Padding(0, 1).Render(text)
}

// Layout. The list is a box with a one-cell border and a one-cell margin
// inside it, and the status bar takes the last line.

func (m model) boxHeight() int { return max(m.h-1, 3) }

func (m model) innerHeight() int { return m.boxHeight() - 2 }

func (m model) View() string {
	if m.w == 0 {
		return ""
	}
	var screen []string
	if m.screen == digestScreen {
		screen = m.digestView()
	} else {
		screen = m.sessionsView()
	}
	screen = pad(screen, m.boxHeight())
	switch {
	case m.help:
		screen = centre(screen, m.helpBox(), m.w)
	case m.confirm != "":
		screen = centre(screen, m.confirmBox(), m.w)
	}
	return strings.Join(append(screen, m.statusBar()), "\n")
}

func (m model) sessionsView() []string {
	if m.pick.open {
		return m.pickerView()
	}
	secs := m.sections()
	es := flatten(secs)
	e, _, _ := m.selected(es)
	info := ""
	if m.filter != "" || m.filtering {
		info = fmt.Sprintf("/%s · %d", m.filter, len(es))
	}
	lines, _ := m.listView(secs, e.key())
	return box("sessions", info, lines, m.w, m.boxHeight(), !m.blurred)
}

// pickerView is the add-project picker in the list's place: the query,
// then the matching directories, scrolled to keep the selected one shown.
func (m model) pickerView() []string {
	w, h := m.w-4, m.innerHeight()
	matches := m.pickMatches()
	lines := []string{heading.Render("› ") + fg(pal.Text).Render(m.pick.query) + dim.Render("▏"), ""}
	switch {
	case !m.pick.read:
		lines = append(lines, dim.Render("reading…"))
	case len(matches) == 0 && isPathQuery(m.pick.query):
		lines = append(lines, wrap(dim.Render("Nothing here matches. enter adds "+m.short(expandPath(m.pick.query, m.user))+" if it is a directory."), w)...)
	case len(matches) == 0:
		lines = append(lines, wrap(dim.Render("Nothing matches. A path starting with / or ~ looks anywhere."), w)...)
	default:
		if m.pick.sel < 0 {
			dir, _ := splitPathQuery(m.pick.query, m.user)
			lines = append(lines, wrap(dim.Render("enter adds "+m.short(dir)+", down picks a folder in it."), w)...)
		}
		room := max(h-len(lines), 1)
		sel := max(m.pick.sel, 0)
		start := window(0, sel, sel+1, len(matches), room)
		for i := start; i < min(start+room, len(matches)); i++ {
			text := "  " + m.short(matches[i])
			if i == m.pick.sel {
				lines = append(lines, m.cursorBar().Render(fit(text, w)))
			} else {
				lines = append(lines, fg(pal.Text).Render(text))
			}
		}
	}
	info := ""
	if m.pick.read {
		info = fmt.Sprintf("%d", len(matches))
	}
	return box("add project", info, lines, m.w, m.boxHeight(), !m.blurred)
}

// box draws a bordered pane with the title set into its top edge and an
// optional note in its bottom edge. The focused pane gets the accent colour.
func box(title, info string, body []string, w, h int, focused bool) []string {
	edge, name := fg(pal.Border), lipgloss.NewStyle().Foreground(pal.Text)
	if focused {
		edge, name = fg(pal.Accent), heading
	}
	inner := w - 2
	title = ansi.Truncate(title, max(inner-4, 1), "…")
	top := edge.Render("┌─ ") + name.Render(title) + " "
	top += edge.Render(strings.Repeat("─", max(inner-ansi.StringWidth(title)-3, 0)) + "┐")

	bottom := edge.Render("└" + strings.Repeat("─", inner) + "┘")
	if info != "" {
		info = ansi.Truncate(info, max(inner-4, 1), "…")
		bottom = edge.Render("└"+strings.Repeat("─", max(inner-ansi.StringWidth(info)-3, 0))) +
			" " + dim.Render(info) + " " + edge.Render("─┘")
	}

	out := []string{top}
	for i := 0; i < h-2; i++ {
		line := ""
		if i < len(body) {
			line = body[i]
		}
		out = append(out, edge.Render("│")+" "+fit(line, inner-2)+" "+edge.Render("│"))
	}
	return append(out, bottom)
}

// listLines renders the whole list, unscrolled, with the key of the entry
// each line belongs to ("" for headings, gaps and messages). Clicks read
// the owners.
func (m model) listLines(secs []section, sel string, w int) (lines, owners []string) {
	add := func(owner string, ls ...string) {
		lines = append(lines, ls...)
		for range ls {
			owners = append(owners, owner)
		}
	}
	if !m.loaded && m.rowsErr == nil {
		add("", dim.Render("reading sessions…"))
		return lines, owners
	}
	if m.rowsErr != nil {
		add("", red.Render(m.rowsErr.Error()), "")
	}
	if len(secs) == 0 {
		msg := "no sessions running"
		if m.filter != "" {
			msg = "nothing matches /" + m.filter
		}
		add("", dim.Render(msg))
		return lines, owners
	}
	for i, sec := range secs {
		if i > 0 {
			add("", "")
		}
		style := heading
		switch {
		case sec.NeedsYou:
			style = red
		case sec.Away:
			style = dim.Bold(true)
		}
		n := 0
		for _, e := range sec.Entries {
			if e.kind != projectEntry {
				n++
			}
		}
		add("", style.Render(sec.Title)+dim.Render(fmt.Sprintf(" %d", n)))
		for _, e := range sec.Entries {
			add(e.key(), m.entryLines(e, w, e.key() == sel, sec.NeedsYou)...)
		}
	}
	return lines, owners
}

// listStart is the first list line shown: the last one, moved just enough
// to show the cursor's row whole.
func (m model) listStart(owners []string, sel string, h int) int {
	selTop, selBottom := 0, 0
	for i, o := range owners {
		if o == sel && sel != "" {
			if selBottom == 0 {
				selTop = i
			}
			selBottom = i + 1
		}
	}
	return window(m.listTop, selTop, selBottom, len(owners), h)
}

// window keeps a window of h lines over n as close to top as it can while
// it shows [selTop, selBottom), and the top of that span when it is taller
// than the window. It never leaves empty lines below the end.
func window(top, selTop, selBottom, n, h int) int {
	top = min(top, max(n-h, 0))
	if selBottom > top+h {
		top = selBottom - h
	}
	return max(min(top, selTop), 0)
}

// listView is the shown part of the list and its owners.
func (m model) listView(secs []section, sel string) (lines, owners []string) {
	lines, owners = m.listLines(secs, sel, m.w-4)
	start := m.listStart(owners, sel, m.boxHeight()-2)
	return lines[start:], owners[start:]
}

// entryLines renders one entry as two lines. A terminal shows what runs in
// it and where, and an empty pinned project the keys that fill it.
func (m model) entryLines(e entry, w int, selected, pinned bool) []string {
	switch e.kind {
	case terminalEntry:
		return m.plainLines("$", e.term.Title, "terminal", m.short(e.term.Path), w, selected)
	case projectEntry:
		return m.plainLines("+", "no sessions", "", "c claude · t terminal · x unpin", w, selected)
	}
	return m.rowLines(e.row, w, selected, pinned)
}

// cursorBar is the style of the selected entry: solid while the list has
// the keyboard, dimmed otherwise.
func (m model) cursorBar() lipgloss.Style {
	bar := lipgloss.NewStyle().Background(pal.CursorDimBg).Foreground(pal.CursorDimFg).Bold(true)
	if !m.blurred {
		bar = bar.Background(pal.CursorBg).Foreground(pal.CursorFg)
	}
	return bar
}

// plainLines renders an entry that is not a session in rowLines' shape:
// mark, name and a right-hand note, then a dim second line.
func (m model) plainLines(mark, name, right, sub string, w int, selected bool) []string {
	name = ansi.Truncate(name, max(w-ansi.StringWidth(right)-4, 4), "…")
	gap := strings.Repeat(" ", max(w-ansi.StringWidth(name)-ansi.StringWidth(right)-2, 1))
	if selected {
		bar := m.cursorBar()
		return []string{bar.Render(fit(mark+" "+name+gap+right, w)), bar.Bold(false).Render(fit("  "+sub, w))}
	}
	return []string{dim.Render(mark) + " " + fg(pal.Text).Render(name) + gap + dim.Render(right), "  " + dim.Render(sub)}
}

// rowLines renders one session as two lines: name, place and age, then what
// it is about. The selected row is a solid bar, which needs plain text
// underneath, since every inner colour would reset its background.
func (m model) rowLines(r fleet.Row, w int, selected, pinned bool) []string {
	mark, when, sub := "○", Ago(r.Since), Subject(r)
	style := dim
	switch {
	case r.State == fleet.Blocked:
		mark, when, style = "!", "waiting "+Ago(r.Since), red
		if sub == "" {
			sub = "blocked"
		}
	case r.Asking():
		mark, when, style = "?", "waiting "+Ago(r.LastReplyAt), amber
		sub = question(r.LastReply)
	case r.State == fleet.Working:
		mark, style = spinner[m.frame%len(spinner)], green
	}
	// Under a project heading the project is already known, so say the state
	// instead. In the pinned section the project is what is missing. A
	// session inside another app is placed by that app, wherever it is, and
	// is dimmed, since it is not one of the user's own.
	place, placed := m.name(r.Cwd), ""
	app, away := m.hostOf(r)
	switch {
	case away:
		place = app
	case !pinned:
		place = string(r.State)
		if place == "" {
			place = "idle"
		}
	}
	placed = place + " · "
	if b := reviewBadge(m.reviews[r.SessionID]); b != "" {
		placed = b + " · " + placed
	}
	right := placed + when
	nameStyle := fg(pal.Text)
	if away && !r.NeedsYou() {
		nameStyle = dim
	}
	name := ansi.Truncate(r.Name, max(w-ansi.StringWidth(right)-4, 4), "…")
	gap := strings.Repeat(" ", max(w-ansi.StringWidth(name)-ansi.StringWidth(right)-2, 1))

	if selected {
		bar := m.cursorBar()
		return []string{
			bar.Render(fit(mark+" "+name+gap+right, w)),
			bar.Bold(false).Render(fit("  "+sub, w)),
		}
	}
	line := style.Render(mark) + " " + nameStyle.Render(name) + gap + dim.Render(placed) + style.Render(when)
	subStyle := dim
	if r.NeedsYou() {
		subStyle = style.Bold(false)
	}
	return []string{line, "  " + subStyle.Render(sub)}
}

type detailCache struct {
	key   string
	lines []string
}

// detail returns a row's details, rebuilt only when something they show has
// changed. detailLines is a pure function of the key's parts, and the
// cache only spares the restyling that the holder would otherwise redo on
// every spinner frame.
func (m model) detail(r fleet.Row, w int) []string {
	key := fmt.Sprint(r.SessionID, w, r.State, Ago(r.Since), Ago(r.LastReplyAt), r.Reason, r.Title, m.ttys[r.PID], m.hosts[r.PID],
		m.written[r.Transcript], r.Transcript == m.turns.key.path, m.turns.key)
	if m.cache != nil && m.cache.key == key {
		return m.cache.lines
	}
	lines := m.detailLines(r, w)
	if m.cache != nil {
		m.cache.key, m.cache.lines = key, lines
	}
	return lines
}

// detailLines renders everything known about one session: where it is, what
// it is waiting on, and its last few turns.
func (m model) detailLines(r fleet.Row, w int) []string {
	var out []string
	add := func(s string) { out = append(out, wrap(s, w)...) }

	var state string
	switch {
	case r.State == fleet.Blocked:
		state = pill("blocked "+Ago(r.Since), pal.Blocked)
	case r.Asking():
		state = pill("asking "+Ago(r.LastReplyAt), pal.Asking)
	case r.State == fleet.Working:
		state = pill("working "+Ago(r.Since), pal.Working)
	default:
		state = pill("idle "+Ago(r.Since), pal.Muted)
	}
	kind := "interactive"
	h, away := m.hosts[r.PID]
	switch {
	case r.Background():
		kind = "background " + r.ID
	case away && h.Name != "":
		kind += " · inside " + h.Name
	case away:
		kind += " · no terminal"
	case m.ttys[r.PID] != "":
		kind += " · " + m.ttys[r.PID]
	}
	add(state + "  " + dim.Render(kind))
	out = append(out, "")

	meta := []string{m.short(r.Cwd)}
	meta = append(meta, "started "+Ago(time.UnixMilli(r.StartedAt))+" ago")
	if !r.Hooked {
		meta = append(meta, "no hook")
	}
	if r.Title != "" {
		add(italic.Render(r.Title))
	}
	add(dim.Render(strings.Join(meta, " · ")))
	if away && !r.Background() {
		out = append(out, "")
		if h.Name != "" {
			add(dim.Render(h.Name + " started this session and runs it without a terminal. enter brings " + h.Name + " forward. To end it, close it there."))
		} else {
			add(dim.Render("Something other than a terminal started this session, so there is nothing to show here."))
		}
	}

	callout := func(style lipgloss.Style, label, text string) {
		out = append(out, "")
		bar := style.Render("▌ ")
		out = append(out, bar+style.Render(label))
		for _, l := range wrap(text, w-2) {
			out = append(out, bar+l)
		}
	}
	switch {
	case r.State == fleet.Blocked:
		reason := r.Reason
		if reason == "" {
			reason = "Claude Code marks it as waiting on you. Attach to see what for."
		}
		callout(red, "blocked", reason)
	case r.Asking():
		callout(amber, "asking you", question(r.LastReply))
	}

	turns := m.turnsOf(r.Transcript)
	if len(turns) == 0 {
		if r.LastPrompt != "" {
			out = append(out, "", rule("last prompt", w))
			add(heading.Render("› ") + r.LastPrompt)
		}
		return out
	}
	out = append(out, "", rule(fmt.Sprintf("last %d turns", len(turns)), w))
	for i, t := range turns {
		out = append(out, "")
		prompt := wrap(heading.Render("› ")+bold.Foreground(pal.Text).Render(oneLine(t.Prompt)), w)
		if len(prompt) > 3 {
			prompt = append(prompt[:3], dim.Render("  …"))
		}
		out = append(out, prompt...)
		if t.Reply == "" {
			out = append(out, dim.Render("  no reply yet"))
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

// rule is a labelled divider: "── label ──────".
func rule(label string, w int) string {
	return fg(pal.Border).Render("── ") + heading.Render(label) + " " +
		fg(pal.Border).Render(strings.Repeat("─", max(w-ansi.StringWidth(label)-4, 0)))
}

func (m model) digestLines() []string {
	if m.digest == "" {
		return []string{dim.Render("No digest yet. Press D to write one, or start the orchestrator.")}
	}
	return wrap(styleDigest(m.digest), min(m.w-4, 100))
}

func (m model) digestView() []string {
	lines := m.digestLines()
	h := m.innerHeight()
	start := max(0, min(m.digestScroll, len(lines)-h))
	info := "none yet"
	if len(m.digests) > 0 {
		info = fmt.Sprintf("%s · %d of %d", m.digestAt.Format("Mon 15:04"), m.digestIdx+1, len(m.digests))
	}
	return box("digest", info, lines[start:min(start+h, len(lines))], m.w, m.boxHeight(), true)
}

// Overlays.

func (m model) helpBox() []string {
	type entry struct{ keys, what string }
	section := func(title string, es ...entry) []string {
		out := []string{heading.Render(title)}
		for _, e := range es {
			out = append(out, "  "+fg(pal.Accent).Render(fmt.Sprintf("%-9s", e.keys))+" "+fg(pal.Text).Render(e.what))
		}
		return append(out, "")
	}
	left := append(section("sessions",
		entry{"j k", "move"},
		entry{"n N", "next / previous waiting"},
		entry{"g G", "first / last"},
		entry{"/", "search, esc clears"},
	), section("act",
		entry{"enter", "type into the session"},
		entry{"ctrl+[", "back to the list"},
		entry{"x", "stop, close or unpin"},
		entry{"y", "copy attach command"},
		entry{"c", "new session in project"},
		entry{"t", "new terminal in project"},
		entry{"v", "review its changes"},
		entry{"a", "add a project"},
	)...)
	right := section("digest",
		entry{"d", "show / hide digests"},
		entry{"[ ]", "older / newer"},
		entry{"D", "write one now"},
	)
	right = append(right, section("mouse",
		entry{"click", "select, twice to act"},
		entry{"wheel", "move / scroll"},
	)...)
	right = append(right, section("general",
		entry{"r", "refresh"},
		entry{"?", "this help"},
		entry{"q", "detach, Q quits"},
	)...)
	legend := red.Render("!") + dim.Render(" blocked  ") + amber.Render("?") + dim.Render(" asking  ") +
		green.Render(spinner[m.frame%len(spinner)]) + dim.Render(" working  ") + dim.Render("○ idle  $ terminal")

	const col = 40
	// Side by side when the pane is wide enough, one column under the other
	// in the narrow list pane.
	var body []string
	if m.w-4 >= 2*col+6 {
		for i := 0; i < max(len(left), len(right)); i++ {
			l, r := "", ""
			if i < len(left) {
				l = left[i]
			}
			if i < len(right) {
				r = right[i]
			}
			body = append(body, fit(l, col)+r)
		}
	} else {
		body = append(left, right...)
	}
	body = append(body, legend)
	w := min(m.w-4, 2*col+6)
	return box("keys", "esc closes", body, w, min(len(body)+2, m.boxHeight()), true)
}

func (m model) confirmBox() []string {
	w := min(m.w-4, 56)
	for _, e := range m.ordered() {
		if e.kind == terminalEntry && e.key() == m.confirm {
			body := []string{bold.Foreground(pal.Text).Render("Close terminal " + e.term.Title + "?")}
			body = append(body, wrap(dim.Render("What runs in it is killed."), w-4)...)
			body = append(body, "", red.Render("y")+dim.Render(" close   ")+dim.Render("any other key cancels"))
			return box("close terminal", "", body, w, len(body)+2, true)
		}
	}
	name := m.confirm
	for _, r := range m.rows {
		if r.SessionID == m.confirm {
			name = r.Name
		}
	}
	body := []string{bold.Foreground(pal.Text).Render("Stop " + name + "?")}
	body = append(body, wrap(dim.Render("Its conversation is kept, and claude attach brings it back."), w-4)...)
	body = append(body, "", red.Render("y")+dim.Render(" stop   ")+dim.Render("any other key cancels"))
	return box("stop session", "", body, w, len(body)+2, true)
}

// centre draws an overlay in the middle of the screen.
func centre(screen, over []string, w int) []string {
	ow := 0
	for _, l := range over {
		ow = max(ow, ansi.StringWidth(l))
	}
	x, y := max((w-ow)/2, 0), max((len(screen)-len(over))/2, 0)
	out := append([]string(nil), screen...)
	for i, l := range over {
		if y+i >= len(out) {
			break
		}
		bg := out[y+i]
		out[y+i] = ansi.Truncate(bg, x, "") + "\x1b[0m" + l + "\x1b[0m" + ansi.TruncateLeft(bg, x+ansi.StringWidth(l), "")
	}
	return out
}

func (m model) statusBar() string {
	base := lipgloss.NewStyle().Background(pal.StatusBg).Foreground(pal.StatusFg)
	strong := base.Bold(true)

	var left []string
	switch {
	case m.pick.open:
		left = []string{strong.Render("add project"), base.Render("enter pins · tab completes · esc cancels")}
	case m.filtering:
		left = []string{strong.Render("/" + m.filter + "▏"), base.Render("enter opens · esc clears")}
	case m.note != "":
		left = []string{strong.Render(m.note)}
	default:
		need, working, idle := m.counts()
		if need > 0 {
			left = append(left, strong.Render(fmt.Sprintf("%d need you", need)))
		}
		if working > 0 {
			left = append(left, base.Render(fmt.Sprintf("%d working", working)))
		}
		left = append(left, base.Render(fmt.Sprintf("%d idle", idle)))
		if m.loaded && m.rowsErr == nil {
			switch h := m.home.HealthOf(m.rows); {
			case !h.Running:
				left = append(left, strong.Render("orchestrator off"))
			case h.Stale:
				left = append(left, strong.Render("orchestrator silent "+Ago(h.LastCheck)))
			case m.digesting:
				left = append(left, base.Render("writing a digest…"))
			}
		}
	}
	sep := base.Render(" │ ")
	l := strong.Render(" ◆ "+m.home.Name()) + sep + strings.Join(left, sep)

	keys := "↵ type · ^[ back · / search · n next · c new · t term · a add · ? help "
	if m.screen == digestScreen {
		keys = "[ ] older/newer · D write · d sessions · ? help "
	}
	r := base.Render(keys)
	gap := m.w - ansi.StringWidth(l) - ansi.StringWidth(r)
	if gap < 1 {
		return fit(l, m.w)
	}
	return l + base.Render(strings.Repeat(" ", gap)) + r
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
	text := fg(pal.Text)
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
			t = fg(pal.Accent).Render("•") + " " + t[2:]
		}
		t = strong.ReplaceAllStringFunc(t, func(s string) string { return bold.Foreground(pal.Text).Render(s[2 : len(s)-2]) })
		t = inlineCode.ReplaceAllStringFunc(t, func(s string) string { return codeFg.Render(s[1 : len(s)-1]) })
		lines[i] = indent + text.Render(t)
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
