package tui

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"overview/internal/fleet"
	"overview/internal/issues"
	"overview/internal/world"
)

// A workstream is an open issue of a project that tracks its work with the
// issues tool, together with the sessions and terminals linked to it. The
// refresh reads each project's open issues into a tracker, and the list
// groups the project's entries under them.

// retryAfter is how long a failed issues run is kept before the refresh
// tries again, when nothing in .issues has changed.
const retryAfter = 10 * time.Second

// taskKey names a workstream: its project's root and its issue's number.
func taskKey(root string, id int) string { return world.Key{Root: root, ID: id}.String() }

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

// readTrackers asks for the trackers the World needs, and drops those of
// projects that have left it. A read is asked for a root
// only when none is out for it, so however long the issues CLI takes,
// at most one runs per project, and the rows refresh never waits on one.
// Without a runner there is nothing to read.
func (m model) readTrackers() (model, tea.Cmd) {
	if m.issues == nil {
		return m, nil
	}
	roots := m.world().Trackers
	kept := trackers{}
	for _, r := range roots {
		if t, ok := m.trackers[r]; ok {
			kept[r] = t
		}
	}
	if len(kept) != len(m.trackers) {
		m.trackers = kept
		m = m.setInputs(nil)
	}
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
	} else {
		return m
	}
	return m.setInputs(nil)
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

// trackerReading says whether a read of root's tracker is out, so the task
// picker can say it is reading rather than that the project tracks
// nothing. It is the one place outside the effects that looks at the
// reads in flight.
func (m model) trackerReading(root string) bool { return m.reading[root] }

// projectID is the issues project id of the project at root, as the World
// has it.
func (m model) projectID(root string) string {
	if p := m.world().ProjectOf(root); p != nil {
		return p.ID
	}
	return filepath.Base(root)
}

// opening is the first prompt of a session started on a workstream, which
// its transcript links it by: "Work on overview#6: <title>".
func opening(projectID string, i issues.Issue) string {
	return fmt.Sprintf("Work on %s#%d: %s", projectID, i.ID, i.Title)
}

// opens says whether a transcript's reader takes prompt back as the
// opening of a session on project id's issue i, so it links the session by
// itself.
func opens(prompt, id string, i issues.Issue) bool {
	r, ok := fleet.OpeningTask(prompt, "")
	return ok && r.Project == id && r.ID == i.ID
}
