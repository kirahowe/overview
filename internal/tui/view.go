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
	"overview/internal/world"
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

// Layout. tmux draws the rule along the top of the pane, with the list's
// title in it, so the list starts on the pane's first line. The last line
// is the footer. Every line is exactly as wide as the pane: column 0
// holds the cursor's bar, the content follows, and one blank column ends
// it.

func (m model) listHeight() int { return max(m.h-1, 1) }

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
	case m.cockpit.open && !m.pick.open && !m.taskPick.open:
		body = m.cockpitBody()
	default:
		body = m.sessionsView()
	}
	for len(body) < m.listHeight() {
		body = append(body, m.plain(""))
	}
	screen := slices.Clone(body[:m.listHeight()])
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
	if m.taskPick.open {
		lines, _ := m.taskPickBody()
		return lines
	}
	secs := m.sections()
	e, _, _ := m.selected(flatten(secs))
	return m.listView(secs, e.key()).lines
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

// taskPickBody is the task picker's lines above the footer, with the row
// each shows, as taskPickLines has them. Where taskSplit finds room, the
// rows go down the left at the list's width, under a rule as the cockpit
// draws its list, and the selected issue's details fill the rest.
func (m model) taskPickBody() (lines []string, owners []int) {
	lw := m.taskSplit()
	if lw == 0 {
		return m.taskPickLines()
	}
	h := m.listHeight()
	lm := m
	lm.w, lm.h = lw, m.h-1
	rows, rowOwners := lm.taskPickLines()
	segs := append(m.listTitle(), titleSeg{"  · start a task", pal.Muted})
	left := append([]string{panelRule(m.titleText(segs, !m.blurred), "", lw, !m.blurred)}, rows...)
	owners = append([]int{-1}, rowOwners...)
	right := m.taskDetail(m.w-lw-1, h)
	for i := 0; i < h; i++ {
		l, join := lm.plain(""), "│"
		if i < len(left) {
			l = left[i]
		}
		if i == 0 {
			join = "┬"
		}
		lines = append(lines, l+fg(pal.Border).Render(join)+right[i])
	}
	return lines, owners[:min(len(owners), h)]
}

// taskDetailLines are the selected issue's details as the picker lays
// them out w cells wide, with the issue they are of, false when none is
// selected.
func (m model) taskDetailLines(w int) (issues.Issue, []string, bool) {
	l := m.taskList()
	j := m.taskSel(l)
	if j < 0 {
		return issues.Issue{}, nil, false
	}
	i := l.rows[j].issue
	r, ok := m.issueReads[taskKey(m.taskPick.root, i.ID)]
	switch {
	case !ok || r.info.loading:
		return i, []string{dim.Render("reading…")}, true
	case r.info.err != nil:
		return i, wrap(dim.Render(oneLine(r.info.err.Error())), w), true
	case strings.TrimSpace(r.info.details) == "":
		return i, []string{dim.Render("no details")}, true
	}
	return i, detailLines(r.info.details, w), true
}

// taskDetailTop is the first line of n the details show in a body h
// lines high, scrolled as far as taskPick.top asks and no further than
// leaves the last line at the bottom.
func taskDetailTop(top, n, h int) int { return max(min(top, n-h), 0) }

// taskDetail is the details card beside the picker's rows, w wide and h
// high: the issue's number, status and priority in its rule, which lines
// show when they do not all fit, and the details under it.
func (m model) taskDetail(w, h int) []string {
	c := card{title: "details"}
	i, body, ok := m.taskDetailLines(cardInner(w))
	if ok {
		c.title = "#" + strconv.Itoa(i.ID) + " · " + i.Status + " · " + i.Priority
	}
	room := max(h-1, 0)
	top := taskDetailTop(m.taskPick.top, len(body), room)
	if len(body) > room && room > 0 {
		c.right = dim.Render(fmt.Sprintf("%d-%d of %d", top+1, min(top+room, len(body)), len(body)))
	}
	return c.render(body[top:], w, h, false)
}

// taskPickLines is the task picker in the list's place: the query, then
// its note and the issues, a blank line between them, scrolled to keep
// the selected one shown, with the row each line shows, -1 for none, for
// clicks.
func (m model) taskPickLines() (lines []string, owners []int) {
	w, h := m.contentWidth(), m.listHeight()
	l := m.taskList()
	add := func(s string, owner int) {
		lines, owners = append(lines, s), append(owners, owner)
	}
	add(m.plain(heading.Render("› ")+fg(pal.Text).Render(m.taskPick.query)+dim.Render("▏")), -1)
	add(m.plain(""), -1)
	if l.note != "" {
		for _, s := range wrap(dim.Render(l.note), w) {
			add(m.plain(s), -1)
		}
		add(m.plain(""), -1)
	}
	if len(l.rows) == 0 {
		return lines, owners
	}
	// Every row, a blank line after each but the last, so the window
	// can be found in lines.
	sel := m.taskSel(l)
	var body []string
	var rows []int
	selTop, selBottom := 0, 0
	for i, r := range l.rows {
		if i > 0 {
			body, rows = append(body, m.plain("")), append(rows, -1)
		}
		if i == sel {
			selTop = len(body)
		}
		for _, s := range m.taskLines(r, i == sel) {
			body, rows = append(body, s), append(rows, i)
		}
		if i == sel {
			selBottom = len(body)
		}
	}
	room := max(h-len(lines), 1)
	start := window(0, selTop, selBottom, len(body), room)
	for j := start; j < min(start+room, len(body)); j++ {
		add(body[j], rows[j])
	}
	return lines, owners
}

// taskLines is one issue in the task picker: its number and its title,
// wrapped to show all of it, with its status, the sessions on it and its
// priority at the right of the first line.
func (m model) taskLines(r taskRow, selected bool) []string {
	i := r.issue
	status := dim
	switch i.Status {
	case issues.Review:
		status = fg(pal.Blocked)
	case issues.Blocked:
		status = fg(pal.Asking)
	}
	right := []seg{{i.Status + " ", status}}
	switch {
	case r.sessions == 1:
		right = append(right, seg{"· 1 session ", dim})
	case r.sessions > 1:
		right = append(right, seg{"· " + strconv.Itoa(r.sessions) + " sessions ", dim})
	}
	right = append(right, seg{i.Priority, dim})
	num := "  #" + strconv.Itoa(i.ID) + " "
	nw := ansi.StringWidth(num)
	// The title wraps in the column left of the right side, or under the
	// whole line when that column is too narrow to read.
	tw := m.contentWidth() - nw - segsWidth(right) - 1
	if tw < 12 {
		right, tw = nil, m.contentWidth()-nw
	}
	title := []string{i.Title}
	if tw > 0 {
		title = wrap(i.Title, tw)
	}
	out := []string{m.line([]seg{{num, dim}, {title[0], fg(pal.Text)}}, right, false, selected)}
	for _, t := range title[1:] {
		out = append(out, m.line([]seg{{strings.Repeat(" ", nw), dim}, {t, fg(pal.Text)}}, nil, false, selected))
	}
	return out
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
		// In the panels mode, or in the issues card, the row loses its
		// background, so a lit row always means the keys move it.
		if m.blurred || m.listHasKeys() {
			bg = func(st lipgloss.Style) lipgloss.Style { return st.Background(pal.CursorDimBg) }
		}
		edge := pal.CursorBg
		if !m.listHasKeys() {
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

// sectionNote is what a project's rule says on the right: how many tasks
// a tracked project has under way, or how many issues are open when none
// is, or that reading them failed; and how many entries an untracked one
// has.
func sectionNote(sec section) string {
	if sec.Failed {
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
	case tasks == 0:
		return strconv.Itoa(sec.Open) + " open"
	case tasks == 1:
		return "1 task"
	}
	return strconv.Itoa(tasks) + " tasks"
}

// projectLines renders the projects, unscrolled, with the key of the
// entry each line belongs to ("" for headings, gaps and messages). Clicks
// read the owners.
func (m model) projectLines(secs []section, sel string) (lines, owners []string) {
	add := func(owner string, ls ...string) {
		lines = append(lines, ls...)
		for range ls {
			owners = append(owners, owner)
		}
	}
	w := m.world()
	if !w.Loaded && w.RefreshErr == nil {
		add("", m.plain(dim.Render("reading sessions…")))
		return lines, owners
	}
	if w.RefreshErr != nil {
		add("", m.plain(red.Render(w.RefreshErr.Error())), m.plain(""))
	}
	if len(secs) == 0 {
		msg := "no sessions running"
		if m.filter != "" {
			msg = "nothing matches /" + m.filter
		}
		add("", m.plain(dim.Render(msg)))
		return lines, owners
	}
	first := true
	for _, sec := range secs {
		if sec.Agents {
			continue
		}
		if !first {
			add("", m.plain(""))
		}
		first = false
		style := heading
		if sec.Away {
			style = dim.Bold(true)
		}
		add("", m.rule(sec.Title, style, sectionNote(sec)))
		for j, e := range sec.Entries {
			add(e.key(), m.entryLine(e, e.key() == sel))
			if e.kind == workstreamEntry && (j+1 == len(sec.Entries) || sec.Entries[j+1].stream != e.stream) {
				add(e.key(), m.plain(dim.Render("   no sessions")))
			}
		}
	}
	return lines, owners
}

// agentEntries are the entries of the agents list, nil when it has none.
func agentEntries(secs []section) []entry {
	for _, sec := range secs {
		if sec.Agents {
			return sec.Entries
		}
	}
	return nil
}

// column is the list as its pane shows it, h lines: the projects, then,
// when there are sessions, the agents panel, a rule and its lines. Each
// part scrolls on its own, as little as its cursor needs, from projTop
// and agentsTop. owners holds the key of each line's entry, "" for
// others, and rule the line of the agents panel's rule, -1 for none.
type column struct {
	lines, owners      []string
	projTop, agentsTop int
	projH, agentsH     int
	rule               int
}

// column lays the list out in h lines, with sel under the cursor.
func (m model) column(secs []section, sel string, h int) column {
	plines, powners := m.projectLines(secs, sel)
	var alines, aowners []string
	for _, e := range agentEntries(secs) {
		alines, aowners = append(alines, m.entryLine(e, e.key() == sel)), append(aowners, e.key())
	}
	c := column{rule: -1}
	c.projH, c.agentsH = splitList(len(plines), len(alines), h)
	c.projTop = scrollTo(m.listTop, powners, sel, c.projH)
	c.agentsTop = scrollTo(m.agentsTop, aowners, sel, c.agentsH)
	show := func(lines, owners []string, top, n int) {
		end := min(top+n, len(lines))
		c.lines, c.owners = append(c.lines, lines[top:end]...), append(c.owners, owners[top:end]...)
		for i := end - top; i < n; i++ {
			c.lines, c.owners = append(c.lines, m.plain("")), append(c.owners, "")
		}
	}
	show(plines, powners, c.projTop, c.projH)
	if c.agentsH > 0 {
		c.rule = len(c.lines)
		c.lines, c.owners = append(c.lines, panelRule(m.titleText([]titleSeg{{text: agentsTitle}}, m.agentsLit()), dim.Render("priority"), m.w, m.agentsLit())), append(c.owners, "")
		show(alines, aowners, c.agentsTop, c.agentsH)
	}
	return c
}

// scrollTo is the first of owners' lines shown in h: top, moved just
// enough to show the lines of sel whole, when they are there.
func scrollTo(top int, owners []string, sel string, h int) int {
	selTop, selBottom := 0, 0
	for i, o := range owners {
		if o == sel && sel != "" {
			if selBottom == 0 {
				selTop = i
			}
			selBottom = i + 1
		}
	}
	return window(top, selTop, selBottom, len(owners), h)
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

// listView is the list as its pane shows it.
func (m model) listView(secs []section, sel string) column {
	return m.column(secs, sel, m.listHeight())
}

// stateMark is how a session's state shows: its mark and the mark's
// style, and the word, age and style on the right.
func (m model) stateMark(r fleet.Row) (mark string, markStyle lipgloss.Style, word, age string, wordStyle lipgloss.Style) {
	switch {
	case r.State == fleet.Blocked:
		return "!", red, "blocked", m.ago(r.Since), red
	case r.Asking():
		return "?", amber, "asking", m.ago(r.LastReplyAt), amber
	case r.State == fleet.Working:
		return spinner[m.frame%len(spinner)], green, "working", m.ago(r.Since), dim
	}
	word = string(r.State)
	if word == "" || r.State == fleet.Idle {
		word = "idle"
	}
	return "○", dim, word, m.ago(r.Since), dim
}

// entryLine renders one entry as one line.
func (m model) entryLine(e entry, selected bool) string {
	text := fg(pal.Text)
	indent := []seg{}
	if e.stream != nil && e.kind != workstreamEntry {
		indent = []seg{{"   ", text}}
	}
	switch e.kind {
	case workstreamEntry:
		i := e.stream.Issue
		var right []seg
		switch i.Status {
		case issues.InProgress:
		case issues.Review:
			right = append(right, seg{"review ", fg(pal.Blocked)})
		case issues.Blocked:
			right = append(right, seg{"blocked ", fg(pal.Asking)})
		default:
			right = append(right, seg{i.Status + " ", dim})
		}
		right = append(right, seg{i.Priority, dim})
		left := []seg{{"#" + strconv.Itoa(i.ID) + " ", dim}, {i.Title, text.Bold(true)}}
		return m.line(left, right, false, selected)
	case terminalEntry:
		left := append(indent, seg{"$ ", codeFg}, seg{e.term.Title, text})
		return m.line(left, []seg{{m.short(e.term.Path), dim}}, true, selected)
	case projectEntry:
		offer := "c claude · t terminal"
		if e.proj.Tracked {
			offer = "s task · " + offer
		}
		return m.line([]seg{{"+ ", dim}, {offer, dim}}, nil, false, selected)
	}
	r := e.sess
	mark, markStyle, word, age, wordStyle := m.stateMark(r.Row)
	app, away := "", r.Host != nil
	if away {
		app = r.Host.Label()
	}
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
		if b := reviewBadge(r.Review); b != "" {
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
	switch st := e.sess.Primary(); {
	case st != nil:
		ref := "#" + strconv.Itoa(st.ID)
		if p := st.Project.Name; p != e.sess.Project.Name && used+ansi.StringWidth(p)+len(ref)+2 <= m.contentWidth() {
			return p + " " + ref
		}
		return ref
	case away:
		return app
	}
	return e.sess.Project.Name
}

type detailCache struct {
	key   string
	lines []string
}

// detail returns a row's details, rebuilt only when something they show has
// changed. detailLines is a pure function of the key's parts, and the
// cache only spares the restyling that the holder would otherwise redo on
// every spinner frame.
func (m model) detail(r *world.Session, w int) []string {
	host := world.Host{}
	if r.Host != nil {
		host = *r.Host
	}
	key := fmt.Sprint(r.SessionID, w, r.State, m.ago(r.Since), m.ago(r.LastReplyAt), r.Reason, r.Title, r.TTY, r.Host != nil, host,
		r.Written, r.Transcript == m.turns.key.path, m.turns.key)
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
func (m model) detailLines(r *world.Session, w int) []string {
	var out []string
	add := func(s string) { out = append(out, wrap(s, w)...) }

	var state string
	switch {
	case r.State == fleet.Blocked:
		state = pill("blocked "+m.ago(r.Since), pal.Blocked)
	case r.Asking():
		state = pill("asking "+m.ago(r.LastReplyAt), pal.Asking)
	case r.State == fleet.Working:
		state = pill("working "+m.ago(r.Since), pal.Working)
	default:
		state = pill("idle "+m.ago(r.Since), pal.Muted)
	}
	kind := "interactive"
	h, away := world.Host{}, r.Host != nil
	if away {
		h = *r.Host
	}
	switch {
	case r.Background():
		kind = "background " + r.ID
	case away && h.Name != "":
		kind += " · inside " + h.Name
	case away:
		kind += " · no terminal"
	case r.TTY != "":
		kind += " · " + r.TTY
	}
	add(state + "  " + dim.Render(kind))
	out = append(out, "")

	meta := []string{m.short(r.Cwd)}
	meta = append(meta, "started "+m.ago(time.UnixMilli(r.StartedAt))+" ago")
	// Only an interactive session needs the hook to show as blocked.
	if !r.Hooked && !r.Background() {
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

// titleSeg is a run of a panel's title in one colour, "" for the colour
// of the title itself, which is the accent while the panel is lit.
type titleSeg struct {
	text   string
	colour lipgloss.Color
}

// listTitle is the title of the list's panel: the home's name, then how
// many sessions need you, how many work, and how many are idle, and a
// warning when no session reports through the hook, as then none can
// show as blocked.
func (m model) listTitle() []titleSeg {
	w := m.world()
	need, working, idle := w.Need, w.Working, w.Idle
	segs := []titleSeg{{text: "◆ " + m.home.Name()}}
	if need > 0 {
		segs = append(segs, titleSeg{"  ! " + strconv.Itoa(need), pal.Blocked})
	}
	if working > 0 {
		segs = append(segs, titleSeg{"  ● " + strconv.Itoa(working), pal.Working})
	}
	segs = append(segs, titleSeg{"  ○ " + strconv.Itoa(idle), pal.Muted})
	if w.NoHook {
		segs = append(segs, titleSeg{"  · " + noHook, pal.Asking})
	}
	return segs
}

// noHook is the list's warning when interactive sessions are listed and
// none reports through the hook, so none of them can show as blocked.
const noHook = "hook not installed"

// titleText renders a title as the view draws it into a rule: accent and
// bold while lit, as tmux draws the rules of its panes.
func (m model) titleText(segs []titleSeg, lit bool) string {
	base := fg(pal.Text)
	if lit {
		base = heading
	}
	var b strings.Builder
	for _, sg := range segs {
		st := base
		if sg.colour != "" {
			st = fg(sg.colour)
		}
		b.WriteString(st.Render(sg.text))
	}
	return b.String()
}

// panelRule is the rule along the top of a panel the view draws, as tmux
// draws its panes': "── title ────── right ──", w cells, in the accent
// colour when lit. title and right are styled already; right goes when
// it does not fit beside the whole title. Each column in cuts that
// falls on the dashes between them becomes a ┴, where a divider above
// ends.
func panelRule(title, right string, w int, lit bool, cuts ...int) string {
	if w < 6 {
		return strings.Repeat(" ", max(w, 0))
	}
	edge := fg(pal.Border)
	if lit {
		edge = fg(pal.Accent)
	}
	rw := ansi.StringWidth(right)
	if right != "" && ansi.StringWidth(title)+rw+9 > w {
		right, rw = "", 0
	}
	// "── " title " " dashes, then " " right " ──" when there is one.
	room := w - 4
	if right != "" {
		room -= rw + 4
	}
	title = ansi.Truncate(title, max(room-1, 1), "…")
	from := 3 + ansi.StringWidth(title) + 1
	dashes := []rune(strings.Repeat("─", max(room-ansi.StringWidth(title), 0)))
	for _, c := range cuts {
		if i := c - from; i >= 0 && i < len(dashes) {
			dashes[i] = '┴'
		}
	}
	out := edge.Render("── ") + title + " " + edge.Render(string(dashes))
	if right != "" {
		out += " " + right + " " + edge.Render("──")
	}
	return fit(out, w)
}

// panelHints are the footer's keys for where the keyboard is: in the
// cockpit's issues card, in the list, where the keys that act on the
// cursor's entry and esc to go to the panels are, or in the panels mode,
// where hjkl moves tmux's focus and enter stays where it is. In the
// cockpit o closes it first.
func (m model) panelHints() []hint {
	help := hint{"?", "help"}
	if m.inIssues() {
		enter := hint{"enter", "start"}
		if !m.pickStarts() {
			enter.label = "go to"
		}
		return []hint{enter, {"j k", "move"}, {"h", "list"}, {"esc", "close"}, help}
	}
	if m.inPanels() {
		return []hint{{"hjkl", "move"}, {"ctrl+[", "list"}, {"enter", "act"}}
	}
	e, _, ok := m.selected(m.ordered())
	hs := m.entryHints(e, ok)
	hs = hs[:len(hs)-1] // help goes last, after what is added here
	if m.cockpit.open {
		lead := []hint{{"o", "close"}}
		if _, tracked := m.world().Tracker(m.target().root); tracked && m.issuesShown() {
			lead = append(lead, hint{"l", "issues"})
		}
		return append(append(lead, hs...), help)
	}
	return append(append(hs, hint{"esc", "panels"}), help)
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
	// s goes after enter and before c in a project that tracks its tasks.
	var s []hint
	if _, tracked := m.world().Tracker(m.rootOf(e, ok)); tracked {
		s = []hint{{"s", "task"}}
	}
	enter := m.enterHint(e)
	switch e.kind {
	case workstreamEntry:
		return slices.Concat(enter, s, []hint{c, t, {"n", "next"}, help})
	case terminalEntry:
		return slices.Concat(enter, s, []hint{c, t, {"x", "close"}, help})
	case projectEntry:
		return slices.Concat(enter, s, []hint{c, t, {"x", "unpin"}, {"a", "add"}, help})
	}
	hs := slices.Concat(enter, s, []hint{c, t, {"v", "review"}})
	if e.sess.Background() {
		hs = append(hs, hint{"x", "stop"})
	}
	return append(hs, help)
}

// enterHint is enter's hint for an entry, as enterOn decides what enter
// does there, and none when it only says something.
func (m model) enterHint(e entry) []hint {
	if _, k := m.enterOn(e); k.label() != "" {
		return []hint{{"enter", k.label()}}
	}
	return nil
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

// part is a piece of the footer's text and how it is drawn.
type part struct {
	text  string
	style lipgloss.Style
}

// footerSpec is what the footer has to say, before it is fitted to a
// width.
type footerSpec struct {
	status []part // what runs in the background, most important first
	mode   []part // where the keyboard is: a search, a picker, the panels mode
	cut    int    // the mode part cut from its left when the line is short, -1 for none
	note   string // what the view has to say, in place of the hints
	hints  []hint
}

// minHints is how many hints the footer keeps however long the rest is.
const minHints = 2

// layFooter fits a footer into w cells. The mode always shows, cut from
// the left of its cut part when it must be, so a search keeps its input
// and a long path its last directories. The first minHints hints keep
// their room. Status goes first when the line is short, the least
// important first, and then the note is cut at its end.
func layFooter(f footerSpec, w int) string {
	hintsW := func(hs []hint) int {
		n := 0
		for i, h := range hs {
			if i > 0 {
				n += 3 // " • "
			}
			n += ansi.StringWidth(h.key) + 1 + ansi.StringWidth(h.label)
		}
		return n
	}
	modeText := func() string {
		var b strings.Builder
		for _, p := range f.mode {
			b.WriteString(p.text)
		}
		return b.String()
	}
	plain := func(status []part, note string) []string {
		var out []string
		for _, p := range status {
			out = append(out, p.text)
		}
		if mt := modeText(); mt != "" {
			out = append(out, mt)
		}
		if note != "" {
			out = append(out, note)
		}
		return out
	}
	width := func(items []string) int { return ansi.StringWidth(strings.Join(items, "  ")) }
	room := w
	if keep := f.hints[:min(minHints, len(f.hints))]; len(keep) > 0 {
		room -= hintsW(keep) + 2
	}
	status, note := f.status, f.note
	for len(status) > 0 && width(plain(status, note)) > room {
		status = status[:len(status)-1]
	}
	if over := width(plain(status, note)) - room; over > 0 && note != "" {
		keep := max(ansi.StringWidth(note)-over, 2)
		note = ansi.Truncate(note, keep, "…")
	}
	if over := width(plain(status, note)) - room; over > 0 && f.cut >= 0 && f.cut < len(f.mode) && f.mode[f.cut].text != "" {
		c := f.mode[f.cut].text
		keep := max(ansi.StringWidth(c)-over-1, 0)
		f.mode[f.cut].text = "…" + ansi.TruncateLeft(c, ansi.StringWidth(c)-keep, "")
	}
	var lead []string
	for _, p := range status {
		lead = append(lead, p.style.Render(p.text))
	}
	if modeText() != "" {
		var b strings.Builder
		for _, p := range f.mode {
			b.WriteString(p.style.Render(p.text))
		}
		lead = append(lead, b.String())
	}
	if note != "" {
		lead = append(lead, fg(pal.Text).Render(note))
	}
	return hintLine(lead, f.hints, w)
}

// footer is the last line: what runs in the background, where the
// keyboard is, and then the note when there is one, and otherwise the
// keys for what is selected.
func (m model) footer() string {
	f := footerSpec{cut: -1, note: m.note}
	if w := m.world(); w.Loaded && w.RefreshErr == nil {
		switch h := m.world().Orchestrator; {
		case !h.Running:
			f.status = append(f.status, part{"orchestrator off", red})
		case h.Stale:
			f.status = append(f.status, part{"orchestrator silent " + m.ago(h.LastCheck), red})
		}
	}
	if m.digesting {
		f.status = append(f.status, part{"writing a digest…", dim})
	}
	count := func() part { return part{fmt.Sprintf(" · %d", len(m.ordered())), dim} }
	switch {
	case m.pick.open:
		f.mode = []part{{"add project", heading}}
		f.hints = []hint{{"enter", "pin"}, {"tab", "complete"}, {"esc", "cancel"}}
	case m.taskPick.open:
		f.mode, f.cut = []part{{"start a task in ", heading}, {m.name(m.taskPick.root), heading}}, 1
		enter := hint{"enter", "start"}
		if l := m.taskList(); m.taskSel(l) >= 0 && taskGoes(l.rows[m.taskSel(l)]) {
			enter.label = "go to"
		}
		f.hints = []hint{enter, {"↑↓", "move"}}
		if m.taskSplit() > 0 {
			f.hints = append(f.hints, hint{"pgup/pgdn", "details"})
		}
		f.hints = append(f.hints, hint{"esc", "cancel"})
	case m.filtering:
		f.mode, f.cut = []part{{"/", heading}, {m.filter, heading}, {"▏", heading}, count()}, 1
		if e, _, ok := m.selected(m.ordered()); ok {
			f.hints = m.enterHint(e)
		}
		f.hints = append(f.hints, hint{"↑↓", "move"}, hint{"esc", "clear"})
	case m.screen == digestScreen && m.note == "":
		info := "no digest yet"
		if len(m.digests) > 0 {
			info = fmt.Sprintf("%s · %d of %d", m.digestAt.Format("Mon 15:04"), m.digestIdx+1, len(m.digests))
		}
		f.mode = []part{{info, dim}}
		f.hints = []hint{{"[ ]", "older/newer"}, {"D", "write"}, {"d", "sessions"}, {"?", "help"}}
	case m.screen != sessionsScreen:
		// A note on the digests: the list's search and panels are not here.
	default:
		if m.filter != "" {
			f.mode, f.cut = []part{{"/", heading}, {m.filter, heading}, count()}, 1
		}
		if m.inPanels() {
			f.mode = append(f.mode, part{"panels", heading})
			if len(f.mode) > 1 {
				f.mode[len(f.mode)-1].text = "  panels"
			}
		}
		if m.note == "" {
			f.hints = m.panelHints()
		}
	}
	if m.pick.open || m.taskPick.open || m.filtering {
		f.note = "" // a picker or a search has the line, and says what it needs
	}
	return m.plain(layFooter(f, m.contentWidth()))
}

// Overlays.

// helpMode is where a help entry's keys are handled, which the test that
// keeps the help and the key handler in step uses to try them there.
type helpMode int

const (
	listMode    helpMode = iota // keyActions in the list
	cockpitMode                 // keyActions with the cockpit open and the issues card lit
	digestMode                  // keyActions on the digest screen
	tmuxMode                    // tmux's panels key table, not the view
	mouseMode                   // the mouse
)

type helpEntry struct {
	keys, what string
	mode       helpMode
}

type helpSection struct {
	title   string
	entries []helpEntry
}

// helpSections are the help's two columns. In the narrow list pane the
// second goes under the first.
func helpSections() (left, right []helpSection) {
	e := func(keys, what string) helpEntry { return helpEntry{keys, what, listMode} }
	in := func(mode helpMode, keys, what string) helpEntry { return helpEntry{keys, what, mode} }
	left = []helpSection{
		{"panels", []helpEntry{
			e("esc", "from the list, move focus"),
			in(tmuxMode, "h j k l", "focus the pane that way"),
			in(tmuxMode, "enter", "act in the focused pane"),
			in(tmuxMode, "ctrl+[", "back to the list"),
		}},
		{"in the list", []helpEntry{
			e("j k", "move"),
			e("n N", "next / previous waiting"),
			e("g G", "first / last"),
			e("/", "search, esc clears"),
			e("1 2", "sessions / digests"),
		}},
		{"act", []helpEntry{
			e("enter", "type in its session or terminal"),
			e("l tab", "the same as enter"),
			e("x", "stop, close or unpin"),
			e("y", "copy attach command"),
			e("c", "new session in project"),
			e("t", "new terminal in project"),
			e("v", "review its changes"),
			e("s", "start a task in project"),
			e("a", "add a project"),
		}},
		{"on a task", []helpEntry{
			e("enter", "first session, else cockpit"),
			e("c", "new session on the task"),
			e("t", "new terminal for it"),
		}},
		{"cockpit", []helpEntry{
			e("o", "open the cockpit"),
			in(cockpitMode, "o esc", "close the cockpit"),
			in(cockpitMode, "l h", "into / out of the issues"),
			in(cockpitMode, "tab", "list or issues card"),
			in(cockpitMode, "enter", "on an issue, start or go to it"),
		}},
	}
	right = []helpSection{
		{"digest", []helpEntry{
			e("d", "show / hide digests"),
			in(digestMode, "[ ]", "older / newer"),
			e("D", "write one now"),
		}},
		{"mouse", []helpEntry{
			in(mouseMode, "click", "select, twice to act"),
			in(mouseMode, "wheel", "move / scroll"),
		}},
		{"general", []helpEntry{
			e("r", "refresh"),
			e("?", "this help"),
			e("q ctrl+c", "detach"),
			e("Q", "quit the view"),
		}},
	}
	return left, right
}

// helpCol is the width of the help's first column when two sit side by side.
const helpCol = 40

// helpWide says whether the help has two columns: the pane has room.
func (m model) helpWide() bool { return m.w-4 >= 2*helpCol+6 }

// helpLen is how many lines helpBody has, counted without drawing them.
func (m model) helpLen() int {
	ls, rs := helpSections()
	n := func(ss []helpSection) (c int) {
		for _, s := range ss {
			c += len(s.entries) + 2
		}
		return c
	}
	if m.helpWide() {
		return max(n(ls), n(rs)) + 1
	}
	return n(ls) + n(rs) + 1
}

// helpBody is every line of the help, before it is cut to the pane.
func (m model) helpBody() []string {
	render := func(ss []helpSection) []string {
		var out []string
		for _, s := range ss {
			out = append(out, heading.Render(s.title))
			for _, e := range s.entries {
				out = append(out, "  "+fg(pal.Accent).Render(fmt.Sprintf("%-9s", e.keys))+" "+fg(pal.Text).Render(e.what))
			}
			out = append(out, "")
		}
		return out
	}
	ls, rs := helpSections()
	left, right := render(ls), render(rs)
	legend := red.Render("!") + dim.Render(" blocked  ") + amber.Render("?") + dim.Render(" asking  ") +
		green.Render(spinner[m.frame%len(spinner)]) + dim.Render(" working  ") + dim.Render("○ idle  ") + codeFg.Render("$") + dim.Render(" terminal  #6 task")

	// Side by side when the pane is wide enough, one column under the other
	// in the narrow list pane.
	var body []string
	if m.helpWide() {
		for i := 0; i < max(len(left), len(right)); i++ {
			l, r := "", ""
			if i < len(left) {
				l = left[i]
			}
			if i < len(right) {
				r = right[i]
			}
			body = append(body, fit(l, helpCol)+r)
		}
	} else {
		body = append(left, right...)
	}
	return append(body, legend)
}

// helpRows is how many lines of the help the box shows: all of them when
// the pane has room, else what fits between the box's borders.
func (m model) helpRows() int {
	n := m.helpLen()
	if n+2 <= m.listHeight() {
		return n
	}
	return max(m.listHeight()-2, 1)
}

// helpMax is how far the help can scroll, 0 when it all fits.
func (m model) helpMax() int { return max(m.helpLen()-m.helpRows(), 0) }

func (m model) helpBox() []string {
	body := m.helpBody()
	rows := m.helpRows()
	top := max(0, min(m.helpTop, len(body)-rows))
	more := top+rows < len(body)
	info := "esc closes"
	if more {
		info = "↓ more · esc closes"
	}
	w := min(m.w-4, 2*helpCol+6)
	out := box("keys", info, body[top:top+rows], w, rows+2, true)
	if top > 0 && w >= 12 {
		// The marker takes the title's place when the box is too narrow
		// for both.
		marker, edge := " ↑ more ", fg(pal.Accent)
		if w >= 24 {
			out[0] = ansi.Truncate(out[0], w-len(marker)-2, "") + dim.Render(marker) + edge.Render("─┐")
		} else {
			out[0] = edge.Render("┌─") + dim.Render(marker) + edge.Render(strings.Repeat("─", w-len(marker)-3)+"┐")
		}
	}
	return out
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
	if s := m.world().Sessions[m.confirm]; s != nil {
		name = s.Name
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

// wrap breaks s, which may be styled, into lines w cells wide, at spaces
// where it can, each padded to w. It is the one word wrap in the view. A
// line the wrapper leaves too wide, as ansi.Wrap does when it keeps a
// hyphen with the letter before it, is broken again at w, so no line is
// ever wider than w.
func wrap(s string, w int) []string {
	if w <= 0 {
		return nil
	}
	var out []string
	for _, l := range strings.Split(lipgloss.NewStyle().Width(w).Render(s), "\n") {
		if ansi.StringWidth(l) <= w {
			out = append(out, l)
			continue
		}
		out = append(out, strings.Split(ansi.Hardwrap(l, w, true), "\n")...)
	}
	return out
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

// Ago renders how long ago t was, in one unit, by the clock now.
func Ago(t time.Time) string { return agoAt(time.Now(), t) }

// ago is Ago for the view, measured from when Update read the clock, so
// drawing the screen reads no clock of its own.
func (m model) ago(t time.Time) string { return agoAt(m.now, t) }

// agoAt renders how long before now t was, in one unit.
func agoAt(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
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
