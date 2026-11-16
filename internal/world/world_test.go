package world

import (
	"slices"
	"testing"
	"time"

	"overview/internal/fleet"
	"overview/internal/issues"
	"overview/internal/review"
	"overview/internal/tmux"
)

var t0 = time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)

// sess is an idle interactive session in cwd.
func sess(id, cwd string) fleet.Row {
	return fleet.Row{Live: fleet.Live{SessionID: id, Cwd: cwd, Name: id}, State: fleet.Idle, Since: t0}
}

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

// Two sessions in a tracked project, and #4 in review with no session on
// it. Both sessions are the project's, and #4 waits on you: it counts in
// the project's InReview and in Need, with no session joined to it.
func TestAProjectListsItsSessionsAndItsIssuesInReview(t *testing.T) {
	w := Derive(Inputs{
		Rows: []fleet.Row{sess("s1", "/code/app"), sess("s2", "/code/app")},
		Trackers: map[string]Tracker{"/code/app": tracker("app",
			issues.Issue{ID: 6, Status: issues.InProgress, Priority: "p1"},
			issues.Issue{ID: 4, Status: issues.Review, Priority: "p2"},
			issues.Issue{ID: 2, Status: issues.Ready},
		)},
	})
	eq(t, "projects", names(w.Projects), []string{"/code/app"})
	p := w.Projects[0]
	eq(t, "project's sessions", sessionIDs(p.Sessions), []string{"s1", "s2"})
	if !p.Tracked || p.ID != "app" || len(p.Open) != 3 || p.InReview != 1 {
		t.Errorf("project %+v", p)
	}
	if w.Need != 1 || w.Idle != 2 || w.Working != 0 {
		t.Errorf("counts %d %d %d, want the review and two idle", w.Need, w.Working, w.Idle)
	}
}

// Issues in review count in the project that tracks them, across all its
// roots, and only open ones in review do. A root the world holds nothing
// in does not count.
func TestInReviewCountsAProjectsReviewIssues(t *testing.T) {
	w := Derive(Inputs{
		Rows:   []fleet.Row{sess("a", "/u/code/projects/one"), sess("b", "one")},
		Pinned: []string{"/w/pinned"},
		Trackers: map[string]Tracker{
			"/u/code/projects/one": tracker("one", issue(1, issues.Review), issue(2, issues.Ready)),
			"one":                  tracker("one", issue(1, issues.Review), issue(3, issues.Review), issue(4, issues.Done)),
			"/w/pinned":            tracker("pinned", issue(1, issues.Review)),
			"/w/unknown":           tracker("unknown", issue(1, issues.Review)),
		},
		User: "/u",
	})
	eq(t, "projects", names(w.Projects), []string{"/w/pinned", "one"})
	if got := w.ProjectOf("one").InReview; got != 3 {
		t.Errorf("one: %d in review, want 3 across both roots", got)
	}
	if got := w.ProjectOf("/w/pinned").InReview; got != 1 {
		t.Errorf("pinned: %d in review, want 1", got)
	}
	// The unknown root's tracker is not in the world, so its issue is not
	// counted.
	if w.ProjectOf("/w/unknown") != nil || w.Need != 4 {
		t.Errorf("need %d, want 4", w.Need)
	}
}

// A project with issues in review and nothing running in it and no pin is
// not listed, yet its issues still count in Need.
func TestAProjectWithOnlyReviewIssuesIsNotListed(t *testing.T) {
	away := sess("away", "/w/app")
	away.PID = 3
	w := Derive(Inputs{
		Rows:     []fleet.Row{away},
		Hosts:    map[int]Host{3: {}},
		Trackers: map[string]Tracker{"/w/app": tracker("app", issue(1, issues.Review))},
	})
	p := w.ProjectOf("/w/app")
	if len(w.Projects) != 0 || p == nil || p.InReview != 1 || !p.Empty() || w.Need != 1 {
		t.Errorf("listed %v, project %+v, need %d", names(w.Projects), p, w.Need)
	}
}

// The orchestrator is in no list and counts for nothing. Its pane is still no terminal.
func TestTheOrchestratorIsNotInTheWorld(t *testing.T) {
	o := sess("o", "/w/app")
	o.Name, o.PID, o.Hooked, o.State = fleet.OrchestratorName, 9, true, fleet.Working
	w := Derive(Inputs{
		Rows:     []fleet.Row{o, sess("a", "/w/app")},
		Trackers: map[string]Tracker{"/w/app": tracker("app", issue(6, issues.Ready), issue(7, issues.Ready))},
		TTYs:     map[int]string{9: "ttys009"},
		Panes:    []tmux.Pane{{ID: "%9", TTY: "ttys009", Session: "ov", Window: "@9", Command: "claude"}},
		Session:  "ov",
	})
	if _, ok := w.Sessions["o"]; ok || len(w.Agents) != 1 {
		t.Fatalf("the orchestrator is listed: %v", sessionIDs(w.Agents))
	}
	// The orchestrator's hook does not stand for a's.
	if w.Working != 0 || w.Idle != 1 || !w.NoHook {
		t.Errorf("counts %d %d, no hook %v", w.Working, w.Idle, w.NoHook)
	}
	if len(w.Terminals) != 0 {
		t.Errorf("the orchestrator's pane is a terminal: %v", windows(w.Terminals))
	}
	eq(t, "trackers", w.Trackers, []string{"/w/app"})
}

// A project lists its sessions newest started first, whatever their
// state; the agents list puts those that need you first, longest wait
// first.
func TestSessionOrder(t *testing.T) {
	started := int64(0)
	at := func(id string, st fleet.State, min int) fleet.Row {
		r := sess(id, "/w/a")
		r.State, r.Since = st, t0.Add(time.Duration(min)*time.Minute)
		started += 100
		r.StartedAt = started
		return r
	}
	asking := at("ask", fleet.Idle, 1)
	asking.LastReply = "Which one?"
	w := Derive(Inputs{Rows: []fleet.Row{
		at("old", fleet.Idle, 1), at("new", fleet.Idle, 5), at("busy", fleet.Working, 0),
		at("b2", fleet.Blocked, 3), at("b1", fleet.Blocked, 2), asking,
	}})
	eq(t, "project", sessionIDs(w.Projects[0].Sessions), []string{"b1", "b2", "busy", "new", "old", "ask"})
	eq(t, "agents", sessionIDs(w.Agents), []string{"ask", "b1", "b2", "busy", "new", "old"})
	if w.Need != 3 || w.Working != 1 || w.Idle != 2 {
		t.Errorf("counts %d %d %d", w.Need, w.Working, w.Idle)
	}
}

// A session another app runs is in Away, whether or not it names a host. A
// background session never is.
func TestAwaySessions(t *testing.T) {
	away := sess("away", "/w/app")
	away.PID = 3
	linked := sess("linked", "/w/app")
	linked.PID = 4
	bg := sess("bg", "/w/app")
	bg.PID, bg.Kind = 5, "background"
	w := Derive(Inputs{
		Rows:  []fleet.Row{away, linked, bg},
		Hosts: map[int]Host{3: {Name: "Notes"}, 4: {}, 5: {Name: "Nope"}},
	})
	eq(t, "away", sessionIDs(w.Away), []string{"away", "linked"})
	eq(t, "members", sessionIDs(w.Projects[0].Members), []string{"away", "bg", "linked"})
	// A project with only an away session is not listed.
	if w := Derive(Inputs{Rows: []fleet.Row{away}, Hosts: map[int]Host{3: {}}}); len(w.Projects) != 0 || w.ProjectOf("/w/app") == nil {
		t.Errorf("an away session's project: listed %v", names(w.Projects))
	}
	eq(t, "project", sessionIDs(w.Projects[0].Sessions), []string{"bg"})
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
		{ID: "%11", Session: "ov", Window: "@6", Command: ""},                       // beside the list
		{ID: "%12", Session: "ov", Window: "@6", Command: "overview", Agents: true}, // under it
		{ID: "%13", Session: "ov", Window: "@8", Command: "overview", Agents: true}, // left behind
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

func TestPinnedProjects(t *testing.T) {
	w := Derive(Inputs{
		Rows:   []fleet.Row{sess("s", "/w/alpha")},
		Pinned: []string{"/w/alpha", "/w/delta", "/p/delta/", ""},
		Trackers: map[string]Tracker{
			"/w/delta": {ID: "delta", Open: []issues.Issue{issue(2, issues.Ready)}, Err: "exit status 1"},
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
	if tr, ok := w.Tracker("/w/delta"); !ok || tr.ID != "delta" || tr.Err != "exit status 1" {
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

// The hook is missing only when an interactive session is listed and no
// session reports through it. Background sessions report blocked through
// Claude Code, so they alone never need it.
func TestNoHook(t *testing.T) {
	a, b := sess("a", "/w"), sess("b", "/w")
	if !Derive(Inputs{Rows: []fleet.Row{a, b}}).NoHook {
		t.Errorf("no hook not reported with no hooked session")
	}
	b.Hooked = true
	if Derive(Inputs{Rows: []fleet.Row{a, b}}).NoHook {
		t.Errorf("no hook reported with a hooked session")
	}
	bg := sess("bg", "/w")
	bg.Kind, bg.ID = "background", "bg1"
	if Derive(Inputs{Rows: []fleet.Row{bg}}).NoHook {
		t.Errorf("no hook reported with only a background session")
	}
	if Derive(Inputs{}).NoHook {
		t.Errorf("no hook reported with no sessions")
	}
}

// The trackers a refresh reads: the projects' roots, pinned and
// terminals' too.
func TestTrackerRoots(t *testing.T) {
	w := Derive(Inputs{
		Rows:    []fleet.Row{sess("s", "/w/a/sub")},
		Roots:   map[string]string{"/w/a/sub": "/w/a"},
		Panes:   []tmux.Pane{{ID: "%1", Session: "ov", Window: "@1", Path: "/w/t"}},
		Session: "ov",
		Pinned:  []string{"/w/p"},
	})
	eq(t, "trackers", w.Trackers, []string{"/w/a", "/w/p", "/w/t"})
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

// A session carries its review, and the World the orchestrator's health,
// as the refresh read them; the orchestrator's review goes with it.
func TestReviewsAndHealthPassThrough(t *testing.T) {
	orch := sess("o", "/w/alpha")
	orch.Name = fleet.OrchestratorName
	health := fleet.Health{Running: true, Stale: true, LastCheck: time.Unix(100, 0)}
	w := Derive(Inputs{
		Rows: []fleet.Row{sess("s", "/w/alpha"), sess("t", "/w/alpha"), orch},
		Reviews: map[string]review.Summary{
			"s": {Open: 2, Waiting: 1},
			"o": {Open: 9},
		},
		Orchestrator: health,
	})
	if got := w.Sessions["s"].Review; got != (review.Summary{Open: 2, Waiting: 1}) {
		t.Errorf("s's review %+v", got)
	}
	if got := w.Sessions["t"].Review; got != (review.Summary{}) {
		t.Errorf("t has no review, yet %+v", got)
	}
	if _, ok := w.Sessions["o"]; ok {
		t.Error("the orchestrator is in the world")
	}
	if w.Orchestrator != health {
		t.Errorf("health %+v", w.Orchestrator)
	}
}

// Two roots that share a name are one project, with one ID, but IssuesID
// names each root by its own tracker's id, and an untracked root by its
// base name.
func TestIssuesIDIsOfTheRoot(t *testing.T) {
	w := Derive(Inputs{
		Pinned: []string{"/u/code/projects/alpha", "alpha"},
		Trackers: map[string]Tracker{
			"/u/code/projects/alpha": {ID: "a1", Open: []issues.Issue{issue(3, issues.InProgress)}},
			"alpha":                  {ID: "a2", Open: []issues.Issue{issue(3, issues.InProgress)}},
		},
		User: "/u",
	})
	if len(w.Projects) != 1 || w.Projects[0].ID != "a1" {
		t.Fatalf("projects %+v", w.Projects)
	}
	for root, want := range map[string]string{"/u/code/projects/alpha": "a1", "alpha": "a2"} {
		if got := w.IssuesID(root); got != want {
			t.Errorf("%s: IssuesID %q, want %q", root, got, want)
		}
	}
	if got := w.IssuesID("/w/untracked"); got != "untracked" {
		t.Errorf("an untracked root's id %q", got)
	}
}
