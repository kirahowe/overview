package tui

import (
	"os"
	"path/filepath"
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
	Entries []entry
}

type entryKind int

const (
	sessionEntry    entryKind = iota
	terminalEntry             // a window of plain shells, or a dev server
	projectEntry              // a pinned project with nothing in it yet
	workstreamEntry           // an issue that is being worked on
)

// The prefixes of the keys of the entries that are not a session's own.
const (
	taskPrefix   = "task:"
	agentsPrefix = "agents"
)

// entry is one thing the cursor can be on: a Claude session, a terminal,
// the placeholder of an empty pinned project, or a workstream. It points
// into the World the list was presented from.
//
// A session can be listed more than once: under each workstream it is
// linked to, and in the agents list. Its primary entry, under the
// workstream it was linked to last or else in its project, has alias "".
// The others have the placement as their alias, and act exactly as the
// primary does.
type entry struct {
	kind entryKind
	sess *world.Session  // sessionEntry
	term *world.Terminal // terminalEntry
	proj *world.Project  // projectEntry
	// stream is a workstream entry's own, or the one an entry sits under.
	// An agents alias sits under none.
	stream *world.Stream
	alias  string // a session's placement, "" for its primary entry
}

// key names an entry for the cursor and for clicks, across refreshes: a
// session's id, a terminal's window ("@7"), "project:" and the pinned
// directory, or "task:" and the workstream's key. A session's alias puts
// its placement and a slash before its id: "task:/w/app#6/<id>" or
// "agents/<id>".
func (e entry) key() string {
	switch e.kind {
	case terminalEntry:
		return e.term.Window
	case projectEntry:
		return "project:" + e.pin()
	case workstreamEntry:
		return taskPrefix + e.task()
	}
	if e.sess == nil {
		return "" // no entry at all, as selected gives with nothing listed
	}
	if e.alias != "" {
		return e.alias + "/" + e.sess.SessionID
	}
	return e.sess.SessionID
}

// pin is a placeholder's pinned directory, which x unpins.
func (e entry) pin() string {
	if e.proj == nil || len(e.proj.Pins) == 0 {
		return ""
	}
	return e.proj.Pins[0]
}

// task is the key of the workstream the entry is or sits under, "" for
// none.
func (e entry) task() string {
	if e.stream == nil {
		return ""
	}
	return e.stream.Key.String()
}

// session is the entry's session, and whether it is a session at all.
func (e entry) session() (*world.Session, bool) {
	return e.sess, e.kind == sessionEntry && e.sess != nil
}

// children are the entries listed under the workstream task in es, in
// order.
func children(es []entry, task string) []entry {
	var out []entry
	for _, e := range es {
		if e.task() == task && e.kind != workstreamEntry && e.alias != agentsPrefix {
			out = append(out, e)
		}
	}
	return out
}

// lead is the entry under a workstream that stands for it, of its
// children cs: the first with a pane, as hasPane says, which the stage
// shows beside the workstream and enter goes to, or else the first
// session, which enter attaches to. ok is false when there is neither.
// It picks from what the list shows, as World's Lead picks from the whole
// workstream.
func lead(cs []entry, hasPane func(entry) bool) (entry, bool) {
	for _, c := range cs {
		if hasPane(c) {
			return c, true
		}
	}
	for _, c := range cs {
		if c.kind == sessionEntry {
			return c, true
		}
	}
	return entry{}, false
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
// Each of the World's projects is a section: its workstreams, each with
// the sessions and terminals under it, then its own sessions and
// terminals. A pinned project with nothing in it is a placeholder, which
// the query matches on the project's name. Then come the sessions other
// apps run, and last the agents list: every session shown above, once,
// in the World's order.
//
// A workstream matches the query on its ref, number, title and project
// name, and is shown when it or anything under it matches. When it
// matches itself, everything under it is shown. A session matches on its
// project's name, title, last prompt, name and host app, and a terminal
// on its project's name, title and directory.
func present(w *world.World, query string) []section {
	words := strings.Fields(strings.ToLower(query))
	shown := map[*world.Session]bool{}
	session := func(s *world.Session) bool {
		app := ""
		if s.Host != nil {
			app = s.Host.Label()
		}
		return matchFields(words, s.Project.Name, s.Title, s.LastPrompt, s.Name, app)
	}
	terminal := func(t *world.Terminal) bool {
		return matchFields(words, t.Project.Name, t.Title, w.Short(t.Path))
	}
	var out []section
	for _, p := range w.Projects {
		sec := section{Title: p.Name, Tracked: p.Tracked, Failed: p.Failed, Open: len(p.Open)}
		if p.Empty() {
			if matchFields(words, p.Name) {
				sec.Entries = []entry{{kind: projectEntry, proj: p}}
				out = append(out, sec)
			}
			continue
		}
		for _, st := range p.Streams {
			whole := matchFields(words, issueRef(p.ID, st.Issue), "#"+strconv.Itoa(st.ID), st.Issue.Title, p.Name)
			var under []entry
			for _, s := range st.Sessions {
				if whole || session(s) {
					e := entry{kind: sessionEntry, sess: s, stream: st}
					if s.Primary() != st {
						e.alias = taskPrefix + st.Key.String()
					}
					under = append(under, e)
					shown[s] = true
				}
			}
			for _, t := range st.Terminals {
				if whole || terminal(t) {
					under = append(under, entry{kind: terminalEntry, term: t, stream: st})
				}
			}
			if whole || len(under) > 0 {
				sec.Entries = append(append(sec.Entries, entry{kind: workstreamEntry, stream: st}), under...)
			}
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
		if len(sec.Entries) > 0 {
			out = append(out, sec)
		}
	}
	away := section{Title: awayTitle, Away: true}
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
