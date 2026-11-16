package world

import (
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"overview/internal/fleet"
	"overview/internal/issues"
	"overview/internal/tmux"
)

// Derive joins the sources into a World. It is pure: it reads nothing but
// in, and gives the same World for the same sets of inputs, whatever
// their order.
//
// The orchestrator is left out everywhere. A session's project is its
// cwd's root. A session sits in its project, or in Away when it runs
// inside another app, and a terminal sits in its project. Sessions and
// issues are not joined: an issue's status in its tracker is all the
// world says of it.
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

	// The roots of the projects that hold something.
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

	// The trackers in the world: those of the known roots.
	tracked := map[string]Tracker{}
	for r := range known {
		if t, ok := in.Trackers[r]; ok {
			tracked[r] = t
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
	pinned := map[string][]string{} // root → the pinned directories with it
	for _, p := range in.Pinned {
		if p != "" && !slices.Contains(pinned[root(p)], p) {
			pinned[root(p)] = append(pinned[root(p)], p)
		}
	}
	w := World{
		Sessions:     map[string]*Session{},
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
				if i.Status == issues.Review {
					p.InReview++
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
		t.Project.Terminals = append(t.Project.Terminals, t)
		w.Terminals = append(w.Terminals, t)
	}

	// The order of everything.
	bySession := func(ss []*Session) {
		sort.Slice(ss, func(i, j int) bool { return StartedLess(ss[i].Row, ss[j].Row) })
	}
	byWindow := func(ts []*Terminal) {
		sort.Slice(ts, func(i, j int) bool { return WindowLess(ts[i].Window, ts[j].Window) })
	}
	for _, p := range w.byName {
		bySession(p.Sessions)
		bySession(p.Members)
		byWindow(p.Terminals)
		// The list shows a project with something in it, or a pinned one.
		if !p.Empty() || p.Pinned {
			w.Projects = append(w.Projects, p)
		}
	}
	sort.Slice(w.Projects, func(i, j int) bool { return NameLess(w.Projects[i].Name, w.Projects[j].Name) })
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
		w.Need += p.InReview
	}

	// The trackers a refresh reads: the roots of every project, listed or
	// not.
	trackerRoots := map[string]bool{}
	for _, p := range w.byName {
		for _, r := range p.Roots {
			trackerRoots[r] = true
		}
	}
	delete(trackerRoots, "")
	w.Trackers = slices.Sorted(maps.Keys(trackerRoots))
	return w
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
func terminals(panes []tmux.Pane, session, self, opened string, claude map[string]bool, root func(string) string) []*Terminal {
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
	var out []*Terminal
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
		out = append(out, &Terminal{Window: w, Pane: first.ID, Title: termTitle(ps), Path: first.Path, Root: r})
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
