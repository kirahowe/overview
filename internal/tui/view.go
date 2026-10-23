package tui

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"overview/internal/config"
	"overview/internal/fleet"
	"overview/internal/issues"
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

// Layout. The first line is the header and the last the footer, and the
// list takes the lines between. Every line is exactly as wide as the pane:
// column 0 holds the cursor's bar, the content follows, and one blank
// column ends it.

func (m model) listHeight() int { return max(m.h-2, 1) }

// contentWidth is the width of a line's content, between the bar's column
// and the blank one at the end.
func (m model) contentWidth() int { return max(m.w-2, 0) }

func (m model) View() string {
	if m.w <= 0 || m.h <= 0 {
		return ""
	}
	var body []string
	switch {
	case m.screen == digestScreen:
		body = m.digestView()
	case m.cockpit.open && !m.pick.open:
		body = m.cockpitBody()
	default:
		body = m.sessionsView()
	}
	for len(body) < m.listHeight() {
		body = append(body, m.plain(""))
	}
	screen := append([]string{m.header()}, body[:m.listHeight()]...)
	switch {
	case m.help:
		screen = centre(screen, m.helpBox(), m.w)
	case m.confirm != "":
		screen = centre(screen, m.confirmBox(), m.w)
	}
	screen = append(screen, m.footer())
	return strings.Join(screen[:min(len(screen), m.h)], "\n")
}

func (m model) sessionsView() []string {
	if m.pick.open {
		return m.pickerView()
	}
	secs := m.sections()
	e, _, _ := m.selected(flatten(secs))
	lines, _ := m.listView(secs, e.key())
	return lines
}

// pickerView is the add-project picker in the list's place: the query,
// then the matching directories, scrolled to keep the selected one shown.
func (m model) pickerView() []string {
	w, h := m.contentWidth(), m.listHeight()
	matches := m.pickMatches()
	lines := []string{m.plain(heading.Render("› ") + fg(pal.Text).Render(m.pick.query) + dim.Render("▏")), m.plain("")}
	say := func(s string) {
		for _, l := range wrap(dim.Render(s), w) {
			lines = append(lines, m.plain(l))
		}
	}
	switch {
	case !m.pick.read:
		say("reading…")
	case len(matches) == 0 && isPathQuery(m.pick.query):
		say("Nothing here matches. enter adds " + m.short(expandPath(m.pick.query, m.user)) + " if it is a directory.")
	case len(matches) == 0:
		say("Nothing matches. A path starting with / or ~ looks anywhere.")
	default:
		if m.pick.sel < 0 {
			dir, _ := splitPathQuery(m.pick.query, m.user)
			say("enter adds " + m.short(dir) + ", down picks a folder in it.")
		}
		room := max(h-len(lines), 1)
		sel := max(m.pick.sel, 0)
		start := window(0, sel, sel+1, len(matches), room)
		for i := start; i < min(start+room, len(matches)); i++ {
			lines = append(lines, m.line([]seg{{"  " + m.short(matches[i]), fg(pal.Text)}}, nil, false, i == m.pick.sel))
		}
	}
	return lines
}

// box draws a bordered pane with the title set into its top edge and an
// optional note in its bottom edge, for the overlays. The focused pane
// gets the accent colour. A pane too narrow for the top edge with a
// character of title, 6 columns, gets blank lines.
func box(title, info string, body []string, w, h int, focused bool) []string {
	if w < 6 {
		return pad(nil, max(h, 0))
	}
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

// seg is a run of text in one style. A line is built of segments so the
// cursor's row can put its background under each without losing their
// colours.
type seg struct {
	text  string
	style lipgloss.Style
}

func segsWidth(ss []seg) int {
	n := 0
	for _, s := range ss {
		n += ansi.StringWidth(s.text)
	}
	return n
}

// cutSegs cuts segments to w cells, with an ellipsis where it cut.
func cutSegs(ss []seg, w int) []seg {
	if segsWidth(ss) <= w {
		return ss
	}
	var out []seg
	left := w
	for _, s := range ss {
		sw := ansi.StringWidth(s.text)
		if sw < left {
			out = append(out, s)
			left -= sw
			continue
		}
		if left > 0 {
			out = append(out, seg{ansi.Truncate(s.text, left, "…"), s.style})
		}
		break
	}
	return out
}

// line renders one line of the list: left at the left edge and right at
// the right one, cut to fit. A right side that does not fit beside at
// least a few cells of the left goes, unless shrink lets it lose its
// start instead, as a directory can. The selected line gets the cursor's
// bar and background.
func (m model) line(left, right []seg, shrink, selected bool) string {
	cw := m.contentWidth()
	lw, rw := segsWidth(left), segsWidth(right)
	if rw > 0 {
		room := cw - min(lw, 6) - 1
		switch {
		case rw <= room:
		case shrink && room >= 4:
			text := ""
			for _, s := range right {
				text += s.text
			}
			right = []seg{{"…" + ansi.TruncateLeft(text, rw-room+1, ""), right[len(right)-1].style}}
		default:
			right = nil
		}
		rw = segsWidth(right)
	}
	if rw > 0 {
		left = cutSegs(left, cw-rw-1)
	} else {
		left = cutSegs(left, cw)
	}
	gap := max(cw-segsWidth(left)-rw, 0)
	bg := func(st lipgloss.Style) lipgloss.Style { return st }
	bar := " "
	if selected {
		bg = func(st lipgloss.Style) lipgloss.Style { return st.Background(pal.CursorDimBg) }
		edge := pal.CursorBg
		if m.blurred {
			edge = pal.Border
		}
		bar = fg(edge).Render("▌")
	}
	var b strings.Builder
	b.WriteString(bar)
	for _, s := range left {
		b.WriteString(bg(s.style).Render(s.text))
	}
	b.WriteString(bg(lipgloss.NewStyle()).Render(strings.Repeat(" ", gap)))
	for _, s := range right {
		b.WriteString(bg(s.style).Render(s.text))
	}
	if m.w >= 2 {
		b.WriteString(bg(lipgloss.NewStyle()).Render(" "))
	}
	return fit(b.String(), m.w)
}

// plain is a line with no cursor on it: already styled text, cut to fit.
func (m model) plain(s string) string {
	if m.w < 2 {
		return fit("", m.w)
	}
	return " " + fit(s, m.contentWidth()) + " "
}

// rule heads a section: its title, a line to the right edge, and a note,
// which goes when there is no room for it beside a few cells of title.
func (m model) rule(title string, style lipgloss.Style, note string) string {
	cw := m.contentWidth()
	nw := ansi.StringWidth(note)
	if note != "" && cw < nw+8 {
		note, nw = "", 0
	}
	// title, a space, at least one ─, and a space and the note.
	room := cw - 2
	if note != "" {
		room -= nw + 1
	}
	// A long title keeps its end, where a project's name is.
	if tw := ansi.StringWidth(title); tw > max(room, 1) {
		title = "…" + ansi.TruncateLeft(title, tw-max(room, 1)+1, "")
	}
	n := max(room-ansi.StringWidth(title)+1, 0)
	s := style.Render(title) + " " + fg(pal.Border).Render(strings.Repeat("─", n))
	if note != "" {
		s += " " + dim.Render(note)
	}
	return m.plain(s)
}

// sectionNote is what a section's rule says on the right: how many tasks
// a tracked project has, or that reading them failed, how many entries an
// untracked one has, and for the agents list its order.
func sectionNote(sec section) string {
	switch {
	case sec.Agents:
		return "priority"
	case sec.Failed:
		return "issues failed"
	}
	n, tasks := 0, 0
	for _, e := range sec.Entries {
		switch e.kind {
		case workstreamEntry:
			tasks++
		case projectEntry:
		default:
			n++
		}
	}
	switch {
	case !sec.Tracked && n == 0:
		return ""
	case !sec.Tracked:
		return strconv.Itoa(n)
	case tasks == 1:
		return "1 task"
	}
	return strconv.Itoa(tasks) + " tasks"
}

// listLines renders the whole list, unscrolled, for a list area h lines
// high, with the key of the entry each line belongs to ("" for headings,
// gaps and messages). Clicks read the owners. When everything fits, blank
// lines push the agents list to the bottom of the area.
func (m model) listLines(secs []section, sel string, h int) (lines, owners []string) {
	add := func(owner string, ls ...string) {
		lines = append(lines, ls...)
		for range ls {
			owners = append(owners, owner)
		}
	}
	if !m.loaded && m.rowsErr == nil {
		add("", m.plain(dim.Render("reading sessions…")))
		return lines, owners
	}
	if m.rowsErr != nil {
		add("", m.plain(red.Render(m.rowsErr.Error())), m.plain(""))
	}
	if len(secs) == 0 {
		msg := "no sessions running"
		if m.filter != "" {
			msg = "nothing matches /" + m.filter
		}
		add("", m.plain(dim.Render(msg)))
		return lines, owners
	}
	var agents []string
	var agentOwners []string
	for i, sec := range secs {
		var ls, ows []string
		put := func(owner, l string) { ls, ows = append(ls, l), append(ows, owner) }
		style := heading
		if sec.Away || sec.Agents {
			style = dim.Bold(true)
		}
		put("", m.rule(sec.Title, style, sectionNote(sec)))
		for j, e := range sec.Entries {
			put(e.key(), m.entryLine(e, e.key() == sel))
			if e.kind == workstreamEntry && (j+1 == len(sec.Entries) || sec.Entries[j+1].task != e.task) {
				put(e.key(), m.plain(dim.Render("   no sessions")))
			}
		}
		if sec.Agents {
			agents, agentOwners = ls, ows
			continue
		}
		if i > 0 {
			add("", m.plain(""))
		}
		lines, owners = append(lines, ls...), append(owners, ows...)
	}
	if agents != nil {
		gap := 1
		if n := len(lines) + 1 + len(agents); n < h {
			gap += h - n
		}
		for range gap {
			add("", m.plain(""))
		}
		lines, owners = append(lines, agents...), append(owners, agentOwners...)
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
	h := m.listHeight()
	lines, owners = m.listLines(secs, sel, h)
	start := m.listStart(owners, sel, h)
	end := min(start+h, len(lines))
	return lines[start:end], owners[start:end]
}

// stateMark is how a session's state shows: its mark and the mark's
// style, and the word, age and style on the right.
func (m model) stateMark(r fleet.Row) (mark string, markStyle lipgloss.Style, word, age string, wordStyle lipgloss.Style) {
	switch {
	case r.State == fleet.Blocked:
		return "!", red, "blocked", Ago(r.Since), red
	case r.Asking():
		return "?", amber, "asking", Ago(r.LastReplyAt), amber
	case r.State == fleet.Working:
		return spinner[m.frame%len(spinner)], green, "working", Ago(r.Since), dim
	}
	word = string(r.State)
	if word == "" || r.State == fleet.Idle {
		word = "idle"
	}
	return "○", dim, word, Ago(r.Since), dim
}

// entryLine renders one entry as one line.
func (m model) entryLine(e entry, selected bool) string {
	text := fg(pal.Text)
	indent := []seg{}
	if e.task != "" && e.kind != workstreamEntry {
		indent = []seg{{"   ", text}}
	}
	switch e.kind {
	case workstreamEntry:
		var right []seg
		switch e.issue.Status {
		case issues.InProgress:
		case issues.Review:
			right = append(right, seg{"review ", fg(pal.Blocked)})
		case issues.Blocked:
			right = append(right, seg{"blocked ", fg(pal.Asking)})
		default:
			right = append(right, seg{e.issue.Status + " ", dim})
		}
		right = append(right, seg{e.issue.Priority, dim})
		left := []seg{{"#" + strconv.Itoa(e.issue.ID) + " ", dim}, {e.issue.Title, text.Bold(true)}}
		return m.line(left, right, false, selected)
	case terminalEntry:
		left := append(indent, seg{"$ ", codeFg}, seg{e.term.Title, text})
		return m.line(left, []seg{{m.short(e.term.Path), dim}}, true, selected)
	case projectEntry:
		return m.line([]seg{{"+ ", dim}, {"c claude · t terminal", dim}}, nil, false, selected)
	}
	r := e.row
	mark, markStyle, word, age, wordStyle := m.stateMark(r)
	app, away := m.hostOf(r)
	name := text
	if away && !r.NeedsYou() {
		name = dim
	}
	left := append(indent, seg{mark + " ", markStyle}, seg{r.Name, name})
	var right []seg
	switch {
	case e.alias == agentsPrefix:
		right = []seg{{m.agentsNote(e, app, away, segsWidth(left)), dim}}
	case away:
		right = []seg{{app, dim}}
	default:
		if b := reviewBadge(m.reviews[r.SessionID]); b != "" {
			right = append(right, seg{b + " ", fg(pal.Accent)})
		}
		state := word
		if age != "" {
			state += " " + age
		}
		right = append(right, seg{state, wordStyle})
	}
	return m.line(left, right, false, selected)
}

// agentsNote is what the agents list says of a session: the workstream it
// works on, with its project's name when that is not the session's own
// and there is room, or else its project, or the app it runs in.
func (m model) agentsNote(e entry, app string, away bool, used int) string {
	switch {
	case e.issue.ID != 0:
		ref := "#" + strconv.Itoa(e.issue.ID)
		if p := m.name(e.project); p != m.name(e.row.Cwd) && used+ansi.StringWidth(p)+len(ref)+2 <= m.contentWidth() {
			return p + " " + ref
		}
		return ref
	case away:
		return app
	}
	return m.name(e.row.Cwd)
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
		return wrap(dim.Render("No digest yet. Press D to write one, or start the orchestrator."), m.contentWidth())
	}
	return wrap(styleDigest(m.digest), min(m.contentWidth(), 100))
}

func (m model) digestView() []string {
	lines := m.digestLines()
	h := m.listHeight()
	start := max(0, min(m.digestScroll, len(lines)-h))
	var out []string
	for _, l := range lines[start:min(start+h, len(lines))] {
		out = append(out, m.plain(l))
	}
	return out
}

// header is the first line: the home's name, bright while the list has
// the keyboard, and the counts of what needs you, what works and what is
// idle, as many as fit.
func (m model) header() string {
	name := heading
	if m.blurred {
		name = dim
	}
	left := []seg{{"◆ " + m.home.Name(), name}}
	need, working, idle := m.counts()
	if m.cockpit.open && m.screen == sessionsScreen {
		return m.cockpitHeader(left, need, working, idle)
	}
	var right []seg
	if need > 0 {
		right = append(right, seg{fmt.Sprintf("! %d  ", need), red})
	}
	if working > 0 {
		right = append(right, seg{fmt.Sprintf("● %d  ", working), green})
	}
	right = append(right, seg{fmt.Sprintf("○ %d", idle), dim})
	for len(right) > 1 && segsWidth(left)+segsWidth(right)+1 > m.contentWidth() {
		right = right[1:]
	}
	return m.line(left, right, false, false)
}

// cockpitHeader is the header across the cockpit: the counts in full
// after the home's name, and on the right how old the digest is and how
// the orchestrator is, as much as fits.
func (m model) cockpitHeader(left []seg, need, working, idle int) string {
	left = append(left, seg{"   ", dim})
	if need > 0 {
		left = append(left, seg{fmt.Sprintf("! %d need you  ", need), red})
	}
	if working > 0 {
		left = append(left, seg{fmt.Sprintf("● %d working  ", working), green})
	}
	left = append(left, seg{fmt.Sprintf("○ %d idle", idle), dim})
	if n := len(m.terminals()); n > 0 {
		left = append(left, seg{fmt.Sprintf("  $ %d terminals", n), codeFg})
	}
	var right []seg
	if !m.digestAt.IsZero() {
		right = append(right, seg{"digest " + Ago(m.digestAt) + " ago · ", dim})
	}
	switch h := m.home.HealthOf(m.rows); {
	case !m.loaded:
	case !h.Running:
		right = append(right, seg{"orchestrator off", red})
	case h.Stale:
		right = append(right, seg{"orchestrator silent " + Ago(h.LastCheck), red})
	default:
		right = append(right, seg{"orchestrator ok", dim})
	}
	if segsWidth(left)+segsWidth(right)+1 > m.contentWidth() {
		right = nil
	}
	return m.line(left, right, false, false)
}

// cockpitHints are the footer's keys in the cockpit, for where the
// keyboard is.
func (m model) cockpitHints() []hint {
	if m.cockpit.issues {
		enter := hint{"enter", "start"}
		if !m.pickStarts() {
			enter.label = "go to"
		}
		return []hint{enter, {"j k", "move"}, {"tab", "list"}, {"esc", "close"}, {"?", "help"}}
	}
	return []hint{{"o", "close"}, {"tab", "issues"}, {"enter", "type"}, {"c", "claude"}, {"t", "term"}, {"v", "review"}, {"?", "help"}}
}

// hint is a key and what it does, as the footer shows it.
type hint struct{ key, label string }

// entryHints are the keys that act on the selected entry, the most used
// first, since the footer drops them from the end.
func (m model) entryHints(e entry, ok bool) []hint {
	c, t, help := hint{"c", "claude"}, hint{"t", "term"}, hint{"?", "help"}
	if !ok {
		return []hint{c, t, {"a", "add"}, help}
	}
	switch e.kind {
	case workstreamEntry:
		return []hint{{"enter", "type"}, c, t, {"n", "next"}, help}
	case terminalEntry:
		return []hint{{"enter", "type"}, c, t, {"x", "close"}, help}
	case projectEntry:
		return []hint{c, t, {"x", "unpin"}, {"a", "add"}, help}
	}
	enter := hint{"enter", "type"}
	if _, away := m.hostOf(e.row); away {
		enter = hint{"enter", "open app"}
	} else if e.row.Background() && m.paneOf(e.row) == "" {
		enter = hint{"enter", "attach"}
	}
	hs := []hint{enter, c, t, {"v", "review"}}
	if e.row.Background() {
		hs = append(hs, hint{"x", "stop"})
	}
	return append(hs, help)
}

// hintLine puts lead, styled already, before as many whole hints as fit
// in w cells, as "key label • key label".
func hintLine(lead []string, hints []hint, w int) string {
	render := func(n int) string {
		parts := slices.Clone(lead)
		var hs []string
		for _, h := range hints[:n] {
			hs = append(hs, fg(pal.Accent).Render(h.key)+" "+dim.Render(h.label))
		}
		if len(hs) > 0 {
			parts = append(parts, strings.Join(hs, dim.Render(" • ")))
		}
		return strings.Join(parts, "  ")
	}
	n := len(hints)
	for n > 0 && ansi.StringWidth(render(n)) > w {
		n--
	}
	return render(n)
}

// footer is the last line: what the orchestrator is up to when it is in
// trouble or writing, then the note when there is one, and otherwise the
// keys for what is selected.
func (m model) footer() string {
	var lead []string
	if m.loaded && m.rowsErr == nil {
		switch h := m.home.HealthOf(m.rows); {
		case !h.Running:
			lead = append(lead, red.Render("orchestrator off"))
		case h.Stale:
			lead = append(lead, red.Render("orchestrator silent "+Ago(h.LastCheck)))
		}
	}
	if m.digesting && m.note != "writing a digest…" {
		lead = append(lead, dim.Render("writing a digest…"))
	}
	var hints []hint
	switch {
	case m.pick.open:
		lead = append(lead, heading.Render("add project"))
		hints = []hint{{"enter", "pin"}, {"tab", "complete"}, {"esc", "cancel"}}
	case m.filtering:
		lead = append(lead, heading.Render("/"+m.filter+"▏")+dim.Render(fmt.Sprintf(" · %d", len(m.ordered()))))
		hints = []hint{{"enter", "open"}, {"↑↓", "move"}, {"esc", "clear"}}
	case m.note != "":
		lead = append(lead, fg(pal.Text).Render(m.note))
	case m.screen == digestScreen:
		info := "no digest yet"
		if len(m.digests) > 0 {
			info = fmt.Sprintf("%s · %d of %d", m.digestAt.Format("Mon 15:04"), m.digestIdx+1, len(m.digests))
		}
		lead = append(lead, dim.Render(info))
		hints = []hint{{"[ ]", "older/newer"}, {"D", "write"}, {"d", "sessions"}, {"?", "help"}}
	default:
		if m.filter != "" {
			lead = append(lead, heading.Render("/"+m.filter)+dim.Render(fmt.Sprintf(" · %d", len(m.ordered()))))
		}
		e, _, ok := m.selected(m.ordered())
		hints = m.entryHints(e, ok)
		if m.cockpit.open {
			hints = m.cockpitHints()
		}
	}
	return m.plain(hintLine(lead, hints, m.contentWidth()))
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
	left = append(left, section("on a task",
		entry{"enter", "its first session"},
		entry{"c", "new session on the task"},
		entry{"t", "new terminal for it"},
	)...)
	left = append(left, section("cockpit",
		entry{"o", "open / close the cockpit"},
		entry{"tab", "list or issues card"},
		entry{"enter", "on an issue, start or go to it"},
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
		green.Render(spinner[m.frame%len(spinner)]) + dim.Render(" working  ") + dim.Render("○ idle  ") + codeFg.Render("$") + dim.Render(" terminal  #6 task")

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
	return box("keys", "esc closes", body, w, min(len(body)+2, m.listHeight()+1), true)
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
