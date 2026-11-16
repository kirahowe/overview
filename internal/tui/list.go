package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"overview/internal/issues"
	"overview/internal/world"
)

// awayTitle heads the section of sessions other apps run, and agentsTitle
// the last one. Project sections are headed by the project's display name.
const (
	awayTitle   = "in other apps"
	agentsTitle = "agents"
)

// section is one block of the list: the entries of one project, the
// sessions other apps run, or the agents list of every session.
type section struct {
	Title   string
	Away    bool
	Agents  bool
	Tracked bool // the project tracks its work with issues
	Failed  bool // and the last read of its issues failed
	Open    int  // and how many of its issues are open
	// Waiting, Sessions and Terminals count what the World has in the
	// section, before any search: its issues that wait on you, its
	// sessions and its terminals.
	Waiting, Sessions, Terminals int
	Entries                      []entry
}

type entryKind int

const (
	sessionEntry  entryKind = iota
	terminalEntry           // a window of plain shells, or a dev server
	projectEntry            // a project's heading
	issueEntry              // an issue that waits on you
)

// The prefixes of the keys of the entries that are not a session's own.
const (
	taskPrefix   = "task:"
	agentsPrefix = "agents"
)

// entry is one thing the cursor can be on: a Claude session, a terminal,
// a project's heading, or an issue that waits on you. It points into the
// World the list was presented from.
//
// A session is listed twice: in its project, or among the sessions other
// apps run, and in the agents list. Its entry in the agents list has the
// alias agentsPrefix, and acts exactly as its primary entry does.
type entry struct {
	kind  entryKind
	sess  *world.Session  // sessionEntry
	term  *world.Terminal // terminalEntry
	proj  *world.Project  // projectEntry
	task  *world.Task     // issueEntry
	alias string          // agentsPrefix for a session's entry in the agents list, else ""
}

// key names an entry for the cursor and for clicks, across refreshes: a
// session's id, a terminal's window ("@7"), "project:" and the project's
// root, or "task:" and the issue's key. A session's entry in the agents
// list is "agents/<id>".
func (e entry) key() string {
	switch e.kind {
	case terminalEntry:
		return e.term.Window
	case projectEntry:
		return "project:" + e.proj.Root
	case issueEntry:
		return taskPrefix + e.task.Key.String()
	}
	if e.sess == nil {
		return "" // no entry at all, as selected gives with nothing listed
	}
	if e.alias != "" {
		return e.alias + "/" + e.sess.SessionID
	}
	return e.sess.SessionID
}

// pin is a heading's pinned directory, which x unpins, "" when the
// project is not pinned.
func (e entry) pin() string {
	if e.proj == nil || len(e.proj.Pins) == 0 {
		return ""
	}
	return e.proj.Pins[0]
}

// projectName is the name of the project the entry is listed in, "" for
// a session another app runs.
func (e entry) projectName() string {
	var p *world.Project
	switch {
	case e.kind == projectEntry:
		p = e.proj
	case e.kind == terminalEntry:
		p = e.term.Project
	case e.kind == issueEntry:
		p = e.task.Project
	case e.sess != nil && e.sess.Host == nil:
		p = e.sess.Project
	}
	if p == nil {
		return ""
	}
	return p.Name
}

// focus is the task the keys that act on one act on from the entry: an
// issue's own, a terminal's, or a session's when it has exactly one. nil
// for none, and for a session with several, which leaves the choice to
// the project's cockpit.
func (e entry) focus() *world.Task {
	switch {
	case e.kind == issueEntry:
		return e.task
	case e.kind == terminalEntry:
		return e.term.Task
	case e.kind == sessionEntry && e.sess != nil && len(e.sess.Holds) == 1:
		return e.sess.Holds[0]
	}
	return nil
}

// focusKey is the key of the entry's focus, "" for none.
func (e entry) focusKey() string {
	if t := e.focus(); t != nil {
		return t.Key.String()
	}
	return ""
}

// session is the entry's session, and whether it is a session at all.
func (e entry) session() (*world.Session, bool) {
	return e.sess, e.kind == sessionEntry && e.sess != nil
}

// lead is the primary entry in es of what stands for task t, as
// Task.Lead picks it: the session linked to it last that has a pane, which
// the stage shows beside an issue and enter goes to. ok is false when t
// has neither a session nor a terminal, or es does not show it.
func lead(es []entry, t *world.Task) (entry, bool) {
	s, tm := t.Lead()
	for _, e := range es {
		if e.alias == "" && (s != nil && e.kind == sessionEntry && e.sess == s || tm != nil && e.kind == terminalEntry && e.term == tm) {
			return e, true
		}
	}
	return entry{}, false
}

// taskAt is where the cursor goes for task key in es, -1 when nowhere:
// the issue's own entry, when it waits on you, else its lead's.
func taskAt(w *world.World, es []entry, key string) int {
	if i := slices.IndexFunc(es, func(e entry) bool { return e.kind == issueEntry && e.task.Key.String() == key }); i >= 0 {
		return i
	}
	k, ok := world.ParseKey(key)
	if t := w.Tasks[k]; ok && t != nil {
		if l, ok := lead(es, t); ok {
			return slices.IndexFunc(es, func(e entry) bool { return e.key() == l.key() })
		}
	}
	return -1
}

// labels are the refs of the tasks a session holds, as its row shows
// them: "#6" for an issue of the project it is listed in, here, and the
// whole ref for another project's.
func labels(s *world.Session, here *world.Project) []string {
	var out []string
	for _, t := range s.Holds {
		if t.Project == here {
			out = append(out, "#"+strconv.Itoa(t.ID))
		} else {
			out = append(out, t.Ref())
		}
	}
	return out
}

// issueRef is how an issue is named: its ref as the CLI gives it, or the
// project id and number.
func issueRef(id string, i issues.Issue) string {
	if i.Ref != "" {
		return i.Ref
	}
	return id + "#" + strconv.Itoa(i.ID)
}

// present is the list: the World laid out in sections, with the entries
// the query does not match left out. It joins nothing and orders nothing,
// so the list is the World's order. The renderer, the cursor, n/N and
// the header all read it.
//
// Each of the World's projects is a section: its heading, then its
// sessions, its terminals, and the issues that wait on you there. The
// heading is shown with anything else the section shows, and a pinned
// project with nothing in it has only its heading, which the query
// matches on the project's name. Then come the sessions other apps run,
// and last the agents list: every session shown above, once, in the
// World's order.
//
// A session matches the query on its project's name, title, last prompt,
// name, host app, and the refs and titles of the tasks it holds. A
// terminal matches on its project's name, title and directory, and an
// issue on its ref, number, title and project name.
func present(w *world.World, query string) []section {
	words := strings.Fields(strings.ToLower(query))
	shown := map[*world.Session]bool{}
	session := func(s *world.Session) bool {
		app := ""
		if s.Host != nil {
			app = s.Host.Label()
		}
		fields := []string{s.Project.Name, s.Title, s.LastPrompt, s.Name, app}
		for _, t := range s.Holds {
			fields = append(fields, t.Ref(), "#"+strconv.Itoa(t.ID), t.Issue.Title)
		}
		return matchFields(words, fields...)
	}
	terminal := func(t *world.Terminal) bool {
		return matchFields(words, t.Project.Name, t.Title, w.Short(t.Path))
	}
	var out []section
	for _, p := range w.Projects {
		sec := section{Title: p.Name, Tracked: p.Tracked, Failed: p.Failed, Open: len(p.Open),
			Waiting: len(p.Waiting), Sessions: len(p.Sessions), Terminals: len(p.Terminals)}
		if p.Empty() {
			if matchFields(words, p.Name) {
				sec.Entries = []entry{{kind: projectEntry, proj: p}}
				out = append(out, sec)
			}
			continue
		}
		for _, s := range p.Sessions {
			if session(s) {
				sec.Entries = append(sec.Entries, entry{kind: sessionEntry, sess: s})
				shown[s] = true
			}
		}
		for _, t := range p.Terminals {
			if terminal(t) {
				sec.Entries = append(sec.Entries, entry{kind: terminalEntry, term: t})
			}
		}
		for _, t := range p.Waiting {
			if matchFields(words, t.Ref(), "#"+strconv.Itoa(t.ID), t.Issue.Title, p.Name) {
				sec.Entries = append(sec.Entries, entry{kind: issueEntry, task: t})
			}
		}
		if len(sec.Entries) > 0 {
			sec.Entries = append([]entry{{kind: projectEntry, proj: p}}, sec.Entries...)
			out = append(out, sec)
		}
	}
	away := section{Title: awayTitle, Away: true, Sessions: len(w.Away)}
	for _, s := range w.Away {
		if session(s) {
			away.Entries = append(away.Entries, entry{kind: sessionEntry, sess: s})
			shown[s] = true
		}
	}
	if len(away.Entries) > 0 {
		out = append(out, away)
	}
	agents := section{Title: agentsTitle, Agents: true}
	for _, s := range w.Agents {
		if shown[s] {
			agents.Entries = append(agents.Entries, entry{kind: sessionEntry, sess: s, alias: agentsPrefix})
		}
	}
	if len(agents.Entries) > 0 {
		out = append(out, agents)
	}
	return out
}

// matchFields reports whether every term, already lower case, is found in
// at least one field, ignoring case. A term is matched against one field at
// a time, so it can never straddle two.
func matchFields(terms []string, fields ...string) bool {
	if len(terms) == 0 {
		return true
	}
	lower := make([]string, len(fields))
	for i, f := range fields {
		lower[i] = strings.ToLower(f)
	}
	for _, t := range terms {
		found := false
		for _, f := range lower {
			if strings.Contains(f, t) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// matches counts what a search shows, each session once, though it is
// listed in the agents list too. A project's heading counts only when the
// project is empty, since otherwise it heads what was found.
func matches(secs []section) int {
	seen := map[string]bool{}
	for _, e := range flatten(secs) {
		k := e.key()
		switch {
		case e.kind == projectEntry && !e.proj.Empty():
			continue
		case e.sess != nil:
			k = e.sess.SessionID
		}
		seen[k] = true
	}
	return len(seen)
}

// flatten is the list as the cursor walks it.
func flatten(secs []section) []entry {
	var out []entry
	for _, s := range secs {
		out = append(out, s.Entries...)
	}
	return out
}

// roots maps directories to the roots of their projects, symlinks
// resolved so /tmp and /private/tmp agree. The Cmds that read the disk
// fill one for every directory their message carries, and the model only
// looks directories up in it.
type roots map[string]string

// findRoots finds the root of each of dirs: repoRoot of the directory with
// its symlinks resolved. exists and resolve are the only disk access.
func findRoots(exists func(string) bool, resolve func(string) string, dirs ...string) roots {
	out := roots{}
	for _, d := range dirs {
		if _, ok := out[d]; d != "" && !ok {
			out[d] = repoRoot(resolve(d), exists)
		}
	}
	return out
}

// exists says whether there is anything at p.
func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// resolvePath resolves symlinks in p, or only cleans it if that fails (the
// directory may be gone by now).
func resolvePath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// root is the root of the project dir belongs to, "" for no dir, as the
// World looks it up.
func (m model) root(dir string) string { return m.world().Root(dir) }

// name is the display name of the project dir belongs to.
func (m model) name(dir string) string { return m.world().Name(dir) }

// short is a directory as the view shows it.
func (m model) short(dir string) string { return m.world().Short(dir) }

// repoRoot returns the nearest ancestor of cwd (cwd included) that holds a
// .jj or .git entry, file or directory. Without one it returns cwd. Either
// way the result is clean.
func repoRoot(cwd string, exists func(path string) bool) string {
	if cwd == "" {
		return ""
	}
	for dir := filepath.Clean(cwd); ; {
		if exists(filepath.Join(dir, ".jj")) || exists(filepath.Join(dir, ".git")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Clean(cwd)
		}
		dir = parent
	}
}
