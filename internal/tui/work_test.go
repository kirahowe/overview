package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
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
	"overview/internal/world"
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
	if got := streamKeys(m.world().Sessions["s1"].Streams); !slices.Equal(got, []string{"/w/app#6"}) {
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
	if len(m.in.Rows) != 2 {
		t.Fatalf("rows %d: the refresh waited on issues", len(m.in.Rows))
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

// A project without project.edn has its root's name for an id, which a
// ref by that id links to.
func TestATrackerWithoutProjectEdnLinksByItsName(t *testing.T) {
	f := newFakeIssues()
	f.project("/w/tool", "", "3 in-progress t")
	got := readAll(nil, []string{"/w/tool"}, f.run, f.deps(), t0)
	r := fleet.Row{Live: fleet.Live{SessionID: "s", Cwd: "/w/tool"}, Tasks: []fleet.TaskRef{{Project: "tool", ID: 3}}}
	w := world.Derive(world.Inputs{Rows: []fleet.Row{r}, Trackers: got.inputs()})
	if keys := streamKeys(w.Sessions["s"].Streams); !slices.Equal(keys, []string{"/w/tool#3"}) {
		t.Errorf("missing project.edn: %v", keys)
	}
}

// streamKeys are the keys of ss.
func streamKeys(ss []*world.Stream) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.Key.String())
	}
	return out
}

// targetTask is the key of a cockpit target's workstream, "" for a
// project.
func targetTask(t target) string {
	if t.stream == nil {
		return ""
	}
	return t.stream.Key.String()
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
	m = m.setInputs(nil)
	m, cmd := step(m, rowsMsg{rows: m.in.Rows, ttys: m.in.TTYs})
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
	if need, _, _ := counts(m); need != 1 {
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
// list, and enter opens the cockpit with the keyboard still in the list.
// enter again leaves it open, as there is nothing to attach to.
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
	if !m.cockpit.open || !slices.Equal(f.zooms, []string{"%0 true"}) || !m.listHasKeys() || len(f.selects) != 0 {
		t.Fatalf("open %v zooms %v list has keys %v selects %v", m.cockpit.open, f.zooms, m.listHasKeys(), f.selects)
	}
	m = pressSettled(t, m, "enter")
	if !m.cockpit.open || len(f.zooms) != 1 || m.note != "No session works on #4. c starts one, t a terminal." {
		t.Fatalf("after enter again: open %v zooms %v note %q", m.cockpit.open, f.zooms, m.note)
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
	m = m.setInputs(nil)
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
	bg := entry{kind: sessionEntry, sess: &world.Session{Row: row("b", "bg", "", 0)}}
	s := entry{kind: sessionEntry, sess: &world.Session{Row: row("s", "s", "", 1)}}
	shown := entry{kind: sessionEntry, sess: &world.Session{Row: row("p", "p", "", 2), Pane: "%2"}}
	term := entry{kind: terminalEntry, term: &world.Terminal{Window: "@4", Pane: "%4"}}
	for _, c := range []struct {
		name string
		cs   []entry
		want string
	}{
		{"a paneless first session", []entry{bg, shown, term}, "p"},
		{"only a terminal has a pane", []entry{bg, s, term}, "@4"},
		{"nothing has a pane", []entry{bg, s}, "b"},
		{"nothing at all", nil, ""},
	} {
		got, ok := lead(c.cs)
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
	m = m.setInputs(nil)
	m, cmd := step(m, rowsMsg{rows: m.in.Rows, ttys: m.in.TTYs})
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
		m = m.setInputs(nil)
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
	m = m.setInputs(nil)
	text := ansi.Strip(m.View())
	if !strings.Contains(text, "issues failed") || strings.Contains(text, "2 tasks") {
		t.Fatalf("rule:\n%s", text)
	}
	if secs := m.sections(); !secs[0].Failed || len(flatten(secs[:1])) != 4 {
		t.Fatalf("sections %+v: want the failed project with its workstreams", secs)
	}
}

// The footer's enter hint says what enter does on every entry, in the
// list, in the cockpit and while searching, and is left out where enter
// only says how to start something: on #4, with no session, enter opens
// the cockpit, and in the cockpit it only says so. A search for zebra
// shows only the background session under #6, so enter attaches to it,
// though s1 leads #6 in the whole list.
func TestTheEnterHintSaysWhatEnterDoes(t *testing.T) {
	f := newFakeMux()
	m := taskModel(t, f)
	bg := fleet.Row{Live: fleet.Live{SessionID: "b1", ID: "b1", Name: "zebra", Kind: "background", Cwd: "/code/app"}, State: fleet.Idle}
	bg.Tasks = []fleet.TaskRef{{Project: "app", Dir: "/code/app", ID: 6}}
	obs := row("obs", "claude-bd", "", 63381)
	obs.Cwd = "/"
	m.deps.reveal = func(string) error { return nil }
	m, cmd := step(m, rowsMsg{rows: append(slices.Clone(m.in.Rows), bg, obs), ttys: m.in.TTYs, pinned: []string{"/code/empty"},
		hosts: map[int]world.Host{63381: {Name: "Obsidian", Path: "/Applications/Obsidian.app"}}})
	m = settle(t, m, cmd)
	m = pressSettled(t, selectKey(t, resized(t, m, 120, 30), "s2"), "t")
	said := regexp.MustCompile(`enter (open app|type|attach|cockpit)`)
	check := func(at model, where string) string {
		t.Helper()
		at.note = ""
		label := ""
		if got := said.FindStringSubmatch(ansi.Strip(at.footer())); got != nil {
			label = got[1]
		}
		after, _ := at.apply(action{do: open})
		did := ""
		switch {
		case after.entering != "":
			did = "type"
		case strings.HasPrefix(after.note, "attaching"):
			did = "attach"
		case strings.HasPrefix(after.note, "opening"):
			did = "open app"
		case after.cockpit.open && !at.cockpit.open:
			did = "cockpit"
		}
		if label != did {
			t.Errorf("%s: the footer says enter %q, enter did %q (note %q)", where, label, did, after.note)
		}
		return did
	}
	seen := map[string]bool{}
	for _, cockpit := range []bool{false, true} {
		for _, e := range m.ordered() {
			at := selectKey(t, m, e.key())
			if cockpit {
				at = pressSettled(t, at, "o")
			}
			seen[check(at, fmt.Sprintf("cockpit %v, on %s", cockpit, e.key()))] = true
		}
	}
	for _, q := range []string{"zebra", "#6", "obsidian", "red", "empty"} {
		at := pressSettled(t, m, append([]string{"/"}, typed(q)...)...)
		seen[check(at, "searching "+q)] = true
		at = pressSettled(t, at, "down")
		seen[check(at, "searching "+q+", one down")] = true
	}
	for _, k := range []string{"", "type", "attach", "open app", "cockpit"} {
		if !seen[k] {
			t.Errorf("no entry where enter does %q", k)
		}
	}
	// The search for zebra puts the cursor on #6, and enter attaches.
	at := pressSettled(t, m, append([]string{"/"}, typed("zebra")...)...)
	if e, _, _ := at.selected(at.ordered()); e.key() != "task:/code/app#6" {
		t.Fatalf("searching zebra, the cursor is on %s", e.key())
	}
	if after, _ := at.apply(action{do: open}); !strings.HasPrefix(after.note, "attaching to zebra") {
		t.Fatalf("enter on #6 searching zebra: note %q, entering %q", after.note, after.entering)
	}
}

// A note answers the last thing the user did, so the next key that means
// something clears it: l into the issues card shows the card's keys, not
// the note enter left.
func TestTheNextKeyClearsANote(t *testing.T) {
	f := newFakeMux()
	m := pressSettled(t, taskModel(t, f), "g", "enter", "enter")
	if !m.cockpit.open || !strings.HasPrefix(m.note, "No session works on #4") {
		t.Fatalf("open %v, note %q", m.cockpit.open, m.note)
	}
	m = pressSettled(t, m, "l")
	foot := ansi.Strip(m.footer())
	if m.note != "" || !m.inIssues() || !strings.Contains(foot, "j k move") {
		t.Fatalf("note %q, in the issues %v, footer %q", m.note, m.inIssues(), foot)
	}
	// A key that means nothing leaves a note be.
	m = m.say("kept")
	if m = pressSettled(t, m, "%"); m.note != "kept" {
		t.Fatalf("note %q after a key bound to nothing", m.note)
	}
}

// The wheel only looks, so it moves the cursor and leaves the note; a
// click on a row clears it, as a key does.
func TestTheWheelLeavesANote(t *testing.T) {
	m := taskModel(t, newFakeMux()).say("kept")
	sel := m.sel
	m, cmd := step(m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown, X: 2, Y: 2})
	m = settle(t, m, cmd)
	if m.note != "kept" || m.sel == sel {
		t.Fatalf("after the wheel: note %q, sel %s (was %s)", m.note, m.sel, sel)
	}
	m, cmd = step(m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 2, Y: 2})
	m = settle(t, m, cmd)
	if m.note != "" {
		t.Fatalf("after a click: note %q", m.note)
	}
}
