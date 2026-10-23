package tui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

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
	id     string // the project id, from project.edn or the root's name
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

// readTrackers reads the trackers of roots, the projects that have a
// .issues directory among them, starting from old. A tracker whose stamp
// is unchanged is kept as it is, unless its last run failed more than
// retryAfter ago. Only the others run the issues CLI. With no runner there
// is nothing to read. It runs in a Cmd, and never changes old.
func readTrackers(old trackers, roots []string, run issues.Runner, d deps, now time.Time) trackers {
	out := trackers{}
	if run == nil {
		return out
	}
	for _, root := range roots {
		if _, done := out[root]; done || root == "" || !issues.HasTracker(d.exists, root) {
			continue
		}
		stamp := issueStamp(d.glob, d.modTime, root)
		t, ok := old[root]
		if ok && t.stamp == stamp && (t.err == nil || now.Sub(t.ran) < retryAfter) {
			out[root] = t
			continue
		}
		t.id = filepath.Base(root)
		if b, err := d.readFile(filepath.Join(root, ".issues", "project.edn")); err == nil {
			if id, ok := issues.ProjectID(string(b)); ok && id != "" {
				t.id = id
			}
		}
		t.stamp, t.ran = stamp, now
		list, err := issues.List(run, root)
		if t.err = err; err == nil {
			t.issues = slices.DeleteFunc(list, func(i issues.Issue) bool { return !issues.Open(i.Status) })
		}
		out[root] = t
	}
	return out
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
// each tracked project by root, its project id, and the workstreams each
// session is linked to.
type tasks struct {
	open  map[string][]issues.Issue
	ids   map[string]string
	links map[string][]string
}

// tasksOf is tasks for the trackers of the known roots, and of the roots
// the links point to.
func tasksOf(ts trackers, known []string, rows []fleet.Row, root func(string) string) tasks {
	isKnown := map[string]bool{}
	for _, r := range known {
		isKnown[r] = true
	}
	tk := tasks{open: map[string][]issues.Issue{}, ids: map[string]string{}, links: resolveLinks(rows, ts, isKnown, root)}
	use := func(r string) {
		if t, ok := ts[r]; ok {
			tk.open[r], tk.ids[r] = t.issues, t.id
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
