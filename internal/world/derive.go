package world

import (
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"overview/internal/fleet"
	"overview/internal/issues"
	"overview/internal/tmux"
)

// Derive joins the sources into a World. It is pure: it reads nothing but
// in, and gives the same World for the same sets of inputs, whatever
// their order.
//
// The orchestrator is left out everywhere. A session's project is its
// cwd's root, and its links resolve as resolve has it. A task is an open
// issue of a tracked project in the world that is in progress, in review
// or blocked, or that a session is linked to or a terminal was opened
// for. A session sits in its project, or in Away when it runs inside
// another app, wherever its tasks are, and has as tasks the issues in
// progress it is linked to. A terminal sits in its project, with the task
// its window was opened for when that is one of its own project's. An
// issue that waits on you sits in its own project.
func Derive(in Inputs) World {
	root := func(dir string) string { return rootOf(in.Roots, dir) }
	name := func(dir string) string { return nameOf(in.User, root(dir)) }

	var rows []fleet.Row
	for _, r := range in.Rows {
		if r.Name != fleet.OrchestratorName {
			rows = append(rows, r)
		}
	}
	slices.SortFunc(rows, func(a, b fleet.Row) int { return strings.Compare(a.SessionID, b.SessionID) })

	// Every listed session's pane, the orchestrator's too, is no terminal.
	claude := map[string]bool{}
	for _, r := range in.Rows {
		if tty := in.TTYs[r.PID]; tty != "" && !r.Background() {
			claude[tty] = true
		}
	}
	terms := terminals(in.Panes, in.Session, in.Self, in.Opening, claude, root)

	// The roots of the projects that hold something before tasks.
	known := map[string]bool{}
	for _, r := range rows {
		known[root(r.Cwd)] = true
	}
	for _, t := range terms {
		known[t.Root] = true
	}
	for _, p := range in.Pinned {
		if p != "" {
			known[root(p)] = true
		}
	}

	// Links, by session id, and when each was first made.
	links := map[string][]Key{}
	linkedAt := map[string]map[Key]time.Time{}
	targets := map[string]bool{} // roots a link resolves into
	for _, r := range rows {
		var keys []Key
		when := map[Key]time.Time{}
		for _, ref := range r.Tasks {
			k, ok := resolve(ref, root(r.Cwd), in.Trackers, known, root)
			if !ok {
				continue
			}
			keys = slices.DeleteFunc(keys, func(o Key) bool { return o == k })
			keys = append(keys, k)
			// Two refs, such as a prompt's and a command's, can name the
			// same issue: the earlier link counts.
			if t := ref.Linked; !t.IsZero() && (when[k].IsZero() || t.Before(when[k])) {
				when[k] = t
			}
			targets[k.Root] = true
		}
		if len(keys) > 0 {
			links[r.SessionID] = keys
		}
		linkedAt[r.SessionID] = when
	}

	// The trackers in the world: those of the known roots and the roots
	// links resolve into.
	tracked := map[string]Tracker{}
	for r := range known {
		if t, ok := in.Trackers[r]; ok {
			tracked[r] = t
		}
	}
	for r := range targets {
		tracked[r] = in.Trackers[r]
	}

	// The tasks.
	linked := map[Key]bool{}
	for _, keys := range links {
		for _, k := range keys {
			linked[k] = true
		}
	}
	// A terminal's task is one of its own project's, which is by name.
	for _, t := range terms {
		if k, ok := ParseKey(t.task); ok && nameOf(in.User, k.Root) == nameOf(in.User, t.Root) {
			linked[k] = true
		}
	}
	tasks := map[Key]*Task{}
	for r, t := range tracked {
		for _, i := range t.Open {
			k := Key{r, i.ID}
			if _, dup := tasks[k]; dup || !issues.Open(i.Status) || !active(i.Status) && !linked[k] {
				continue
			}
			tasks[k] = &Task{Key: k, Issue: i}
		}
	}

	// The projects, by name, with every root they hold.
	rootsOf := map[string]map[string]bool{}
	add := func(r string) {
		n := nameOf(in.User, r)
		if rootsOf[n] == nil {
			rootsOf[n] = map[string]bool{}
		}
		rootsOf[n][r] = true
	}
	for r := range known {
		add(r)
	}
	for k := range tasks {
		add(k.Root)
	}
	pinned := map[string][]string{} // root → the pinned directories with it
	for _, p := range in.Pinned {
		if p != "" && !slices.Contains(pinned[root(p)], p) {
			pinned[root(p)] = append(pinned[root(p)], p)
		}
	}
	w := World{
		Sessions:     map[string]*Session{},
		Tasks:        tasks,
		Orchestrator: in.Orchestrator,
		Loaded:       in.Loaded,
		RefreshErr:   in.RefreshErr,
		byName:       map[string]*Project{},
		tracked:      tracked,
		roots:        maps.Clone(in.Roots),
		user:         in.User,
	}
	for n, rs := range rootsOf {
		p := &Project{Name: n, Roots: slices.Sorted(maps.Keys(rs))}
		p.Root = p.Roots[0]
		for _, r := range p.Roots {
			p.Pins = append(p.Pins, pinned[r]...)
			t, ok := tracked[r]
			if !ok {
				continue
			}
			if !p.Tracked && t.ID != "" {
				p.ID = t.ID
			}
			p.Tracked, p.Failed = true, p.Failed || t.Failed()
			for _, i := range t.Open {
				if issues.Open(i.Status) {
					p.Open = append(p.Open, i)
				}
			}
		}
		if p.ID == "" {
			p.ID = baseName(p.Root)
		}
		slices.Sort(p.Pins)
		p.Pinned = len(p.Pins) > 0
		// Roots are sorted and each tracker's numbers are its own, so a
		// stable sort by number leaves equal numbers in root order.
		slices.SortStableFunc(p.Open, func(a, b issues.Issue) int { return a.ID - b.ID })
		w.byName[n] = p
	}
	for _, t := range tasks {
		t.Project = w.byName[nameOf(in.User, t.Root)]
		t.IssuesID = w.IssuesID(t.Root)
	}

	// The sessions, in the places they sit.
	for _, r := range rows {
		s := &Session{Row: r, Project: w.byName[name(r.Cwd)], Pane: paneOf(in, r), Elsewhere: elsewhere(in, r), Review: in.Reviews[r.SessionID]}
		if r.Transcript != "" {
			s.Written = in.Written[r.Transcript]
		}
		if !r.Background() {
			s.TTY = in.TTYs[r.PID]
		}
		s.Project.Members = append(s.Project.Members, s)
		if h, ok := in.Hosts[r.PID]; ok && !r.Background() {
			s.Host = &h
		}
		for _, k := range links[r.SessionID] {
			t := tasks[k]
			if t.Issue.Status == issues.InProgress {
				s.Holds = append(s.Holds, t)
			}
			t.Sessions = append(t.Sessions, s)
			at := linkedAt[r.SessionID][k]
			if at.IsZero() && r.StartedAt > 0 {
				at = time.UnixMilli(r.StartedAt)
			}
			if !at.IsZero() && (t.Started.IsZero() || at.Before(t.Started)) {
				t.Started = at
			}
		}
		// A session's tasks keep the order it took them on in, so its
		// labels stay put as it links to them again.
		at := func(t *Task) time.Time { return linkedAt[r.SessionID][t.Key] }
		slices.SortStableFunc(s.Holds, func(a, b *Task) int { return at(a).Compare(at(b)) })
		switch {
		case s.Host != nil:
			w.Away = append(w.Away, s)
		default:
			s.Project.Sessions = append(s.Project.Sessions, s)
		}
		w.Sessions[r.SessionID] = s
		w.Agents = append(w.Agents, s)
	}

	for _, t := range terms {
		t.Project = w.byName[nameOf(in.User, t.Root)]
		if k, ok := ParseKey(t.task); ok {
			if task := tasks[k]; task != nil && task.Project == t.Project {
				t.Task = task
				task.Terminals = append(task.Terminals, &t.Terminal)
			}
		}
		t.Project.Terminals = append(t.Project.Terminals, &t.Terminal)
		w.Terminals = append(w.Terminals, &t.Terminal)
	}

	// The issues that wait on you, each in its own project.
	for _, t := range tasks {
		if t.Waits() {
			t.Project.Waiting = append(t.Project.Waiting, t)
		}
	}

	// The order of everything.
	bySession := func(ss []*Session) {
		sort.Slice(ss, func(i, j int) bool { return StartedLess(ss[i].Row, ss[j].Row) })
	}
	byWindow := func(ts []*Terminal) {
		sort.Slice(ts, func(i, j int) bool { return WindowLess(ts[i].Window, ts[j].Window) })
	}
	for _, p := range w.byName {
		sort.Slice(p.Waiting, func(i, j int) bool { return WaitingLess(p.Waiting[i], p.Waiting[j]) })
		bySession(p.Sessions)
		bySession(p.Members)
		byWindow(p.Terminals)
		// The list shows a project with something in it, or a pinned one.
		if !p.Empty() || p.Pinned {
			w.Projects = append(w.Projects, p)
		}
	}
	sort.Slice(w.Projects, func(i, j int) bool { return NameLess(w.Projects[i].Name, w.Projects[j].Name) })
	for _, t := range tasks {
		// The session linked to it last comes first, as its lead.
		at := func(s *Session) time.Time { return linkedAt[s.SessionID][t.Key] }
		sort.Slice(t.Sessions, func(i, j int) bool {
			a, b := t.Sessions[i], t.Sessions[j]
			if c := at(a).Compare(at(b)); c != 0 {
				return c > 0
			}
			return StartedLess(a.Row, b.Row)
		})
		byWindow(t.Terminals)
	}
	bySession(w.Away)
	byWindow(w.Terminals)
	sort.Slice(w.Agents, func(i, j int) bool { return needsFirst(w.Agents[i].Row, w.Agents[j].Row) })

	// The counts, and whether the hook reports.
	hooked, interactive := false, false
	for _, s := range w.Agents {
		hooked = hooked || s.Hooked
		interactive = interactive || !s.Background()
		switch {
		case s.NeedsYou():
			w.Need++
		case s.State == fleet.Working:
			w.Working++
		default:
			w.Idle++
		}
	}
	w.NoHook = interactive && !hooked
	for _, p := range w.byName {
		w.Need += len(p.Waiting)
	}

	// The trackers a refresh reads: the roots of every project, listed or
	// not, and of the directories links name, whose ids a link may need.
	trackerRoots := map[string]bool{}
	for _, p := range w.byName {
		for _, r := range p.Roots {
			trackerRoots[r] = true
		}
	}
	for _, r := range rows {
		for _, ref := range r.Tasks {
			trackerRoots[root(ref.Dir)] = true
		}
	}
	delete(trackerRoots, "")
	w.Trackers = slices.Sorted(maps.Keys(trackerRoots))
	return w
}

// resolve is the task a session's ref links it to, ok false for
// none. The ref names its project by id, or else by its directory, whose
// tracker gives the id. The task is in the known project with that
// id, preferring the session's own (own), and else in the ref's
// directory's project: a subagent's `issues --dir <worktree> start 6`
// names a worktree, which is a root of its own with the same id. The ref
// links only if that project lists the issue as open.
//
// A ref with neither a project nor a directory is a start the reader of
// the transcript could not place. It links to the session's own project,
// and only to an issue that is in progress, in review or blocked there. A
// start puts its issue in progress, so an issue of that number still
// waiting in the session's own project is not the one that was started.
func resolve(ref fleet.TaskRef, own string, ts map[string]Tracker, known map[string]bool, root func(string) string) (Key, bool) {
	if ref.Project == "" && ref.Dir == "" {
		for _, i := range ts[own].Open {
			if i.ID == ref.ID && active(i.Status) {
				return Key{own, i.ID}, true
			}
		}
		return Key{}, false
	}
	dirRoot := root(ref.Dir)
	id := ref.Project
	if id == "" {
		id = ts[dirRoot].ID
	}
	target := ""
	if id != "" {
		var hits []string
		for r, t := range ts {
			if known[r] && t.ID == id {
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
	if !ok || id != "" && t.ID != id {
		return Key{}, false
	}
	for _, i := range t.Open {
		if i.ID == ref.ID && issues.Open(i.Status) {
			return Key{target, i.ID}, true
		}
	}
	return Key{}, false
}

// active says whether an issue's status makes it a task by itself.
func active(status string) bool {
	return status == issues.InProgress || status == issues.Review || status == issues.Blocked
}

// paneOf is the pane that shows a session in the view's session, "" when
// it has none: the pane its claude runs in, or the attach opened for a
// background session.
func paneOf(in Inputs, r fleet.Row) string {
	if r.Background() {
		for _, p := range in.Panes {
			if r.ID != "" && p.Attach == r.ID && p.Session == in.Session {
				return p.ID
			}
		}
		return ""
	}
	tty := in.TTYs[r.PID]
	for _, p := range in.Panes {
		if tty != "" && p.TTY == tty && p.ID != in.Self && p.Session == in.Session {
			return p.ID
		}
	}
	return ""
}

// elsewhere says whether an interactive session runs in a pane of another
// tmux session on the view's server.
func elsewhere(in Inputs, r fleet.Row) bool {
	tty := in.TTYs[r.PID]
	for _, p := range in.Panes {
		if tty != "" && p.TTY == tty && p.Session != in.Session {
			return true
		}
	}
	return false
}

// term is a Terminal as terminals finds it, with its window's task option.
type term struct {
	Terminal
	task string
}

// shells are the commands a terminal's title skips over, since a shell is
// what every terminal runs.
var shells = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ksh": true, "mksh": true,
	"tcsh": true, "csh": true, "nu": true, "xonsh": true, "elvish": true, "pwsh": true,
}

// terminals finds the terminals among the panes. A window of the view's
// session is one unless it holds the holder, an attach, a pane on the tty
// of a listed Claude session (claude), or the pane c just opened and the
// list has yet to adopt. The list's own pane is left out, and so is the
// agents' pane under it, so a window with only the list's column is no
// terminal. A terminal's root is its window's project
// option, or else the root of its first pane's directory.
func terminals(panes []tmux.Pane, session, self, opened string, claude map[string]bool, root func(string) string) []*term {
	byWindow := map[string][]tmux.Pane{}
	skip := map[string]bool{}
	var order []string
	for _, p := range panes {
		if p.Session != session || p.ID == self || p.Agents || p.Window == "" {
			continue
		}
		if _, ok := byWindow[p.Window]; !ok {
			order = append(order, p.Window)
		}
		byWindow[p.Window] = append(byWindow[p.Window], p)
		if p.Holder || p.Attach != "" || (p.TTY != "" && claude[p.TTY]) || (opened != "" && p.ID == opened) {
			skip[p.Window] = true
		}
	}
	var out []*term
	for _, w := range order {
		if skip[w] {
			continue
		}
		ps := byWindow[w]
		first := ps[0]
		r := first.Project
		if r == "" {
			r = root(first.Path)
		}
		out = append(out, &term{Terminal{Window: w, Pane: first.ID, Title: termTitle(ps), Path: first.Path, Root: r}, first.Task})
	}
	sort.Slice(out, func(i, j int) bool { return WindowLess(out[i].Window, out[j].Window) })
	return out
}

// termTitle names a terminal by what runs in it: the distinct commands of
// its panes that are not shells, or the first pane's shell when only
// shells run.
func termTitle(panes []tmux.Pane) string {
	var cmds []string
	for _, p := range panes {
		c := strings.TrimPrefix(p.Command, "-")
		if c != "" && !shells[c] && !slices.Contains(cmds, c) {
			cmds = append(cmds, c)
		}
	}
	if len(cmds) > 0 {
		return strings.Join(cmds, ", ")
	}
	for _, p := range panes {
		if c := strings.TrimPrefix(p.Command, "-"); c != "" {
			return c
		}
	}
	return "shell"
}

// baseName is the last element of a root, as a project with no tracker
// id is named.
func baseName(root string) string {
	if root == "" {
		return ""
	}
	return filepath.Base(root)
}
