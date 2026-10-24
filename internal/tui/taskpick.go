package tui

import (
	"slices"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"overview/internal/fleet"
	"overview/internal/issues"
)

// The task picker is s in the list: it takes the list pane, as the
// add-project picker does, and offers the open issues of one project to
// start work on. Its rows are a pure function of the project's tracker,
// the sessions linked to its workstreams and the query, and enter acts
// on the selected one through taskGoes. Starting a session is startOn's
// Cmd, as for c on a workstream.

// taskPick is the task picker's state. The zero value is a closed picker.
type taskPick struct {
	open  bool
	root  string // the project's root
	query string
	sel   int
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
func (m model) openTasks(root string) model {
	_, tracked := m.trackers[root]
	open, note := taskOpens(m.name(root), tracked, m.reading[root], m.issues != nil)
	if !open {
		return m.say(note)
	}
	m.taskPick = taskPick{open: true, root: root}
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
