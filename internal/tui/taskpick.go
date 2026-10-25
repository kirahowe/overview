package tui

import (
	"slices"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"overview/internal/fleet"
	"overview/internal/issues"
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

// taskOrder ranks a status in the picker, lowest first: ready, then
// inbox, which are what there is to start, then the rest as issues.Rank
// orders them.
func taskOrder(status string) int {
	switch status {
	case issues.Ready:
		return 0
	case issues.Inbox:
		return 1
	}
	return 2 + issues.Rank(status)
}

// taskRowLess orders the picker's rows: by taskOrder, then priority, then
// number.
func taskRowLess(a, b issues.Issue) bool {
	if ra, rb := taskOrder(a.Status), taskOrder(b.Status); ra != rb {
		return ra < rb
	}
	if pa, pb := issues.PriorityRank(a.Priority), issues.PriorityRank(b.Priority); pa != pb {
		return pa < pb
	}
	return a.ID < b.ID
}

// taskMatches says whether an issue of project id matches every word,
// already lower case, on its ref (#6 or overview#6) or its title.
func taskMatches(words []string, id string, i issues.Issue) bool {
	return matchFields(words, "#"+strconv.Itoa(i.ID), issueRef(id, i), i.Title)
}

// taskRows are the open issues of open that match query, in the
// picker's order, each with the sessions linked to it, by number, as
// sessions has them.
func taskRows(open []issues.Issue, id string, sessions map[int]int, query string) []taskRow {
	words := strings.Fields(strings.ToLower(query))
	var out []taskRow
	for _, i := range open {
		if issues.Open(i.Status) && taskMatches(words, id, i) {
			out = append(out, taskRow{issue: i, sessions: sessions[i.ID]})
		}
	}
	slices.SortStableFunc(out, func(a, b taskRow) int {
		switch {
		case taskRowLess(a.issue, b.issue):
			return -1
		case taskRowLess(b.issue, a.issue):
			return 1
		}
		return 0
	})
	return out
}

// taskListOf is what the picker shows for the project called name. tool
// says whether overview has the issues binary, tracked whether the
// project's tracker has been read, as t, and reading whether a read of
// it is out.
func taskListOf(name string, t tracker, tracked, reading, tool bool, sessions map[int]int, query string) taskList {
	switch {
	case !tool:
		return taskList{note: "Install the issues tool to track tasks."}
	case !tracked && reading:
		return taskList{note: "reading…"}
	case !tracked:
		return taskList{note: untrackedNote(name)}
	}
	l := taskList{rows: taskRows(t.issues, t.id, sessions, query)}
	switch {
	case t.err != nil:
		l.note = "issues failed: " + oneLine(t.err.Error())
	case !slices.ContainsFunc(t.issues, func(i issues.Issue) bool { return issues.Open(i.Status) }):
		l.note = "No open issues in " + name + "."
	case len(l.rows) == 0:
		l.note = "Nothing matches."
	}
	return l
}

func untrackedNote(name string) string {
	return name + " doesn't track its tasks. issues init in its root starts it."
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

// streamSessions counts the sessions linked to each workstream of the
// project at root, by issue number, from links, the workstreams of each
// session by session ID. The orchestrator is no session of a project.
func streamSessions(rows []fleet.Row, links map[string][]string, root string) map[int]int {
	out := map[int]int{}
	prefix := root + "#"
	for _, r := range rows {
		if r.Name == fleet.OrchestratorName {
			continue
		}
		seen := map[int]bool{}
		for _, k := range links[r.SessionID] {
			n, err := strconv.Atoi(strings.TrimPrefix(k, prefix))
			if !strings.HasPrefix(k, prefix) || err != nil || seen[n] {
				continue
			}
			seen[n] = true
			out[n]++
		}
	}
	return out
}

// taskList is what the open picker shows now.
func (m model) taskList() taskList {
	root := m.taskPick.root
	t, tracked := m.trackers[root]
	sessions := streamSessions(m.rows, m.tasks().links, root)
	return taskListOf(m.name(root), t, tracked, m.reading[root], m.issues != nil, sessions, m.taskPick.query)
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
	_, tracked := m.trackers[root]
	open, note := taskOpens(m.name(root), tracked, m.reading[root], m.issues != nil)
	if !open {
		return m.say(note), nil
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
		for i, l := range reflow(text, max(w-hang, 1)) {
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
			for _, l := range reflow(h, w) {
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

// reflow wraps text at w cells, at spaces where it can. ansi.Wrap can
// leave a line a cell too wide, keeping a hyphen with the letter before
// it, so its lines are broken again at w.
func reflow(text string, w int) []string {
	return strings.Split(ansi.Hardwrap(ansi.Wrap(text, w, ""), w, true), "\n")
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
