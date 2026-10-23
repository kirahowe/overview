package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"overview/internal/diff"
	"overview/internal/issues"
	"overview/internal/review"
)

// The cockpit's cards are built from plain data by pure functions: each
// card's body is a function of the width inside its border, and
// layoutCards stacks them into the lines the cockpit shows.

// pairMin is the narrowest the cards area shows two cards side by side,
// and pairShare the left one's share of it, in percent.
const (
	pairMin   = 70
	pairShare = 58
)

// card is one box of the cockpit: a title and a note set into its top
// edge, and a body drawn at the width inside the border. right is styled
// already. accent draws the border in the accent colour, for the header
// and for a card with the keyboard.
type card struct {
	title, right string
	body         func(w int) []string
	accent       bool
}

// render draws c as h lines w wide, its body padded with blank lines or
// cut to fit. A card narrower than a border and a cell of title is blank.
func (c card) render(body []string, w, h int) []string {
	if w < 6 || h < 2 {
		return blanks(w, h)
	}
	edge, name := fg(pal.Border), fg(pal.Text).Bold(true)
	if c.accent {
		edge, name = fg(pal.Accent), heading
	}
	inner := w - 2
	right := c.right
	// The top edge is "┌─ title ", dashes, then " right ─┐" when right fits.
	if right != "" && ansi.StringWidth(right)+ansi.StringWidth(c.title)+8 > inner {
		right = ""
	}
	room := inner - 3
	if right != "" {
		room -= ansi.StringWidth(right) + 3
	}
	title := ansi.Truncate(c.title, max(room-1, 1), "…")
	top := edge.Render("┌─ ") + name.Render(title) + " "
	if right != "" {
		top += edge.Render(strings.Repeat("─", max(room-ansi.StringWidth(title), 0))) + " " + right + " " + edge.Render("─┐")
	} else {
		top += edge.Render(strings.Repeat("─", max(room-ansi.StringWidth(title), 0)) + "┐")
	}
	out := []string{fit(top, w)}
	for i := 0; i < h-2; i++ {
		l := ""
		if i < len(body) {
			l = body[i]
		}
		out = append(out, edge.Render("│")+" "+fit(l, max(inner-2, 0))+" "+edge.Render("│"))
	}
	return append(out, edge.Render("└"+strings.Repeat("─", inner)+"┘"))
}

// blanks is h blank lines w wide.
func blanks(w, h int) []string {
	out := make([]string, max(h, 0))
	for i := range out {
		out[i] = strings.Repeat(" ", max(w, 0))
	}
	return out
}

// cardInner is the width a card w wide draws its body at.
func cardInner(w int) int { return max(w-4, 0) }

// layoutCards stacks rows of cards from the top, h lines of exactly w
// cells, and says how many cards it showed. A row of two goes side by
// side when w has room, and else becomes two rows. A row that does not fit
// the height left is dropped, with every row after it, so what is dropped
// is always the last cards, never a squeezed one.
func layoutCards(rows [][]card, w, h int) (lines []string, shown int) {
	var flat [][]card
	for _, r := range rows {
		if len(r) == 2 && w < pairMin {
			flat = append(flat, r[:1], r[1:])
		} else if len(r) > 0 {
			flat = append(flat, r)
		}
	}
	for _, r := range flat {
		widths := []int{w}
		if len(r) == 2 {
			left := w * pairShare / 100
			widths = []int{left, w - left - 1}
		}
		bodies := make([][]string, len(r))
		tall := 0
		for i, c := range r {
			bodies[i] = c.body(cardInner(widths[i]))
			tall = max(tall, len(bodies[i])+2)
		}
		if len(lines)+tall > h {
			break
		}
		drawn := make([][]string, len(r))
		for i, c := range r {
			drawn[i] = c.render(bodies[i], widths[i], tall)
		}
		for j := 0; j < tall; j++ {
			l := drawn[0][j]
			if len(r) == 2 {
				l += " " + drawn[1][j]
			}
			lines = append(lines, l)
		}
		shown += len(r)
	}
	return append(lines, blanks(w, h-len(lines))...), shown
}

// spread puts left at the left of w cells and right at the right, cutting
// left to make room, and leaving right out when it does not fit beside a
// few cells of left.
func spread(left, right string, w int) string {
	lw, rw := ansi.StringWidth(left), ansi.StringWidth(right)
	if right == "" || rw+min(lw, 6)+1 > w {
		return fit(left, w)
	}
	left = ansi.Truncate(left, w-rw-1, "…")
	return left + strings.Repeat(" ", w-ansi.StringWidth(left)-rw) + right
}

// chip is a key and what it does, as a card shows it: the key on a faint
// background, then its label.
func chip(key, label string) string {
	return lipgloss.NewStyle().Foreground(pal.Accent).Background(pal.CursorDimBg).Bold(true).Render(" "+key+" ") + " " + dim.Render(label)
}

func chips(hs ...hint) string {
	parts := make([]string, len(hs))
	for i, h := range hs {
		parts[i] = chip(h.key, h.label)
	}
	return strings.Join(parts, "  ")
}

// member is a session or terminal of a workstream or project, as its
// header lists it: a session's state word and age, styled, or a
// terminal's title.
type member struct {
	name, word, age string
	style           lipgloss.Style
	term            bool
}

func (mb member) String() string {
	if mb.term {
		return codeFg.Render("$") + " " + fg(pal.Text).Render(mb.name)
	}
	state := mb.word
	if mb.age != "" {
		state += " " + mb.age
	}
	return mb.style.Render("●") + " " + fg(pal.Text).Render(mb.name) + " " + mb.style.Render(state)
}

// membersLine is the members on one line, or that there are none.
func membersLine(ms []member) string {
	if len(ms) == 0 {
		return dim.Render("no sessions")
	}
	parts := make([]string, len(ms))
	for i, mb := range ms {
		parts[i] = mb.String()
	}
	return strings.Join(parts, "   ")
}

// streamHeader is the first card of a workstream's cockpit.
func streamHeader(ref, status, priority, title, started string, ms []member) card {
	right := []string{}
	for _, s := range []string{status, priority} {
		if s != "" {
			right = append(right, s)
		}
	}
	return card{title: ref, right: dim.Render(strings.Join(right, " · ")), accent: true, body: func(w int) []string {
		when := ""
		if started != "" {
			when = dim.Render("started " + started + " ago")
		}
		return []string{
			spread(bold.Foreground(pal.Text).Render(title), when, w),
			membersLine(ms),
			chips(hint{"enter", "attach"}, hint{"v", "review"}, hint{"c", "claude"}, hint{"t", "term"}),
		}
	}}
}

// projectHeader is the first card of a project's cockpit. open is how many
// open issues it has, -1 when it does not track them.
func projectHeader(name, root, kind string, open int, ms []member) card {
	right := "no issues"
	if open >= 0 {
		right = strconv.Itoa(open) + " open"
	}
	return card{title: name, right: dim.Render(right), accent: true, body: func(w int) []string {
		return []string{
			spread(fg(pal.Text).Render(root), dim.Render(kind), w),
			membersLine(ms),
			chips(hint{"c", "claude"}, hint{"t", "term"}),
		}
	}}
}

// issueSummaryLines is how many lines of an issue's summary its card shows.
const issueSummaryLines = 4

// issueSummary is the first paragraph of an issue's details, after its
// "# Title" heading and any "## Context" heading, on one line.
func issueSummary(details string) string {
	var para []string
	for _, line := range strings.Split(details, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == "" && len(para) > 0:
			return strings.Join(para, " ")
		case t == "":
		case strings.HasPrefix(t, "#") && len(para) > 0:
			return strings.Join(para, " ")
		case strings.HasPrefix(t, "#"):
			h := strings.ToLower(strings.TrimSpace(strings.TrimLeft(t, "#")))
			if !strings.HasPrefix(t, "# ") && h != "context" {
				return "" // another section comes before any paragraph
			}
		default:
			para = append(para, t)
		}
	}
	return strings.Join(para, " ")
}

// issueInfo is what the issue card shows, read with issues show.
type issueInfo struct {
	loading  bool
	err      error
	summary  string
	criteria []issues.Criterion
}

// issueCard is the issue's details: its summary and its acceptance
// criteria, and which are done.
func issueCard(id int, info issueInfo) card {
	return card{title: "Issue", right: dim.Render("issues show " + strconv.Itoa(id)), body: func(w int) []string {
		switch {
		case info.loading:
			return []string{dim.Render("reading…")}
		case info.err != nil:
			return wrap(dim.Render(info.err.Error()), w)
		}
		var out []string
		if info.summary != "" {
			lines := wrap(fg(pal.Text).Render(info.summary), w)
			if len(lines) > issueSummaryLines {
				lines = lines[:issueSummaryLines]
				last := ansi.Truncate(lines[issueSummaryLines-1], max(w-1, 0), "")
				lines[issueSummaryLines-1] = last + dim.Render("…")
			}
			out = append(out, lines...)
		}
		if len(info.criteria) == 0 {
			if len(out) == 0 {
				out = append(out, dim.Render("no details"))
			}
			return out
		}
		if len(out) > 0 {
			out = append(out, "")
		}
		done := 0
		for _, c := range info.criteria {
			if c.Done {
				done++
			}
		}
		out = append(out, heading.Render("acceptance")+"  "+dim.Render(fmt.Sprintf("%d of %d", done, len(info.criteria))))
		for _, c := range info.criteria {
			if c.Done {
				out = append(out, green.Render("✓")+" "+fg(pal.Text).Render(c.Text))
			} else {
				out = append(out, dim.Render("○")+" "+dim.Render(c.Text))
			}
		}
		return out
	}}
}

// fileStat is one file's lines added and removed.
type fileStat struct {
	path     string
	add, del int
	binary   bool
}

// fileStats counts each file's added and removed lines.
func fileStats(files []diff.File) []fileStat {
	out := make([]fileStat, len(files))
	for i, f := range files {
		s := fileStat{path: f.Path(), binary: f.Binary}
		for _, h := range f.Hunks {
			for _, l := range h.Lines {
				switch l.Kind {
				case diff.Added:
					s.add++
				case diff.Removed:
					s.del++
				}
			}
		}
		out[i] = s
	}
	return out
}

// changeInfo is what a changes card shows. uncommitted is how many of the
// files have changes not yet committed, -1 to leave it out.
type changeInfo struct {
	loading     bool
	noRepo      bool
	noStart     bool // a workstream no session has started on
	err         error
	files       []fileStat
	uncommitted int
}

// shownFiles is how many files a changes card lists before it says how
// many more there are.
const shownFiles = 6

// changesCard lists the files changed, the most changed first.
func changesCard(title, since string, info changeInfo) card {
	right := ""
	if since != "" {
		right = dim.Render("since " + since)
	}
	return card{title: title, right: right, body: func(w int) []string {
		switch {
		case info.noRepo:
			return []string{dim.Render("not in a repository")}
		case info.noStart:
			return wrap(dim.Render("No session has worked on it yet."), w)
		case info.loading && info.files == nil:
			return []string{dim.Render("reading…")}
		case info.err != nil:
			return wrap(dim.Render(info.err.Error()), w)
		case len(info.files) == 0:
			return []string{dim.Render("no changes")}
		}
		return fileLines(info.files, info.uncommitted, w)
	}}
}

// fileLines is the total and the files by lines changed, most first, with
// the counts in columns at the right.
func fileLines(files []fileStat, uncommitted, w int) []string {
	fs := append([]fileStat(nil), files...)
	sort.SliceStable(fs, func(i, j int) bool {
		if a, b := fs[i].add+fs[i].del, fs[j].add+fs[j].del; a != b {
			return a > b
		}
		return fs[i].path < fs[j].path
	})
	add, del := 0, 0
	for _, f := range fs {
		add, del = add+f.add, del+f.del
	}
	plural := "s"
	if len(fs) == 1 {
		plural = ""
	}
	total := fg(pal.Added).Render(fmt.Sprintf("+%d", add)) + " " + fg(pal.Removed).Render(fmt.Sprintf("−%d", del)) +
		dim.Render(fmt.Sprintf(" in %d file%s", len(fs), plural))
	note := ""
	if uncommitted >= 0 {
		note = dim.Render(strconv.Itoa(uncommitted) + " uncommitted")
	}
	out := []string{spread(total, note, w)}
	n := len(fs)
	if n > shownFiles {
		n = shownFiles - 1
	}
	aw, dw := 0, 0
	for _, f := range fs[:n] {
		aw, dw = max(aw, len(strconv.Itoa(f.add))), max(dw, len(strconv.Itoa(f.del)))
	}
	for _, f := range fs[:n] {
		counts := fg(pal.Added).Render(fmt.Sprintf("+%*d", aw, f.add)) + " " + fg(pal.Removed).Render(fmt.Sprintf("−%*d", dw, f.del))
		if f.binary {
			counts = dim.Render(fmt.Sprintf("%*s", aw+dw+3, "binary"))
		}
		// A long path keeps its end, where the file's name is.
		room := max(w-ansi.StringWidth(counts)-1, 1)
		p := f.path
		if pw := ansi.StringWidth(p); pw > room {
			p = "…" + ansi.TruncateLeft(p, pw-room+1, "")
		}
		out = append(out, spread(fg(pal.Text).Render(p), counts, w))
	}
	if more := len(fs) - n; more > 0 {
		out = append(out, dim.Render(fmt.Sprintf("… %d more files", more)))
	}
	return out
}

// reviewCard sums up the reviews of a workstream's sessions.
func reviewCard(s review.Summary) card {
	return card{title: "Review", body: func(w int) []string {
		if s.Open == 0 && s.Waiting == 0 {
			return []string{dim.Render("no notes")}
		}
		wait := dim.Render(fmt.Sprintf("%d replies wait", s.Waiting))
		if s.Waiting > 0 {
			wait = red.Render(fmt.Sprintf("%d replies wait", s.Waiting))
		}
		return []string{
			spread(fg(pal.Text).Render(fmt.Sprintf("%d open", s.Open)), wait, w),
			chips(hint{"v", "open"}, hint{"s", "send"}),
		}
	}}
}

// upNext is how many ready issues the issues card offers.
const upNext = 3

// issueGroups is what the issues card lists of a project's other open
// issues: those in review, those in progress, the first ready ones by
// priority, and how many are in the inbox.
func issueGroups(others []issues.Issue) (review, progress, next []issues.Issue, inbox int) {
	sorted := append([]issues.Issue(nil), others...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if a, b := issues.PriorityRank(sorted[i].Priority), issues.PriorityRank(sorted[j].Priority); a != b {
			return a < b
		}
		return sorted[i].ID < sorted[j].ID
	})
	for _, i := range sorted {
		switch i.Status {
		case issues.Review:
			review = append(review, i)
		case issues.InProgress:
			progress = append(progress, i)
		case issues.Ready:
			if len(next) < upNext {
				next = append(next, i)
			}
		case issues.Inbox:
			inbox++
		}
	}
	return
}

// pickable is the issues the issues card's cursor walks, in its order.
func pickable(others []issues.Issue) []issues.Issue {
	r, p, n, _ := issueGroups(others)
	return append(append(r, p...), n...)
}

// issuesCard lists the project's other open issues. linked counts the
// sessions on each, by number. With the keyboard, pick marks the issue
// enter starts work on. err is why the last read of them failed, if it
// did, and the card then lists what the last good read found.
func issuesCard(others []issues.Issue, linked map[int]int, focused bool, pick int, err error) card {
	return card{title: "Issues", right: dim.Render(strconv.Itoa(len(others)) + " open"), accent: focused, body: func(w int) []string {
		var out []string
		if err != nil {
			out = wrap(fg(pal.Asking).Render("issues failed: "+issues.OneLine(err.Error())), w)
		}
		rev, prog, next, inbox := issueGroups(others)
		if len(rev)+len(prog)+len(next) == 0 && inbox == 0 {
			return append(out, dim.Render("no other open issues"))
		}
		const label = 13
		n := 0
		group := func(name string, style lipgloss.Style, is []issues.Issue) {
			for j, i := range is {
				head := strings.Repeat(" ", label)
				if j == 0 {
					head = style.Render(fmt.Sprintf("%-*s", label, name))
				}
				mark := "  "
				if focused && n == pick {
					mark = heading.Render("▸ ")
				}
				text := dim.Render("#"+strconv.Itoa(i.ID)+" ") + fg(pal.Text).Render(i.Title)
				if k := linked[i.ID]; k > 0 && i.Status == issues.InProgress {
					s := "s"
					if k == 1 {
						s = ""
					}
					text += dim.Render(fmt.Sprintf(" · %d session%s", k, s))
				}
				out = append(out, mark+head+text)
				n++
			}
		}
		group("in review", fg(pal.Blocked), rev)
		group("in progress", fg(pal.Working), prog)
		group("up next", fg(pal.Accent), next)
		if inbox > 0 {
			out = append(out, "  "+dim.Render(fmt.Sprintf("inbox %d", inbox)))
		}
		return out
	}}
}

// tasksCard takes the issues card's place in a project that does not
// track its tasks. tool says whether the issues binary is there.
func tasksCard(root string, tool bool) card {
	return card{title: "Tasks", body: func(w int) []string {
		out := wrap(fg(pal.Text).Render("This project doesn't track its tasks."), w)
		out = append(out, "")
		out = append(out, wrap(dim.Render("With issues, each task becomes a workstream: its sessions and terminals group under it in the list, "+
			"and its cockpit shows the task's acceptance criteria and what has changed for it."), w)...)
		out = append(out, "")
		if !tool {
			return append(out, wrap(fg(pal.Text).Render("Install the issues tool to track tasks here."), w)...)
		}
		return append(out, wrap(fg(pal.Text).Render("Start with ")+heading.Render("issues init")+fg(pal.Text).Render(" in "+root+"."), w)...)
	}}
}
