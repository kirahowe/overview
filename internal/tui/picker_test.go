package tui

import (
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"pgregory.net/rapid"

	"overview/internal/fleet"
	"overview/internal/world"
)

const testHome = "/u/kira"

// testShort is fleet.ShortDir for a user whose home is testHome.
func testShort(dir string) string { return fleet.ShortDir(dir, testHome) }

func TestSplitPathQuery(t *testing.T) {
	for _, c := range []struct{ q, dir, prefix string }{
		{"/", "/", ""},
		{"/us", "/", "us"},
		{"/Users/ki", "/Users", "ki"},
		{"/Users/kira/", "/Users/kira", ""},
		{"~", testHome, ""},
		{"~/", testHome, ""},
		{"~/code/ov", testHome + "/code", "ov"},
		{"~/code//x", testHome + "/code", "x"},
	} {
		dir, prefix := splitPathQuery(c.q, testHome)
		if dir != c.dir || prefix != c.prefix {
			t.Errorf("%q: (%q, %q), want (%q, %q)", c.q, dir, prefix, c.dir, c.prefix)
		}
		if src := pickSource(c.q, testHome); src != c.dir {
			t.Errorf("%q: source %q", c.q, src)
		}
	}
	if pickSource("code ov", testHome) != "" || pickSource("", testHome) != "" {
		t.Error("words need the folders beside the projects")
	}
}

func TestPickParents(t *testing.T) {
	got := pickParents([]string{"/c/b", "", "/c/a", "/", "/d/x", "/c/b"})
	if !slices.Equal(got, []string{"/c", "/d"}) {
		t.Fatalf("parents %v", got)
	}
}

func TestPickCompleteAndTarget(t *testing.T) {
	for _, c := range []struct{ q, match, want string }{
		{"~/co", testHome + "/code", "~/code/"},
		{"/u/kira/co", testHome + "/code", "/u/kira/code/"},
		{"ov", "/w/overview", "/w/overview/"},
		{"~/x", "/elsewhere/x", "/elsewhere/x/"},
	} {
		if got := pickComplete(c.q, testHome, c.match); got != c.want {
			t.Errorf("complete %q to %q: %q, want %q", c.q, c.match, got, c.want)
		}
	}
	if p, typed := pickTarget("ov", testHome, []string{"/w/a", "/w/b"}, 1); p != "/w/b" || typed {
		t.Errorf("target %q %v", p, typed)
	}
	if p, typed := pickTarget("~/new/", testHome, nil, 0); p != testHome+"/new" || !typed {
		t.Errorf("typed target %q %v", p, typed)
	}
	if p, _ := pickTarget("nothing", testHome, nil, 0); p != "" {
		t.Errorf("words with no match target %q", p)
	}
}

var pickNames = []string{"alpha", "Beta", "app", ".git", ".hidden", "beta-two", "zed"}

func genPickPaths(t *rapid.T) []string {
	return rapid.SliceOf(rapid.Custom(func(t *rapid.T) string {
		return rapid.SampledFrom([]string{"/w", "/x/y", testHome}).Draw(t, "dir") + "/" + rapid.SampledFrom(pickNames).Draw(t, "name")
	})).Draw(t, "paths")
}

// Words keep exactly the paths that hold every word in their short form,
// are not hidden and are not shown already, each once and in order.
func TestPropPickMatchesWords(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		paths := genPickPaths(t)
		shown := map[string]bool{}
		for _, p := range paths {
			if rapid.IntRange(0, 4).Draw(t, "shown") == 0 {
				shown[p] = true
			}
		}
		q := strings.Join(rapid.SliceOfN(rapid.SampledFrom([]string{"a", "BE", "two", "w", "y/al", "zz"}), 0, 2).Draw(t, "q"), " ")
		got := pickMatches(q, testHome, paths, shown, testShort)
		var want []string
		for _, p := range paths {
			ok := !shown[p] && !strings.HasPrefix(filepath.Base(p), ".") && !slices.Contains(want, p)
			for _, w := range strings.Fields(strings.ToLower(q)) {
				ok = ok && strings.Contains(strings.ToLower(testShort(p)), w)
			}
			if ok {
				want = append(want, p)
			}
		}
		if len(got) != len(want) {
			t.Fatalf("%q: got %v, want %v", q, got, want)
		}
		for i, p := range got {
			if !slices.Contains(want, p) {
				t.Fatalf("%q matched %s", q, p)
			}
			if i > 0 && !world.NameLess(testShort(got[i-1]), testShort(p)) && !(testShort(got[i-1]) == testShort(p) && got[i-1] < p) {
				t.Fatalf("out of order: %v", got)
			}
		}
	})
}

// A path query keeps exactly the paths whose name starts with what was
// typed after the last slash, ignoring case, hidden ones only when that
// starts with a dot. tab then makes the query list the inside of the match.
func TestPropPickMatchesPath(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dir := rapid.SampledFrom([]string{"/w", "/x/y", testHome}).Draw(t, "dir")
		var paths []string
		for _, n := range rapid.SliceOfDistinct(rapid.SampledFrom(pickNames), rapid.ID[string]).Draw(t, "names") {
			paths = append(paths, dir+"/"+n)
		}
		prefix := rapid.SampledFrom([]string{"", "a", "B", "be", ".", ".h", "q"}).Draw(t, "prefix")
		q := dir + "/" + prefix
		if dir == testHome && rapid.Bool().Draw(t, "tilde") {
			q = "~/" + prefix
		}
		shown := map[string]bool{} // a path query offers projects shown already
		for _, p := range paths {
			shown[p] = true
		}
		got := pickMatches(q, testHome, paths, shown, testShort)
		var want []string
		for _, p := range paths {
			name := filepath.Base(p)
			if strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)) && (!strings.HasPrefix(name, ".") || strings.HasPrefix(prefix, ".")) {
				want = append(want, p)
			}
		}
		sort.Strings(want)
		sorted := slices.Clone(got)
		sort.Strings(sorted)
		if !slices.Equal(sorted, want) {
			t.Fatalf("%q: got %v, want %v", q, got, want)
		}
		for _, p := range got {
			c := pickComplete(q, testHome, p)
			if pickSource(c, testHome) != p || strings.HasPrefix(c, "~") != strings.HasPrefix(q, "~") {
				t.Fatalf("tab on %s turned %q into %q", p, q, c)
			}
		}
	})
}

// fakeDirs is a directory tree for readDirs: each directory's children by
// name.
type fakeDirs map[string][]string

func useDirs(m *model, d fakeDirs) *[]string {
	var reads []string
	m.deps.readDirs = func(dir string) ([]string, error) {
		reads = append(reads, dir)
		names, ok := d[dir]
		if !ok {
			return nil, &fsError{dir}
		}
		return names, nil
	}
	return &reads
}

type fsError struct{ dir string }

func (e *fsError) Error() string { return e.dir + ": no such directory" }

// pickerModel lists red in /code/app and lets a pick of a directory under
// /code find its root as itself.
func pickerModel(t *testing.T, f *fakeMux) model {
	t.Helper()
	m := repoModel(t, f)
	m.user = testHome
	m.deps.exists = func(p string) bool { return p == "/code/app/.jj" || p == "/code/shop/.jj" || p == "/code/blog/.jj" }
	return refreshed(t, m)
}

func TestAPinsAPickedProjectAndShowsIt(t *testing.T) {
	f := newFakeMux()
	m := pickerModel(t, f)
	reads := useDirs(&m, fakeDirs{"/code": {"app", "blog", "shop", ".cache"}})
	m = pressSettled(t, m, "a")
	if !m.pick.open || !slices.Equal(*reads, []string{"/code"}) {
		t.Fatalf("open %v, read %v", m.pick.open, *reads)
	}
	// app is shown already, and .cache is hidden.
	if got := m.pickMatches(); !slices.Equal(got, []string{"/code/blog", "/code/shop"}) {
		t.Fatalf("matches %v", got)
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "add project") || !strings.Contains(view, "/code/shop") || strings.Contains(view, "red") {
		t.Fatalf("view %s", view)
	}
	m = pressSettled(t, m, "s", "h")
	if got := m.pickMatches(); !slices.Equal(got, []string{"/code/shop"}) || len(*reads) != 1 {
		t.Fatalf("matches %v, reads %v", got, *reads)
	}
	m = pressSettled(t, m, "enter")
	if m.pick.open || !slices.Equal(m.home.Projects(), []string{"/code/shop"}) {
		t.Fatalf("open %v, pinned %v", m.pick.open, m.home.Projects())
	}
	if m.sel != "project:/code/shop" || !strings.Contains(m.note, "pinned") || !f.staged("%1") {
		t.Fatalf("sel %s, note %q, list in %s", m.sel, m.note, f.stage())
	}
}

func TestAPathQueryCompletesAndPinsWhatWasTyped(t *testing.T) {
	f := newFakeMux()
	m := pickerModel(t, f)
	reads := useDirs(&m, fakeDirs{"/code": {"app"}, testHome: {"code", "Documents", ".config"}, testHome + "/code": {}, testHome + "/code/new": {}})
	m = pressSettled(t, m, "a", "~", "/")
	if got := m.pickMatches(); !slices.Equal(got, []string{testHome + "/code", testHome + "/Documents"}) {
		t.Fatalf("matches %v", got)
	}
	m = pressSettled(t, m, "c", "tab")
	if m.pick.query != "~/code/" || (*reads)[len(*reads)-1] != testHome+"/code" {
		t.Fatalf("query %q, reads %v", m.pick.query, *reads)
	}
	// Nothing in it, so enter pins the typed path once it is a directory.
	m = pressSettled(t, m, "n", "e", "w", "enter")
	if !slices.Equal(m.home.Projects(), []string{testHome + "/code/new"}) || m.sel != "project:"+testHome+"/code/new" {
		t.Fatalf("pinned %v, sel %s", m.home.Projects(), m.sel)
	}
	m = pressSettled(t, m, "a", "/", "n", "o", "p", "e", "enter")
	if len(m.home.Projects()) != 1 || !strings.Contains(m.note, "could not add") {
		t.Fatalf("a missing directory: pinned %v, note %q", m.home.Projects(), m.note)
	}
}

// An answer for a query that has moved on is dropped.
func TestAStalePickAnswerIsDropped(t *testing.T) {
	f := newFakeMux()
	m := pickerModel(t, f)
	useDirs(&m, fakeDirs{"/code": {"app", "blog"}, "/": {"code"}})
	m, cmd := press(m, "a")
	stale := cmd
	m = pressSettled(t, m, "/")
	m, _ = step(m, stale())
	if got := m.pickMatches(); !slices.Equal(got, []string{"/code"}) {
		t.Fatalf("matches %v after a stale answer", got)
	}
}

func TestPickerKeys(t *testing.T) {
	f := newFakeMux()
	m := pickerModel(t, f)
	useDirs(&m, fakeDirs{"/code": {"a1", "a2", "a3"}})
	m = pressSettled(t, m, "a", "down", "down", "down")
	if m.pick.sel != 2 {
		t.Fatalf("sel %d", m.pick.sel)
	}
	m = update(m, tea.KeyMsg{Type: tea.KeyCtrlP})
	if m.pick.sel != 1 {
		t.Fatalf("ctrl+p: sel %d", m.pick.sel)
	}
	// While picking, letters are text, and the list's keys do nothing.
	m = pressSettled(t, m, "j", "q", "c")
	if m.pick.query != "jqc" || len(f.spawned) != 0 || f.detach != 0 {
		t.Fatalf("query %q, spawned %v", m.pick.query, f.spawned)
	}
	m = pressSettled(t, m, "esc")
	if m.pick.open || len(m.home.Projects()) != 0 {
		t.Fatal("esc did not cancel")
	}
}

// A query such as ~x is words, not a path, and typing it does not panic.
func TestATildeWordIsNotAPath(t *testing.T) {
	for _, q := range []string{"~x", "~kira", "~.config"} {
		if isPathQuery(q) || pickSource(q, testHome) != "" {
			t.Errorf("%q is taken for a path", q)
		}
	}
	f := newFakeMux()
	m := pickerModel(t, f)
	useDirs(&m, fakeDirs{"/code": {"app", "kit"}})
	m = pressSettled(t, m, "a", "~", "k")
	if got := m.pickMatches(); m.pick.query != "~k" || len(got) != 0 {
		t.Fatalf("query %q, matches %v", m.pick.query, got)
	}
}

// Tab completes to a directory, and enter then pins that directory, not
// the first folder in it, unless one was chosen with down.
func TestEnterAfterTabPinsTheDirectory(t *testing.T) {
	for _, c := range []struct {
		keys []string
		want string
	}{
		{[]string{"tab", "enter"}, "/work/x"},
		{[]string{"tab", "down", "enter"}, "/work/x/one"},
	} {
		f := newFakeMux()
		m := pickerModel(t, f)
		useDirs(&m, fakeDirs{"/work": {"x"}, "/work/x": {"one", "two"}, "/work/x/one": {}})
		m = pressSettled(t, m, append(append([]string{"a"}, typed("/work/x")...), c.keys...)...)
		if got := m.home.Projects(); !slices.Equal(got, []string{c.want}) {
			t.Errorf("%v pinned %v, want %s", c.keys, got, c.want)
		}
	}
}

// A long query keeps its end and the caret in view, and two long
// directories that differ only at the end stay told apart, as a project's
// heading keeps its end.
func TestThePickerKeepsTheEndsOfLongPaths(t *testing.T) {
	m := newTestModel(t)
	m = update(m, tea.WindowSizeMsg{Width: 60, Height: 20})
	dir := "/private/tmp/claude-501/-Users-kira-code-projects-overview"
	m.pick = picker{open: true, query: dir + "/work-", from: dir, read: true,
		paths: []string{dir + "/work-alpha", dir + "/work-beta"}}
	got := screenText(m)
	if !strings.Contains(got[0], "/work-▏") || !strings.HasPrefix(strings.TrimSpace(got[0]), "› …") {
		t.Errorf("the query line is %q", got[0])
	}
	var rows []string
	for _, l := range got {
		if strings.Contains(l, "work-alpha") || strings.Contains(l, "work-beta") {
			rows = append(rows, l)
		}
	}
	if len(rows) != 2 || !strings.Contains(rows[0], "…") {
		t.Errorf("rows %q, want both, ellipsized at the start", rows)
	}
}

// Whatever is typed, at any width, the query's last characters and the
// caret are on its line, and no line of the picker is wider than the pane.
func TestPropThePickerShowsTheEndOfTheQuery(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		m := newModel(caseHome(t, root), "")
		m.deps = fakeDeps(nil)
		w := rapid.IntRange(12, 200).Draw(t, "w")
		m = update(m, tea.WindowSizeMsg{Width: w, Height: rapid.IntRange(3, 40).Draw(t, "h")})
		q := rapid.StringMatching(`/[a-zA-Z0-9/._ 日本-]{0,150}`).Draw(t, "query")
		dir, _ := splitPathQuery(q, "")
		var paths []string
		for _, s := range rapid.SliceOfN(rapid.StringMatching(`[a-z-]{1,80}`), 0, 5).Draw(t, "names") {
			paths = append(paths, filepath.Join(dir, s))
		}
		m.pick = picker{open: true, query: q, from: dir, read: true, paths: paths}
		lines := strings.Split(ansi.Strip(m.View()), "\n")
		for i, l := range lines {
			if ansi.StringWidth(l) > w {
				t.Fatalf("line %d is %d wide in %d: %q", i, ansi.StringWidth(l), w, l)
			}
		}
		// The end of the query that fits beside "› " and the caret.
		tail := []rune(q)
		tail = tail[max(len(tail)-3, 0):]
		if !strings.Contains(lines[0], string(tail)+"▏") {
			t.Fatalf("the query line %q hides the end of %q", lines[0], q)
		}
	})
}
