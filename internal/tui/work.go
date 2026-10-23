package tui

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"overview/internal/fleet"
	"overview/internal/issues"
)

// A workstream is an open issue of a project that tracks its work with the
// issues tool, together with the sessions and terminals linked to it. The
// refresh reads each project's open issues into a tracker, and the list
// groups the project's entries under them.

// retryAfter is how long a failed issues run is kept before the refresh
// tries again, when nothing in .issues has changed.
const retryAfter = 10 * time.Second

// taskKey names a workstream: its project's root and its issue's number.
func taskKey(root string, id int) string { return root + "#" + strconv.Itoa(id) }

// tracker is one project's issues as the refreshes read them. A run that
// failed keeps the issues of the last that worked, and its error.
type tracker struct {
	id     string // the project id: the list's, else project.edn's, else the root's name
	stamp  string // .issues when last read: see issueStamp
	ran    time.Time
	issues []issues.Issue // the open ones
	err    error
}

// trackers holds the trackers of the projects that have one, by root.
type trackers map[string]tracker

// issueStamp says how .issues looked: the newest change to an .edn file in
// it and how many there are, so an edit, a new issue or a removed one all
// change it. glob and modTime are the only disk access.
func issueStamp(glob func(string) []string, modTime func(string) time.Time, root string) string {
	files := glob(filepath.Join(root, ".issues", "*.edn"))
	var newest time.Time
	for _, f := range files {
		if t := modTime(f); t.After(newest) {
			newest = t
		}
	}
	return fmt.Sprintf("%d@%d", len(files), newest.UnixNano())
}

// readTracker reads the tracker of the project at root, given what the
// last read found, old, if anything. ok is false when the project has no
// .issues directory. A tracker whose stamp is unchanged is kept as it is,
// unless its last run failed more than retryAfter ago. Only the others
// run the issues CLI. It runs in a Cmd.
func readTracker(old tracker, root string, run issues.Runner, d deps, now time.Time) (tracker, bool) {
	if !issues.HasTracker(d.exists, root) {
		return tracker{}, false
	}
	stamp := issueStamp(d.glob, d.modTime, root)
	t := old
	if t.ran.IsZero() || t.stamp != stamp || t.err != nil && now.Sub(t.ran) >= retryAfter {
		t.stamp, t.ran = stamp, now
		list, err := issues.List(run, root)
		if t.err = err; err == nil {
			t.id = listedID(list)
			t.issues = slices.DeleteFunc(list, func(i issues.Issue) bool { return !issues.Open(i.Status) })
		}
		if t.id == "" {
			t.id = fallbackID(d.readFile, root)
		}
	}
	return t, true
}

// trackerMsg is what readTracker found at root.
type trackerMsg struct {
	root string
	t    tracker
	ok   bool
}

// readTrackers asks for the trackers of the projects in the list, and
// drops those of projects that have left it. A read is asked for a root
// only when none is out for it, so however long the issues CLI takes,
// at most one runs per project, and the rows refresh never waits on one.
// Without a runner there is nothing to read.
func (m model) readTrackers() (model, tea.Cmd) {
	if m.issues == nil {
		return m, nil
	}
	roots := trackerRoots(m.knownRoots(), m.rows, m.root)
	kept := trackers{}
	for _, r := range roots {
		if t, ok := m.trackers[r]; ok {
			kept[r] = t
		}
	}
	m.trackers = kept
	var cmds []tea.Cmd
	for _, root := range roots {
		if root == "" || m.reading[root] {
			continue
		}
		m.reading = withRead(m.reading, root, true)
		old, run, d, now := m.trackers[root], m.issues, m.deps, m.deps.now()
		cmds = append(cmds, func() tea.Msg {
			t, ok := readTracker(old, root, run, d, now)
			return trackerMsg{root, t, ok}
		})
	}
	return m, tea.Batch(cmds...)
}

// landTracker takes a read readTrackers asked for.
func (m model) landTracker(msg trackerMsg) model {
	reading := maps.Clone(m.reading)
	delete(reading, msg.root)
	m.reading = reading
	if msg.ok {
		m.trackers = withRead(m.trackers, msg.root, msg.t)
	} else if _, had := m.trackers[msg.root]; had {
		ts := maps.Clone(m.trackers)
		delete(ts, msg.root)
		m.trackers = ts
	}
	return m
}

// listedID is the project id the issues list gives, "" when it has no
// issue that names one. It is the CLI's own word for the project, so it
// wins over the fallbacks.
func listedID(list []issues.Issue) string {
	for _, i := range list {
		if i.Project != "" {
			return i.Project
		}
	}
	return ""
}

// fallbackID is the project id of a tracker whose list names none, as an
// empty one does: the :id of its project.edn, or else the root's name.
func fallbackID(readFile func(string) ([]byte, error), root string) string {
	if b, err := readFile(filepath.Join(root, ".issues", "project.edn")); err == nil {
		if id, ok := issues.ProjectID(string(b)); ok && id != "" {
			return id
		}
	}
	return filepath.Base(root)
}

// trackerRoots are the roots whose trackers a refresh reads: the known
// roots, and those of the directories the sessions' task refs name, whose
// project ids the refs may need.
func trackerRoots(known []string, rows []fleet.Row, root func(string) string) []string {
	out := slices.Clone(known)
	for _, r := range rows {
		for _, ref := range r.Tasks {
			out = append(out, root(ref.Dir))
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// resolveTask is the workstream a session's ref links it to, "" for none.
// The ref names its project by id, or else by its directory, whose
// tracker gives the id. The workstream is in the known project with that
// id, preferring the session's own (own), and else in the ref's
// directory's project: a subagent's `issues --dir <worktree> start 6`
// names a worktree, which is a root of its own with the same id. The ref
// links only if that project lists the issue as open.
func resolveTask(ref fleet.TaskRef, own string, ts trackers, known map[string]bool, root func(string) string) string {
	dirRoot := root(ref.Dir)
	id := ref.Project
	if id == "" {
		id = ts[dirRoot].id
	}
	target := ""
	if id != "" {
		var hits []string
		for r, t := range ts {
			if known[r] && t.id == id {
				hits = append(hits, r)
			}
		}
		slices.Sort(hits)
		switch {
		case slices.Contains(hits, own):
			target = own
		case len(hits) > 0:
			target = hits[0]
		}
	}
	if target == "" {
		target = dirRoot
	}
	t, ok := ts[target]
	if !ok || id != "" && t.id != id {
		return ""
	}
	for _, i := range t.issues {
		if i.ID == ref.ID && issues.Open(i.Status) {
			return taskKey(target, i.ID)
		}
	}
	return ""
}

// resolveLinks is the workstreams each session is linked to, by session
// ID, oldest first and each once, where it sits as late as it was last
// linked. own is the root of a session's project.
func resolveLinks(rows []fleet.Row, ts trackers, known map[string]bool, root func(string) string) map[string][]string {
	out := map[string][]string{}
	for _, r := range rows {
		var keys []string
		for _, ref := range r.Tasks {
			k := resolveTask(ref, root(r.Cwd), ts, known, root)
			if k == "" {
				continue
			}
			keys = slices.DeleteFunc(keys, func(o string) bool { return o == k })
			keys = append(keys, k)
		}
		if len(keys) > 0 {
			out[r.SessionID] = keys
		}
	}
	return out
}

// tasks is what buildList knows of the projects' issues: the open ones of
// each tracked project by root, its project id, whether its last read
// failed, and the workstreams each session is linked to.
type tasks struct {
	open   map[string][]issues.Issue
	ids    map[string]string
	failed map[string]bool
	links  map[string][]string
}

// tasksOf is tasks for the trackers of the known roots, and of the roots
// the links point to.
func tasksOf(ts trackers, known []string, rows []fleet.Row, root func(string) string) tasks {
	isKnown := map[string]bool{}
	for _, r := range known {
		isKnown[r] = true
	}
	tk := tasks{open: map[string][]issues.Issue{}, ids: map[string]string{}, failed: map[string]bool{}, links: resolveLinks(rows, ts, isKnown, root)}
	use := func(r string) {
		if t, ok := ts[r]; ok {
			tk.open[r], tk.ids[r], tk.failed[r] = t.issues, t.id, t.err != nil
		}
	}
	for _, r := range known {
		use(r)
	}
	for _, keys := range tk.links {
		for _, k := range keys {
			use(k[:strings.LastIndexByte(k, '#')])
		}
	}
	return tk
}

// tasks is the model's tasks, as the list shows them.
func (m model) tasks() tasks { return tasksOf(m.trackers, m.knownRoots(), m.rows, m.root) }

// projectID is the issues project id of the project at root.
func (m model) projectID(root string) string {
	if t, ok := m.trackers[root]; ok && t.id != "" {
		return t.id
	}
	return filepath.Base(root)
}

// opening is the first prompt of a session started on a workstream, which
// the hook reads to link it: "Work on overview#6: <title>".
func opening(projectID string, i issues.Issue) string {
	return fmt.Sprintf("Work on %s#%d: %s", projectID, i.ID, i.Title)
}
