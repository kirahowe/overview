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
	if got := streamKeys(m.world().Sessions["s1"].Holds); !slices.Equal(got, []string{"/w/app#6"}) {
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
	if keys := streamKeys(w.Sessions["s"].Holds); !slices.Equal(keys, []string{"/w/tool#3"}) {
		t.Errorf("missing project.edn: %v", keys)
	}
}

// streamKeys are the keys of ss.
func streamKeys(ss []*world.Task) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.Key.String())
	}
	return out
}

// targetTask is the key of a cockpit target's task, "" for a project.
func targetTask(t target) string {
	if t.task == nil {
		return ""
	}
	return t.task.Key.String()
}

// taskModel runs in the fake tmux with two sessions in /code/app, which
// tracks its issues: s1 works on #6, and #4 is in review, waiting on you.
// s2 is linked to what refs give, nothing by default.
func taskModel(t *testing.T, f *fakeMux, refs ...fleet.TaskRef) model {
	t.Helper()
	s1, s2 := row("s1", "red", "", 11), row("s2", "blue", "", 12)
	s1.Cwd, s2.Cwd = "/code/app", "/code/app"
	s1.Tasks = []fleet.TaskRef{{Project: "app", Dir: "/code/app", ID: 6}}
	s2.Tasks = refs
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
	want := []string{"s1", "s2", "task:/code/app#4"}
	if got := ids(m.sections()); !slices.Equal(got, want) {
		t.Fatalf("listed %v, want %v", got, want)
	}
	if need, _, _ := counts(m); need != 1 {
		t.Fatalf("need %d, want the review", need)
	}
	if got := ansi.Strip(m.View()); !strings.Contains(got, "red") || !strings.Contains(got, "#6") {
		t.Fatalf("s1's row does not say it holds #6:\n%s", got)
	}
}

// enter on an issue that waits on you goes to the session linked to it,
// so you can ask it about the work, and the cursor stays on the issue.
func TestEnterOnAWaitingIssueGoesToItsSession(t *testing.T) {
	f := newFakeMux()
	m := taskModel(t, f, fleet.TaskRef{Project: "app", Dir: "/code/app", ID: 4})
	m = selectKey(t, m, "task:/code/app#4")
	m = settle(t, m, nil)
	m, cmd := step(m, keyMsg("r")) // any update restages
	m = settle(t, m, cmd)
	s2 := paneWithTTY(f, "ttys012")
	if !f.staged(s2) {
		t.Fatalf("on #4 the list is in %s, want beside s2's %s", f.stage(), s2)
	}
	m = pressSettled(t, m, "enter")
	if !slices.Equal(f.selects, []string{s2}) {
		t.Fatalf("selects %v, want [%s]", f.selects, s2)
	}
	if m.sel != "task:/code/app#4" {
		t.Fatalf("enter moved the cursor to %s", m.sel)
	}
}

// An issue no session is linked to shows its cockpit's cards beside the
// list, and enter opens the cockpit with the keyboard still in the list.
// enter again leaves it open, as there is nothing to attach to.
func TestAnIssueWithNoSessionsShowsItsCockpit(t *testing.T) {
	f := newFakeMux()
	m := taskModel(t, f)
	m = selectKey(t, m, "task:/code/app#4")
	m, cmd := step(m, keyMsg("r")) // any update restages
	m = settle(t, m, cmd)
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
	if !m.cockpit.open || len(f.zooms) != 1 || m.note != "No session is linked to #4. c starts one, t a terminal." {
		t.Fatalf("after enter again: open %v zooms %v note %q", m.cockpit.open, f.zooms, m.note)
	}
}

// c on a session that holds one task, from either of its entries, starts
// another session on that task; on an issue, one on the issue.
func TestCStartsASessionOnTheTask(t *testing.T) {
	for _, key := range []string{"s1", "agents/s1"} {
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
	f := newFakeMux()
	pressSettled(t, selectKey(t, taskModel(t, f), "task:/code/app#4"), "c")
	if len(f.spawned) != 1 || len(f.spawned[0].command) != 2 || f.spawned[0].command[1] != "Work on app#4: show cost" {
		t.Fatalf("on #4: spawned %+v", f.spawned)
	}
	// Elsewhere, c is a plain claude: on s2, which holds no task, and on
	// the project's heading.
	for _, key := range []string{"s2", "project:/code/app"} {
		f := newFakeMux()
		m := selectKey(t, taskModel(t, f), key)
		m = pressSettled(t, m, "c")
		if len(f.spawned) != 1 || len(f.spawned[0].command) != 1 {
			t.Fatalf("%s: spawned %+v", key, f.spawned)
		}
	}
}

// With every session linked to an issue, the project's heading is where c
// starts a session on none: a plain claude at the project's root. The heading
// shows the project's cockpit beside the list, enter says what starts
// there, and x has nothing to unpin.
func TestCOnAProjectsHeadingStartsASessionOnNoTask(t *testing.T) {
	f := newFakeMux()
	s1, s2 := row("s1", "red", "", 11), row("s2", "blue", "", 12)
	s1.Cwd, s2.Cwd = "/code/app", "/code/app"
	s1.Tasks = []fleet.TaskRef{{Project: "app", Dir: "/code/app", ID: 6}}
	s2.Tasks = []fleet.TaskRef{{Project: "app", Dir: "/code/app", ID: 4}}
	m := stagedModel(t, f, s1, s2)
	m.trackers = trackers{"/code/app": tracked("app",
		issues.Issue{ID: 6, Ref: "app#6", Title: "keep width", Status: issues.InProgress, Priority: "p1"},
		issues.Issue{ID: 4, Ref: "app#4", Title: "show cost", Status: issues.Review, Priority: "p2"},
	)}
	m = m.setInputs(nil)
	m, cmd := step(m, rowsMsg{rows: m.in.Rows, ttys: m.in.TTYs})
	m = settle(t, m, cmd)
	if m.sel == "project:/code/app" {
		t.Fatal("the cursor starts on the heading, not the first row")
	}
	if m = pressSettled(t, m, "j", "g"); m.sel != "s1" {
		t.Fatalf("g goes to %s, want the first row under the heading", m.sel)
	}
	m = pressSettled(t, m, "g", "k")
	if m.sel != "project:/code/app" || !f.staged("%1") || !strings.Contains(ansi.Strip(m.View()), "▌/code/app ─") {
		t.Fatalf("g k: sel %s, list in %s\n%s", m.sel, f.stage(), ansi.Strip(m.View()))
	}
	if text := ansi.Strip(f.drawn["ttys901"]); !strings.Contains(text, "#4") || !strings.Contains(text, "#6") {
		t.Fatalf("the holder shows no cockpit cards: %q", text)
	}
	if m = pressSettled(t, m, "enter"); len(f.selects) != 0 || m.note != "c starts Claude in /code/app on no task, s one on a task, t a terminal." {
		t.Fatalf("enter: selects %v, note %q", f.selects, m.note)
	}
	if m = pressSettled(t, m, "x"); m.confirm != "" || !strings.Contains(m.note, "not pinned") {
		t.Fatalf("x: confirm %q, note %q", m.confirm, m.note)
	}
	m = pressSettled(t, m, "c")
	if len(f.spawned) != 1 || f.spawned[0].dir != "/code/app" || len(f.spawned[0].command) != 1 {
		t.Fatalf("spawned %+v", f.spawned)
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
	want := []string{"s1", "s2", shell.Window, "task:/code/app#4"}
	if got := ids(m.sections()); !slices.Equal(got, want) {
		t.Fatalf("listed %v, want %v", got, want)
	}
	if tm := m.world().Terminals[0]; tm.Task == nil || tm.Task.Key.String() != "/code/app#6" {
		t.Fatalf("the terminal's task %+v", tm.Task)
	}
	// Once #6 is done, the terminal has no task, and stays where it was.
	m.trackers = trackers{"/code/app": tracked("app",
		issues.Issue{ID: 4, Ref: "app#4", Title: "show cost", Status: issues.Review, Priority: "p2"})}
	m = m.setInputs(nil)
	if got := ids(m.sections()); !slices.Equal(got, want) {
		t.Fatalf("after #6 closed: listed %v, want %v", got, want)
	}
	if tm := m.world().Terminals[0]; tm.Task != nil {
		t.Fatalf("the terminal keeps %s", tm.Task.Key)
	}
}

func TestKeysForASessionSayAnIssueIsNone(t *testing.T) {
	m := selectKey(t, taskModel(t, newFakeMux()), "task:/code/app#4")
	m, cmd := step(m, noteMsg("")) // the rules show the new cursor
	m = settle(t, m, cmd)
	for _, k := range []string{"x", "v", "y"} {
		m2, cmd := press(m, k)
		if cmd != nil || m2.confirm != "" || !strings.Contains(m2.note, "#4 is an issue") {
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
		task := &world.Task{}
		for _, e := range c.cs {
			if e.kind == sessionEntry {
				task.Sessions = append(task.Sessions, e.sess)
			} else {
				task.Terminals = append(task.Terminals, e.term)
			}
		}
		got, ok := lead(c.cs, task)
		if (c.want == "") == ok || ok && got.key() != c.want {
			t.Errorf("%s: %s %v, want %q", c.name, got.key(), ok, c.want)
		}
	}
}

// enter on an issue goes where the stage shows: to the first session
// under it with a pane, not to a background one listed above it.
func TestEnterOnAnIssueGoesWhereTheStageIs(t *testing.T) {
	f := newFakeMux()
	s1, bg := row("s1", "red", "", 11), row("b1", "bgwork", "", 0)
	bg.Kind, bg.ID, bg.State = "background", "abc123", fleet.Working
	s1.Cwd, bg.Cwd = "/code/app", "/code/app"
	s1.Tasks = []fleet.TaskRef{{Project: "app", ID: 6}}
	bg.Tasks = s1.Tasks
	m := stagedModel(t, f, s1, bg)
	ts := trackers{"/code/app": tracked("app", issues.Issue{ID: 6, Ref: "app#6", Title: "w", Status: issues.Review})}
	m.trackers = ts
	m = m.setInputs(nil)
	m, cmd := step(m, rowsMsg{rows: m.in.Rows, ttys: m.in.TTYs})
	m = settle(t, m, cmd)
	if got := m.world().Tasks[world.Key{Root: "/code/app", ID: 6}].Sessions; len(got) != 2 || got[0].SessionID != "b1" {
		t.Fatalf("#6's sessions %v, want the background session first", got)
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

// The cursor on an issue that waited on you follows it when a session
// picks it up, to that session's row. An issue that leaves the list with
// nobody on it leaves the cursor nearby, as any entry does.
func TestTheCursorFollowsAnIssueToItsSession(t *testing.T) {
	s1, s2 := row("s1", "red", "", 11), row("s2", "blue", "", 12)
	s1.Cwd, s2.Cwd = "/code/app", "/code/app"
	list := func(s1refs []int, open ...issues.Issue) []entry {
		r1 := s1
		for _, id := range s1refs {
			r1.Tasks = append(r1.Tasks, fleet.TaskRef{Project: "app", ID: id})
		}
		m := newTestModel(t)
		m.trackers = trackers{"/code/app": tracked("app", open...)}
		m = m.setInputs(nil)
		m = update(m, rowsMsg{rows: []fleet.Row{r1, s2}})
		return m.ordered()
	}
	stalled, ready := issue(7, issues.InProgress, "t"), issue(7, issues.Ready, "t")
	before := list(nil, stalled)
	if got := keys(before); !slices.Equal(got, []string{"project:/code/app", "s2", "s1", "task:/code/app#7", "agents/s2", "agents/s1"}) &&
		!slices.Equal(got, []string{"project:/code/app", "s1", "s2", "task:/code/app#7", "agents/s1", "agents/s2"}) {
		t.Fatalf("before: %v", got)
	}
	if got, ok := nearKey(before, list([]int{7}, stalled), "task:/code/app#7"); !ok || got != "s1" {
		t.Errorf("picked up: %s %v, want s1", got, ok)
	}
	if got, ok := samePlace(before, list(nil, ready), "task:/code/app#7"); ok {
		t.Errorf("put back to ready: samePlace gave %s", got)
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
		t.Fatalf("sections %+v: want the failed project with what is in it", secs)
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
	// The search for zebra puts the cursor on it, and enter attaches.
	at := pressSettled(t, m, append([]string{"/"}, typed("zebra")...)...)
	if e, _, _ := at.selected(at.ordered()); e.kind != sessionEntry || e.sess.Name != "zebra" {
		t.Fatalf("searching zebra, the cursor is on %s", e.key())
	}
	if after, _ := at.apply(action{do: open}); !strings.HasPrefix(after.note, "attaching to zebra") {
		t.Fatalf("enter searching zebra: note %q, entering %q", after.note, after.entering)
	}
}

// A note answers the last thing the user did, so the next key that means
// something clears it: l into the issues card shows the card's keys, not
// the note enter left.
func TestTheNextKeyClearsANote(t *testing.T) {
	f := newFakeMux()
	m := pressSettled(t, selectKey(t, taskModel(t, f), "task:/code/app#4"), "enter", "enter")
	if !m.cockpit.open || !strings.HasPrefix(m.note, "No session is linked to #4") {
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
