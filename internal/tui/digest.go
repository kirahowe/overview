package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"overview/internal/fleet"
)

// The digests are a page: the view's own pane zoomed to fill its window,
// as the cockpit's is, with a rule along the top that says which digest
// shows and a column of its items under it, set in the middle of the
// window. An item is a block of its own: the sessions it is about, the
// issue, and for one that waits on the reader, what to do about it.

// digestWidth is the widest the page's column goes. A longer line is
// hard to read, however wide the window is.
const digestWidth = 96

// inlineSpan is a bold span or a code span in a line of Markdown.
var inlineSpan = regexp.MustCompile("\\*\\*(.+?)\\*\\*|`([^`]+)`")

// styleInline styles a line of a digest in base, its bold spans bold and
// its code spans in the code's colour. Each run is styled by itself, so
// the end of one does not end the colour of what follows it.
func styleInline(s string, base lipgloss.Style) string {
	var b strings.Builder
	for s != "" {
		loc := inlineSpan.FindStringSubmatchIndex(s)
		if loc == nil {
			b.WriteString(base.Render(s))
			break
		}
		if loc[0] > 0 {
			b.WriteString(base.Render(s[:loc[0]]))
		}
		if loc[2] >= 0 {
			b.WriteString(base.Bold(true).Render(s[loc[2]:loc[3]]))
		} else {
			b.WriteString(codeFg.Render(s[loc[4]:loc[5]]))
		}
		s = s[loc[1]:]
	}
	return b.String()
}

// hung wraps styled text to lines w wide, the first after lead and the
// others hung under where it starts. A lead that would take more than
// half the line gives way, as wrapMarkdown's markers do, so the text is
// never squeezed out by it.
func hung(lead, text string, w int) []string {
	hang := ansi.StringWidth(lead)
	if hang > w/2 {
		return wrap(text, w)
	}
	lines := wrap(text, w-hang)
	for i, l := range lines {
		if i > 0 {
			lead = strings.Repeat(" ", hang)
		}
		lines[i] = lead + l
	}
	return lines
}

// digestRule heads a section of the page: its title, in red for what
// needs the reader, how many items it has, and a line to the right edge.
// A title too long for the line stands without one.
func digestRule(s fleet.DigestSection, w int) []string {
	st := heading
	if s.Kind() == fleet.DigestNeeds {
		st = red
	}
	label := st.Render(s.Title) + dim.Render(" · "+strconv.Itoa(len(s.Items)))
	lw := ansi.StringWidth(label)
	if lw+5 > w {
		return wrap(label, w)
	}
	edge := fg(pal.Border)
	return []string{edge.Render("── ") + label + " " + edge.Render(strings.Repeat("─", w-lw-4))}
}

// digestMark is the mark an item of a section starts with, as the list
// marks a session's state.
func digestMark(k fleet.DigestKind) string {
	switch k {
	case fleet.DigestNeeds:
		return red.Render("!")
	case fleet.DigestFinished:
		return green.Render("✓")
	case fleet.DigestProgress:
		return green.Render("●")
	}
	return fg(pal.Accent).Render("•")
}

// digestItem lays one item out w cells wide. Its first line names its
// sessions and their project, with how long it has waited at the right.
// The issue follows, then what to do after an arrow, in bold, as the line
// the reader is looking for, and the reason and any notes under that.
// Text that belongs to no session is a paragraph.
func digestItem(it fleet.DigestItem, kind fleet.DigestKind, w int) []string {
	text := fg(pal.Text)
	if len(it.Names) == 0 {
		return wrap(styleInline(it.Issue, text), w)
	}
	lead := digestMark(kind) + " "
	head := heading.Render(strings.Join(it.Names, ", "))
	if it.Project != "" {
		head += "  " + dim.Render(it.Project)
	}
	since := ""
	if it.Since != "" {
		since = dim.Render("since " + it.Since)
	}
	var out []string
	switch {
	case since == "":
		out = hung(lead, head, w)
	case ansi.StringWidth(lead+head)+2+ansi.StringWidth(since) <= w:
		out = []string{spread(lead+head, since, w)}
	default:
		out = append(hung(lead, head, w), hung("  ", since, w)...)
	}
	if it.Issue != "" {
		out = append(out, hung("  ", styleInline(it.Issue, text), w)...)
	}
	in := "  "
	if it.Do != "" {
		out = append(out, hung("  "+amber.Render("→")+" ", styleInline(it.Do, bold.Foreground(pal.Text)), w)...)
		in = "    "
	}
	if it.Why != "" {
		out = append(out, hung(in, styleInline(it.Why, dim), w)...)
	}
	for _, n := range it.Notes {
		out = append(out, hung(in, styleInline(n, text), w)...)
	}
	return out
}

// digestPage lays a digest out as lines w cells wide: each section under
// its rule, and a blank line between one block and the next.
func digestPage(doc fleet.DigestDoc, w int) []string {
	if w <= 0 {
		return nil
	}
	var out []string
	block := func(lines []string) {
		if len(out) > 0 && len(lines) > 0 {
			out = append(out, strings.Repeat(" ", w))
		}
		out = append(out, lines...)
	}
	for _, s := range doc.Sections {
		if s.Title != "" {
			block(digestRule(s, w))
		}
		for _, it := range s.Items {
			block(digestItem(it, s.Kind(), w))
		}
	}
	return out
}

// pageCache is the digest's page as last laid out, with the text and the
// width it was laid out for. Laying it out is pure, and the cache only
// spares doing it again on every spinner frame.
type pageCache struct {
	text  string
	w     int
	doc   fleet.DigestDoc
	lines []string
}

// digestColumn is the width of the page's column and of the margin left
// of it, which sets the column in the middle of the pane.
func (m model) digestColumn() (w, left int) {
	cw := m.contentWidth()
	w = min(cw, digestWidth)
	return w, (cw - w) / 2
}

// digestShown is the digest that shows, parsed, and its page's lines.
func (m model) digestShown() (fleet.DigestDoc, []string) {
	w, _ := m.digestColumn()
	if m.digest == "" {
		return fleet.DigestDoc{}, wrap(dim.Render("No digest yet. Press D to write one, or start the orchestrator."), w)
	}
	if c := m.page; c != nil && c.w == w && c.text == m.digest {
		return c.doc, c.lines
	}
	doc := fleet.ParseDigest(m.digest)
	lines := digestPage(doc, w)
	if len(lines) == 0 {
		lines = wrap(dim.Render("This digest says nothing."), w)
	}
	if m.page != nil {
		*m.page = pageCache{m.digest, w, doc, lines}
	}
	return doc, lines
}

func (m model) digestLines() []string {
	_, lines := m.digestShown()
	return lines
}

// digestHead is how many lines stand above the page: the rule and a
// blank line under it. A pane of a few lines gives them up for the page,
// the blank line first.
func (m model) digestHead() int { return max(min(m.listHeight()-1, 2), 0) }

// digestRows is how many lines of the page show at once.
func (m model) digestRows() int { return max(m.listHeight()-m.digestHead(), 1) }

// digestTop is the first line of the page that shows.
func (m model) digestTop() int {
	return max(0, min(m.digestScroll, len(m.digestLines())-m.digestRows()))
}

// digestTitle is the rule along the top of the page: which digest this
// is, by when it looked at the fleet and how long ago it was written,
// and its place among the digests kept.
func (m model) digestTitle() string {
	doc, _ := m.digestShown()
	rest, right := "", ""
	if m.digest != "" {
		stamp := doc.Stamp
		if stamp == "" && !m.digestAt.IsZero() {
			stamp = m.digestAt.Format("Mon 2 Jan 15:04")
		}
		rest = fg(pal.Text).Render(stamp)
		if age := m.ago(m.digestAt); age != "" {
			rest += dim.Render(" · " + agoPhrase(age))
		}
	}
	if len(m.digests) > 0 {
		right = dim.Render(fmt.Sprintf("%d of %d", m.digestIdx+1, len(m.digests)))
	}
	return panelRule(ruleTitle{name: "digest", rest: rest}, right, m.w, m.listHasKeys())
}

// digestView is the page above the footer: its rule, a line, and the
// lines of the digest from where it is scrolled to.
func (m model) digestView() []string {
	lines := m.digestLines()
	_, left := m.digestColumn()
	top := m.digestTop()
	out := []string{m.digestTitle(), m.plain("")}[:m.digestHead()]
	for _, l := range lines[top:min(top+m.digestRows(), len(lines))] {
		out = append(out, m.plain(strings.Repeat(" ", left)+l))
	}
	return out
}

// digestPlace says where the page is scrolled to, for the footer: how
// many lines are below, or that this is the end. It is "" for a page
// that shows whole.
func (m model) digestPlace() string {
	n, rows := len(m.digestLines()), m.digestRows()
	switch below := n - rows - m.digestTop(); {
	case n <= rows:
		return ""
	case below > 0:
		return fmt.Sprintf("↓ %d more", below)
	}
	return "end"
}

// show goes to screen s, closing the cockpit on the way. The digests
// take the whole window, so the list's pane is zoomed for them, as it is
// for the cockpit, and put back on leaving. While the list is moving
// between windows, or the agents' pane is being fitted, the zoom waits
// for that to land, as the task picker's does.
func (m model) show(s screen) (model, tea.Cmd) {
	if m.screen == s && !m.cockpit.open {
		return m, nil
	}
	m.screen, m.cockpit = s, cockpit{}
	if s == digestScreen {
		m.zoomPanes = m.inWindow()
		if m.staging || m.agentsBusy {
			return m, nil
		}
	}
	return m.syncZoom()
}
