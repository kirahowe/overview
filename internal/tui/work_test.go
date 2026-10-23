package tui

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

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
}

func newFakeIssues() *fakeIssues {
	return &fakeIssues{lists: map[string]string{}, edn: map[string]string{}, mtime: map[string]time.Time{},
		runs: map[string]int{}, fail: map[string]bool{}}
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

func TestReadTrackersRunsOnlyWhenIssuesChange(t *testing.T) {
	f := newFakeIssues()
	f.project("/w/app", "app", "6 in-progress Rework", "7 done Old")
	f.project("/w/lib", "", "1 review Docs")
	roots := []string{"/w/app", "/w/lib", "/w/plain"}
	d := f.deps()

	ts := readTrackers(nil, roots, f.run, d, t0)
	if len(ts) != 2 || f.runs["/w/app"] != 1 || f.runs["/w/lib"] != 1 || f.runs["/w/plain"] != 0 {
		t.Fatalf("first read: %v runs %v", ts, f.runs)
	}
	if app := ts["/w/app"]; app.id != "app" || len(app.issues) != 1 || app.issues[0].ID != 6 {
		t.Fatalf("app: %+v (only open issues, id from project.edn)", app)
	}
	if lib := ts["/w/lib"]; lib.id != "lib" {
		t.Fatalf("lib without project.edn has id %q, want its root's name", lib.id)
	}

	again := readTrackers(ts, roots, f.run, d, t0.Add(time.Minute))
	if f.runs["/w/app"] != 1 || f.runs["/w/lib"] != 1 {
		t.Fatalf("unchanged .issues ran again: %v", f.runs)
	}
	if len(again["/w/app"].issues) != 1 {
		t.Fatalf("kept tracker lost its issues: %+v", again["/w/app"])
	}

	f.project("/w/app", "app", "6 review Rework")
	f.mtime["/w/app"] = t0.Add(2 * time.Second)
	changed := readTrackers(again, roots, f.run, d, t0.Add(2*time.Minute))
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
	ts := readTrackers(nil, []string{"/w/app"}, f.run, d, t0)

	f.fail["/w/app"] = true
	f.mtime["/w/app"] = t0.Add(time.Second)
	failed := readTrackers(ts, []string{"/w/app"}, f.run, d, t0.Add(time.Minute))
	if app := failed["/w/app"]; app.err == nil || len(app.issues) != 1 || f.runs["/w/app"] != 2 {
		t.Fatalf("after a failed run: %+v, runs %d", app, f.runs["/w/app"])
	}
	// Soon after, nothing changed: no new run.
	readTrackers(failed, []string{"/w/app"}, f.run, d, t0.Add(time.Minute+5*time.Second))
	if f.runs["/w/app"] != 2 {
		t.Fatalf("retried within %v: %d runs", retryAfter, f.runs["/w/app"])
	}
	f.fail["/w/app"] = false
	ok := readTrackers(failed, []string{"/w/app"}, f.run, d, t0.Add(time.Minute+retryAfter+time.Second))
	if app := ok["/w/app"]; app.err != nil || f.runs["/w/app"] != 3 {
		t.Fatalf("no retry after %v: %+v, runs %d", retryAfter, app, f.runs["/w/app"])
	}
}

func TestReadTrackersWithoutARunner(t *testing.T) {
	f := newFakeIssues()
	f.project("/w/app", "app", "6 in-progress Rework")
	if ts := readTrackers(nil, []string{"/w/app"}, nil, f.deps(), t0); len(ts) != 0 {
		t.Fatalf("no runner, yet %v", ts)
	}
}

// The refresh reads the trackers of the sessions' projects and of their
// task refs' directories, and travels back with them.
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
	msg := m.rowsMsg([]fleet.Row{r}, nil, nil, nil)
	if _, ok := msg.trackers["/w/app"]; !ok || f.runs["/w/app"] != 1 || f.runs["/w/app-wt"] != 1 {
		t.Fatalf("trackers %v runs %v", msg.trackers, f.runs)
	}
	m = update(m, msg)
	if got := m.tasks().links["s1"]; !slices.Equal(got, []string{"/w/app#6"}) {
		t.Fatalf("links %v, want the worktree's ref on the project", got)
	}
	m.issues = f.run
	update(m, m.rowsMsg([]fleet.Row{r}, nil, nil, nil))
	if f.runs["/w/app"] != 1 {
		t.Fatalf("a second refresh ran issues again: %v", f.runs)
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
	got := readTrackers(nil, []string{"/w/tool"}, f.run, f.deps(), t0)
	if k := resolveTask(fleet.TaskRef{Project: "tool", ID: 3}, "/w/tool", got, map[string]bool{"/w/tool": true}, root); k != "/w/tool#3" {
		t.Errorf("missing project.edn: %q", k)
	}
}

func TestResolveLinksKeepsTheLatestLast(t *testing.T) {
	ts := trackers{"/w/app": tracked("app", issue(1, issues.Ready, ""), issue(2, issues.Ready, ""), issue(3, issues.Done, ""))}
	r := fleet.Row{Live: fleet.Live{SessionID: "s", Cwd: "/w/app"}, Tasks: []fleet.TaskRef{
		{Project: "app", ID: 1}, {Project: "app", ID: 2}, {Dir: "/w/app", ID: 1}, {Project: "app", ID: 3},
	}}
	got := resolveLinks([]fleet.Row{r}, ts, map[string]bool{"/w/app": true}, func(d string) string { return d })
	if want := []string{"/w/app#2", "/w/app#1"}; !slices.Equal(got["s"], want) {
		t.Fatalf("links %v, want %v", got["s"], want)
	}
}
