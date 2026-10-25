package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"overview/internal/fleet"
	"overview/internal/issues"
)

// fakeIssues is a set of projects with .issues directories, each listing
// some issues, and counts the runs of the issues CLI per root.
type fakeIssues struct {
	lists map[string]string // the JSON list --json prints, by root
	edn   map[string]string // project.edn, by root; missing for none
	mtime map[string]time.Time
	runs  map[string]int
	fail  map[string]bool
	// details is what issues show prints as an issue's details, by
	// taskKey.
	details map[string]string
}

func newFakeIssues() *fakeIssues {
	return &fakeIssues{lists: map[string]string{}, edn: map[string]string{}, mtime: map[string]time.Time{},
		runs: map[string]int{}, fail: map[string]bool{}, details: map[string]string{}}
}

// project adds a tracked project at root with id (no project.edn when id is
// "") and issues given as "6 in-progress Title".
func (f *fakeIssues) project(root, id string, list ...string) {
	var ws []string
	for _, l := range list {
		parts := strings.SplitN(l, " ", 3)
		ws = append(ws, fmt.Sprintf(`{"id": %s, "ref": "%s#%s", "status": "%s", "title": %q, "priority": "p2"}`,
			parts[0], path.Base(root), parts[0], parts[1], parts[2]))
	}
	f.lists[root] = `{"issues": [` + strings.Join(ws, ",") + `]}`
	if id != "" {
		f.edn[root] = `{:id "` + id + `"}`
	}
	f.mtime[root] = t0
}

func (f *fakeIssues) run(args ...string) ([]byte, error) {
	root := args[1]
	f.runs[root]++
	if f.fail[root] {
		return nil, errors.New("issues: boom")
	}
	if len(args) > 3 && args[2] == "show" {
		id, _ := strconv.Atoi(args[3])
		b, err := json.Marshal(map[string]any{"id": id, "details-text": f.details[taskKey(root, id)]})
		return b, err
	}
	return []byte(f.lists[root]), nil
}

func (f *fakeIssues) deps() deps {
	d := fakeDeps(nil)
	d.exists = func(p string) bool {
		_, ok := f.lists[path.Dir(p)]
		return path.Base(p) == ".issues" && ok
	}
	d.glob = func(pattern string) []string {
		root := path.Dir(path.Dir(pattern))
		if _, ok := f.lists[root]; !ok {
			return nil
		}
		return []string{root + "/.issues/1-a.edn"}
	}
	d.modTime = func(p string) time.Time { return f.mtime[path.Dir(path.Dir(p))] }
	d.readFile = func(p string) ([]byte, error) {
		if e, ok := f.edn[path.Dir(path.Dir(p))]; ok {
			return []byte(e), nil
		}
		return nil, errors.New("no such file")
	}
	return d
}

// readAll is readTracker for each of roots, as the reads readTrackers
// asks for find them.
func readAll(old trackers, roots []string, run issues.Runner, d deps, now time.Time) trackers {
	out := trackers{}
	if run == nil {
		return out
	}
	for _, r := range roots {
		if t, ok := readTracker(old[r], r, run, d, now); ok {
			out[r] = t
		}
	}
	return out
}

func TestReadTrackersRunsOnlyWhenIssuesChange(t *testing.T) {
	f := newFakeIssues()
	f.project("/w/app", "app", "6 in-progress Rework", "7 done Old")
	f.project("/w/lib", "", "1 review Docs")
	roots := []string{"/w/app", "/w/lib", "/w/plain"}
	d := f.deps()

	ts := readAll(nil, roots, f.run, d, t0)
	if len(ts) != 2 || f.runs["/w/app"] != 1 || f.runs["/w/lib"] != 1 || f.runs["/w/plain"] != 0 {
		t.Fatalf("first read: %v runs %v", ts, f.runs)
	}
	if app := ts["/w/app"]; app.id != "app" || len(app.issues) != 1 || app.issues[0].ID != 6 {
		t.Fatalf("app: %+v (only open issues, id from project.edn)", app)
	}
	if lib := ts["/w/lib"]; lib.id != "lib" {
		t.Fatalf("lib without project.edn has id %q, want its root's name", lib.id)
	}

	again := readAll(ts, roots, f.run, d, t0.Add(time.Minute))
	if f.runs["/w/app"] != 1 || f.runs["/w/lib"] != 1 {
		t.Fatalf("unchanged .issues ran again: %v", f.runs)
	}
	if len(again["/w/app"].issues) != 1 {
		t.Fatalf("kept tracker lost its issues: %+v", again["/w/app"])
	}

	f.project("/w/app", "app", "6 review Rework")
	f.mtime["/w/app"] = t0.Add(2 * time.Second)
	changed := readAll(again, roots, f.run, d, t0.Add(2*time.Minute))
	if f.runs["/w/app"] != 2 || f.runs["/w/lib"] != 1 {
		t.Fatalf("a changed .issues did not run once: %v", f.runs)
	}
	if got := changed["/w/app"].issues[0].Status; got != issues.Review {
		t.Fatalf("status %q after the change", got)
	}
	if again["/w/app"].issues[0].Status != issues.InProgress {
		t.Fatal("reading changed the old trackers")
	}
}

func TestReadTrackersKeepsTheListWhenARunFails(t *testing.T) {
	f := newFakeIssues()
	f.project("/w/app", "app", "6 in-progress Rework")
	d := f.deps()
	ts := readAll(nil, []string{"/w/app"}, f.run, d, t0)

	f.fail["/w/app"] = true
	f.mtime["/w/app"] = t0.Add(time.Second)
	failed := readAll(ts, []string{"/w/app"}, f.run, d, t0.Add(time.Minute))
	if app := failed["/w/app"]; app.err == nil || len(app.issues) != 1 || f.runs["/w/app"] != 2 {
		t.Fatalf("after a failed run: %+v, runs %d", app, f.runs["/w/app"])
	}
	// Soon after, nothing changed: no new run.
	readAll(failed, []string{"/w/app"}, f.run, d, t0.Add(time.Minute+5*time.Second))
	if f.runs["/w/app"] != 2 {
		t.Fatalf("retried within %v: %d runs", retryAfter, f.runs["/w/app"])
	}
	f.fail["/w/app"] = false
	ok := readAll(failed, []string{"/w/app"}, f.run, d, t0.Add(time.Minute+retryAfter+time.Second))
	if app := ok["/w/app"]; app.err != nil || f.runs["/w/app"] != 3 {
		t.Fatalf("no retry after %v: %+v, runs %d", retryAfter, app, f.runs["/w/app"])
	}
}

func TestReadTrackersWithoutARunner(t *testing.T) {
	f := newFakeIssues()
	f.project("/w/app", "app", "6 in-progress Rework")
	if ts := readAll(nil, []string{"/w/app"}, nil, f.deps(), t0); len(ts) != 0 {
		t.Fatalf("no runner, yet %v", ts)
	}
}

// After a refresh the model reads the trackers of the sessions' projects
// and of their task refs' directories.
func TestRefreshReadsTrackers(t *testing.T) {
	f := newFakeIssues()
	f.project("/w/app", "app", "6 in-progress Rework")
	f.project("/w/app-wt", "app", "6 in-progress Rework")
	m := newTestModel(t)
	m.deps = f.deps()
	m.deps.exists = func(p string) bool {
		return p == "/w/app/.jj" || p == "/w/app-wt/.jj" || strings.HasSuffix(p, ".issues") && f.lists[path.Dir(p)] != ""
	}
	m.issues = f.run
	r := row("s1", "red", "", 11)
	r.Cwd = "/w/app/sub"
	r.Tasks = []fleet.TaskRef{{Dir: "/w/app-wt", ID: 6}}
	m, cmd := step(m, m.rowsMsg([]fleet.Row{r}, nil, nil, nil))
	m = settle(t, m, cmd)
	if _, ok := m.trackers["/w/app"]; !ok || f.runs["/w/app"] != 1 || f.runs["/w/app-wt"] != 1 {
		t.Fatalf("trackers %v runs %v", m.trackers, f.runs)
	}
	if got := m.tasks().links["s1"]; !slices.Equal(got, []string{"/w/app#6"}) {
		t.Fatalf("links %v, want the worktree's ref on the project", got)
	}
	m, cmd = step(m, m.rowsMsg([]fleet.Row{r}, nil, nil, nil))
	m = settle(t, m, cmd)
	if f.runs["/w/app"] != 1 {
		t.Fatalf("a second refresh ran issues again: %v", f.runs)
	}
	// A project that leaves the list takes its tracker with it.
	r.Cwd, r.Tasks = "/w/other", nil
	m, cmd = step(m, m.rowsMsg([]fleet.Row{r}, nil, nil, nil))
	m = settle(t, m, cmd)
	if len(m.trackers) != 0 {
		t.Fatalf("trackers %v after the projects left", m.trackers)
	}
}

// An issues CLI that hangs holds up nothing: the rows refresh as before,
// and no second run starts for a project while one is out for it.
func TestAHangingIssuesRunBlocksNothing(t *testing.T) {
	f := newFakeIssues()
	f.project("/w/app", "app", "6 in-progress Rework")
	started, release := make(chan struct{}, 4), make(chan struct{})
	var calls atomic.Int32
	m := newTestModel(t)
	m.deps = f.deps()
	m.issues = func(args ...string) ([]byte, error) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return f.run(args...)
	}
	r := row("s1", "red", "", 11)
	r.Cwd = "/w/app"
	msgs := make(chan tea.Msg, 8)
	run := func(cmd tea.Cmd) <-chan struct{} {
		done := make(chan struct{})
		go func() { runAll(cmd, msgs); close(done) }()
		return done
	}
	m, cmd := step(m, rowsMsg{rows: []fleet.Row{r}})
	run(cmd)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("issues never ran")
	}
	r2 := row("s2", "blue", "", 12)
	r2.Cwd = "/w/app"
	m, cmd = step(m, rowsMsg{rows: []fleet.Row{r, r2}})
	if len(m.rows) != 2 {
		t.Fatalf("rows %d: the refresh waited on issues", len(m.rows))
	}
	select {
	case <-run(cmd):
	case <-time.After(5 * time.Second):
		t.Fatal("the second refresh's commands waited on issues")
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d issues runs for one project at once", n)
	}
	close(release)
	m = update(m, <-msgs)
	if _, ok := m.trackers["/w/app"]; !ok || len(m.reading) != 0 {
		t.Fatalf("trackers %v reading %v after the run", m.trackers, m.reading)
	}
	// With nothing out, the next refresh may read again.
	f.mtime["/w/app"] = t0.Add(time.Second)
	m, cmd = step(m, rowsMsg{rows: []fleet.Row{r, r2}})
	<-run(cmd)
	if n := calls.Load(); n != 2 || !m.reading["/w/app"] {
		t.Fatalf("%d runs, reading %v", n, m.reading)
	}
}

// runAll runs cmd and the commands it batches, and sends what they say to
// out.
func runAll(cmd tea.Cmd, out chan<- tea.Msg) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			runAll(c, out)
		}
	default:
		out <- msg
	}
}

func tracked(id string, list ...issues.Issue) tracker { return tracker{id: id, issues: list} }

func issue(id int, status, title string) issues.Issue {
	return issues.Issue{ID: id, Status: status, Title: title, Priority: "p2"}
}

func TestResolveTask(t *testing.T) {
	ts := trackers{
		"/w/app":    tracked("app", issue(6, issues.InProgress, "a"), issue(7, issues.Done, "b")),
		"/w/app-wt": tracked("app", issue(6, issues.InProgress, "a")),
		"/w/lib":    tracked("lib", issue(1, issues.Ready, "c")),
		"/x/other":  tracked("other", issue(2, issues.Ready, "d")),
	}
	known := map[string]bool{"/w/app": true, "/w/lib": true, "/w/plain": true}
	root := func(dir string) string {
		switch {
		case strings.HasPrefix(dir, "/w/app-wt"):
			return "/w/app-wt"
		case strings.HasPrefix(dir, "/w/app"):
			return "/w/app"
		}
		return dir
	}
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
		if got := resolveTask(c.ref, c.own, ts, known, root); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// Without a runner there are no trackers, and nothing links.
	if got := resolveTask(fleet.TaskRef{Project: "app", ID: 6}, "/w/app", trackers{}, known, root); got != "" {
		t.Errorf("no trackers: %q", got)
	}
	// A project without project.edn has its root's name for an id.
	f := newFakeIssues()
	f.project("/w/tool", "", "3 in-progress t")
	got := readAll(nil, []string{"/w/tool"}, f.run, f.deps(), t0)
	if k := resolveTask(fleet.TaskRef{Project: "tool", ID: 3}, "/w/tool", got, map[string]bool{"/w/tool": true}, root); k != "/w/tool#3" {
		t.Errorf("missing project.edn: %q", k)
	}
}

func TestResolveLinksKeepsTheLatestLast(t *testing.T) {
	ts := trackers{"/w/app": tracked("app", issue(1, issues.Ready, ""), issue(2, issues.Ready, ""), issue(3, issues.Done, ""))}
	r := fleet.Row{Live: fleet.Live{SessionID: "s", Cwd: "/w/app"}, Tasks: []fleet.TaskRef{
		{Project: "app", ID: 1}, {Project: "app", ID: 2}, {Dir: "/w/app", ID: 1}, {Project: "app", ID: 3},
	}}
	got, _ := resolveLinks([]fleet.Row{r}, ts, map[string]bool{"/w/app": true}, func(d string) string { return d })
	if want := []string{"/w/app#2", "/w/app#1"}; !slices.Equal(got["s"], want) {
		t.Fatalf("links %v, want %v", got["s"], want)
	}
}

// taskModel runs in the fake tmux with two sessions in /code/app, which
// tracks its issues: s1 works on #6, and #4 is in review with nobody on
// it. s2 is linked to nothing.
func taskModel(t *testing.T, f *fakeMux) model {
	t.Helper()
	s1, s2 := row("s1", "red", "", 11), row("s2", "blue", "", 12)
	s1.Cwd, s2.Cwd = "/code/app", "/code/app"
	s1.Tasks = []fleet.TaskRef{{Project: "app", Dir: "/code/app", ID: 6}}
	m := stagedModel(t, f, s1, s2)
	ts := trackers{"/code/app": tracked("app",
		issues.Issue{ID: 6, Ref: "app#6", Title: "keep width", Status: issues.InProgress, Priority: "p1"},
		issues.Issue{ID: 4, Ref: "app#4", Title: "show cost", Status: issues.Review, Priority: "p2"},
	)}
	m.trackers = ts
	m, cmd := step(m, rowsMsg{rows: m.rows, ttys: m.ttys})
	return settle(t, m, cmd)
}

func selectKey(t *testing.T, m model, key string) model {
	t.Helper()
	for i, e := range m.ordered() {
		if e.key() == key {
			return m.moveTo(m.ordered(), i)
		}
	}
	t.Fatalf("%s is not listed: %v", key, ids(m.sections()))
	return m
}

func TestTaskModelLists(t *testing.T) {
	m := taskModel(t, newFakeMux())
	want := []string{"task:/code/app#4", "task:/code/app#6", "s1", "s2"}
	if got := ids(m.sections()); !slices.Equal(got, want) {
		t.Fatalf("listed %v, want %v", got, want)
	}
	if need, _, _ := m.counts(); need != 1 {
		t.Fatalf("need %d, want the review", need)
	}
}

func TestEnterOnAWorkstreamGoesToItsFirstSession(t *testing.T) {
	f := newFakeMux()
	m := taskModel(t, f)
	m = selectKey(t, m, "task:/code/app#6")
	m = settle(t, m, nil)
	m, cmd := step(m, keyMsg("r")) // any update restages
	m = settle(t, m, cmd)
	s1 := paneWithTTY(f, "ttys011")
	if !f.staged(s1) {
		t.Fatalf("on #6 the list is in %s, want beside s1's %s", f.stage(), s1)
	}
	m = pressSettled(t, m, "enter")
	if !slices.Equal(f.selects, []string{s1}) {
		t.Fatalf("selects %v, want [%s]", f.selects, s1)
	}
	if m.sel != "task:/code/app#6" {
		t.Fatalf("enter moved the cursor to %s", m.sel)
	}
}

// A workstream no session works on shows its cockpit's cards beside the
// list, and enter opens the cockpit with the keyboard on them. enter on
// its header then leaves it open, as there is nothing to attach to.
func TestAWorkstreamWithNoSessionsShowsItsCockpit(t *testing.T) {
	f := newFakeMux()
	m := taskModel(t, f)
	m = pressSettled(t, m, "g") // #4, first
	if m.sel != "task:/code/app#4" || !f.staged("%1") {
		t.Fatalf("sel %s, list in %s", m.sel, f.stage())
	}
	text := ansi.Strip(f.drawn["ttys901"])
	for _, want := range []string{"app#4", "show cost", "review · p2", "no sessions", "c  claude", "t  term"} {
		if !strings.Contains(text, want) {
			t.Errorf("holder lacks %q: %q", want, text)
		}
	}
	if strings.Contains(text, "attach") {
		t.Errorf("holder offers to attach to no session: %q", text)
	}
	m = pressSettled(t, m, "enter")
	if !m.cockpit.open || !slices.Equal(f.zooms, []string{"%0 true"}) || m.navAt() != headerPanel || len(f.selects) != 0 {
		t.Fatalf("open %v zooms %v nav %+v selects %v", m.cockpit.open, f.zooms, m.nav, f.selects)
	}
	m = pressSettled(t, m, "enter")
	if !m.cockpit.open || len(f.zooms) != 1 || m.note != "No session works on #4. c starts one, t a terminal." {
		t.Fatalf("after enter on the header: open %v zooms %v note %q", m.cockpit.open, f.zooms, m.note)
	}
	if !strings.Contains(ansi.Strip(m.View()), "no sessions") {
		t.Fatal("no 'no sessions' line under #4")
	}
}

func TestCStartsASessionOnTheTask(t *testing.T) {
	for _, key := range []string{"task:/code/app#6", "s1"} {
		f := newFakeMux()
		m := taskModel(t, f)
		m = selectKey(t, m, key)
		m = pressSettled(t, m, "c")
		if len(f.spawned) != 1 {
			t.Fatalf("%s: spawned %+v", key, f.spawned)
		}
		call := f.spawned[0]
		if call.dir != "/code/app" || len(call.command) != 2 || call.command[1] != "Work on app#6: keep width" {
			t.Fatalf("%s: spawned %+v", key, call)
		}
		if p := f.panes[f.find(m.opened.ID)]; p.Project != "/code/app" || p.Task != "" {
			t.Fatalf("%s: the window has project %q task %q", key, p.Project, p.Task)
		}
	}
	// Elsewhere, c is a plain claude: on s2, and on s1's agents entry,
	// which sits under no workstream.
	for _, key := range []string{"s2", "agents/s1"} {
		f := newFakeMux()
		m := selectKey(t, taskModel(t, f), key)
		m = pressSettled(t, m, "c")
		if len(f.spawned) != 1 || len(f.spawned[0].command) != 1 {
			t.Fatalf("%s: spawned %+v", key, f.spawned)
		}
	}
}

func TestTOpensATerminalForTheTask(t *testing.T) {
	f := newFakeMux()
	m := selectKey(t, taskModel(t, f), "s1")
	m = pressSettled(t, m, "t")
	shell := f.panes[len(f.panes)-1]
	if shell.Project != "/code/app" || shell.Task != "/code/app#6" || m.sel != shell.Window {
		t.Fatalf("terminal %+v, sel %s", shell, m.sel)
	}
	want := []string{"task:/code/app#4", "task:/code/app#6", "s1", shell.Window, "s2"}
	if got := ids(m.sections()); !slices.Equal(got, want) {
		t.Fatalf("listed %v, want %v", got, want)
	}
	// Once #6 is done, the terminal is the project's, as its session is.
	m.trackers = trackers{"/code/app": tracked("app",
		issues.Issue{ID: 4, Ref: "app#4", Title: "show cost", Status: issues.Review, Priority: "p2"})}
	want = []string{"task:/code/app#4", "s1", "s2", shell.Window}
	if got := ids(m.sections()); !slices.Equal(got, want) {
		t.Fatalf("after #6 closed: listed %v, want %v", got, want)
	}
}

func TestKeysForASessionSayWhereToGoOnAWorkstream(t *testing.T) {
	m := selectKey(t, taskModel(t, newFakeMux()), "task:/code/app#6")
	m, cmd := step(m, noteMsg("")) // the rules show the new cursor
	m = settle(t, m, cmd)
	for _, k := range []string{"x", "v", "y"} {
		m2, cmd := press(m, k)
		if cmd != nil || m2.confirm != "" || !strings.Contains(m2.note, "move to one under #6") {
			t.Errorf("%s: cmd %v confirm %q note %q", k, cmd != nil, m2.confirm, m2.note)
		}
	}
}

func TestAnAliasActsAsItsSession(t *testing.T) {
	f := newFakeMux()
	m := selectKey(t, taskModel(t, f), "agents/s1")
	m = settle(t, m, nil)
	m, cmd := step(m, keyMsg("r"))
	m = settle(t, m, cmd)
	s1 := paneWithTTY(f, "ttys011")
	if !f.staged(s1) {
		t.Fatalf("list in %s, want beside %s", f.stage(), s1)
	}
	var copied string
	m.deps.copyText = func(s string) error { copied = s; return nil }
	m, cmd = press(m, "y")
	update(m, cmd())
	if copied != "s1" {
		t.Fatalf("y copied %q", copied)
	}
	m = pressSettled(t, m, "enter")
	if !slices.Equal(f.selects, []string{s1}) {
		t.Fatalf("selects %v", f.selects)
	}
}

func TestLeadIsTheFirstWithAPaneElseTheFirstSession(t *testing.T) {
	bg := entry{kind: sessionEntry, row: row("b", "bg", "", 0)}
	s := entry{kind: sessionEntry, row: row("s", "s", "", 1)}
	term := entry{kind: terminalEntry, term: terminal{Window: "@4", Pane: "%4"}}
	has := func(ids ...string) func(entry) bool {
		return func(e entry) bool { return slices.Contains(ids, e.key()) }
	}
	for _, c := range []struct {
		name string
		cs   []entry
		has  func(entry) bool
		want string
	}{
		{"a paneless first session", []entry{bg, s, term}, has("s", "@4"), "s"},
		{"only a terminal has a pane", []entry{bg, s, term}, has("@4"), "@4"},
		{"nothing has a pane", []entry{term, bg, s}, has(), "b"},
		{"only a paneless terminal", []entry{term}, has(), ""},
	} {
		got, ok := lead(c.cs, c.has)
		if (c.want == "") == ok || ok && got.key() != c.want {
			t.Errorf("%s: %s %v, want %q", c.name, got.key(), ok, c.want)
		}
	}
}

// enter on a workstream goes where the stage shows: to the first session
// under it with a pane, not to a background one listed above it.
func TestEnterOnAWorkstreamGoesWhereTheStageIs(t *testing.T) {
	f := newFakeMux()
	s1, bg := row("s1", "red", "", 11), row("b1", "bgwork", "", 0)
	bg.Kind, bg.ID, bg.State = "background", "abc123", fleet.Working
	s1.Cwd, bg.Cwd = "/code/app", "/code/app"
	s1.Tasks = []fleet.TaskRef{{Project: "app", ID: 6}}
	bg.Tasks = s1.Tasks
	m := stagedModel(t, f, s1, bg)
	ts := trackers{"/code/app": tracked("app", issues.Issue{ID: 6, Ref: "app#6", Title: "w", Status: issues.InProgress})}
	m.trackers = ts
	m, cmd := step(m, rowsMsg{rows: m.rows, ttys: m.ttys})
	m = settle(t, m, cmd)
	if got := ids(m.sections())[:3]; !slices.Equal(got, []string{"task:/code/app#6", "b1", "s1"}) {
		t.Fatalf("listed %v, want the background session first", got)
	}
	m = selectKey(t, m, "task:/code/app#6")
	m, cmd = step(m, keyMsg("r"))
	m = settle(t, m, cmd)
	pane := paneWithTTY(f, "ttys011")
	if !f.staged(pane) {
		t.Fatalf("list in %s, want beside s1's %s", f.stage(), pane)
	}
	pressSettled(t, m, "enter")
	if len(f.spawned) != 0 || !slices.Equal(f.selects, []string{pane}) {
		t.Fatalf("spawned %+v selects %v, want s1's pane", f.spawned, f.selects)
	}
}

// The list's project field names the project, whatever project.edn and
// the root's name say. An empty list falls back to them, and a failed run
// keeps the id the last good one gave.
func TestTheListNamesTheProject(t *testing.T) {
	f := newFakeIssues()
	f.project("/w/app", "edn-id")
	f.lists["/w/app"] = `{"issues": [{"id": 6, "ref": "listed#6", "project": "listed", "status": "in-progress", "title": "w"}]}`
	f.project("/w/empty", "edn-id")
	f.project("/w/bare", "")
	d := f.deps()
	ts := readAll(nil, []string{"/w/app", "/w/empty", "/w/bare"}, f.run, d, t0)
	for root, want := range map[string]string{"/w/app": "listed", "/w/empty": "edn-id", "/w/bare": "bare"} {
		if got := ts[root].id; got != want {
			t.Errorf("%s: id %q, want %q", root, got, want)
		}
	}
	f.fail["/w/app"] = true
	f.mtime["/w/app"] = t0.Add(time.Second)
	if got := readAll(ts, []string{"/w/app"}, f.run, d, t0.Add(time.Minute))["/w/app"]; got.err == nil || got.id != "listed" {
		t.Errorf("after a failed run: %+v", got)
	}
}

// The cursor on an alias stays with its session when the alias goes: to
// the session's entry under the same workstream, else its primary one. On
// a workstream whose issue closed it goes to the first session that was
// under it.
func TestTheCursorFollowsItsSessionWhenAnAliasGoes(t *testing.T) {
	s1, s2 := row("s1", "red", "", 11), row("s2", "blue", "", 12)
	s1.Cwd, s2.Cwd = "/code/app", "/code/app"
	ref := func(ids ...int) []fleet.TaskRef {
		var out []fleet.TaskRef
		for _, id := range ids {
			out = append(out, fleet.TaskRef{Project: "app", ID: id})
		}
		return out
	}
	list := func(s1refs []int, open ...int) []entry {
		r1, r2 := s1, s2
		r1.Tasks, r2.Tasks = ref(s1refs...), ref(6)
		m := newTestModel(t)
		var is []issues.Issue
		for _, id := range open {
			is = append(is, issue(id, issues.InProgress, "t"))
		}
		m.trackers = trackers{"/code/app": tracked("app", is...)}
		m = update(m, rowsMsg{rows: []fleet.Row{r1, r2}})
		return m.ordered()
	}
	for _, c := range []struct {
		name        string
		before, now []entry
		sel, want   string
	}{
		// s1 is under #6 and #7, primary on #7; #6 closes. The next entry
		// in the old order was s2.
		{"its primary issue stays", list([]int{6, 7}, 6, 7), list([]int{6, 7}, 7), "task:/code/app#6/s1", "s1"},
		// #7 closes, and s1's place under #6 becomes its primary entry.
		{"the same workstream", list([]int{6, 7}, 6, 7), list([]int{6, 7}, 6), "task:/code/app#6/s1", "s1"},
		// s1 re-links: now primary on #6, alias on #7.
		{"relinked", list([]int{6, 7}, 6, 7), list([]int{7, 6}, 6, 7), "task:/code/app#6/s1", "s1"},
		{"the workstream closed", list([]int{7}, 6, 7), list([]int{7}, 6), "task:/code/app#7", "s1"},
	} {
		if got, ok := nearKey(c.before, c.now, c.sel); !ok || got != c.want {
			t.Errorf("%s: %s %v, want %s", c.name, got, ok, c.want)
		}
	}
}

// A project whose last issues read failed says so on its rule, in place
// of its task count.
func TestARuleSaysTheIssuesFailed(t *testing.T) {
	m := taskModel(t, newFakeMux())
	m.w = 60
	tr := m.trackers["/code/app"]
	tr.err = errors.New("boom")
	m.trackers = trackers{"/code/app": tr}
	text := ansi.Strip(m.View())
	if !strings.Contains(text, "issues failed") || strings.Contains(text, "2 tasks") {
		t.Fatalf("rule:\n%s", text)
	}
	if secs := m.sections(); !secs[0].Failed || len(flatten(secs[:1])) != 4 {
		t.Fatalf("sections %+v: want the failed project with its workstreams", secs)
	}
}
