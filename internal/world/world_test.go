package world

import (
	"slices"
	"strings"
	"testing"
	"time"

	"overview/internal/fleet"
	"overview/internal/issues"
	"overview/internal/tmux"
)

var t0 = time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)

// sess is an idle interactive session in cwd, linked by refs.
func sess(id, cwd string, refs ...fleet.TaskRef) fleet.Row {
	return fleet.Row{Live: fleet.Live{SessionID: id, Cwd: cwd, Name: id}, State: fleet.Idle, Since: t0, Tasks: refs}
}

func ref(project string, id int) fleet.TaskRef { return fleet.TaskRef{Project: project, ID: id} }

func issue(id int, status string) issues.Issue {
	return issues.Issue{ID: id, Status: status, Title: "t", Priority: "p2"}
}

func tracker(id string, open ...issues.Issue) Tracker { return Tracker{ID: id, Open: open} }

// sessionIDs are the ids of ss, in order.
func sessionIDs(ss []*Session) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.SessionID)
	}
	return out
}

func streamKeys(ss []*Stream) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.String())
	}
	return out
}

func windows(ts []*Terminal) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Window)
	}
	return out
}

func names(ps []*Project) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}

func eq[T comparable](t *testing.T, what string, got, want []T) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s: %v, want %v", what, got, want)
	}
}

// Two sessions in a tracked project: s1 works on #6, and #4 is in review
// with nobody on it. s2 is linked to nothing.
func TestAProjectListsItsWorkstreamsThenItsSessions(t *testing.T) {
	s1, s2 := sess("s1", "/code/app", fleet.TaskRef{Project: "app", Dir: "/code/app", ID: 6}), sess("s2", "/code/app")
	w := Derive(Inputs{
		Rows: []fleet.Row{s1, s2},
		Trackers: map[string]Tracker{"/code/app": tracker("app",
			issues.Issue{ID: 6, Status: issues.InProgress, Priority: "p1"},
			issues.Issue{ID: 4, Status: issues.Review, Priority: "p2"},
			issues.Issue{ID: 2, Status: issues.Ready},
		)},
	})
	eq(t, "projects", names(w.Projects), []string{"/code/app"})
	p := w.Projects[0]
	eq(t, "streams", streamKeys(p.Streams), []string{"/code/app#4", "/code/app#6"})
	eq(t, "under #6", sessionIDs(p.Streams[1].Sessions), []string{"s1"})
	eq(t, "under #4", sessionIDs(p.Streams[0].Sessions), nil)
	eq(t, "project's sessions", sessionIDs(p.Sessions), []string{"s2"})
	if !p.Tracked || p.ID != "app" || len(p.Open) != 3 {
		t.Errorf("project %+v", p)
	}
	if w.Need != 1 || w.Idle != 2 || w.Working != 0 {
		t.Errorf("counts %d %d %d, want the review and two idle", w.Need, w.Working, w.Idle)
	}
	if got := w.Sessions["s1"].Primary(); got != p.Streams[1] {
		t.Errorf("s1's primary is %v", got)
	}
	if w.Sessions["s2"].Primary() != nil {
		t.Errorf("s2 has a primary")
	}
}

// The cases resolveTask covered, each as one session with one ref.
func TestLinksResolveAsResolveTaskDid(t *testing.T) {
	ts := map[string]Tracker{
		"/w/app":    tracker("app", issue(6, issues.InProgress), issue(7, issues.Done)),
		"/w/app-wt": tracker("app", issue(6, issues.InProgress)),
		"/w/lib":    tracker("lib", issue(1, issues.Ready)),
		"/x/other":  tracker("other", issue(2, issues.Ready)),
	}
	roots := map[string]string{"/w/app/sub": "/w/app", "/w/app-wt": "/w/app-wt"}
	// /w/app, /w/lib and /w/plain are known through these.
	others := []fleet.Row{sess("o1", "/w/app"), sess("o2", "/w/lib"), sess("o3", "/w/plain")}
	for _, c := range []struct {
		name string
		ref  fleet.TaskRef
		own  string
		want string
	}{
		{"by project id", fleet.TaskRef{Project: "app", Dir: "/w/lib", ID: 6}, "/w/lib", "/w/app#6"},
		{"by the directory's tracker", fleet.TaskRef{Dir: "/w/app/sub", ID: 6}, "/w/app", "/w/app#6"},
		{"a worktree with the same id", fleet.TaskRef{Dir: "/w/app-wt", ID: 6}, "/w/app", "/w/app#6"},
		{"a worktree named by id", fleet.TaskRef{Project: "app", Dir: "/w/app-wt", ID: 6}, "/w/plain", "/w/app#6"},
		{"a project not listed", fleet.TaskRef{Dir: "/x/other", ID: 2}, "/w/app", "/x/other#2"},
		{"a closed issue", fleet.TaskRef{Project: "app", ID: 7}, "/w/app", ""},
		{"an unknown issue", fleet.TaskRef{Project: "app", ID: 99}, "/w/app", ""},
		{"an unknown project id", fleet.TaskRef{Project: "nope", Dir: "/w/app", ID: 6}, "/w/app", ""},
		{"no tracker", fleet.TaskRef{Dir: "/w/plain", ID: 1}, "/w/plain", ""},
		{"a ready issue", fleet.TaskRef{Project: "lib", ID: 1}, "/w/app", "/w/lib#1"},
	} {
		rows := append(slices.Clone(others), sess("s", c.own, c.ref))
		w := Derive(Inputs{Rows: rows, Trackers: ts, Roots: roots})
		got := ""
		if p := w.Sessions["s"].Primary(); p != nil {
			got = p.String()
		}
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
		if c.want != "" && !slices.Contains(w.Streams[w.Sessions["s"].Primary().Key].Sessions, w.Sessions["s"]) {
			t.Errorf("%s: the stream does not list its session", c.name)
		}
	}
	// Without trackers nothing links.
	w := Derive(Inputs{Rows: []fleet.Row{sess("s", "/w/app", ref("app", 6))}})
	if len(w.Sessions["s"].Streams) != 0 || len(w.Streams) != 0 {
		t.Errorf("no trackers: %+v", w.Sessions["s"].Streams)
	}
}

func TestLinksKeepTheLatestLast(t *testing.T) {
	ts := map[string]Tracker{"/w/app": tracker("app", issue(1, issues.Ready), issue(2, issues.Ready), issue(3, issues.Done))}
	r := sess("s", "/w/app", ref("app", 1), ref("app", 2), fleet.TaskRef{Dir: "/w/app", ID: 1}, ref("app", 3))
	w := Derive(Inputs{Rows: []fleet.Row{r}, Trackers: ts})
	eq(t, "links", streamKeys(w.Sessions["s"].Streams), []string{"/w/app#2", "/w/app#1"})
	// Both are under the session's project, and the session in neither
	// its project's list nor Away.
	p := w.ProjectOf("/w/app")
	eq(t, "streams", streamKeys(p.Streams), []string{"/w/app#1", "/w/app#2"})
	if len(p.Sessions) != 0 || len(w.Away) != 0 {
		t.Errorf("a linked session is listed in its project: %v", sessionIDs(p.Sessions))
	}
}

// A session linked to another project's issue is under that project's
// workstream, and its own project lists nothing for it.
func TestALinkIntoAnotherProject(t *testing.T) {
	w := Derive(Inputs{
		Rows:     []fleet.Row{sess("s", "/w/lib", ref("app", 3))},
		Trackers: map[string]Tracker{"/w/app": tracker("app", issue(3, issues.Ready))},
		Pinned:   []string{"/w/app"},
	})
	// lib has nothing listed in it, so the list leaves it out, but it is
	// still s's project and s is its member.
	eq(t, "projects", names(w.Projects), []string{"/w/app"})
	app, lib := w.Projects[0], w.ProjectOf("/w/lib")
	eq(t, "app's streams", streamKeys(app.Streams), []string{"/w/app#3"})
	if lib == nil || w.Sessions["s"].Project != lib || !lib.Empty() || app.Empty() {
		t.Fatalf("s in %v; lib %+v", w.Sessions["s"].Project.Name, lib)
	}
	eq(t, "lib's members", sessionIDs(lib.Members), []string{"s"})
	eq(t, "app's members", sessionIDs(app.Members), nil)
}

// The orchestrator is in no list, its links make no workstream and it
// counts for nothing. Its pane is still no terminal.
func TestTheOrchestratorIsNotInTheWorld(t *testing.T) {
	o := sess("o", "/w/app", ref("app", 7))
	o.Name, o.PID, o.Hooked, o.State = fleet.OrchestratorName, 9, true, fleet.Working
	o.Tasks = append(o.Tasks, fleet.TaskRef{Dir: "/w/elsewhere", ID: 1})
	w := Derive(Inputs{
		Rows:     []fleet.Row{o, sess("a", "/w/app", ref("app", 6))},
		Trackers: map[string]Tracker{"/w/app": tracker("app", issue(6, issues.Ready), issue(7, issues.Ready))},
		TTYs:     map[int]string{9: "ttys009"},
		Panes:    []tmux.Pane{{ID: "%9", TTY: "ttys009", Session: "ov", Window: "@9", Command: "claude"}},
		Session:  "ov",
	})
	if _, ok := w.Sessions["o"]; ok || len(w.Agents) != 1 {
		t.Fatalf("the orchestrator is listed: %v", sessionIDs(w.Agents))
	}
	if _, ok := w.Streams[Key{"/w/app", 7}]; ok {
		t.Errorf("the orchestrator's link made a workstream")
	}
	if w.Working != 0 || w.Idle != 1 || w.Hooked {
		t.Errorf("counts %d %d, hooked %v", w.Working, w.Idle, w.Hooked)
	}
	if len(w.Terminals) != 0 {
		t.Errorf("the orchestrator's pane is a terminal: %v", windows(w.Terminals))
	}
	eq(t, "trackers", w.Trackers, []string{"/w/app"})
}

// How many sessions work on each issue, as the task picker counts them.
func TestAStreamListsEachSessionOnce(t *testing.T) {
	o := sess("o", "/w/app", ref("app", 7))
	o.Name = fleet.OrchestratorName
	w := Derive(Inputs{
		Rows: []fleet.Row{
			sess("a", "/w/app", ref("app", 6), ref("app", 7), fleet.TaskRef{Dir: "/w/app-wt", ID: 6}, ref("app", 6)),
			sess("b", "/w/app", ref("app", 6)), o,
		},
		Trackers: map[string]Tracker{
			"/w/app":    tracker("app", issue(6, issues.Ready), issue(7, issues.Ready)),
			"/w/app-wt": tracker("app", issue(6, issues.Ready)),
		},
	})
	for k, want := range map[Key][]string{{"/w/app", 6}: {"a", "b"}, {"/w/app", 7}: {"a"}} {
		eq(t, k.String(), sessionIDs(w.Streams[k].Sessions), want)
	}
	// a's worktree ref names the id app, which its own project has.
	eq(t, "a's links", streamKeys(w.Sessions["a"].Streams), []string{"/w/app#7", "/w/app#6"})
}

func TestStartedIsTheEarliestLink(t *testing.T) {
	k := Key{"/w", 1}
	ts := map[string]Tracker{"/w": tracker("w", issue(1, issues.Ready), issue(2, issues.Ready))}
	a := sess("a", "/w", fleet.TaskRef{Project: "w", ID: 1, Linked: time.UnixMilli(200)})
	a.StartedAt = 500
	b := sess("b", "/w", ref("w", 1), fleet.TaskRef{Project: "w", ID: 2, Linked: time.UnixMilli(100)})
	b.StartedAt = 300
	c := sess("c", "/w", ref("w", 1)) // no time at all
	for _, x := range []struct {
		name string
		rows []fleet.Row
		want time.Time
	}{
		{"a's link", []fleet.Row{a, b, c}, time.UnixMilli(200)},
		{"b's start, its link to #1 having no time", []fleet.Row{b, c}, time.UnixMilli(300)},
		{"nothing says", []fleet.Row{c}, time.Time{}},
	} {
		w := Derive(Inputs{Rows: x.rows, Trackers: ts})
		if got := w.Streams[k].Started; !got.Equal(x.want) {
			t.Errorf("%s: %v, want %v", x.name, got, x.want)
		}
	}
	// Two refs to one issue: the earlier time counts.
	d := sess("d", "/w", fleet.TaskRef{Project: "w", ID: 1, Linked: time.UnixMilli(900)}, fleet.TaskRef{Dir: "/w", ID: 1, Linked: time.UnixMilli(700)})
	if got := Derive(Inputs{Rows: []fleet.Row{d}, Trackers: ts}).Streams[k].Started; !got.Equal(time.UnixMilli(700)) {
		t.Errorf("two refs: %v", got)
	}
	// An active issue nobody works on has no start.
	ts["/w"] = tracker("w", issue(1, issues.InProgress))
	if got := Derive(Inputs{Rows: []fleet.Row{sess("e", "/w")}, Trackers: ts}).Streams[k]; got == nil || !got.Started.IsZero() {
		t.Errorf("no sessions: %+v", got)
	}
}

// Only open issues that are in progress, in review or blocked are
// workstreams by themselves; any other open one only with a session.
func TestWhichIssuesAreWorkstreams(t *testing.T) {
	ts := map[string]Tracker{"/w/app": tracker("app",
		issue(1, issues.InProgress), issue(2, issues.Review), issue(3, issues.Blocked),
		issue(4, issues.Ready), issue(5, issues.Inbox), issue(6, issues.Done), issue(7, issues.Ready))}
	w := Derive(Inputs{Rows: []fleet.Row{sess("s", "/w/app", ref("app", 7), ref("app", 6))}, Trackers: ts})
	eq(t, "streams", streamKeys(w.Projects[0].Streams), []string{"/w/app#2", "/w/app#1", "/w/app#3", "/w/app#7"})
	if len(w.Projects[0].Open) != 6 {
		t.Errorf("open %v", w.Projects[0].Open)
	}
	// A tracker for a root the world has nothing in is not read in.
	w = Derive(Inputs{Rows: []fleet.Row{sess("s", "/w/lib")}, Trackers: ts})
	if len(w.Streams) != 0 || len(w.Projects) != 1 {
		t.Errorf("an unknown tracker made %v", streamKeys(w.Projects[0].Streams))
	}
}

func TestStreamOrder(t *testing.T) {
	ts := map[string]Tracker{"/w/app": tracker("app",
		issues.Issue{ID: 9, Status: issues.InProgress, Priority: "p1"},
		issues.Issue{ID: 3, Status: issues.InProgress, Priority: "p2"},
		issues.Issue{ID: 1, Status: issues.InProgress, Priority: "p1"},
		issues.Issue{ID: 8, Status: issues.Review, Priority: "p3"},
		issues.Issue{ID: 2, Status: issues.Blocked, Priority: "p0"},
	)}
	w := Derive(Inputs{Rows: []fleet.Row{sess("s", "/w/app")}, Trackers: ts})
	eq(t, "streams", streamKeys(w.Projects[0].Streams), []string{"/w/app#8", "/w/app#1", "/w/app#9", "/w/app#3", "/w/app#2"})
}

// Sessions are working first, then newest; the agents list puts those
// that need you first, longest wait first.
func TestSessionOrder(t *testing.T) {
	at := func(id string, st fleet.State, min int) fleet.Row {
		r := sess(id, "/w/a")
		r.State, r.Since = st, t0.Add(time.Duration(min)*time.Minute)
		return r
	}
	asking := at("ask", fleet.Idle, 1)
	asking.LastReply = "Which one?"
	w := Derive(Inputs{Rows: []fleet.Row{
		at("old", fleet.Idle, 1), at("new", fleet.Idle, 5), at("busy", fleet.Working, 0),
		at("b2", fleet.Blocked, 3), at("b1", fleet.Blocked, 2), asking,
	}})
	eq(t, "project", sessionIDs(w.Projects[0].Sessions), []string{"busy", "new", "b2", "b1", "ask", "old"})
	eq(t, "agents", sessionIDs(w.Agents), []string{"ask", "b1", "b2", "busy", "new", "old"})
	if w.Need != 3 || w.Working != 1 || w.Idle != 2 {
		t.Errorf("counts %d %d %d", w.Need, w.Working, w.Idle)
	}
}

// A session another app runs is in Away unless it is linked. A
// background session never is.
func TestAwaySessions(t *testing.T) {
	away := sess("away", "/w/app")
	away.PID = 3
	linked := sess("linked", "/w/app", ref("app", 1))
	linked.PID = 4
	bg := sess("bg", "/w/app")
	bg.PID, bg.Kind = 5, "background"
	w := Derive(Inputs{
		Rows:     []fleet.Row{away, linked, bg},
		Hosts:    map[int]Host{3: {Name: "Notes"}, 4: {}, 5: {Name: "Nope"}},
		Trackers: map[string]Tracker{"/w/app": tracker("app", issue(1, issues.Ready))},
	})
	eq(t, "away", sessionIDs(w.Away), []string{"away"})
	eq(t, "members", sessionIDs(w.Projects[0].Members), []string{"away", "bg", "linked"})
	// A project with only an away session is not listed.
	if w := Derive(Inputs{Rows: []fleet.Row{away}, Hosts: map[int]Host{3: {}}}); len(w.Projects) != 0 || w.ProjectOf("/w/app") == nil {
		t.Errorf("an away session's project: listed %v", names(w.Projects))
	}
	eq(t, "project", sessionIDs(w.Projects[0].Sessions), []string{"bg"})
	eq(t, "#1", sessionIDs(w.Streams[Key{"/w/app", 1}].Sessions), []string{"linked"})
	if h := w.Sessions["away"].Host; h == nil || h.Label() != "Notes" {
		t.Errorf("away's host %+v", h)
	}
	if h := w.Sessions["linked"].Host; h == nil || h.Label() != "no terminal" {
		t.Errorf("linked's host %+v", h)
	}
	if w.Sessions["bg"].Host != nil {
		t.Errorf("a background session has a host")
	}
}

// The panes of the view's session that are terminals, and the ones that
// are not.
func TestTerminals(t *testing.T) {
	cl := sess("cl", "/w/a")
	cl.PID = 7
	panes := []tmux.Pane{
		{ID: "%1", Session: "ov", Window: "@1", Command: "zsh", Path: "/w/a/web"},
		{ID: "%2", Session: "ov", Window: "@1", Command: "node", Path: "/w/a"},
		{ID: "%3", Session: "ov", Window: "@1", Command: "vite", Path: "/w/a"},
		{ID: "%4", Session: "ov", Window: "@10", Command: "-zsh", Path: "/w/b", Project: "/w/p"},
		{ID: "%5", Session: "ov", Window: "@2", Command: "zsh", Holder: true},
		{ID: "%6", Session: "ov", Window: "@3", Attach: "bg1"},
		{ID: "%7", Session: "ov", Window: "@4", TTY: "ttys007", Command: "claude"},
		{ID: "%8", Session: "ov", Window: "@5", Command: "zsh"}, // the one c opened
		{ID: "%9", Session: "ov", Window: "@6", Command: "overview"},
		{ID: "%10", Session: "other", Window: "@7", Command: "zsh"},
		{ID: "%11", Session: "ov", Window: "@6", Command: ""}, // beside the list
	}
	w := Derive(Inputs{
		Rows: []fleet.Row{cl}, TTYs: map[int]string{7: "ttys007"}, Panes: panes,
		Session: "ov", Self: "%9", Opening: "%8", Roots: map[string]string{"/w/a/web": "/w/a"},
	})
	eq(t, "terminals", windows(w.Terminals), []string{"@1", "@6", "@10"})
	t1, t6, t10 := w.Terminals[0], w.Terminals[1], w.Terminals[2]
	if t1.Pane != "%1" || t1.Title != "node, vite" || t1.Path != "/w/a/web" || t1.Root != "/w/a" || t1.Project.Name != "/w/a" {
		t.Errorf("@1: %+v", t1)
	}
	if t6.Pane != "%11" || t6.Title != "shell" || t6.Root != "" {
		t.Errorf("@6: %+v", t6)
	}
	if t10.Title != "zsh" || t10.Root != "/w/p" || t10.Project.Name != "/w/p" {
		t.Errorf("@10: %+v", t10)
	}
	eq(t, "/w/a's terminals", windows(w.ProjectOf("/w/a").Terminals), []string{"@1"})
	if w.Sessions["cl"].Pane != "%7" {
		t.Errorf("cl's pane %q", w.Sessions["cl"].Pane)
	}
}

// A terminal is under the workstream its window was opened for when that
// is one of its project's; else it is in its project.
func TestTerminalsUnderWorkstreams(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%1", Session: "ov", Window: "@1", Path: "/w/app", Task: "/w/app#6"},
		{ID: "%2", Session: "ov", Window: "@2", Path: "/w/lib", Task: "/w/app#6"},  // another project's
		{ID: "%3", Session: "ov", Window: "@3", Path: "/w/app", Task: "/w/app#9"},  // no workstream
		{ID: "%4", Session: "ov", Window: "@4", Path: "/w/app", Task: "/w/app#6x"}, // not a key
		{ID: "%5", Session: "ov", Window: "@5", Path: "/w/app/sub", Task: "/w/app#6"},
	}
	w := Derive(Inputs{
		Panes: panes, Session: "ov", Roots: map[string]string{"/w/app/sub": "/w/app"},
		Trackers: map[string]Tracker{"/w/app": tracker("app", issue(6, issues.InProgress), issue(9, issues.Done))},
	})
	st := w.Streams[Key{"/w/app", 6}]
	eq(t, "under #6", windows(st.Terminals), []string{"@1", "@5"})
	eq(t, "app's", windows(w.ProjectOf("/w/app").Terminals), []string{"@3", "@4"})
	eq(t, "lib's", windows(w.ProjectOf("/w/lib").Terminals), []string{"@2"})
	if w.Terminals[0].Stream != st || w.Terminals[1].Stream != nil {
		t.Errorf("streams %v %v", w.Terminals[0].Stream, w.Terminals[1].Stream)
	}
	// A project's every terminal: its own and its workstreams'.
	eq(t, "all of app's", windows(w.ProjectOf("/w/app").AllTerminals), []string{"@1", "@3", "@4", "@5"})
	eq(t, "all of lib's", windows(w.ProjectOf("/w/lib").AllTerminals), []string{"@2"})
	if s, term := st.Lead(); s != nil || term != w.Terminals[0] {
		t.Errorf("lead %v %v", s, term)
	}
}

func TestLead(t *testing.T) {
	a, b := sess("a", "/w", ref("w", 1)), sess("b", "/w", ref("w", 1))
	a.State, b.PID = fleet.Working, 2
	in := Inputs{
		Rows:     []fleet.Row{a, b},
		Trackers: map[string]Tracker{"/w": tracker("w", issue(1, issues.Ready))},
		TTYs:     map[int]string{2: "ttys002"},
		Session:  "ov",
	}
	if s, _ := Derive(in).Streams[Key{"/w", 1}].Lead(); s == nil || s.SessionID != "a" {
		t.Errorf("with no panes, the first session: %v", s)
	}
	in.Panes = []tmux.Pane{{ID: "%2", TTY: "ttys002", Session: "ov", Window: "@2"}}
	if s, _ := Derive(in).Streams[Key{"/w", 1}].Lead(); s == nil || s.SessionID != "b" {
		t.Errorf("the first with a pane: %v", s)
	}
}

func TestPinnedProjects(t *testing.T) {
	w := Derive(Inputs{
		Rows:   []fleet.Row{sess("s", "/w/alpha")},
		Pinned: []string{"/w/alpha", "/w/delta", "/p/delta/", ""},
		Trackers: map[string]Tracker{
			"/w/delta": {ID: "delta", Open: []issues.Issue{issue(2, issues.Ready)}, Failed: true},
		},
		User: "/p",
	})
	eq(t, "projects", names(w.Projects), []string{"/w/alpha", "/w/delta", "~/delta"})
	alpha, delta, home := w.Projects[0], w.Projects[1], w.Projects[2]
	if !alpha.Pinned || alpha.Empty() {
		t.Errorf("alpha %+v", alpha)
	}
	if !delta.Pinned || !delta.Empty() || !delta.Tracked || !delta.Failed || delta.ID != "delta" || len(delta.Open) != 1 {
		t.Errorf("delta %+v", delta)
	}
	if !home.Pinned || home.Root != "/p/delta" || home.ID != "delta" || home.Tracked {
		t.Errorf("~/delta %+v", home)
	}
	// Pins keeps a pinned directory as it was given, which is how it is
	// unpinned.
	eq(t, "~/delta's pins", home.Pins, []string{"/p/delta/"})
	eq(t, "alpha's pins", alpha.Pins, []string{"/w/alpha"})
	if tr, ok := w.Tracker("/w/delta"); !ok || tr.ID != "delta" || !tr.Failed {
		t.Errorf("delta's tracker %+v %v", tr, ok)
	}
	if _, ok := w.Tracker("/w/alpha"); ok {
		t.Errorf("alpha has no tracker, yet the world holds one")
	}
	if p := w.Sessions["s"].Project; len(p.Pins) != 1 {
		t.Errorf("alpha's session's project %+v", p)
	}
}

// Every root with one name is one project, under the least root.
func TestRootsWithOneNameAreOneProject(t *testing.T) {
	w := Derive(Inputs{
		Rows:   []fleet.Row{sess("a", "/w/b/x"), sess("b", "/w/a/x")},
		Roots:  map[string]string{"/w/b/x": "/w/b", "/w/a/x": "/w/a"},
		Pinned: []string{"/w/c"},
		User:   "/w/a",
	})
	// /w/a is the user's home, and /w/a/../b and /w/c are named in full.
	eq(t, "projects", names(w.Projects), []string{"/w/b", "/w/c", "~"})
	// A relative directory is its own root, and is named as it is, as
	// the project under ~/code/projects of the same name is.
	w = Derive(Inputs{
		Rows:     []fleet.Row{sess("a", "/u/code/projects/one"), sess("b", "one")},
		Trackers: map[string]Tracker{"one": tracker("one", issue(1, issues.InProgress))},
		User:     "/u",
	})
	eq(t, "projects", names(w.Projects), []string{"one"})
	p := w.Projects[0]
	eq(t, "roots", p.Roots, []string{"/u/code/projects/one", "one"})
	eq(t, "sessions", sessionIDs(p.Sessions), []string{"a", "b"})
	eq(t, "streams", streamKeys(p.Streams), []string{"one#1"})
	if p.Root != "/u/code/projects/one" || !p.Tracked || p.ID != "one" || w.Sessions["b"].Project != p {
		t.Errorf("project %+v", p)
	}
}

func TestProjectNames(t *testing.T) {
	w := Derive(Inputs{
		Rows:  []fleet.Row{sess("h", "/u"), sess("p", "/u/code/projects/app/sub"), sess("d", "/u/notes/"), sess("x", "/tmp/x"), sess("e", "")},
		Roots: map[string]string{"/u/code/projects/app/sub": "/u/code/projects/app"},
		User:  "/u",
	})
	eq(t, "projects", names(w.Projects), []string{"", "/tmp/x", "app", "~", "~/notes"})
	if w.Name("/u/code/projects/app/sub") != "app" || w.Root("/u/notes/") != "/u/notes" || w.Root("") != "" {
		t.Errorf("lookups %q %q", w.Name("/u/code/projects/app/sub"), w.Root("/u/notes/"))
	}
	if w.Short("/u/notes/x") != "~/notes/x" || w.Short("/tmp/x") != "/tmp/x" {
		t.Errorf("Short %q %q", w.Short("/u/notes/x"), w.Short("/tmp/x"))
	}
	if w.ProjectOf("/nowhere") != nil || w.ProjectOf("/u/notes") != w.Sessions["d"].Project {
		t.Errorf("ProjectOf")
	}
}

// The pane a session shows in: an interactive one's tty, never the
// list's own pane nor another tmux session's; a background one's attach
// in the view's session.
func TestSessionPanes(t *testing.T) {
	in, bg, gone := sess("in", "/w"), sess("bg", "/w"), sess("gone", "/w")
	in.PID, gone.PID = 1, 2
	bg.Kind, bg.ID = "background", "bg1"
	w := Derive(Inputs{
		Rows: []fleet.Row{in, bg, gone},
		TTYs: map[int]string{1: "ttys001", 2: "ttys002"},
		Panes: []tmux.Pane{
			{ID: "%0", TTY: "ttys001", Session: "ov", Window: "@0"}, // the list
			{ID: "%1", TTY: "ttys001", Session: "ov", Window: "@1"},
			{ID: "%2", TTY: "ttys002", Session: "other", Window: "@2"},
			{ID: "%3", Session: "other", Window: "@3", Attach: "bg1"},
			{ID: "%4", Session: "ov", Window: "@4", Attach: "bg1"},
		},
		Session: "ov", Self: "%0",
	})
	for id, want := range map[string]string{"in": "%1", "bg": "%4", "gone": ""} {
		if got := w.Sessions[id].Pane; got != want {
			t.Errorf("%s: pane %q, want %q", id, got, want)
		}
	}
	for id, want := range map[string]string{"in": "ttys001", "bg": "", "gone": "ttys002"} {
		if got := w.Sessions[id].TTY; got != want {
			t.Errorf("%s: tty %q, want %q", id, got, want)
		}
	}
	if w.Sessions["in"].Elsewhere || !w.Sessions["gone"].Elsewhere || w.Sessions["bg"].Elsewhere {
		t.Errorf("elsewhere %v %v %v", w.Sessions["in"].Elsewhere, w.Sessions["gone"].Elsewhere, w.Sessions["bg"].Elsewhere)
	}
}

func TestCountsSkipTerminalsAndPlaceholders(t *testing.T) {
	r := sess("s", "/w/a")
	r.State = fleet.Working
	w := Derive(Inputs{Rows: []fleet.Row{r}, Panes: []tmux.Pane{{ID: "%1", Session: "ov", Window: "@1", Project: "/w/a"}}, Session: "ov", Pinned: []string{"/w/b"}})
	if w.Need != 0 || w.Working != 1 || w.Idle != 0 {
		t.Fatalf("counts %d %d %d", w.Need, w.Working, w.Idle)
	}
}

func TestHooked(t *testing.T) {
	a, b := sess("a", "/w"), sess("b", "/w")
	if Derive(Inputs{Rows: []fleet.Row{a, b}}).Hooked {
		t.Errorf("hooked with no hooked session")
	}
	b.Hooked = true
	if !Derive(Inputs{Rows: []fleet.Row{a, b}}).Hooked {
		t.Errorf("not hooked")
	}
}

// The trackers a refresh reads: the projects' roots, pinned and
// terminals' too, and the roots of the directories links name.
func TestTrackerRoots(t *testing.T) {
	w := Derive(Inputs{
		Rows:    []fleet.Row{sess("s", "/w/a/sub", fleet.TaskRef{Dir: "/w/wt/x", ID: 1}, ref("b", 2))},
		Roots:   map[string]string{"/w/a/sub": "/w/a", "/w/wt/x": "/w/wt"},
		Panes:   []tmux.Pane{{ID: "%1", Session: "ov", Window: "@1", Path: "/w/t"}},
		Session: "ov",
		Pinned:  []string{"/w/p"},
	})
	eq(t, "trackers", w.Trackers, []string{"/w/a", "/w/p", "/w/t", "/w/wt"})
}

func TestParseKey(t *testing.T) {
	for s, want := range map[string]Key{"/w/app#6": {"/w/app", 6}, "/a#b#12": {"/a#b", 12}} {
		if got, ok := ParseKey(s); !ok || got != want || got.String() != s {
			t.Errorf("%q: %v %v", s, got, ok)
		}
	}
	for _, s := range []string{"", "#6", "/w/app", "/w/app#", "/w/app#x", "/w/app#06", "/w/app#+6"} {
		if k, ok := ParseKey(s); ok {
			t.Errorf("%q parsed as %v", s, k)
		}
	}
	if !strings.HasSuffix(Key{"/r", 3}.String(), "#3") {
		t.Errorf("String")
	}
}

func TestTermTitle(t *testing.T) {
	p := func(cmds ...string) []tmux.Pane {
		var out []tmux.Pane
		for _, c := range cmds {
			out = append(out, tmux.Pane{Command: c})
		}
		return out
	}
	for _, c := range []struct {
		panes []tmux.Pane
		want  string
	}{
		{p("zsh"), "zsh"},
		{p("-zsh", "bash"), "zsh"},
		{p("zsh", "node"), "node"},
		{p("node", "zsh", "vite", "node"), "node, vite"},
		{p(""), "shell"},
		{nil, "shell"},
	} {
		if got := termTitle(c.panes); got != c.want {
			t.Errorf("%v: %q, want %q", c.panes, got, c.want)
		}
	}
}
