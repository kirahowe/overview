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

// After a refresh the model reads the trackers of the sessions' projects,
// and runs the issues tool once for a project however often it refreshes.
func TestRefreshReadsTrackers(t *testing.T) {
	f := newFakeIssues()
	f.project("/w/app", "app", "6 in-progress Rework")
	m := newTestModel(t)
	m.deps = f.deps()
	m.deps.exists = func(p string) bool {
		return p == "/w/app/.jj" || strings.HasSuffix(p, ".issues") && f.lists[path.Dir(p)] != ""
	}
	m.issues = f.run
	r := row("s1", "red", "", 11)
	r.Cwd = "/w/app/sub"
	m, cmd := step(m, m.rowsMsg([]fleet.Row{r}, nil, nil, nil))
	m = settle(t, m, cmd)
	if _, ok := m.trackers["/w/app"]; !ok || f.runs["/w/app"] != 1 {
		t.Fatalf("trackers %v runs %v", m.trackers, f.runs)
	}
	m, cmd = step(m, m.rowsMsg([]fleet.Row{r}, nil, nil, nil))
	m = settle(t, m, cmd)
	if f.runs["/w/app"] != 1 {
		t.Fatalf("a second refresh ran issues again: %v", f.runs)
	}
	// A project that leaves the list takes its tracker with it.
	r.Cwd = "/w/other"
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

// taskModel runs in the fake tmux with two sessions in /code/app, which
// tracks its issues: #6 is in progress, and #4 is in review, waiting on you.
func taskModel(t *testing.T, f *fakeMux) model {
	t.Helper()
	s1, s2 := row("s1", "red", "", 11), row("s2", "blue", "", 12)
	s1.Cwd, s2.Cwd = "/code/app", "/code/app"
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

// The list holds the sessions only: an issue in review is not a row. It
// counts as waiting on you, and the project's heading says so.
func TestTaskModelLists(t *testing.T) {
	m := taskModel(t, newFakeMux())
	want := []string{"s1", "s2"}
	if got := ids(m.sections()); !slices.Equal(got, want) {
		t.Fatalf("listed %v, want %v", got, want)
	}
	if need, _, _ := counts(m); need != 1 {
		t.Fatalf("need %d, want the review", need)
	}
	got := ansi.Strip(m.View())
	if !strings.Contains(got, "1 to review") || strings.Contains(got, "#4") {
		t.Fatalf("the heading does not say 1 to review, or the list shows the issue:\n%s", got)
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

// A project whose last issues read failed says so on its rule, in place
// of its count of issues to review.
func TestARuleSaysTheIssuesFailed(t *testing.T) {
	m := taskModel(t, newFakeMux())
	m.w = 60
	tr := m.trackers["/code/app"]
	tr.err = errors.New("boom")
	m.trackers = trackers{"/code/app": tr}
	m = m.setInputs(nil)
	text := ansi.Strip(m.View())
	if !strings.Contains(text, "issues failed") || strings.Contains(text, "to review") {
		t.Fatalf("rule:\n%s", text)
	}
	if secs := m.sections(); !secs[0].Failed || len(flatten(secs[:1])) != 3 {
		t.Fatalf("sections %+v: want the failed project with what is in it", secs)
	}
}

// The footer's enter hint says what enter does on every entry, in the
// list, in the cockpit and while searching, and is left out where enter
// only says how to start something, as on a project's heading. A search for
// zebra shows only the background session, so enter attaches to it.
func TestTheEnterHintSaysWhatEnterDoes(t *testing.T) {
	f := newFakeMux()
	m := taskModel(t, f)
	bg := fleet.Row{Live: fleet.Live{SessionID: "b1", ID: "b1", Name: "zebra", Kind: "background", Cwd: "/code/app"}, State: fleet.Idle}
	obs := row("obs", "claude-bd", "", 63381)
	obs.Cwd = "/"
	m.deps.reveal = func(string) error { return nil }
	m, cmd := step(m, rowsMsg{rows: append(slices.Clone(m.in.Rows), bg, obs), ttys: m.in.TTYs, pinned: []string{"/code/empty"},
		hosts: map[int]world.Host{63381: {Name: "Obsidian", Path: "/Applications/Obsidian.app"}}})
	m = settle(t, m, cmd)
	m = pressSettled(t, selectKey(t, resized(t, m, 120, 30), "s2"), "t")
	said := regexp.MustCompile(`enter (open app|type|attach)`)
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
	for _, q := range []string{"zebra", "obsidian", "red", "empty"} {
		at := pressSettled(t, m, append([]string{"/"}, typed(q)...)...)
		seen[check(at, "searching "+q)] = true
		at = pressSettled(t, at, "down")
		seen[check(at, "searching "+q+", one down")] = true
	}
	for _, k := range []string{"", "type", "attach", "open app"} {
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
// the note left before it.
func TestTheNextKeyClearsANote(t *testing.T) {
	f := newFakeMux()
	m := resized(t, selectKey(t, taskModel(t, f), "project:/code/app"), 150, 40)
	m = pressSettled(t, m, "enter") // a project's heading only says how to start something
	if m.note == "" {
		t.Fatal("enter on a heading left no note")
	}
	m = pressSettled(t, m, "o")
	m = m.say("an answer")
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
