package tui

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"overview/internal/fleet"
	"overview/internal/issues"
	"overview/internal/tmux"
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
// the placeholder of an empty pinned project, or a workstream.
//
// A session can be listed more than once: under each workstream it is
// linked to, and in the agents list. Its primary entry, under the
// workstream it was linked to last or else in its project, has alias "".
// The others have the placement as their alias, and act exactly as the
// primary does.
type entry struct {
	kind    entryKind
	row     fleet.Row    // sessionEntry
	term    terminal     // terminalEntry
	project string       // projectEntry: the project's root; and a workstream's, see task
	issue   issues.Issue // workstreamEntry, and see task
	// task is the key of the workstream an entry sits under, or a
	// workstream's own. project and issue are then that workstream's. An
	// agents alias has the project and issue of its session's primary
	// workstream, if any, but no task.
	task  string
	alias string // a session's placement, "" for its primary entry
}

// key names an entry for the cursor and for clicks, across refreshes: a
// session's id, a terminal's window ("@7"), "project:" and the root, or
// "task:" and the workstream's key. A session's alias puts its placement
// and a slash before its id: "task:/w/app#6/<id>" or "agents/<id>".
func (e entry) key() string {
	switch e.kind {
	case terminalEntry:
		return e.term.Window
	case projectEntry:
		return "project:" + e.project
	case workstreamEntry:
		return taskPrefix + e.task
	}
	if e.alias != "" {
		return e.alias + "/" + e.row.SessionID
	}
	return e.row.SessionID
}

// session is the entry's row, and whether it is a session at all.
func (e entry) session() (fleet.Row, bool) { return e.row, e.kind == sessionEntry }

// workstream is the workstream an entry is or sits under, and whether it
// has one.
func (e entry) workstream() (root string, i issues.Issue, ok bool) {
	return e.project, e.issue, e.task != ""
}

// children are the entries listed under the workstream task in es, in
// order.
func children(es []entry, task string) []entry {
	var out []entry
	for _, e := range es {
		if e.task == task && e.kind != workstreamEntry && e.alias != agentsPrefix {
			out = append(out, e)
		}
	}
	return out
}

// lead is the entry under a workstream that stands for it, of its
// children cs: the first with a pane, as hasPane says, which the stage
// shows beside the workstream and enter goes to, or else the first
// session, which enter attaches to. ok is false when there is neither.
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

func sessionEntries(rows []fleet.Row) []entry {
	out := make([]entry, len(rows))
	for i, r := range rows {
		out[i] = entry{kind: sessionEntry, row: r}
	}
	return out
}

// active says whether an issue's status makes it a workstream by itself.
func active(status string) bool {
	return status == issues.InProgress || status == issues.Review || status == issues.Blocked
}

// workstreamLess orders a project's workstreams: by status as issues.Rank
// has it, then priority, then number, then root.
func workstreamLess(a, b entry) bool {
	if ra, rb := issues.Rank(a.issue.Status), issues.Rank(b.issue.Status); ra != rb {
		return ra < rb
	}
	if pa, pb := issues.PriorityRank(a.issue.Priority), issues.PriorityRank(b.issue.Priority); pa != pb {
		return pa < pb
	}
	if a.issue.ID != b.issue.ID {
		return a.issue.ID < b.issue.ID
	}
	return a.project < b.project
}

// issueRef is how an issue is named: its ref as the CLI gives it, or the
// project id and number.
func issueRef(id string, i issues.Issue) string {
	if i.Ref != "" {
		return i.Ref
	}
	return id + "#" + strconv.Itoa(i.ID)
}

// needsFirst orders the sessions that need you first, longest wait first,
// then the rest as sortActive does.
func needsFirst(rs []fleet.Row) {
	sort.Slice(rs, func(i, j int) bool {
		ni, nj := rs[i].NeedsYou(), rs[j].NeedsYou()
		switch {
		case ni != nj:
			return ni
		case !ni:
			return activeLess(rs[i], rs[j])
		case !rs[i].Since.Equal(rs[j].Since):
			return rs[i].Since.Before(rs[j].Since)
		}
		return rs[i].SessionID < rs[j].SessionID
	})
}

// buildList is the single source of truth for the list. The renderer, the
// cursor, n/N and the header all read it.
//
// It drops the orchestrator and every entry the query does not match, and
// puts the rest into one section per project, ordered by display name.
// A project that tracks its work with issues lists its workstreams first:
// each open issue that is in progress, in review or blocked, or that a
// session is linked to, ordered by workstreamLess. Under each come the
// sessions linked to it, working first and then the newest activity, then
// the terminals opened for it, by window number. A session linked to
// several sits under each, and its primary entry is under the one it was
// linked to last. Then come the project's other sessions, in the same
// order, and its other terminals. A session that runs inside another app,
// as host reports, is not one the user drives from a project, so unless
// it is linked it goes in a section of its own instead. A pinned project
// with no entry at all, whatever the query, gets a placeholder of its
// own, which the query matches on the project's name. Last comes the
// agents list: every session shown above once, those that need you
// first, longest wait first.
//
// A workstream matches the query on its ref, title and project name, and
// is shown when it or anything under it matches. When it matches itself,
// everything under it is shown. project names the project a directory
// belongs to, and short is how a terminal's directory is shown, which the
// query also matches. The result depends only on the sets it is given,
// never on their order.
func buildList(rows []fleet.Row, terms []terminal, pinned []string, tk tasks, query string, project, short func(dir string) string, host func(fleet.Row) (string, bool)) []section {
	words := strings.Fields(strings.ToLower(query))

	// The workstreams, by key, before the query.
	streams := map[string]entry{}
	tracked := map[string]bool{} // project names
	for root, list := range tk.open {
		tracked[project(root)] = true
		for _, i := range list {
			if issues.Open(i.Status) {
				k := taskKey(root, i.ID)
				streams[k] = entry{kind: workstreamEntry, project: root, issue: i, task: k}
			}
		}
	}
	links := map[string][]string{} // by session id, only to workstreams
	linked := map[string]bool{}    // workstreams with a session
	for _, r := range rows {
		if r.Name == fleet.OrchestratorName {
			continue
		}
		for _, k := range tk.links[r.SessionID] {
			if _, ok := streams[k]; ok {
				links[r.SessionID] = append(links[r.SessionID], k)
				linked[k] = true
			}
		}
	}
	for k, e := range streams {
		if !active(e.issue.Status) && !linked[k] {
			delete(streams, k)
		}
	}
	wsMatch := map[string]bool{}
	for k, e := range streams {
		ref := issueRef(tk.ids[e.project], e.issue)
		wsMatch[k] = matchFields(words, ref, "#"+strconv.Itoa(e.issue.ID), e.issue.Title, project(e.project))
	}

	var away []fleet.Row
	byProject := map[string][]fleet.Row{}
	under := map[string][]entry{} // what sits under each workstream
	termsOf := map[string][]terminal{}
	placeholder := map[string]string{} // project name to root
	used := map[string]bool{}          // projects with an entry
	for _, e := range streams {
		used[project(e.project)] = true
	}
	for _, r := range rows {
		if r.Name == fleet.OrchestratorName {
			continue
		}
		p := project(r.Cwd)
		app, isAway := host(r)
		hit := matches(r, p, app, words)
		if ks := links[r.SessionID]; len(ks) > 0 {
			for j, k := range ks {
				if !hit && !wsMatch[k] {
					continue
				}
				ws := streams[k]
				e := entry{kind: sessionEntry, row: r, project: ws.project, issue: ws.issue, task: k}
				if j != len(ks)-1 {
					e.alias = taskPrefix + k
				}
				under[k] = append(under[k], e)
			}
			continue
		}
		used[p] = true
		switch {
		case !hit:
		case isAway:
			away = append(away, r)
		default:
			byProject[p] = append(byProject[p], r)
		}
	}
	for _, t := range terms {
		p := project(t.Project)
		used[p] = true
		hit := matchFields(words, p, t.Title, short(t.Path))
		if ws, ok := streams[t.Task]; ok && project(ws.project) == p {
			if hit || wsMatch[t.Task] {
				under[t.Task] = append(under[t.Task], entry{kind: terminalEntry, term: t, project: ws.project, issue: ws.issue, task: t.Task})
			}
			continue
		}
		if hit {
			termsOf[p] = append(termsOf[p], t)
		}
	}
	wsOf := map[string][]entry{} // the shown workstreams, by project name
	for k, e := range streams {
		if wsMatch[k] || len(under[k]) > 0 {
			wsOf[project(e.project)] = append(wsOf[project(e.project)], e)
		}
	}
	for _, root := range pinned {
		p := project(root)
		if root == "" || used[p] || !matchFields(words, p) {
			continue
		}
		// Two pinned roots with one name are one project: keep the least.
		if old, ok := placeholder[p]; !ok || root < old {
			placeholder[p] = root
		}
	}

	names := map[string]bool{}
	for p := range byProject {
		names[p] = true
	}
	for p := range termsOf {
		names[p] = true
	}
	for p := range wsOf {
		names[p] = true
	}
	for p := range placeholder {
		names[p] = true // a placeholder's project has nothing else
	}
	order := make([]string, 0, len(names))
	for p := range names {
		order = append(order, p)
	}
	sort.Slice(order, func(i, j int) bool { return nameLess(order[i], order[j]) })

	var out []section
	for _, p := range order {
		if root, ok := placeholder[p]; ok {
			out = append(out, section{Title: p, Tracked: tracked[p], Entries: []entry{{kind: projectEntry, project: root}}})
			continue
		}
		var es []entry
		ws := wsOf[p]
		sort.Slice(ws, func(i, j int) bool { return workstreamLess(ws[i], ws[j]) })
		for _, w := range ws {
			es = append(es, w)
			var sess []entry
			var wterms []terminal
			for _, c := range under[w.task] {
				if c.kind == terminalEntry {
					wterms = append(wterms, c.term)
				} else {
					sess = append(sess, c)
				}
			}
			sort.Slice(sess, func(i, j int) bool { return activeLess(sess[i].row, sess[j].row) })
			es = append(es, sess...)
			sortWindows(wterms)
			for _, t := range wterms {
				es = append(es, entry{kind: terminalEntry, term: t, project: w.project, issue: w.issue, task: w.task})
			}
		}
		rs := byProject[p]
		sortActive(rs)
		es = append(es, sessionEntries(rs)...)
		ts := termsOf[p]
		sortWindows(ts)
		for _, t := range ts {
			es = append(es, entry{kind: terminalEntry, term: t})
		}
		out = append(out, section{Title: p, Tracked: tracked[p], Entries: es})
	}
	if len(away) > 0 {
		sortActive(away)
		out = append(out, section{Title: awayTitle, Away: true, Entries: sessionEntries(away)})
	}
	out = appendAgents(out, links, streams)
	return out
}

// appendAgents adds the agents list: an alias of every session shown in
// secs, once, those that need you first. A linked session's alias has the
// project and issue of its primary workstream.
func appendAgents(secs []section, links map[string][]string, streams map[string]entry) []section {
	seen := map[string]bool{}
	var rs []fleet.Row
	for _, s := range secs {
		for _, e := range s.Entries {
			if r, ok := e.session(); ok && !seen[r.SessionID] {
				seen[r.SessionID] = true
				rs = append(rs, r)
			}
		}
	}
	if len(rs) == 0 {
		return secs
	}
	needsFirst(rs)
	es := make([]entry, len(rs))
	for i, r := range rs {
		es[i] = entry{kind: sessionEntry, row: r, alias: agentsPrefix}
		if ks := links[r.SessionID]; len(ks) > 0 {
			ws := streams[ks[len(ks)-1]]
			es[i].project, es[i].issue = ws.project, ws.issue
		}
	}
	return append(secs, section{Title: agentsTitle, Agents: true, Entries: es})
}

func sortWindows(ts []terminal) {
	sort.Slice(ts, func(i, j int) bool { return windowLess(ts[i].Window, ts[j].Window) })
}

// windowLess orders tmux window ids ("@7") by number, then as text.
func windowLess(a, b string) bool {
	na, errA := strconv.Atoi(strings.TrimPrefix(a, "@"))
	nb, errB := strconv.Atoi(strings.TrimPrefix(b, "@"))
	if errA == nil && errB == nil && na != nb {
		return na < nb
	}
	if (errA == nil) != (errB == nil) {
		return errA == nil
	}
	return a < b
}

// sortActive puts working sessions first, then the newest activity, then
// orders by session id.
func sortActive(rs []fleet.Row) {
	sort.Slice(rs, func(i, j int) bool { return activeLess(rs[i], rs[j]) })
}

func activeLess(a, b fleet.Row) bool {
	wa, wb := a.State == fleet.Working, b.State == fleet.Working
	if wa != wb {
		return wa
	}
	if !a.Since.Equal(b.Since) {
		return a.Since.After(b.Since)
	}
	return a.SessionID < b.SessionID
}

// nameLess orders project names alphabetically, ignoring case, with the
// exact spelling as the tiebreak so the order is total.
func nameLess(a, b string) bool {
	la, lb := strings.ToLower(a), strings.ToLower(b)
	if la != lb {
		return la < lb
	}
	return a < b
}

// matches reports whether every term is found in at least one of a
// session's fields. app is the host app's name, "" for a session in a
// terminal.
func matches(r fleet.Row, project, app string, terms []string) bool {
	return matchFields(terms, project, r.Title, r.LastPrompt, r.Name, app)
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

// terminal is a window of the view's session that runs no Claude session:
// shells, a dev server, a one-off command.
type terminal struct {
	Window  string // "@7"
	Pane    string // its first pane, which the list goes beside
	Project string // the project's root
	Title   string // "node", or the shell's name when only shells run
	Path    string // the first pane's directory
	Task    string // the workstream it was opened for, its window's TaskOption
}

// shells are the commands a terminal's title skips over, since a shell is
// what every terminal runs.
var shells = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ksh": true, "mksh": true,
	"tcsh": true, "csh": true, "nu": true, "xonsh": true, "elvish": true, "pwsh": true,
}

// terminals finds the terminals among the panes, in window order. A window
// of the view's session is one unless it holds the holder, an attach, a pane
// on the tty of a listed Claude session (claude), or the pane c just opened
// and the list has yet to adopt. The list's own pane is left out, so a
// window with only the list is no terminal. A terminal's project is its
// window's ProjectOption, or else the root of its first pane's directory.
func terminals(panes []tmux.Pane, session, self, opened string, claude map[string]bool, root func(string) string) []terminal {
	byWindow := map[string][]tmux.Pane{}
	skip := map[string]bool{}
	var order []string
	for _, p := range panes {
		if p.Session != session || p.ID == self || p.Window == "" {
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
	var out []terminal
	for _, w := range order {
		if skip[w] {
			continue
		}
		ps := byWindow[w]
		first := ps[0]
		project := first.Project
		if project == "" {
			project = root(first.Path)
		}
		out = append(out, terminal{Window: w, Pane: first.ID, Project: project, Title: termTitle(ps), Path: first.Path, Task: first.Task})
	}
	sort.Slice(out, func(i, j int) bool { return windowLess(out[i].Window, out[j].Window) })
	return out
}

// termTitle names a terminal by what runs in it: the distinct commands of
// its panes that are not shells, or the first pane's shell when only shells
// run.
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

// root is the root of the project dir belongs to, "" for no dir: as the
// latest refresh or pane listing found it, or dir itself, cleaned, when
// neither carried it.
func (m model) root(dir string) string {
	if dir == "" {
		return ""
	}
	if r, ok := m.rowRoots[dir]; ok {
		return r
	}
	if r, ok := m.paneRoots[dir]; ok {
		return r
	}
	return filepath.Clean(dir)
}

// name is the display name of the project dir belongs to.
func (m model) name(dir string) string {
	root := m.root(dir)
	if m.user != "" && root == m.user {
		return "~"
	}
	return m.short(root)
}

// short is a directory as the view shows it.
func (m model) short(dir string) string { return fleet.ShortDir(dir, m.user) }

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

// counts tallies the list, filter ignored, for the header: the sessions
// that need you and the workstreams in review, the working sessions, and
// the idle ones. Each session counts once, and terminals and placeholders
// not at all.
func counts(secs []section) (need, working, idle int) {
	for _, s := range secs {
		for _, e := range s.Entries {
			r, ok := e.session()
			switch {
			case e.kind == workstreamEntry && e.issue.Status == issues.Review:
				need++
			case !ok || e.alias != "":
			case r.NeedsYou():
				need++
			case r.State == fleet.Working:
				working++
			default:
				idle++
			}
		}
	}
	return
}

func (m model) counts() (need, working, idle int) {
	return counts(buildList(m.rows, m.terminals(), nil, m.tasks(), "", m.name, m.short, m.hostOf))
}
