package tui

import (
	"slices"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"overview/internal/issues"
	"overview/internal/world"
)

// The task picker is s in the list: it takes the list pane, as the
// add-project picker does, and offers the open issues of one project to
// start work on. Its rows are a pure function of the project's tracker,
// the sessions linked to its workstreams and the query, and enter acts
// on the selected one through taskGoes. Starting a session is startOn's
// Cmd, as for c on a workstream. Like the cockpit it zooms the list's
// pane, and where the window has room the selected issue's details show
// beside the rows, read with issues show as the cursor reaches it.

// taskPick is the task picker's state. The zero value is a closed picker.
// top is the first line of the details shown.
type taskPick struct {
	open  bool
	root  string // the project's root
	query string
	sel   int
	top   int
}

// taskRow is one issue the picker offers, and how many sessions are
// linked to its workstream.
type taskRow struct {
	issue    issues.Issue
	sessions int
}

// taskList is what the picker shows: a note, muted, and then the rows.
type taskList struct {
	note string
	rows []taskRow
}

// taskMatches says whether an issue of project id matches every word,
// already lower case, on its ref (#6 or overview#6) or its title.
func taskMatches(words []string, id string, i issues.Issue) bool {
	return matchFields(words, "#"+strconv.Itoa(i.ID), issueRef(id, i), i.Title)
}

// taskRows are the open issues of open that match query, as
// world.StartLess orders them, each with the sessions linked to it, as sessions counts
// them.
func taskRows(open []issues.Issue, id string, sessions func(issues.Issue) int, query string) []taskRow {
	words := strings.Fields(strings.ToLower(query))
	var out []taskRow
	for _, i := range open {
		if issues.Open(i.Status) && taskMatches(words, id, i) {
			out = append(out, taskRow{issue: i, sessions: sessions(i)})
		}
	}
	slices.SortStableFunc(out, func(a, b taskRow) int {
		switch {
		case world.StartLess(a.issue, b.issue):
			return -1
		case world.StartLess(b.issue, a.issue):
			return 1
		}
		return 0
	})
	return out
}

// taskListOf is what the picker shows for the project called name. tool
// says whether overview has the issues binary, tracked whether the World
// holds the project's tracker, as t, and reading whether a read of it is
// out.
func taskListOf(name string, t world.Tracker, tracked, reading, tool bool, sessions func(issues.Issue) int, query string) taskList {
	switch {
	case !tool:
		return taskList{note: "Install the issues tool to track tasks."}
	case !tracked && reading:
		return taskList{note: "reading…"}
	case !tracked:
		return taskList{note: untrackedNote(name)}
	}
	l := taskList{rows: taskRows(t.Open, t.ID, sessions, query)}
	switch {
	case t.Failed():
		l.note = "issues failed: " + t.Err
	case !slices.ContainsFunc(t.Open, func(i issues.Issue) bool { return issues.Open(i.Status) }):
		l.note = "No open issues in " + name + "."
	case len(l.rows) == 0:
		l.note = "Nothing matches."
	}
	return l
}

func untrackedNote(name string) string {
	return name + " doesn't track its tasks. issues init there starts it."
}

// taskOpens says what s does on a project: open the picker, or only say
// note. It opens on a project whose tracker is read or being read, with
// the issues binary to read it.
func taskOpens(name string, tracked, reading, tool bool) (open bool, note string) {
	switch {
	case !tool:
		return false, "Install the issues tool to track tasks."
	case !tracked && !reading:
		return false, untrackedNote(name)
	}
	return true, ""
}

// taskGoes says whether enter on a row goes to its workstream rather than
// starting a session: it does when sessions already work on the issue,
// so a second one is never started by accident.
func taskGoes(r taskRow) bool { return r.sessions > 0 }

// taskList is what the open picker shows now.
func (m model) taskList() taskList {
	root := m.taskPick.root
	t, tracked := m.world().Tracker(root)
	return taskListOf(m.name(root), t, tracked, m.trackerReading(root), m.issues != nil, m.sessionsOn(root), m.taskPick.query)
}

// taskSel is the selected row of l, -1 when it has none.
func (m model) taskSel(l taskList) int {
	if len(l.rows) == 0 {
		return -1
	}
	return max(0, min(m.taskPick.sel, len(l.rows)-1))
}

// openTasks opens the task picker on the project at root, or says why
// not.
func (m model) openTasks(root string) (model, tea.Cmd) {
	_, tracked := m.world().Tracker(root)
	open, note := taskOpens(m.name(root), tracked, m.trackerReading(root), m.issues != nil)
	if !open {
		return m.sayAbout(note, m.name(root)), nil
	}
	// From the cockpit the zoom it holds stays for the picker.
	m.cockpit, m.cockpitSoon = cockpit{}, false
	m.taskPick, m.zoomPanes = taskPick{open: true, root: root}, m.inStage()
	if m.staging {
		return m, nil // the zoom waits for the move to land
	}
	return m.syncZoom()
}

// taskReads asks for the selected issue's details while the picker shows
// them.
func (m model) taskReads() (model, tea.Cmd) {
	if m.taskSplit() == 0 {
		return m, nil
	}
	l := m.taskList()
	j := m.taskSel(l)
	if j < 0 {
		return m, nil
	}
	return m.readIssue(m.taskPick.root, l.rows[j].issue.ID)
}

// taskSplit is the width of the picker's rows when the details show
// beside them, 0 when the pane has room only for the rows.
func (m model) taskSplit() int {
	if !m.taskPick.open {
		return 0
	}
	return cockpitListWidth(m.width.want, m.w)
}

// taskListWidth is the width of the picker's rows.
func (m model) taskListWidth() int {
	if lw := m.taskSplit(); lw > 0 {
		return lw
	}
	return m.w
}

// detailLines lays out an issue's Markdown details w cells wide. Its
// paragraphs and list items are hard-wrapped in the file, so their lines
// are joined and wrapped again at w, a list item hanging under its
// marker. Headings lose their hashes and stand out, a fenced block keeps
// its lines, broken where they are too long, and runs of blank lines
// become one.
func detailLines(details string, w int) []string {
	if w <= 0 {
		return nil
	}
	var out []string
	blank := func() {
		if len(out) > 0 && out[len(out)-1] != "" {
			out = append(out, "")
		}
	}
	// para is the paragraph or list item being gathered: its marker, as
	// wide as the hang under it, and its words.
	var marker string
	var words []string
	flush := func() {
		if len(words) == 0 {
			return
		}
		// A marker too wide to hang under leads the words instead.
		hang, text := ansi.StringWidth(marker), strings.Join(words, " ")
		if hang > w/2 {
			hang, text = 0, marker+text
		}
		for i, l := range plainWrap(text, max(w-hang, 1)) {
			lead := strings.Repeat(" ", hang)
			if i == 0 && hang > 0 {
				lead = marker
			}
			out = append(out, fg(pal.Text).Render(lead+l))
		}
		marker, words = "", nil
	}
	fence := false
	for _, line := range strings.Split(details, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "```"):
			flush()
			fence = !fence
		case fence:
			for _, l := range strings.Split(ansi.Hardwrap(strings.TrimRight(line, " "), w, true), "\n") {
				out = append(out, dim.Render(l))
			}
		case t == "":
			flush()
			blank()
		case strings.HasPrefix(t, "#"):
			flush()
			blank()
			h := strings.TrimSpace(strings.TrimLeft(t, "#"))
			for _, l := range plainWrap(h, w) {
				out = append(out, heading.Render(l))
			}
		case listMarker(t) != "":
			flush()
			indent := len(line) - len(strings.TrimLeft(line, " "))
			m := listMarker(t)
			marker = strings.Repeat(" ", min(indent, w/4)) + m
			words = strings.Fields(strings.TrimPrefix(t, strings.TrimRight(m, " ")))
		default:
			words = append(words, strings.Fields(t)...)
		}
	}
	flush()
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// plainWrap is wrap for unstyled text, without the padding, so the
// caller can style each line as it is.
func plainWrap(text string, w int) []string {
	if isASCII(text) && w > 0 {
		// A cell to a byte, so the cuts are byte offsets: the help
		// measures itself this way on every key.
		var ls []string
		for _, r := range wrapRanges(text, w) {
			ls = append(ls, text[r[0]:r[1]])
		}
		return ls
	}
	ls := wrap(text, w)
	for i, l := range ls {
		ls[i] = strings.TrimRight(l, " ")
	}
	return ls
}

// listMarker is the marker t starts with as a list item, with a space
// after it, such as "- ", "- [ ] " or "2. ", and "" when t is no item.
func listMarker(t string) string {
	var m string
	switch {
	case strings.HasPrefix(t, "- "), strings.HasPrefix(t, "* "), strings.HasPrefix(t, "+ "):
		m = t[:2]
	default:
		d := 0
		for d < len(t) && t[d] >= '0' && t[d] <= '9' {
			d++
		}
		if d == 0 || d > 3 || !strings.HasPrefix(t[d:], ". ") {
			return ""
		}
		m = t[:d+2]
	}
	rest := t[len(m):]
	for _, box := range []string{"[ ] ", "[x] ", "[X] "} {
		if strings.HasPrefix(rest, box) {
			return m + box
		}
	}
	return m
}

// taskEnter acts on the selected row: it goes to the issue's workstream
// when taskGoes says so, and otherwise starts a session on the issue, as
// c does on a workstream. The picker closes either way.
func (m model) taskEnter() (model, tea.Cmd) {
	l := m.taskList()
	j := m.taskSel(l)
	if j < 0 {
		return m.say("no issue to start"), nil
	}
	r, root := l.rows[j], m.taskPick.root
	m.taskPick = taskPick{}
	m, zoom := m.syncZoom()
	m, cmd := m.taskAct(root, r)
	return m, tea.Batch(zoom, cmd)
}

// taskAct goes to r's workstream or starts a session on it, for
// taskEnter.
func (m model) taskAct(root string, r taskRow) (model, tea.Cmd) {
	if taskGoes(r) {
		key := taskKey(root, r.issue.ID)
		es := m.ordered()
		k := streamAt(es, key)
		if k < 0 && m.filter != "" {
			// The filter hides it: show everything, so the cursor can go.
			m.filter, m.filtering = "", false
			es = m.ordered()
			k = streamAt(es, key)
		}
		if k >= 0 {
			return m.moveTo(es, k), nil
		}
	}
	return m.startOn(root, r.issue)
}
