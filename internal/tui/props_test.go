package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/alecthomas/chroma/v2/styles"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"pgregory.net/rapid"

	"overview/internal/fleet"
	"overview/internal/issues"
	"overview/internal/review"
	"overview/internal/tmux"
	"overview/internal/world"
)

var (
	t0      = time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	words   = []string{"auth", "Billing", "api", "refactor", "fix", "Tests", "deploy", "docs"}
	cwds    = []string{"/w/alpha", "/w/Beta", "/w/alpha/sub", "/w/gamma", "/w/beta2", "", "/w/Alpha"}
	states  = []fleet.State{fleet.Working, fleet.Blocked, fleet.Idle, fleet.Ended, ""}
	replies = []string{"", "done.", "Which one?", "ok *?*", "what now? "}
)

// The list's properties run on Worlds that world.Derive builds from drawn
// inputs. No directory has a root of its own, so a project is named after
// its directory in full, and projects can differ in case only. An
// interactive session with pid 3 runs inside an app, Notes.

// listWorld derives the World of rows, the panes of the view's session
// "ov", the pinned projects and trackers.
func listWorld(rows []fleet.Row, panes []tmux.Pane, pinned []string, ts map[string]world.Tracker) *world.World {
	w := world.Derive(world.Inputs{
		Rows: rows, Hosts: map[int]world.Host{3: {Name: "Notes"}}, Panes: panes, Session: "ov",
		Pinned: pinned, Trackers: ts,
	})
	return &w
}

// fakeProject is the name of the project of cwd.
func fakeProject(cwd string) string {
	if cwd == "" {
		return ""
	}
	return path.Clean(cwd)
}

func phrase(t *rapid.T, label string) string {
	n := rapid.IntRange(0, 3).Draw(t, label+"n")
	ws := make([]string, n)
	for i := range ws {
		ws[i] = rapid.SampledFrom(words).Draw(t, label)
	}
	return strings.Join(ws, " ")
}

func genRow(t *rapid.T, id string) fleet.Row {
	r := fleet.Row{
		State:      rapid.SampledFrom(states).Draw(t, "state"),
		Since:      t0.Add(time.Duration(rapid.IntRange(0, 4).Draw(t, "since")) * time.Minute),
		Title:      phrase(t, "title"),
		LastPrompt: phrase(t, "prompt"),
		LastReply:  rapid.SampledFrom(replies).Draw(t, "reply"),
	}
	r.SessionID = id
	r.Name = rapid.SampledFrom([]string{"red-fox", "Blue-owl", "overview-orchestrator", "quiet-elk", ""}).Draw(t, "name")
	r.Cwd = rapid.SampledFrom(cwds).Draw(t, "cwd")
	r.PID = rapid.IntRange(0, 3).Draw(t, "pid")
	if rapid.IntRange(0, 9).Draw(t, "kind") == 0 {
		r.Kind = "background"
	}
	return r
}

func genRows(t *rapid.T) []fleet.Row {
	n := rapid.IntRange(0, 12).Draw(t, "n")
	rows := make([]fleet.Row, n)
	for i := range rows {
		rows[i] = genRow(t, string(rune('a'+i))+"-id")
	}
	return rows
}

func genQuery(t *rapid.T) string {
	n := rapid.IntRange(0, 3).Draw(t, "terms")
	var terms []string
	for i := 0; i < n; i++ {
		w := strings.ToLower(rapid.SampledFrom(append(words, "fox", "OWL", "alpha", "beta", "crew", "q")).Draw(t, "term"))
		// A prefix, or the whole word in odd case.
		w = w[:rapid.IntRange(1, len(w)).Draw(t, "cut")]
		if rapid.Bool().Draw(t, "upper") {
			w = strings.ToUpper(w)
		}
		terms = append(terms, w)
	}
	return strings.Join(terms, rapid.SampledFrom([]string{" ", "  ", "\t"}).Draw(t, "sep"))
}

// sessionList is the list of rows alone.
func sessionList(rows []fleet.Row, query string) []section {
	return present(listWorld(rows, nil, nil, nil), query)
}

// ids are the keys of the entries above the agents list that are no alias:
// each session, terminal, placeholder and workstream once.
func ids(secs []section) []string {
	var out []string
	for _, s := range secs {
		for _, e := range s.Entries {
			if !s.Agents && e.alias == "" {
				out = append(out, e.key())
			}
		}
	}
	return out
}

// vocab words share no substring a whole-word term could hit by accident,
// and a term never holds a space, so a term matches a field exactly when
// the field was built with that word. The expected matches are known from
// how each row was made, not recomputed by a second matcher.
var vocab = []string{"amber", "birch", "cedar", "dune", "fjord", "kiwi"}

// tokenRow builds a row whose searchable fields are made of vocab words in
// random case. It returns the words it used.
func tokenRow(t *rapid.T, id string) (fleet.Row, map[string]bool) {
	used := map[string]bool{}
	field := func(label string) string {
		n := rapid.IntRange(0, 2).Draw(t, label+"n")
		ws := make([]string, n)
		for i := range ws {
			w := rapid.SampledFrom(vocab).Draw(t, label)
			used[w] = true
			if rapid.Bool().Draw(t, label+"upper") {
				w = strings.ToUpper(w)
			}
			ws[i] = w
		}
		return strings.Join(ws, " ")
	}
	r := genRow(t, id)
	r.Title, r.LastPrompt = field("title"), field("prompt")
	r.Name = field("name")
	// fakeProject names the project after the last path element: one word
	// or none ("?").
	r.Cwd = ""
	if rapid.Bool().Draw(t, "hasproject") {
		w := rapid.SampledFrom(vocab).Draw(t, "project")
		used[w] = true
		r.Cwd = "/w/" + w
	}
	return r, used
}

func TestPropEveryMatchAppearsOnce(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var rows []fleet.Row
		var want []string
		var terms []string
		for i := rapid.IntRange(0, 3).Draw(t, "terms"); i > 0; i-- {
			terms = append(terms, rapid.SampledFrom(append(vocab, "zebra")).Draw(t, "term"))
		}
		for i := rapid.IntRange(0, 10).Draw(t, "n"); i > 0; i-- {
			r, used := tokenRow(t, fmt.Sprintf("s%02d", i))
			if rapid.IntRange(0, 9).Draw(t, "orch") == 0 {
				r.Name = fleet.OrchestratorName
			}
			hit := r.Name != fleet.OrchestratorName
			for _, term := range terms {
				hit = hit && used[term]
			}
			if hit {
				want = append(want, r.SessionID)
			}
			rows = append(rows, r)
		}
		q := strings.Join(terms, " ")
		if rapid.Bool().Draw(t, "qupper") {
			q = strings.ToUpper(q)
		}
		got := ids(sessionList(rows, q))
		sort.Strings(want)
		sort.Strings(got)
		if strings.Join(want, ",") != strings.Join(got, ",") {
			t.Fatalf("query %q: got %v want %v", q, got, want)
		}
	})
}

func TestPropLongerQueryNeverAddsRows(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rows, q := genRows(t), genQuery(t)
		extra := rapid.SampledFrom([]string{"a", "x", "e", " fix", " ", "  api", "\tzz"}).Draw(t, "extra")
		base := map[string]bool{}
		for _, id := range ids(sessionList(rows, q)) {
			base[id] = true
		}
		for _, id := range ids(sessionList(rows, q+extra)) {
			if !base[id] {
				t.Fatalf("%q matches %s but %q does not", q+extra, id, q)
			}
		}
	})
}

func TestPropNoMatchAcrossFieldBoundary(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := rapid.StringMatching(`[a-z]{1,5}`).Draw(t, "a")
		b := rapid.StringMatching(`[a-z]{1,5}`).Draw(t, "b")
		term := a + b // the end of one field glued to the start of the next
		for _, r := range []fleet.Row{
			{Title: a, LastPrompt: b},
			{Name: a, Title: b},
			{LastPrompt: a, Name: b},
		} {
			r.SessionID = "x"
			if len(ids(sessionList([]fleet.Row{r}, term))) != 0 {
				t.Fatalf("%q matched across fields %q|%q", term, a, b)
			}
		}
		// Whitespace splits terms, so a "term" with a space is two terms and
		// each may match a different field.
		r := fleet.Row{SessionID: "x", Title: a, LastPrompt: b}
		if len(ids(sessionList([]fleet.Row{r}, a+" "+b))) != 1 {
			t.Fatalf("%q %q should match as two terms", a, b)
		}
	})
}

func TestMatchingIsPerFieldAndCaseInsensitive(t *testing.T) {
	r := fleet.Row{Live: fleet.Live{SessionID: "s", Cwd: "/w/Billing"}, Title: "Fix Auth", LastPrompt: "rotate keys"}
	for q, want := range map[string]bool{
		"billing": true, "AUTH": true, "fix auth": true, "auth billing": true,
		"authrotate": false, "fixauth": false, "keys fix": true, "nope": false,
	} {
		got := len(ids(sessionList([]fleet.Row{r}, q))) == 1
		if got != want {
			t.Errorf("%q: got %v want %v", q, got, want)
		}
	}
}

// fakeFS is a set of existing paths.
type fakeFS map[string]bool

func (f fakeFS) exists(p string) bool { return f[p] }

func TestPropRepoRoot(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		segs := rapid.SliceOfN(rapid.SampledFrom([]string{"a", "b", "c"}), 0, 5).Draw(t, "segs")
		cwd := "/" + strings.Join(segs, "/")
		// Every ancestor-or-self, root first.
		var chain []string
		for i := 0; i <= len(segs); i++ {
			chain = append(chain, "/"+strings.Join(segs[:i], "/"))
		}
		fs := fakeFS{}
		marked := map[string]bool{}
		for _, dir := range chain {
			switch rapid.IntRange(0, 4).Draw(t, "marker") {
			case 0:
				fs[path.Join(dir, ".git")], marked[dir] = true, true
			case 1:
				fs[path.Join(dir, ".jj")], marked[dir] = true, true
			}
			fs[path.Join(dir, "decoy")] = true
		}
		got := repoRoot(cwd, fs.exists)
		nearest := ""
		for _, dir := range chain {
			if marked[dir] {
				nearest = dir
			}
		}
		switch {
		case nearest == "" && got != cwd:
			t.Fatalf("no marker: got %q want cwd %q", got, cwd)
		case nearest != "" && got != nearest:
			t.Fatalf("got %q want nearest %q (chain %v)", got, nearest, chain)
		}
		if got != cwd && !strings.HasPrefix(cwd+"/", strings.TrimSuffix(got, "/")+"/") {
			t.Fatalf("%q is not an ancestor of %q", got, cwd)
		}
	})
}

func TestRepoRootEdges(t *testing.T) {
	fs := fakeFS{"/r/.git": true}
	for cwd, want := range map[string]string{"/r": "/r", "/r/x/y": "/r", "/r/x/": "/r", "/q": "/q", "/q/": "/q", "/q/./x/..": "/q", "": ""} {
		if got := repoRoot(cwd, fs.exists); got != want {
			t.Errorf("%q: got %q want %q", cwd, got, want)
		}
	}
}

// A cwd reached through a symlink (/tmp is /private/tmp on macOS) belongs
// to the same project as the resolved path, and a refresh carries the
// root of every directory it names.
func TestRootsResolveSymlinks(t *testing.T) {
	resolve := func(s string) string {
		if strings.HasPrefix(s, "/tmp/") {
			return "/private" + s
		}
		return path.Clean(s)
	}
	rs := findRoots(fakeFS{"/private/tmp/r/.git": true}.exists, resolve, "/tmp/r/x", "/private/tmp/r", "/tmp/q/", "/private/tmp/q", "")
	want := roots{"/tmp/r/x": "/private/tmp/r", "/private/tmp/r": "/private/tmp/r", "/tmp/q/": "/private/tmp/q", "/private/tmp/q": "/private/tmp/q"}
	if !reflect.DeepEqual(rs, want) {
		t.Fatalf("roots %v, want %v", rs, want)
	}
}

// The model only looks roots up: in the refresh's, then the listing's, and
// a directory neither knows is its own root.
func TestModelRootLooksUp(t *testing.T) {
	m := newTestModel(t)
	m.rowRoots, m.paneRoots, m.user = roots{"/a/x": "/a"}, roots{"/a/x": "/b", "/c/y": "/c"}, "/u"
	m = m.setInputs(nil)
	for dir, want := range map[string]string{"/a/x": "/a", "/c/y": "/c", "/d/z/": "/d/z", "": ""} {
		if got := m.root(dir); got != want {
			t.Errorf("root(%q) = %q, want %q", dir, got, want)
		}
	}
	if got := m.name("/u"); got != "~" {
		t.Errorf("the home directory is named %q", got)
	}
}

func drawColor(t *rapid.T, label string) lipgloss.Color {
	if rapid.Bool().Draw(t, label+"ansi") {
		return lipgloss.Color(fmt.Sprint(rapid.IntRange(0, 255).Draw(t, label)))
	}
	digits := rapid.SampledFrom([]int{3, 6}).Draw(t, label+"len")
	hex := rapid.StringMatching(fmt.Sprintf("[0-9a-fA-F]{%d}", digits)).Draw(t, label)
	return lipgloss.Color("#" + hex)
}

// A theme printed with Format reads back as the same theme, whatever base
// it is read over, because Format writes every colour.
func TestPropThemeFormatRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var th Theme
		th.Name = "x"
		for _, k := range themeKeys {
			*k.get(&th) = drawColor(t, k.key)
		}
		th.Syntax = rapid.SampledFrom(styles.Names()).Draw(t, "syntax")
		base, _ := builtinTheme(rapid.SampledFrom(builtinNames()).Draw(t, "base"))
		got, err := ParseTheme("x", th.Format(), base)
		if err != nil {
			t.Fatal(err)
		}
		if got != th {
			t.Fatalf("got %+v\nwant %+v", got, th)
		}
	})
}

// A partial theme changes exactly the colours it names and keeps the rest of
// its base.
func TestPropPartialThemeKeepsBase(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		base, _ := builtinTheme(rapid.SampledFrom(builtinNames()).Draw(t, "base"))
		want := base
		want.Name = "x"
		var src strings.Builder
		for _, k := range themeKeys {
			if rapid.Bool().Draw(t, "set "+k.key) {
				c := drawColor(t, k.key)
				*k.get(&want) = c
				fmt.Fprintf(&src, "%s%s = %s\n", strings.Repeat(" ", rapid.IntRange(0, 2).Draw(t, "pad")), k.key, c)
			}
			if rapid.Bool().Draw(t, "comment") {
				src.WriteString("# comment = #fff\n\n")
			}
		}
		got, err := ParseTheme("x", src.String(), base)
		if err != nil {
			t.Fatalf("%v\n%s", err, src.String())
		}
		if got != want {
			t.Fatalf("got %+v\nwant %+v\n%s", got, want, src.String())
		}
	})
}

// The list window shows the cursor's row, stays inside the list, and does
// not move while the row is already in view.
func TestPropWindow(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 60).Draw(t, "n")
		h := rapid.IntRange(1, 30).Draw(t, "h")
		selTop := rapid.IntRange(0, n).Draw(t, "selTop")
		selBottom := rapid.IntRange(selTop, n).Draw(t, "selBottom")
		top := rapid.IntRange(0, 80).Draw(t, "top")
		got := window(top, selTop, selBottom, n, h)

		if got < 0 || got > max(n-h, 0) {
			t.Fatalf("window %d outside [0, %d]", got, max(n-h, 0))
		}
		if selBottom-selTop <= h && (got > selTop || selBottom > got+h) {
			t.Fatalf("window %d hides [%d, %d)", got, selTop, selBottom)
		}
		if selBottom-selTop > h && got != selTop {
			t.Fatalf("a tall row should show its top: %d, want %d", got, selTop)
		}
		if top <= max(n-h, 0) && top <= selTop && selBottom <= top+h && got != top {
			t.Fatalf("window moved from %d to %d with the row in view", top, got)
		}
	})
}

// plan moves the list exactly when the wanted pane is listed in another
// window. Carried out on the listing, the move puts the list in the wanted
// pane's window, after which there is nothing left to plan, and the layout
// fix-up is asked for exactly when the window the list left keeps two or
// more panes. The list goes at the width it keeps, fitted to the window it
// goes to. Until it keeps one, it goes as wide as it is when it shared its
// window, a width the user may have dragged it to, and takes listShare
// when it was alone, and so as wide as the window.
func TestPropPlanTargetsTheWantedWindow(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var panes []tmux.Pane
		winW := map[int]int{}
		for i := range rapid.IntRange(1, 8).Draw(t, "n") {
			win := rapid.IntRange(0, 3).Draw(t, "win")
			if _, ok := winW[win]; !ok {
				winW[win] = rapid.SampledFrom([]int{0, 20, 80, 229, 283}).Draw(t, "winw")
			}
			panes = append(panes, tmux.Pane{
				ID:     fmt.Sprintf("%%%d", i),
				Window: fmt.Sprintf("@%d", win),
				W:      rapid.IntRange(1, 200).Draw(t, "w"),
				WinW:   winW[win],
			})
		}
		self := rapid.SampledFrom(panes).Draw(t, "self")
		want := rapid.SampledFrom(append(slices.Clone(panes), tmux.Pane{ID: "%99"})).Draw(t, "want")
		width := rapid.OneOf(rapid.Just(0), rapid.IntRange(1, 200)).Draw(t, "width")
		mv, ok := plan(self.ID, want.ID, panes, width)
		if ok != (want.ID != "%99" && want.Window != self.Window) {
			t.Fatalf("move %v for want %+v, list %+v", ok, want, self)
		}
		if !ok {
			return
		}
		after := slices.Clone(panes)
		left := 0
		for i, p := range after {
			if p.ID == self.ID {
				after[i].Window = want.Window
			} else if p.Window == self.Window {
				left++
			}
		}
		if mv.to != want.ID || mv.from != self.Window || mv.fix != (left >= 2) {
			t.Fatalf("plan %+v for want %s, list %+v, %d left behind", mv, want.ID, self, left)
		}
		size := listShare
		switch {
		case width > 0 && want.WinW > 0:
			size = strconv.Itoa(fitList(width, want.WinW))
		case width > 0:
			size = strconv.Itoa(width)
		case left > 0:
			size = strconv.Itoa(self.W)
		}
		if mv.size != size {
			t.Fatalf("the list %d wide, keeping %d, with %d beside it, moves %s wide into %d, want %s", self.W, width, left, mv.size, want.WinW, size)
		}
		if again, ok := plan(self.ID, want.ID, after, width); ok {
			t.Fatalf("after the move, plan asks for %+v", again)
		}
	})
}

// genTerms draws the panes of terminals in distinct windows of the view's
// session, in the rows' projects, some of them opened for a workstream.
func genTerms(t *rapid.T) []tmux.Pane {
	n := rapid.IntRange(0, 6).Draw(t, "terms")
	wins := rapid.SliceOfNDistinct(rapid.IntRange(0, 30), n, n, rapid.ID[int]).Draw(t, "wins")
	out := make([]tmux.Pane, n)
	for i, w := range wins {
		out[i] = tmux.Pane{
			ID: fmt.Sprintf("%%%d", 100+w), Session: "ov", Window: fmt.Sprintf("@%d", w),
			Project: rapid.SampledFrom(cwds).Draw(t, "tproject"),
			Command: rapid.SampledFrom([]string{"zsh", "node", "vite", "make"}).Draw(t, "command"),
			Path:    rapid.SampledFrom([]string{"/w/alpha/web", "/x/api", "/"}).Draw(t, "path"),
		}
		if rapid.Bool().Draw(t, "termtask") {
			out[i].Task = genKey().Draw(t, "ttask")
		}
	}
	return out
}

func genPinned(t *rapid.T) []string {
	return rapid.SliceOfDistinct(rapid.SampledFrom([]string{"/w/alpha", "/w/delta", "/w/Beta", "/p/omega", "/w/gamma"}), rapid.ID[string]).Draw(t, "pinned")
}

// The workstreams. Two of the rows' projects track their issues; their
// issues have numbers 1 to 5 and any status, and sessions link to them,
// or to issues that are closed or not there at all, by project id.

var (
	trackedRoots = []string{"/w/alpha", "/w/gamma"}
	statuses     = []string{issues.Inbox, issues.Ready, issues.InProgress, issues.Review, issues.Blocked, issues.Done, issues.Dropped}
)

// genIssues draws the open and closed issues of the tracked roots, by
// root, each root's id its base name.
func genIssues(t *rapid.T) map[string][]issues.Issue {
	out := map[string][]issues.Issue{}
	for _, root := range trackedRoots {
		if rapid.IntRange(0, 4).Draw(t, "untracked") == 0 {
			continue
		}
		out[root] = []issues.Issue{}
		for _, id := range rapid.SliceOfDistinct(rapid.IntRange(1, 5), rapid.ID[int]).Draw(t, "issues") {
			out[root] = append(out[root], issues.Issue{
				ID: id, Ref: fmt.Sprintf("%s#%d", path.Base(root), id), Title: phrase(t, "ititle"),
				Status:   rapid.SampledFrom(statuses).Draw(t, "status"),
				Priority: rapid.SampledFrom([]string{"p0", "p1", "p2", "p3", ""}).Draw(t, "priority"),
			})
		}
	}
	return out
}

// worldTrackers are the trackers of issues, as the World is told of them.
func worldTrackers(is map[string][]issues.Issue) map[string]world.Tracker {
	out := map[string]world.Tracker{}
	for root, list := range is {
		out[root] = world.Tracker{ID: path.Base(root), Open: list}
	}
	return out
}

// genLinks gives each row up to three refs to issues by project id.
func genLinks(t *rapid.T, rows []fleet.Row) {
	for i := range rows {
		for range rapid.IntRange(0, 3).Draw(t, "links") {
			rows[i].Tasks = append(rows[i].Tasks, fleet.TaskRef{
				Project: rapid.SampledFrom([]string{"alpha", "gamma", "Beta"}).Draw(t, "rproject"),
				ID:      rapid.IntRange(1, 6).Draw(t, "rid"),
			})
		}
	}
}

func genKey() *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		return taskKey(rapid.SampledFrom(append(trackedRoots, "/w/Beta")).Draw(t, "kroot"), rapid.IntRange(1, 6).Draw(t, "kid"))
	})
}

// genListWorld draws a whole World: sessions linked to workstreams,
// terminals, pinned projects and trackers.
func genListWorld(t *rapid.T) *world.World {
	rows := genRows(t)
	genLinks(t, rows)
	return listWorld(rows, genTerms(t), genPinned(t), worldTrackers(genIssues(t)))
}

// presented is what the World holds that the list shows, in the World's
// order: each listed project's workstreams, each with its sessions and
// terminals, and its own sessions and terminals, then the away sessions,
// then the agents. A session under a workstream that is not its primary
// one is an alias.
func presented(w *world.World) []string {
	var out []string
	for _, p := range w.Projects {
		if p.Empty() {
			out = append(out, "project:"+p.Pins[0])
		}
		for _, st := range p.Streams {
			out = append(out, "task:"+st.Key.String())
			for _, s := range st.Sessions {
				if s.Primary() == st {
					out = append(out, s.SessionID)
				} else {
					out = append(out, "task:"+st.Key.String()+"/"+s.SessionID)
				}
			}
			for _, tm := range st.Terminals {
				out = append(out, tm.Window)
			}
		}
		for _, s := range p.Sessions {
			out = append(out, s.SessionID)
		}
		for _, tm := range p.Terminals {
			out = append(out, tm.Window)
		}
	}
	for _, s := range w.Away {
		out = append(out, s.SessionID)
	}
	for _, s := range w.Agents {
		out = append(out, "agents/"+s.SessionID)
	}
	return out
}

func keys(es []entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.key())
	}
	return out
}

// With an empty query the list is the whole World, in its order: every
// listed project, workstream, session, terminal and away session, a
// placeholder for each project with nothing in it, and every session once
// in the agents list. Each entry points at what it shows.
func TestPropPresentShowsTheWholeWorld(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		w := genListWorld(t)
		q := rapid.SampledFrom([]string{"", " ", " \t "}).Draw(t, "blank")
		secs := present(w, q)
		if got, want := keys(flatten(secs)), presented(w); !slices.Equal(got, want) {
			t.Fatalf("the list %v, want %v", got, want)
		}
		var titles []string
		for _, s := range secs {
			if !s.Away && !s.Agents {
				titles = append(titles, s.Title)
			}
			for _, e := range s.Entries {
				switch e.kind {
				case sessionEntry:
					if w.Sessions[e.sess.SessionID] != e.sess {
						t.Fatalf("%s is not the World's session", e.key())
					}
				case workstreamEntry:
					if w.Streams[e.stream.Key] != e.stream || e.stream.Project.Name != s.Title {
						t.Fatalf("%s is not the World's workstream in %q", e.key(), s.Title)
					}
				case terminalEntry:
					if !slices.Contains(w.Terminals, e.term) || e.term.Project.Name != s.Title {
						t.Fatalf("%s is not the World's terminal in %q", e.key(), s.Title)
					}
				}
				if e.stream != nil && e.kind != workstreamEntry && (e.alias == agentsPrefix || !slices.Contains(e.stream.Project.Streams, e.stream)) {
					t.Fatalf("%s sits under %s", e.key(), e.task())
				}
			}
		}
		var want []string
		for _, p := range w.Projects {
			want = append(want, p.Name)
		}
		if !slices.Equal(titles, want) {
			t.Fatalf("sections %v, want %v", titles, want)
		}
	})
}

// A query never adds an entry, and what it shows keeps the World's order.
func TestPropAQueryNeverAddsAnEntry(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		w := genListWorld(t)
		q := rapid.OneOf(rapid.Just(genQuery(t)), rapid.SampledFrom([]string{"#2", "alpha#3", "gamma", "#4 alpha", "notes", "node"})).Draw(t, "q")
		all := presented(w)
		at := -1
		for _, k := range keys(flatten(present(w, q))) {
			i := slices.Index(all, k)
			if i < 0 {
				t.Fatalf("%q shows %s, which the World does not list", q, k)
			}
			if i <= at {
				t.Fatalf("%q shows %s out of the World's order", q, k)
			}
			at = i
		}
	})
}

// A search shows a placeholder only when every term is in its project's
// name.
func TestPropSearchShowsPlaceholdersByName(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		w, q := genListWorld(t), genQuery(t)
		for _, e := range flatten(present(w, q)) {
			if e.kind != projectEntry {
				continue
			}
			name := strings.ToLower(e.proj.Name)
			for _, word := range strings.Fields(strings.ToLower(q)) {
				if !strings.Contains(name, word) {
					t.Fatalf("%q shows the placeholder for %q", q, name)
				}
			}
		}
	})
}

func TestTerminalSearch(t *testing.T) {
	term := tmux.Pane{ID: "%9", Session: "ov", Window: "@4", Project: "/w/shop", Command: "node", Path: "/x/shop/web"}
	for q, want := range map[string]bool{
		"": true, "shop": true, "NODE": true, "web": true, "node web": true, "shop/web": true,
		"nodeweb": false, "zsh": false, "terminal": false,
	} {
		got := len(present(listWorld(nil, []tmux.Pane{term}, nil, nil), q)) == 1
		if got != want {
			t.Errorf("%q: got %v want %v", q, got, want)
		}
	}
}

// stuck moves the cursor to the window the session was in only when the
// session has left it and the window is listed as a terminal.
func TestPropStuck(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		es := flatten(present(genListWorld(t), ""))
		win := rapid.SampledFrom([]string{"", "@1", "@2", "@7"}).Draw(t, "win")
		still := rapid.Bool().Draw(t, "still")
		key, ok := stuck(win, still, es)
		isTerm := false
		for _, e := range es {
			isTerm = isTerm || (e.kind == terminalEntry && e.term.Window == win)
		}
		if ok != (win != "" && !still && isTerm) || (ok && key != win) {
			t.Fatalf("stuck(%q, %v) = %q, %v; terminal there %v", win, still, key, ok, isTerm)
		}
	})
}

// nearest lands on a key still listed with none listed strictly between
// it and the lost one in the old order, after the lost one when any after
// it is listed, and finds nothing only when the lost key was not in the
// old order or nothing else in it is left.
func TestPropNearest(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 8).Draw(t, "n")
		old := make([]string, n)
		left := map[string]bool{}
		for i := range old {
			old[i] = fmt.Sprintf("k%d", i)
			left[old[i]] = rapid.Bool().Draw(t, "left")
		}
		sel := rapid.SampledFrom(append(slices.Clone(old), "gone")).Draw(t, "sel")
		left[sel] = false
		got, ok := nearest(old, sel, func(k string) bool { return left[k] })
		i := slices.Index(old, sel)
		anyLeft, leftAfter := false, false
		for j, k := range old {
			anyLeft = anyLeft || left[k]
			leftAfter = leftAfter || (left[k] && j > i)
		}
		if !ok {
			if i >= 0 && anyLeft {
				t.Fatalf("nearest(%v, %s) found nothing, though %v are left", old, sel, left)
			}
			return
		}
		j := slices.Index(old, got)
		if i < 0 || j < 0 || !left[got] {
			t.Fatalf("nearest(%v, %s) = %s, which is not listed", old, sel, got)
		}
		if leftAfter != (j > i) {
			t.Fatalf("nearest(%v, %s) = %s, though one after is left: %v", old, sel, got, leftAfter)
		}
		for _, k := range old[min(i, j)+1 : max(i, j)] {
			if left[k] {
				t.Fatalf("nearest(%v, %s) = %s, past the nearer %s", old, sel, got, k)
			}
		}
	})
}

// follow finds an entry in the list's window exactly when there is one,
// and keeps the cursor's entry when it is one of them.
func TestPropFollow(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var ps []place
		for i := range rapid.IntRange(0, 8).Draw(t, "n") {
			ps = append(ps, place{fmt.Sprintf("k%d", i), rapid.SampledFrom([]string{"", "@1", "@2", "@3"}).Draw(t, "window"), rapid.Bool().Draw(t, "alias")})
		}
		stage := rapid.SampledFrom([]string{"", "@1", "@2"}).Draw(t, "stage")
		sel := rapid.SampledFrom([]string{"k0", "k1", "k5", "gone"}).Draw(t, "sel")
		got, ok := follow(ps, stage, sel)
		there := map[string]bool{}
		for _, p := range ps {
			there[p.key] = stage != "" && p.window == stage
		}
		any := slices.Contains(slices.Collect(maps.Values(there)), true)
		switch {
		case ok != any:
			t.Fatalf("follow found %v, an entry there %v", ok, any)
		case ok && !there[got]:
			t.Fatalf("%s is not in %s", got, stage)
		case there[sel] && got != sel:
			t.Fatalf("the cursor's %s is there, yet follow chose %s", sel, got)
		case !there[sel] && ok:
			// Else the first there that is no alias, or the first alias.
			want := ""
			for _, p := range ps {
				if there[p.key] && !p.alias {
					want = p.key
					break
				}
			}
			for _, p := range ps {
				if want == "" && there[p.key] {
					want = p.key
				}
			}
			if got != want {
				t.Fatalf("follow chose %s, want %s from %+v", got, want, ps)
			}
		}
	})
}

// The view fits whatever pane tmux gives it, down to none at all: every
// line is as wide as the pane and there are as many as it has rows.
// tmux can squeeze the list to a column, and a view that panics there
// takes its pane down with it.
func TestPropViewFitsAnySize(t *testing.T) {
	root := t.TempDir()
	keys := []string{"?", "/", "a", "x", "j", "k", "g", "G", "esc", "tab", "enter", "s"}
	rapid.Check(t, func(t *rapid.T) {
		m := newModel(caseHome(t, root), "")
		m.deps = fakeDeps(nil)
		m = update(m, tea.WindowSizeMsg{Width: 120, Height: 30})
		rows, ttys, ts := workRows(t)
		m.trackers = ts
		m = m.setInputs(nil)
		m = update(m, rowsMsg{rows: rows, ttys: ttys})
		// With the issues tool, s opens the task picker, and it may be
		// open on any project, tracked or not, with any query.
		m.issues = func(...string) ([]byte, error) { return nil, errors.New("not run") }
		if rapid.Bool().Draw(t, "taskpick") {
			m.taskPick = taskPick{open: true, root: rapid.SampledFrom(append(trackedRoots, "/w/Beta")).Draw(t, "taskroot"),
				query: phrase(t, "taskquery"), sel: rapid.IntRange(-1, 8).Draw(t, "tasksel")}
		}
		for _, k := range rapid.SliceOfN(rapid.SampledFrom(keys), 0, 4).Draw(t, "keys") {
			m = update(m, keyMsg(k))
		}
		w := rapid.OneOf(rapid.IntRange(0, 12), rapid.IntRange(0, 200)).Draw(t, "w")
		h := rapid.OneOf(rapid.IntRange(0, 6), rapid.IntRange(0, 60)).Draw(t, "h")
		m = update(m, tea.WindowSizeMsg{Width: w, Height: h})
		view := m.View()
		if w == 0 || h == 0 {
			if view != "" {
				t.Fatalf("a pane %dx%d got %q", w, h, view)
			}
			return
		}
		// Every line is exactly as wide as the pane, and there are as many
		// as it has rows, so nothing of an older frame shows through.
		lines := strings.Split(view, "\n")
		if len(lines) != h {
			t.Fatalf("%d lines in a pane %d high", len(lines), h)
		}
		for i, l := range lines {
			if got := ansi.StringWidth(l); got != w {
				t.Fatalf("line %d is %d wide in a pane %d wide: %q", i, got, w, l)
			}
		}
	})
}

// Whatever tmux does to it, the list ends up at the width the user last
// dragged it to, fitted to its window, once the view has seen it and
// carried out the resize it asked for: a window that narrows, or one the
// list moves into at a width it was left at, squeezes the list only until
// the next listing. A squeeze comes with a change of window or window
// width, and a drag with neither. A list alone in its window fills it and
// is left be.
func TestPropTheListKeepsTheWidthDraggedTo(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// A window the list shares is at least 3 wide: the list, a border,
		// and a column of the pane beside it.
		list := tmux.Pane{ID: "%0", Window: "@0", WinW: rapid.IntRange(3, 300).Draw(t, "winw")}
		list.W = rapid.IntRange(1, list.WinW-2).Draw(t, "w")
		var k listWidth
		dragged := list.W
		look := func(alone bool) {
			var to int
			k, to = k.seen(list, alone)
			if to != 0 {
				list.W = to
			}
		}
		look(false)
		for range rapid.IntRange(1, 12).Draw(t, "events") {
			alone := false
			switch rapid.IntRange(0, 3).Draw(t, "event") {
			case 0: // the user drags the border, if it moves at all
				if w := rapid.IntRange(1, list.WinW-2).Draw(t, "drag"); w != list.W {
					list.W, dragged = w, w
				}
			case 1: // the terminal narrows or widens, and tmux squeezes the list
				list.WinW = rapid.IntRange(3, 300).Filter(func(w int) bool { return w != list.WinW }).Draw(t, "resize")
				list.W = rapid.IntRange(1, list.WinW-2).Draw(t, "squeezed")
			case 2: // the list moves into another window, shown at any width
				list.Window = rapid.SampledFrom([]string{"@0", "@1", "@2"}).Filter(func(w string) bool { return w != list.Window }).Draw(t, "win")
				list.WinW = rapid.IntRange(3, 300).Draw(t, "movedw")
				list.W = rapid.IntRange(1, list.WinW-2).Draw(t, "moved")
			case 3: // the list is alone in its window
				list.W, alone = list.WinW, true
			}
			look(alone)
			if alone {
				if list.W != list.WinW {
					t.Fatalf("a list alone was resized to %d in %d", list.W, list.WinW)
				}
				continue
			}
			if want := fitList(dragged, list.WinW); list.W != want {
				t.Fatalf("the list is %d wide in %d, dragged to %d, want %d (%+v)", list.W, list.WinW, dragged, want, k)
			}
			if _, again := k.seen(list, false); again != 0 {
				t.Fatalf("seen again at %d, the list is resized to %d", list.W, again)
			}
		}
	})
}

// fitList keeps the list at least listMin wide, and leaves besideMin and a
// border beside it, when the window has room for both; in a smaller one
// they share it. It never goes outside the window.
func TestPropFitList(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		want, win := rapid.IntRange(0, 400).Draw(t, "want"), rapid.IntRange(1, 400).Draw(t, "win")
		got := fitList(want, win)
		if got > win || got < 1 {
			t.Fatalf("fitList(%d, %d) = %d, outside the window", want, win, got)
		}
		if win >= listMin+besideMin+1 && (got < listMin || win-got-1 < besideMin) {
			t.Fatalf("fitList(%d, %d) = %d, leaving %d", want, win, got, win-got-1)
		}
		if lo := max(min(listMin, max(win-besideMin-1, win/2)), 1); want >= lo && want <= max(win-besideMin-1, lo) && got != want {
			t.Fatalf("fitList(%d, %d) = %d, though %d fits", want, win, got, want)
		}
	})
}

// Each session has a primary entry, under the workstream it was linked
// to last or in its project, an alias under each other workstream it is
// linked to, and one in the agents list.
func TestPropSessionEntries(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		w := genListWorld(t)
		count := map[string]int{}
		for _, e := range flatten(present(w, "")) {
			if count[e.key()]++; count[e.key()] > 1 {
				t.Fatalf("key %s twice", e.key())
			}
		}
		for id, s := range w.Sessions {
			n := 0
			for k, c := range count {
				if k == id || strings.HasSuffix(k, "/"+id) {
					n += c
				}
			}
			if n != max(len(s.Streams), 1)+1 || count[id] != 1 || count["agents/"+id] != 1 {
				t.Fatalf("%s is listed %d times with streams %v", id, n, s.Streams)
			}
			for _, st := range s.Streams {
				key := "task:" + st.Key.String() + "/" + id
				if st == s.Primary() {
					key = id
				}
				if count[key] != 1 {
					t.Fatalf("%s has no entry %s", id, key)
				}
			}
		}
	})
}

// A search never shows an entry under a workstream without the
// workstream's row, shows everything under a workstream that matches,
// and shows a workstream only when it or something under it matches.
func TestPropSearchKeepsWorkstreamsWhole(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		w := genListWorld(t)
		q := rapid.OneOf(rapid.Just(genQuery(t)), rapid.SampledFrom([]string{"#2", "alpha#3", "gamma", "#4 alpha"})).Draw(t, "q")
		all := present(w, "")
		got := present(w, q)
		words := strings.Fields(strings.ToLower(q))
		shown := map[string]bool{}
		for _, e := range flatten(got) {
			shown[e.key()] = true
		}
		for _, s := range got {
			cur := ""
			for _, e := range s.Entries {
				if e.kind == workstreamEntry {
					cur = e.task()
				} else if e.task() != "" && e.task() != cur {
					t.Fatalf("%q shows %s without its workstream %s", q, e.key(), e.task())
				}
			}
		}
		for _, s := range all {
			for _, e := range s.Entries {
				if e.kind != workstreamEntry {
					continue
				}
				i := e.stream.Issue
				self := matchFields(words, i.Ref, fmt.Sprintf("#%d", i.ID), i.Title, e.stream.Project.Name)
				kids := children(flatten(all), e.task())
				any := false
				for _, c := range kids {
					any = any || shown[c.key()]
					if self && !shown[c.key()] {
						t.Fatalf("%q matches %s but hides %s", q, e.key(), c.key())
					}
				}
				if shown[e.key()] != (self || any) {
					t.Fatalf("%q: %s shown %v, matches %v, a child shown %v", q, e.key(), shown[e.key()], self, any)
				}
			}
		}
	})
}

// workRows are rows whose task refs name the tracked projects by id, and
// trackers for those projects, for a model.
func workRows(t *rapid.T) ([]fleet.Row, map[int]string, trackers) {
	rows, ttys := genStateRows(t)
	for i := range rows {
		for range rapid.IntRange(0, 2).Draw(t, "refs") {
			rows[i].Tasks = append(rows[i].Tasks, fleet.TaskRef{
				Project: rapid.SampledFrom([]string{"alpha", "gamma"}).Draw(t, "rproject"),
				ID:      rapid.IntRange(1, 5).Draw(t, "rid"),
			})
		}
	}
	is := genIssues(t)
	ts := trackers{}
	for _, root := range slices.Sorted(maps.Keys(is)) {
		ts[root] = tracker{id: path.Base(root), issues: parsedIssues(t, is[root])}
	}
	return rows, ttys, ts
}

// messyTitle is a title as an issue's file may hold it: words with
// newlines, tabs, escape sequences, other control characters and wide
// characters among them.
var messyTitle = rapid.Custom(func(t *rapid.T) string {
	parts := rapid.SliceOfN(rapid.SampledFrom([]string{"fix", "the", "list", "日本語", "é", " ", "\n", "\r\n", "\t",
		"\x1b[31m", "\x1b]0;x\x07", "\x1b[2J", "\x00", "\x7f", "\u0085", "\u009b"}), 0, 14).Draw(t, "parts")
	return strings.Join(parts, "")
})

// parsedIssues are list as the issues CLI would print them, with messy
// titles, read back through issues.ParseList as the refresh reads them.
func parsedIssues(t *rapid.T, list []issues.Issue) []issues.Issue {
	var docs []map[string]any
	for _, i := range list {
		docs = append(docs, map[string]any{"id": i.ID, "ref": i.Ref, "title": i.Title + messyTitle.Draw(t, "mtitle"),
			"status": i.Status, "priority": i.Priority})
	}
	b, err := json.Marshal(map[string]any{"issues": docs})
	if err != nil {
		t.Fatal(err)
	}
	out, err := issues.ParseList(b)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// j from the top visits every entry once, aliases and workstreams too, and
// n only ever stops at a session's own entry that needs you or at a
// workstream in review.
func TestPropTheCursorWalksTheWholeList(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		m := newModel(caseHome(t, root), "")
		m.deps = fakeDeps(nil)
		m = update(m, tea.WindowSizeMsg{Width: 60, Height: rapid.IntRange(3, 40).Draw(t, "h")})
		rows, ttys, ts := workRows(t)
		m.trackers = ts
		m = m.setInputs(nil)
		m = update(m, rowsMsg{rows: rows, ttys: ttys})
		es := m.ordered()
		m, _ = press(m, "g")
		var visited []string
		for range len(es) + 2 {
			if len(visited) == 0 || visited[len(visited)-1] != m.sel {
				visited = append(visited, m.sel)
			}
			m, _ = press(m, "j")
		}
		var want []string
		for _, e := range es {
			want = append(want, e.key())
		}
		if len(es) > 0 && !slices.Equal(visited, want) {
			t.Fatalf("j visited %v, want %v", visited, want)
		}
		for range 6 {
			was := m.sel
			m, _ = press(m, rapid.SampledFrom([]string{"n", "N"}).Draw(t, "n"))
			e, _, ok := m.selected(m.ordered())
			if moved := m.sel != was; ok && (moved || m.note != "nothing needs you") && (!needy(e) || e.alias != "") {
				t.Fatalf("n stopped at %s (%+v)", e.key(), e)
			}
		}
	})
}

// The cockpit's target for an entry under a workstream is that
// workstream, and for any other entry its project.
func TestPropCockpitTargetIsTheWorkstreamAbove(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		w := genListWorld(t)
		es := flatten(present(w, ""))
		rootOf := func(e entry, ok bool) string { return "/root/of/" + e.key() }
		var under *world.Stream
		for _, e := range es {
			got := cockpitTarget(w, e, true, rootOf)
			switch {
			case e.kind == workstreamEntry:
				under = e.stream
			case e.stream == nil || e.alias == agentsPrefix:
				under = nil
			}
			switch {
			case e.stream != under && e.alias != agentsPrefix:
				t.Fatalf("%s sits under %s, not the workstream above it", e.key(), e.task())
			case under != nil && (got.stream != under || got.root != under.Root || got.project != under.Project):
				t.Fatalf("%s under %s has target %+v", e.key(), under.Key, got)
			case under == nil && (got.stream != nil || got.root != rootOf(e, true) || got.project != w.ProjectOf(got.root)):
				t.Fatalf("%s under no workstream has target %+v", e.key(), got)
			}
		}
		if got := cockpitTarget(w, entry{}, false, rootOf); got.stream != nil || got.root != rootOf(entry{}, false) {
			t.Fatalf("with nothing selected: %+v", got)
		}
	})
}

// genCards are rows of the cockpit's cards from arbitrary data, each with
// a title of its own.
func genCards(t *rapid.T) [][]card {
	text := rapid.SampledFrom([]string{"", "x", "keep the list's width", strings.Repeat("long words ", 30), "wide 漢字 text"})
	var ms []member
	for range rapid.IntRange(0, 4).Draw(t, "members") {
		ms = append(ms, member{name: text.Draw(t, "mname"), word: "idle", age: "3m", style: dim, term: rapid.Bool().Draw(t, "term")})
	}
	var files []fileStat
	for range rapid.IntRange(0, 9).Draw(t, "files") {
		files = append(files, fileStat{path: text.Draw(t, "path"), add: rapid.IntRange(0, 5000).Draw(t, "add"), del: rapid.IntRange(0, 50).Draw(t, "del"),
			binary: rapid.Bool().Draw(t, "binary")})
	}
	var crit []issues.Criterion
	for range rapid.IntRange(0, 5).Draw(t, "crit") {
		crit = append(crit, issues.Criterion{Text: text.Draw(t, "ctext"), Done: rapid.Bool().Draw(t, "done")})
	}
	statuses := []string{issues.Inbox, issues.Ready, issues.InProgress, issues.Review, issues.Blocked}
	var others []issues.Issue
	for i := range rapid.IntRange(0, 8).Draw(t, "others") {
		others = append(others, issues.Issue{ID: i + 1, Title: text.Draw(t, "otitle"), Status: rapid.SampledFrom(statuses).Draw(t, "ostatus"),
			Priority: rapid.SampledFrom([]string{"p0", "p2", ""}).Draw(t, "oprio")})
	}
	cards := []card{
		streamHeader(text.Draw(t, "ref"), "review", "p1", text.Draw(t, "title"), text.Draw(t, "since"), ms),
		projectHeader(text.Draw(t, "name"), text.Draw(t, "root"), "jj", rapid.IntRange(-1, 9).Draw(t, "open"), ms),
		issueCard(6, issueInfo{summary: text.Draw(t, "summary"), criteria: crit, loading: rapid.Bool().Draw(t, "loading")}),
		changesCard("Changes", text.Draw(t, "csince"), changeInfo{files: files, uncommitted: rapid.IntRange(-1, 9).Draw(t, "unc"),
			noRepo: rapid.Bool().Draw(t, "norepo")}),
		reviewCard(review.Summary{Open: rapid.IntRange(0, 9).Draw(t, "ropen"), Waiting: rapid.IntRange(0, 3).Draw(t, "rwait")}),
		issuesCard(others, map[int]int{1: 2}, rapid.Bool().Draw(t, "focused"), rapid.IntRange(0, 8).Draw(t, "pick"),
			rapid.SampledFrom([]string{"", "issues list: exit status 1: " + strings.Repeat("bad ", 30)}).Draw(t, "err")),
		tasksCard(text.Draw(t, "troot"), rapid.Bool().Draw(t, "tool")),
	}
	var rows [][]card
	for len(cards) > 0 {
		n := min(rapid.IntRange(1, 2).Draw(t, "pair"), len(cards))
		row := slices.Clone(cards[:n])
		for i := range row {
			row[i].title = fmt.Sprintf("card%d", len(rows)*2+i)
		}
		rows, cards = append(rows, row), cards[n:]
	}
	return rows
}

// The cockpit's cards fill exactly the space they are given, whatever it
// is and whatever they hold, and those that do not fit are the last ones,
// dropped whole. Each card is placed where it is drawn: inside the area,
// on no other, with its rule on its first line.
func TestPropCockpitCardsFitAndDropTheLast(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rows := genCards(t)
		w, h := rapid.IntRange(0, 200).Draw(t, "w"), rapid.IntRange(0, 60).Draw(t, "h")
		sel := rapid.SampledFrom([]panelID{"", issuesPanel}).Draw(t, "sel")
		lines, rules, places := layoutCards(rows, w, h, sel)
		shown := len(places)
		if len(lines) != h || len(rules) != h {
			t.Fatalf("%d lines and %d rules, want %d", len(lines), len(rules), h)
		}
		starts := map[int]bool{}
		for i, p := range places {
			if p.r.x < 0 || p.r.y < 0 || p.r.x+p.r.w > w || p.r.y+p.r.h > h || p.r.h < 1 {
				t.Fatalf("card %d at %+v, outside %dx%d", i, p.r, w, h)
			}
			for _, q := range places[:i] {
				if p.r.x < q.r.x+q.r.w && q.r.x < p.r.x+p.r.w && p.r.y < q.r.y+q.r.h && q.r.y < p.r.y+p.r.h {
					t.Fatalf("cards at %+v and %+v overlap", p.r, q.r)
				}
			}
			starts[p.r.y] = true
		}
		for i, r := range rules {
			if r != starts[i] {
				t.Fatalf("line %d: rule %v, but a card starts there %v", i, r, starts[i])
			}
		}
		for i, l := range lines {
			if lw := ansi.StringWidth(l); lw != w {
				t.Fatalf("line %d is %d wide, want %d: %q", i, lw, w, ansi.Strip(l))
			}
		}
		var order []string
		for _, r := range rows {
			for _, c := range r {
				order = append(order, c.title)
			}
		}
		var seen []string
		text := ansi.Strip(strings.Join(lines, "\n"))
		for _, title := range order {
			if strings.Contains(text, "─ "+title+" ") {
				seen = append(seen, title)
			}
		}
		if w >= 20 && !slices.Equal(seen, order[:shown]) {
			t.Fatalf("shown %d, the titles drawn are %v of %v", shown, seen, order)
		}
	})
}

// The whole view fits any size with the cockpit open, on a workstream and
// on a project.
func TestPropCockpitFitsAnySize(t *testing.T) {
	base := workModel(t, 150, 40)
	base.cockpit = cockpit{open: true}
	rapid.Check(t, func(t *rapid.T) {
		m := base
		es := m.ordered()
		m = m.moveTo(es, rapid.IntRange(0, len(es)-1).Draw(t, "sel"))
		if rapid.Bool().Draw(t, "issues") {
			m.cockpit.issues = true
		}
		m.width.want = rapid.IntRange(0, 80).Draw(t, "listw")
		w, h := rapid.IntRange(1, 220).Draw(t, "w"), rapid.IntRange(1, 70).Draw(t, "h")
		m = update(m, tea.WindowSizeMsg{Width: w, Height: h})
		lines := strings.Split(m.View(), "\n")
		if len(lines) != h {
			t.Fatalf("%d lines at %dx%d", len(lines), w, h)
		}
		for i, l := range lines {
			if lw := ansi.StringWidth(l); lw != w {
				t.Fatalf("line %d is %d wide at %dx%d: %q", i, lw, w, h, ansi.Strip(l))
			}
		}
	})
}

// The card with the keyboard is always one the cockpit shows: whatever
// the size, the cursor and the keys pressed, the issues card has the
// keyboard only while it is drawn, with its cursor on a listed issue.
func TestPropTheFocusedCardIsShown(t *testing.T) {
	base := workModel(t, 150, 40)
	rapid.Check(t, func(t *rapid.T) {
		m := base
		m = update(m, keyMsg("o"))
		for n := rapid.IntRange(1, 12).Draw(t, "n"); n > 0; n-- {
			switch rapid.IntRange(0, 3).Draw(t, "do") {
			case 0:
				m = update(m, tea.WindowSizeMsg{Width: rapid.IntRange(20, 220).Draw(t, "w"), Height: rapid.IntRange(4, 70).Draw(t, "h")})
			case 1:
				m = update(m, keyMsg("tab"))
			case 2:
				m = update(m, keyMsg(rapid.SampledFrom([]string{"j", "k"}).Draw(t, "key")))
			case 3:
				// The list's width changes as a drag would, and the next
				// listing lands.
				m.width.want = rapid.IntRange(0, 80).Draw(t, "listw")
				m = update(m, tea.WindowSizeMsg{Width: m.w, Height: m.h})
			}
			if !m.inIssues() {
				continue
			}
			if !m.issuesShown() {
				t.Fatalf("the issues card has the keyboard but is not shown at %dx%d", m.w, m.h)
			}
			if !strings.Contains(ansi.Strip(m.View()), "▸ ") {
				t.Fatalf("the issues card's cursor is not drawn at %dx%d:\n%s", m.w, m.h, ansi.Strip(m.View()))
			}
		}
	})
}

// When the entry under the cursor goes, the cursor stays with its
// session while the session is listed: an issue that closes or links
// that change move its entries, never the cursor to another session.
func TestPropTheCursorStaysWithItsSession(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rows, terms, query := genRows(t), genTerms(t), genQuery(t)
		genLinks(t, rows)
		is := genIssues(t)
		old := flatten(present(listWorld(rows, terms, nil, worldTrackers(is)), query))
		if len(old) == 0 {
			return
		}
		// Issues close and sessions link again, or everything changes.
		next := map[string][]issues.Issue{}
		for _, root := range slices.Sorted(maps.Keys(is)) {
			next[root] = slices.DeleteFunc(slices.Clone(is[root]), func(issues.Issue) bool { return rapid.Bool().Draw(t, "close") })
		}
		rows = slices.Clone(rows)
		for i := range rows {
			rows[i].Tasks = rapid.Permutation(rows[i].Tasks).Draw(t, "relink")
		}
		if rapid.IntRange(0, 4).Draw(t, "anew") == 0 {
			for i := range rows {
				rows[i].Tasks = nil
			}
			genLinks(t, rows)
			next, query = genIssues(t), genQuery(t)
		}
		now := flatten(present(listWorld(rows, terms, nil, worldTrackers(next)), query))
		// keepNear leaves a listed cursor be: the cursor is on one that went.
		gone := slices.DeleteFunc(slices.Clone(old), func(o entry) bool {
			return slices.ContainsFunc(now, func(e entry) bool { return e.key() == o.key() })
		})
		if len(gone) == 0 {
			return
		}
		was := rapid.SampledFrom(gone).Draw(t, "sel")
		sel := was.key()
		got, ok := nearKey(old, now, sel)
		at := slices.IndexFunc(now, func(e entry) bool { return e.key() == got })
		if ok && at < 0 {
			t.Fatalf("moved to %s, which is not listed", got)
		}
		if was.kind != sessionEntry {
			return
		}
		still := slices.ContainsFunc(now, func(e entry) bool {
			r, ok := e.session()
			return ok && r.SessionID == was.sess.SessionID
		})
		if still && (!ok || now[at].kind != sessionEntry || now[at].sess.SessionID != was.sess.SessionID) {
			t.Fatalf("on %s, moved to %s (%v), though %s is listed", sel, got, ok, was.sess.SessionID)
		}
	})
}

// Every opening prompt overview writes for a project id without spaces or
// a #, whatever the issue's title, is one the hook reads back as that
// issue, so the session it starts links itself. An id with them is the
// one case it cannot, and startOn says so.
func TestPropOpeningRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		id := rapid.String().Draw(t, "id")
		i := issues.Issue{ID: rapid.IntRange(1, 1_000_000).Draw(t, "n"), Title: rapid.String().Draw(t, "title")}
		plain := id != "" && !strings.ContainsFunc(id, func(c rune) bool { return c == '#' || unicode.IsSpace(c) })
		if got := opens(opening(id, i), id, i); got != plain {
			t.Fatalf("opening(%q, #%d %q) reads back %v", id, i.ID, i.Title, got)
		}
	})
}

// pickKey is where the task picker puts an issue, stated apart from the
// code: ready, then inbox, then review, in progress and blocked, then by
// priority p0 to p3 and anything else last, then by number.
func pickKey(i issues.Issue) []int {
	group := slices.Index([]string{issues.Ready, issues.Inbox, issues.Review, issues.InProgress, issues.Blocked}, i.Status)
	prio := slices.Index([]string{"p0", "p1", "p2", "p3"}, i.Priority)
	if prio < 0 {
		prio = 4
	}
	return []int{group, prio, i.ID}
}

// The task picker offers only open issues, each once, in its order, and
// exactly those that match every word of the query.
func TestPropTaskRows(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var list []issues.Issue
		for _, id := range rapid.SliceOfDistinct(rapid.IntRange(1, 30), rapid.ID[int]).Draw(t, "ids") {
			list = append(list, issues.Issue{ID: id, Title: phrase(t, "title"),
				Status:   rapid.SampledFrom(statuses).Draw(t, "status"),
				Priority: rapid.SampledFrom([]string{"p0", "p1", "p2", "p3", "", "px"}).Draw(t, "priority")})
		}
		sessions := rapid.MapOf(rapid.IntRange(1, 30), rapid.IntRange(0, 3)).Draw(t, "sessions")
		words := rapid.SliceOfN(rapid.OneOf(rapid.SampledFrom([]string{"fix", "THE", "li", "app#", "#1", "#2"}),
			rapid.Custom(func(t *rapid.T) string { return phrase(t, "word") })), 0, 3).Draw(t, "query")
		query := strings.Join(words, " ")
		rows := taskRows(list, "app", counted(sessions), query)
		terms := strings.Fields(strings.ToLower(query))
		seen := map[int]bool{}
		for k, r := range rows {
			if seen[r.issue.ID] || !slices.ContainsFunc(list, func(i issues.Issue) bool { return reflect.DeepEqual(i, r.issue) }) {
				t.Fatalf("row %+v is not one of the issues, or twice", r)
			}
			seen[r.issue.ID] = true
			if !issues.Open(r.issue.Status) {
				t.Fatalf("a %s issue is offered", r.issue.Status)
			}
			if r.sessions != sessions[r.issue.ID] {
				t.Fatalf("#%d has %d sessions, want %d", r.issue.ID, r.sessions, sessions[r.issue.ID])
			}
			for _, w := range terms {
				if !matchFields([]string{w}, "#"+strconv.Itoa(r.issue.ID), "app#"+strconv.Itoa(r.issue.ID), r.issue.Title) {
					t.Fatalf("#%d %q does not match %q", r.issue.ID, r.issue.Title, w)
				}
			}
			if k > 0 && slices.Compare(pickKey(r.issue), pickKey(rows[k-1].issue)) < 0 {
				t.Fatalf("#%d is after #%d", r.issue.ID, rows[k-1].issue.ID)
			}
		}
		for _, i := range list {
			if issues.Open(i.Status) && taskMatches(terms, "app", i) && !seen[i.ID] {
				t.Fatalf("#%d %q matches %q but is not offered", i.ID, i.Title, query)
			}
		}
	})
}

// The list's lines are shared out whole: the projects, and the agents
// with their rule, fill exactly the height, and the agents get all they
// need when both fit, and never more.
func TestPropSplitListSharesTheHeight(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p, a, h := rapid.IntRange(0, 80).Draw(t, "p"), rapid.IntRange(0, 40).Draw(t, "a"), rapid.IntRange(1, 80).Draw(t, "h")
		proj, agents := splitList(p, a, h)
		used := proj
		if agents > 0 {
			used += agents + 1
		}
		if used != h || proj < 1 || agents > a || agents < 0 {
			t.Fatalf("splitList(%d, %d, %d) = %d, %d", p, a, h, proj, agents)
		}
		if a > 0 && h >= 3 && max(p, 1)+1+a <= h && agents != a {
			t.Fatalf("splitList(%d, %d, %d) = %d, %d: both fit", p, a, h, proj, agents)
		}
		if a > 0 && h >= 3 && agents == 0 {
			t.Fatalf("splitList(%d, %d, %d) left the agents out", p, a, h)
		}
	})
}

// detailLines keeps every character of the details but the hashes of a
// heading outside a code block, the block's fences and the spaces, in
// order, and no line it makes is wider than it was asked for.
func TestDetailLinesProps(t *testing.T) {
	pieces := []string{"word", "longerwordthanmost", "a", "#", "## Heading", "- ", "- [ ] ", "* ", "12. ", "```", "  ", "\n", "\n\n", "é", "x.y"}
	rapid.Check(t, func(t *rapid.T) {
		var b strings.Builder
		for _, p := range rapid.SliceOfN(rapid.SampledFrom(pieces), 0, 40).Draw(t, "pieces") {
			b.WriteString(p)
			if rapid.Bool().Draw(t, "space") {
				b.WriteString(" ")
			}
		}
		details := b.String()
		w := rapid.IntRange(1, 60).Draw(t, "w")
		got := detailLines(details, w)
		var shown strings.Builder
		for i, l := range got {
			l = ansi.Strip(l)
			if lw := ansi.StringWidth(l); lw > w {
				t.Fatalf("line %d is %d wide, over %d: %q", i, lw, w, l)
			}
			shown.WriteString(l)
		}
		var want strings.Builder
		fence := false
		for _, line := range strings.Split(details, "\n") {
			s := strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(s, "```"):
				fence = !fence
				continue
			case !fence && strings.HasPrefix(s, "#"):
				s = strings.TrimLeft(s, "#")
			}
			want.WriteString(s)
		}
		squash := func(s string) string { return strings.Join(strings.Fields(s), "") }
		if squash(shown.String()) != squash(want.String()) {
			t.Fatalf("details %q at %d show as\n%q", details, w, got)
		}
	})
}

// wrap never leaves a line wider than asked, hyphens and long words
// included, and loses no text.
func TestWrapStaysWithinItsWidth(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		words := rapid.SliceOf(rapid.StringMatching(`[a-z]{1,14}(-[a-z]{1,6})?`)).Draw(t, "words")
		w := rapid.IntRange(1, 30).Draw(t, "w")
		lines := wrap(strings.Join(words, " "), w)
		for _, l := range lines {
			if ansi.StringWidth(l) > w {
				t.Fatalf("line %q is wider than %d", l, w)
			}
		}
		if got, want := strings.Join(strings.Fields(strings.Join(lines, " ")), ""), strings.Join(words, ""); got != want {
			t.Fatalf("wrapped to %q, lost text of %q", got, want)
		}
	})
}
