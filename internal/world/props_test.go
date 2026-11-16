package world

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"testing"
	"time"

	"pgregory.net/rapid"

	"overview/internal/fleet"
	"overview/internal/issues"
	"overview/internal/tmux"
)

// The generators draw a small world with collisions on purpose: a
// relative "alpha" has the name of ~/code/projects/alpha when the user's
// home is /u, and a worktree shares its project's issues id.

var (
	dirs = []string{
		"/u/code/projects/alpha", "/u/code/projects/alpha/sub", "alpha", "/u/beta",
		"/w/gamma", "/w/gamma/wt", "/u", "",
	}
	statuses = []string{issues.Inbox, issues.Ready, issues.InProgress, issues.Review, issues.Blocked, issues.Done, issues.Dropped}
	states   = []fleet.State{fleet.Working, fleet.Blocked, fleet.Idle, fleet.Ended, ""}
)

func genRoots(t *rapid.T) map[string]string {
	out := map[string]string{}
	if rapid.Bool().Draw(t, "subroot") {
		out["/u/code/projects/alpha/sub"] = "/u/code/projects/alpha"
	}
	if rapid.Bool().Draw(t, "wtroot") {
		out["/w/gamma/wt"] = "/w/gamma"
	}
	if rapid.Bool().Draw(t, "betaroot") {
		out["/u/beta"] = "/u/beta"
	}
	return out
}

func genRows(t *rapid.T) []fleet.Row {
	n := rapid.IntRange(0, 10).Draw(t, "rows")
	rows := make([]fleet.Row, n)
	for i := range rows {
		r := fleet.Row{
			State:     rapid.SampledFrom(states).Draw(t, "state"),
			Since:     t0.Add(time.Duration(rapid.IntRange(0, 4).Draw(t, "since")) * time.Minute),
			LastReply: rapid.SampledFrom([]string{"", "done.", "Which one?"}).Draw(t, "reply"),
			Hooked:    rapid.IntRange(0, 3).Draw(t, "hooked") == 0,
		}
		r.SessionID = fmt.Sprintf("s%02d", i)
		r.Name = rapid.SampledFrom([]string{"red", "blue", fleet.OrchestratorName, ""}).Draw(t, "name")
		r.Cwd = rapid.SampledFrom(dirs).Draw(t, "cwd")
		r.PID = rapid.IntRange(0, 6).Draw(t, "pid")
		r.StartedAt = int64(rapid.IntRange(0, 9).Draw(t, "started")) * 100
		if rapid.IntRange(0, 5).Draw(t, "kind") == 0 {
			r.Kind = "background"
			r.ID = rapid.SampledFrom([]string{"", "bg1", "bg2"}).Draw(t, "bgid")
		}

		rows[i] = r
	}
	return rows
}

func genTrackers(t *rapid.T) map[string]Tracker {
	out := map[string]Tracker{}
	for _, r := range []string{"/u/code/projects/alpha", "alpha", "/w/gamma", "/w/gamma/wt", "/u/beta"} {
		if rapid.IntRange(0, 2).Draw(t, "untracked") == 0 {
			continue
		}
		tr := Tracker{
			ID: rapid.SampledFrom([]string{"alpha", "gamma", ""}).Draw(t, "tid"),
		}
		if rapid.IntRange(0, 4).Draw(t, "failed") == 0 {
			tr.Err = "exit status 1"
		}
		for _, id := range rapid.SliceOfDistinct(rapid.IntRange(1, 5), rapid.ID[int]).Draw(t, "issues") {
			tr.Open = append(tr.Open, issues.Issue{
				ID:       id,
				Status:   rapid.SampledFrom(statuses).Draw(t, "status"),
				Priority: rapid.SampledFrom([]string{"p0", "p1", "p2", ""}).Draw(t, "priority"),
			})
		}
		out[r] = tr
	}
	return out
}

// genPanes draws a tmux listing of the view's session "ov" and another.
// Each pane has its own tty, as in tmux, and an attach names one session
// at most once.
func genPanes(t *rapid.T) []tmux.Pane {
	n := rapid.IntRange(0, 10).Draw(t, "panes")
	panes := make([]tmux.Pane, n)
	attached := map[string]bool{}
	for i := range panes {
		p := tmux.Pane{
			ID:      fmt.Sprintf("%%%d", i),
			TTY:     fmt.Sprintf("ttys%03d", i),
			Session: rapid.SampledFrom([]string{"ov", "ov", "ov", "other"}).Draw(t, "session"),
			Window:  fmt.Sprintf("@%d", rapid.IntRange(0, 5).Draw(t, "window")),
			Command: rapid.SampledFrom([]string{"zsh", "-zsh", "node", "vim", ""}).Draw(t, "command"),
			Path:    rapid.SampledFrom(dirs).Draw(t, "path"),
			Project: rapid.SampledFrom([]string{"", "", "/w/gamma"}).Draw(t, "project"),
		}
		if a := rapid.SampledFrom([]string{"", "", "bg1", "bg2"}).Draw(t, "attach"); a != "" && !attached[a] {
			p.Attach, attached[a] = a, true
		}
		panes[i] = p
	}
	return panes
}

func genInputs(t *rapid.T) Inputs {
	in := Inputs{
		Rows:     genRows(t),
		TTYs:     map[int]string{},
		Hosts:    map[int]Host{},
		Panes:    genPanes(t),
		Session:  "ov",
		Roots:    genRoots(t),
		Pinned:   rapid.SliceOfDistinct(rapid.SampledFrom(dirs), rapid.ID[string]).Draw(t, "pinned"),
		Trackers: genTrackers(t),
		User:     rapid.SampledFrom([]string{"/u", ""}).Draw(t, "user"),
	}
	for pid := 1; pid <= 6; pid++ {
		switch rapid.IntRange(0, 2).Draw(t, "place") {
		case 0:
			in.TTYs[pid] = fmt.Sprintf("ttys%03d", rapid.IntRange(0, 12).Draw(t, "tty"))
		case 1:
			in.Hosts[pid] = Host{Name: rapid.SampledFrom([]string{"", "Notes"}).Draw(t, "host")}
		}
	}
	pick := func(label string) string {
		if len(in.Panes) == 0 || rapid.Bool().Draw(t, label+"none") {
			return ""
		}
		return rapid.SampledFrom(in.Panes).Draw(t, label).ID
	}
	in.Self, in.Opening = pick("self"), pick("opening")
	if h := pick("holder"); h != "" {
		for i := range in.Panes {
			in.Panes[i].Holder = in.Panes[i].ID == h
		}
	}
	return in
}

// shuffle permutes every input slice. The panes of a window keep their
// order among themselves, since a terminal is its first pane's.
func shuffle(t *rapid.T, in Inputs) Inputs {
	out := in
	out.Rows = rapid.Permutation(in.Rows).Draw(t, "prows")
	out.Pinned = rapid.Permutation(in.Pinned).Draw(t, "ppinned")
	out.Trackers = map[string]Tracker{}
	for r, tr := range in.Trackers {
		tr.Open = rapid.Permutation(tr.Open).Draw(t, "popen")
		out.Trackers[r] = tr
	}
	perm := rapid.Permutation(in.Panes).Draw(t, "ppanes")
	byWindow := map[string][]tmux.Pane{}
	for _, p := range in.Panes {
		byWindow[p.Window] = append(byWindow[p.Window], p)
	}
	out.Panes = make([]tmux.Pane, len(perm))
	for i, p := range perm {
		out.Panes[i], byWindow[p.Window] = byWindow[p.Window][0], byWindow[p.Window][1:]
	}
	return out
}

// canon is a World as plain values, for comparing two Worlds.
type canon struct {
	Projects []canonProject
	Away     []string
	Agents   []string
	Sessions map[string]canonSession

	Terminals []canonTerminal
	Counts    [3]int
	NoHook    bool
	Trackers  []string
}

type canonProject struct {
	Root, Name, ID          string
	Roots                   []string
	Tracked, Failed, Pinned bool
	Open                    []issues.Issue
	InReview                int
	Sessions, Terms         []string
}

type canonSession struct {
	Row       fleet.Row
	Project   string
	Pane      string
	Host      *Host
	Elsewhere bool
}

type canonTerminal struct {
	Terminal Terminal
	Project  string
}

func toCanon(w World) canon {
	c := canon{
		Away: sessionIDs(w.Away), Agents: sessionIDs(w.Agents), Sessions: map[string]canonSession{},
		Counts: [3]int{w.Need, w.Working, w.Idle}, NoHook: w.NoHook, Trackers: w.Trackers,
	}
	for _, p := range w.Projects {
		c.Projects = append(c.Projects, canonProject{p.Root, p.Name, p.ID, p.Roots, p.Tracked, p.Failed, p.Pinned, p.Open,
			p.InReview, sessionIDs(p.Sessions), windows(p.Terminals)})
	}
	for id, s := range w.Sessions {
		c.Sessions[id] = canonSession{s.Row, s.Project.Name, s.Pane, s.Host, s.Elsewhere}
	}
	for _, tm := range w.Terminals {
		ct := canonTerminal{Terminal: *tm, Project: tm.Project.Name}
		ct.Terminal.Project = nil

		c.Terminals = append(c.Terminals, ct)
	}
	return c
}

func TestPropIndependentOfInputOrder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := genInputs(t)
		a, b := toCanon(Derive(in)), toCanon(Derive(shuffle(t, in)))
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("shuffled inputs give another world:\n%+v\n%+v", a, b)
		}
		if !reflect.DeepEqual(a, toCanon(Derive(in))) {
			t.Fatal("not deterministic")
		}
	})
}

// count is how many times x is in xs.
func count[T comparable](xs []T, x T) int {
	n := 0
	for _, y := range xs {
		if y == x {
			n++
		}
	}
	return n
}

// Every session but the orchestrator is in Agents once, in order, and in
// exactly one of its own project's list and Away, in Away exactly when
// another app runs it.
func TestPropEverySessionIsPlacedOnce(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := genInputs(t)
		w := Derive(in)
		var want []string
		for _, r := range in.Rows {
			if r.Name != fleet.OrchestratorName {
				want = append(want, r.SessionID)
			}
		}
		slices.Sort(want)
		got := slices.Sorted(slices.Values(sessionIDs(w.Agents)))
		if !slices.Equal(got, want) || len(w.Sessions) != len(want) {
			t.Fatalf("agents %v, sessions %d, want %v", got, len(w.Sessions), want)
		}
		for i := 1; i < len(w.Agents); i++ {
			if !needsFirst(w.Agents[i-1].Row, w.Agents[i].Row) {
				t.Fatalf("agents out of order: %s then %s", w.Agents[i-1].SessionID, w.Agents[i].SessionID)
			}
		}
		for _, s := range w.Sessions {
			inProjects := 0
			for _, p := range w.Projects {
				inProjects += count(p.Sessions, s)
			}
			inAway := count(w.Away, s)
			if inProjects+inAway != 1 || inAway == 1 != (s.Host != nil) || inProjects == 1 && count(s.Project.Sessions, s) != 1 {
				t.Fatalf("%s: projects %d, away %d, host %v", s.SessionID, inProjects, inAway, s.Host)
			}
			if s.Host != nil != (!s.Background() && hasHost(in, s.PID)) {
				t.Fatalf("%s: host %v", s.SessionID, s.Host)
			}
		}
	})
}

func hasHost(in Inputs, pid int) bool { _, ok := in.Hosts[pid]; return ok }

// Projects are in name order, each with its least root first and its
// sessions and terminals in order, and so are Away and the terminals.
// Pinned projects are projects.
func TestPropOrder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := genInputs(t)
		w := Derive(in)
		for i, p := range w.Projects {
			if i > 0 && !NameLess(w.Projects[i-1].Name, p.Name) {
				t.Fatalf("projects out of order: %q then %q", w.Projects[i-1].Name, p.Name)
			}
			if p.Root != p.Roots[0] || !slices.IsSorted(p.Roots) {
				t.Fatalf("%q: root %q of %v", p.Name, p.Root, p.Roots)
			}
			checkSessions(t, p.Name, p.Sessions)
			checkTerms(t, p.Name, p.Terminals)
		}
		checkSessions(t, "away", w.Away)
		checkTerms(t, "terminals", w.Terminals)
		for _, d := range in.Pinned {
			if p := w.ProjectOf(d); d != "" && (p == nil || !p.Pinned) {
				t.Fatalf("pinned %q: %+v", d, p)
			}
		}
	})
}

// A session's state and activity never move it in its project's list,
// so the rows stay put while the cursor moves through them.
func TestPropStateChangesKeepProjectOrder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := genInputs(t)
		before := projectOrder(Derive(in))
		rows := slices.Clone(in.Rows)
		for i := range rows {
			rows[i].State = rapid.SampledFrom(states).Draw(t, "newstate")
			rows[i].Since = t0.Add(time.Duration(rapid.IntRange(0, 4).Draw(t, "newsince")) * time.Minute)
			rows[i].LastReply = rapid.SampledFrom([]string{"", "done.", "Which one?"}).Draw(t, "newreply")
		}
		in.Rows = rows
		if after := projectOrder(Derive(in)); !slices.Equal(before, after) {
			t.Fatalf("order moved:\n%v\n%v", before, after)
		}
	})
}

// projectOrder lists the sessions as the projects show them, each list
// under its own heading.
func projectOrder(w World) []string {
	var out []string
	for _, p := range w.Projects {
		out = append(out, "project "+p.Name)
		out = append(out, sessionIDs(p.Sessions)...)

	}
	out = append(out, "away")
	return append(out, sessionIDs(w.Away)...)
}

func checkSessions(t *rapid.T, where string, ss []*Session) {
	for i := 1; i < len(ss); i++ {
		if !StartedLess(ss[i-1].Row, ss[i].Row) {
			t.Fatalf("sessions out of order in %s: %s then %s", where, ss[i-1].SessionID, ss[i].SessionID)
		}
	}
}

func checkTerms(t *rapid.T, where string, ts []*Terminal) {
	for i := 1; i < len(ts); i++ {
		if !WindowLess(ts[i-1].Window, ts[i].Window) {
			t.Fatalf("terminals out of order in %s", where)
		}
	}
}

// The counts are a recount of the sessions, and Need adds the issues in
// review of every project's tracked roots to the sessions that need you.
func TestPropCountsAreARecount(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := genInputs(t)
		w := Derive(in)
		need, working, idle := 0, 0, 0
		hooked, interactive := false, false
		for _, s := range w.Agents {
			hooked = hooked || s.Hooked
			interactive = interactive || !s.Background()
			switch {
			case s.NeedsYou():
				need++
			case s.State == fleet.Working:
				working++
			default:
				idle++
			}
		}
		// Every issue in review waits on you, in a project the list shows or not.
		for _, pr := range w.byName {
			inReview := 0
			for _, r := range pr.Roots {
				for _, i := range in.Trackers[r].Open {
					if i.Status == issues.Review {
						inReview++
					}
				}
			}
			if pr.InReview != inReview {
				t.Fatalf("%q: %d in review, want %d", pr.Name, pr.InReview, inReview)
			}
			need += inReview
		}
		if noHook := interactive && !hooked; w.Need != need || w.Working != working || w.Idle != idle || w.NoHook != noHook {
			t.Fatalf("counts %d %d %d %v, want %d %d %d %v", w.Need, w.Working, w.Idle, w.NoHook, need, working, idle, noHook)
		}
	})
}

// Every pointer reachable from a World is to one of its own projects,
// sessions and terminals.
func TestPropPointersStayInTheWorld(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := genInputs(t)
		w := Derive(in)
		projects := map[*Project]bool{}
		for n, p := range w.byName {
			if projects[p] || p.Name != n {
				t.Fatalf("project %q twice or filed under %q", p.Name, n)
			}
			projects[p] = true
		}
		listed := map[*Project]bool{}
		for _, p := range w.Projects {
			if listed[p] || w.byName[p.Name] != p {
				t.Fatalf("project %q listed twice or not by name", p.Name)
			}
			listed[p] = true
		}
		terms := map[*Terminal]bool{}
		for _, tm := range w.Terminals {
			terms[tm] = true
		}
		sess := func(s *Session) {
			if w.Sessions[s.SessionID] != s {
				t.Fatalf("session %s is not the world's", s.SessionID)
			}
		}

		proj := func(p *Project) {
			if !projects[p] {
				t.Fatalf("project %+v is not the world's", p)
			}
		}
		term := func(tm *Terminal) {
			if !terms[tm] {
				t.Fatalf("terminal %s is not the world's", tm.Window)
			}
		}
		for _, s := range w.Away {
			sess(s)
		}
		for _, s := range w.Agents {
			sess(s)
		}
		for id, s := range w.Sessions {
			if s.SessionID != id {
				t.Fatalf("%s filed under %s", s.SessionID, id)
			}
			proj(s.Project)
			if s.Project != w.ProjectOf(s.Cwd) {
				t.Fatalf("%s is in %q, not its cwd's", id, s.Project.Name)
			}

		}
		for _, p := range w.byName {

			for _, s := range p.Sessions {
				sess(s)
			}
			for _, s := range p.Members {
				sess(s)
			}
			for _, tm := range p.Terminals {
				term(tm)
			}
			// Terminals is the world's terminals in the project, in order.
			var all []*Terminal
			for _, tm := range w.Terminals {
				if tm.Project == p {
					all = append(all, tm)
				}
			}
			if !slices.Equal(p.Terminals, all) {
				t.Fatalf("%q's terminals %v, want %v", p.Name, p.Terminals, all)
			}
		}
		for _, tm := range w.Terminals {
			proj(tm.Project)
			if tm.Project != w.ProjectOf(tm.Root) {
				t.Fatalf("terminal %s is in %q, not its root's", tm.Window, tm.Project.Name)
			}

		}
	})
}

// Trackers holds every project's roots, and nothing else.
func TestPropTrackerRoots(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := genInputs(t)
		w := Derive(in)
		want := map[string]bool{}
		for _, p := range w.byName {
			for _, r := range p.Roots {
				want[r] = true
			}
		}

		delete(want, "")
		if got := slices.Sorted(maps.Keys(want)); !slices.Equal(w.Trackers, got) {
			t.Fatalf("trackers %v, want %v", w.Trackers, got)
		}
	})
}

// Two calls on one input share nothing, so a World can be kept while the
// inputs move on.
func TestDeriveCopiesRoots(t *testing.T) {
	in := Inputs{Rows: []fleet.Row{sess("s", "/w/a/b")}, Roots: map[string]string{"/w/a/b": "/w/a"}}
	w := Derive(in)
	in.Roots["/w/a/b"] = "/elsewhere"
	if w.Root("/w/a/b") != "/w/a" {
		t.Fatalf("the world saw its input change")
	}
}

// Projects lists exactly the projects with something in them and the
// pinned ones. The Members lists partition the sessions by project, and
// every member is in its project's Sessions or in Away.
func TestPropProjectsAndMembers(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := genInputs(t)
		w := Derive(in)
		for _, p := range w.byName {
			if listed := count(w.Projects, p) == 1; listed != (!p.Empty() || p.Pinned) {
				t.Fatalf("%q listed %v, empty %v, pinned %v", p.Name, listed, p.Empty(), p.Pinned)
			}
		}
		for _, p := range w.byName {
			// Pins are the pinned directories in it, each once.
			var pins []string
			for _, d := range in.Pinned {
				if d != "" && w.ProjectOf(d) == p && !slices.Contains(pins, d) {
					pins = append(pins, d)
				}
			}
			slices.Sort(pins)
			if !slices.Equal(p.Pins, pins) || p.Pinned != (len(pins) > 0) {
				t.Fatalf("%q pins %v pinned %v, want %v", p.Name, p.Pins, p.Pinned, pins)
			}
		}
		// The world holds the tracker of every root of its projects, and
		// no other.
		roots := map[string]bool{}
		for _, p := range w.byName {
			for _, r := range p.Roots {
				roots[r] = true
			}
		}
		for r, tr := range in.Trackers {
			got, ok := w.Tracker(r)
			if ok != roots[r] || ok && !reflect.DeepEqual(got, tr) {
				t.Fatalf("tracker of %q: %+v %v, a project's root %v", r, got, ok, roots[r])
			}
		}
		seen := map[*Session]int{}
		for _, p := range w.byName {
			checkSessions(t, p.Name+" members", p.Members)
			for _, s := range p.Members {
				seen[s]++
				if s.Project != p {
					t.Fatalf("%s is a member of %q but in %q", s.SessionID, p.Name, s.Project.Name)
				}
				if count(p.Sessions, s)+count(w.Away, s) == 0 {
					t.Fatalf("member %s is placed nowhere", s.SessionID)
				}
			}
			for _, s := range p.Sessions {
				if count(p.Members, s) != 1 {
					t.Fatalf("%s is in %q's sessions but not its members", s.SessionID, p.Name)
				}
			}
		}
		for _, s := range w.Sessions {
			if seen[s] != 1 {
				t.Fatalf("%s is a member %d times", s.SessionID, seen[s])
			}
		}
		if len(seen) != len(w.Sessions) {
			t.Fatalf("%d members, %d sessions", len(seen), len(w.Sessions))
		}
	})
}

// genTermPanes draws a tmux listing: panes in a few windows of the view's
// session "ov" and of another, with the list, the holder, attaches and
// claude ttys among them.
func genTermPanes(t *rapid.T) []tmux.Pane {
	n := rapid.IntRange(0, 10).Draw(t, "panes")
	panes := make([]tmux.Pane, n)
	for i := range panes {
		panes[i] = tmux.Pane{
			ID:      fmt.Sprintf("%%%d", i),
			TTY:     fmt.Sprintf("ttys%03d", i),
			Session: rapid.SampledFrom([]string{"ov", "ov", "ov", "other"}).Draw(t, "session"),
			Window:  fmt.Sprintf("@%d", rapid.IntRange(0, 4).Draw(t, "window")),
			Command: rapid.SampledFrom([]string{"zsh", "-zsh", "node", "claude", "vim", ""}).Draw(t, "command"),
			Path:    rapid.SampledFrom([]string{"/w/a", "/w/a/sub", "/w/b", ""}).Draw(t, "path"),
			Project: rapid.SampledFrom([]string{"", "", "/w/p"}).Draw(t, "project"),
			Attach:  rapid.SampledFrom([]string{"", "", "", "bg1"}).Draw(t, "attach"),
		}
	}
	return panes
}

// terminals lists exactly the windows of the view's session that hold a
// pane besides the list and the agents' pane, and none of the holder, an
// attach, a listed claude, or the pane c just opened. Each comes once, in window order,
// named after its first such pane.
func TestPropTerminals(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		panes := genTermPanes(t)
		pick := func(label string) string {
			if len(panes) == 0 || rapid.Bool().Draw(t, label+"none") {
				return ""
			}
			return rapid.SampledFrom(panes).Draw(t, label).ID
		}
		self, holder, opened, agents := pick("self"), pick("holder"), pick("opened"), pick("agents")
		for i := range panes {
			panes[i].Holder = panes[i].ID == holder
			panes[i].Agents = panes[i].ID == agents
		}
		claude := map[string]bool{}
		for _, p := range panes {
			if rapid.IntRange(0, 4).Draw(t, "claude") == 0 {
				claude[p.TTY] = true
			}
		}
		root := func(p string) string { return "root:" + p }
		got := terminals(panes, "ov", self, opened, claude, root)

		want := map[string][]tmux.Pane{}
		bad := map[string]bool{}
		for _, p := range panes {
			if p.Session != "ov" || p.ID == self || p.ID == agents {
				continue
			}
			want[p.Window] = append(want[p.Window], p)
			if p.ID == holder || p.Attach != "" || claude[p.TTY] || p.ID == opened {
				bad[p.Window] = true
			}
		}
		n := 0
		for w := range want {
			if !bad[w] {
				n++
			}
		}
		if len(got) != n {
			t.Fatalf("%d terminals, want %d: %+v", len(got), n, got)
		}
		for i, tm := range got {
			ps := want[tm.Window]
			if len(ps) == 0 || bad[tm.Window] {
				t.Fatalf("%s is not a terminal: %+v", tm.Window, ps)
			}
			if i > 0 && !WindowLess(got[i-1].Window, tm.Window) {
				t.Fatalf("out of order: %s then %s", got[i-1].Window, tm.Window)
			}
			first := ps[0]
			wantRoot := first.Project
			if wantRoot == "" {
				wantRoot = root(first.Path)
			}
			if tm.Pane != first.ID || tm.Path != first.Path || tm.Root != wantRoot || tm.Title != termTitle(ps) {
				t.Fatalf("terminal %+v from panes %+v", tm, ps)
			}
		}
	})
}

func genIssue(t *rapid.T, label string) issues.Issue {
	return issues.Issue{
		ID:       rapid.IntRange(1, 6).Draw(t, label+"id"),
		Status:   rapid.SampledFrom(append(slices.Clone(statuses), "odd")).Draw(t, label+"status"),
		Priority: rapid.SampledFrom([]string{"p0", "p1", "p2", "p3", "", "px"}).Draw(t, label+"priority"),
	}
}

// IssueLess and StartLess are strict orders, total on issues that differ
// in status rank, priority rank or number. StartLess puts ready and inbox
// first and otherwise agrees with IssueLess.
func TestPropIssueOrders(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a, b, c := genIssue(t, "a"), genIssue(t, "b"), genIssue(t, "c")
		same := func(x, y issues.Issue) bool {
			return issues.Rank(x.Status) == issues.Rank(y.Status) &&
				issues.PriorityRank(x.Priority) == issues.PriorityRank(y.Priority) && x.ID == y.ID
		}
		startable := func(x issues.Issue) bool { return x.Status == issues.Ready || x.Status == issues.Inbox }
		for name, less := range map[string]func(x, y issues.Issue) bool{"IssueLess": IssueLess, "StartLess": StartLess} {
			if less(a, a) {
				t.Fatalf("%s: %+v before itself", name, a)
			}
			if less(a, b) && less(b, a) {
				t.Fatalf("%s: %+v and %+v each first", name, a, b)
			}
			if !same(a, b) && !less(a, b) && !less(b, a) {
				t.Fatalf("%s: %+v and %+v unordered", name, a, b)
			}
			if less(a, b) && less(b, c) && !less(a, c) {
				t.Fatalf("%s: not transitive over %+v %+v %+v", name, a, b, c)
			}
		}
		switch {
		case startable(a) && !startable(b):
			if !StartLess(a, b) {
				t.Fatalf("StartLess put %+v after %+v", a, b)
			}
		case !startable(a) && !startable(b):
			if StartLess(a, b) != IssueLess(a, b) {
				t.Fatalf("StartLess and IssueLess disagree on %+v %+v", a, b)
			}
		}
	})
}
