package tui

import (
	"fmt"
	"path"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"pgregory.net/rapid"

	"overview/internal/fleet"
	"overview/internal/tmux"
)

var (
	t0      = time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	words   = []string{"auth", "Billing", "api", "refactor", "fix", "Tests", "deploy", "docs"}
	cwds    = []string{"/w/alpha", "/w/Beta", "/w/alpha/sub", "/w/gamma", "/w/beta2", "", "/w/Alpha"}
	states  = []fleet.State{fleet.Working, fleet.Blocked, fleet.Idle, fleet.Ended, ""}
	replies = []string{"", "done.", "Which one?", "ok *?*", "what now? "}
)

// fakeProject names a project after its directory's last element, so several
// cwds can share one project and projects can differ in case only.
func fakeProject(cwd string) string {
	if cwd == "" {
		return "?"
	}
	return path.Base(cwd)
}

// fakeHost puts an interactive session with pid 3 inside an app, Notes.
func fakeHost(r fleet.Row) (string, bool) {
	if r.Background() || r.PID != 3 {
		return "", false
	}
	return "Notes", true
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
		Label:      rapid.SampledFrom([]string{"", "", "crew-7", "Ops"}).Draw(t, "label"),
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

func ids(secs []section) []string {
	var out []string
	for _, r := range flatten(secs) {
		out = append(out, r.SessionID)
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
	r.Title, r.LastPrompt, r.Label = field("title"), field("prompt"), field("label")
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
		got := ids(buildList(rows, q, fakeProject, fakeHost))
		sort.Strings(want)
		sort.Strings(got)
		if strings.Join(want, ",") != strings.Join(got, ",") {
			t.Fatalf("query %q: got %v want %v", q, got, want)
		}
	})
}

func TestPropSectionsHoldTheRightRows(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		secs := buildList(genRows(t), genQuery(t), fakeProject, fakeHost)
		seen := map[string]bool{}
		var prev *section
		for i := range secs {
			s := &secs[i]
			if len(s.Rows) == 0 {
				t.Fatalf("empty section %q", s.Title)
			}
			if s.NeedsYou && (i != 0 || s.Title != needsYouTitle) {
				t.Fatalf("needs-you section at %d titled %q, want first and %q", i, s.Title, needsYouTitle)
			}
			for j, r := range s.Rows {
				if seen[r.SessionID] {
					t.Fatalf("%s appears twice", r.SessionID)
				}
				seen[r.SessionID] = true
				if r.NeedsYou() != s.NeedsYou {
					t.Fatalf("%s: NeedsYou=%v in section needs=%v", r.SessionID, r.NeedsYou(), s.NeedsYou)
				}
				_, away := fakeHost(r)
				if !s.NeedsYou && away != s.Away {
					t.Fatalf("%s: away=%v in section away=%v", r.SessionID, away, s.Away)
				}
				if !s.NeedsYou && !s.Away && fakeProject(r.Cwd) != s.Title {
					t.Fatalf("%s (project %q) is under %q", r.SessionID, fakeProject(r.Cwd), s.Title)
				}
				if j == 0 {
					continue
				}
				p := s.Rows[j-1]
				switch {
				case s.NeedsYou:
					if p.Since.After(r.Since) || (p.Since.Equal(r.Since) && p.SessionID >= r.SessionID) {
						t.Fatalf("needs-you order: %s before %s", p.SessionID, r.SessionID)
					}
				default:
					pw, rw := p.State == fleet.Working, r.State == fleet.Working
					bad := (!pw && rw) ||
						(pw == rw && p.Since.Before(r.Since)) ||
						(pw == rw && p.Since.Equal(r.Since) && p.SessionID >= r.SessionID)
					if bad {
						t.Fatalf("project order: %s before %s", p.SessionID, r.SessionID)
					}
				}
			}
			if s.Away && (i != len(secs)-1 || s.Title != awayTitle) {
				t.Fatalf("away section at %d of %d titled %q, want last and %q", i, len(secs), s.Title, awayTitle)
			}
			if !s.NeedsYou && !s.Away {
				if prev != nil && !nameLess(prev.Title, s.Title) {
					t.Fatalf("projects out of order: %q then %q", prev.Title, s.Title)
				}
				prev = s
			}
		}
	})
}

func TestPropIndependentOfInputOrder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rows, q := genRows(t), genQuery(t)
		shuffled := rapid.Permutation(rows).Draw(t, "perm")
		a := buildList(rows, q, fakeProject, fakeHost)
		b := buildList(shuffled, q, fakeProject, fakeHost)
		if !reflect.DeepEqual(ids(a), ids(b)) || len(a) != len(b) {
			t.Fatalf("order changed: %v vs %v", ids(a), ids(b))
		}
		if !reflect.DeepEqual(ids(a), ids(buildList(rows, q, fakeProject, fakeHost))) {
			t.Fatal("not deterministic")
		}
	})
}

func TestPropEmptyQueryIsEverything(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rows := genRows(t)
		q := rapid.SampledFrom([]string{"", " ", " \t "}).Draw(t, "blank")
		want := 0
		for _, r := range rows {
			if r.Name != fleet.OrchestratorName {
				want++
			}
		}
		if got := len(ids(buildList(rows, q, fakeProject, fakeHost))); got != want {
			t.Fatalf("got %d rows, want %d", got, want)
		}
	})
}

func TestPropLongerQueryNeverAddsRows(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rows, q := genRows(t), genQuery(t)
		extra := rapid.SampledFrom([]string{"a", "x", "e", " fix", " ", "  api", "\tzz"}).Draw(t, "extra")
		base := map[string]bool{}
		for _, id := range ids(buildList(rows, q, fakeProject, fakeHost)) {
			base[id] = true
		}
		for _, id := range ids(buildList(rows, q+extra, fakeProject, fakeHost)) {
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
			{LastPrompt: a, Label: b},
		} {
			r.SessionID = "x"
			if len(buildList([]fleet.Row{r}, term, func(string) string { return "" }, fakeHost)) != 0 {
				t.Fatalf("%q matched across fields %q|%q", term, a, b)
			}
		}
		// Whitespace splits terms, so a "term" with a space is two terms and
		// each may match a different field.
		r := fleet.Row{SessionID: "x", Title: a, LastPrompt: b}
		if len(buildList([]fleet.Row{r}, a+" "+b, func(string) string { return "" }, fakeHost)) != 1 {
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
		got := len(buildList([]fleet.Row{r}, q, fakeProject, fakeHost)) == 1
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
// to the same project as the resolved path.
func TestProjectsResolveSymlinks(t *testing.T) {
	p := newProjects()
	p.exists = fakeFS{"/private/tmp/r/.git": true}.exists
	p.resolve = func(s string) string {
		if strings.HasPrefix(s, "/tmp/") {
			return "/private" + s
		}
		return s
	}
	if a, b := p.name("/tmp/r/x"), p.name("/private/tmp/r"); a != b {
		t.Fatalf("%q and %q", a, b)
	}
	if a, b := p.name("/tmp/q/"), p.name("/private/tmp/q"); a != b {
		t.Fatalf("no repo: %q and %q", a, b)
	}
}

func TestProjectsCachePerCwd(t *testing.T) {
	calls := 0
	p := newProjects()
	p.exists = func(string) bool { calls++; return false }
	p.name("/x/y")
	n := calls
	p.name("/x/y")
	if calls != n {
		t.Fatalf("second lookup hit the filesystem %d more times", calls-n)
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

// plan moves the list exactly when the wanted pane is in another window,
// always into that pane's window, as wide as the list is unless it is
// alone, and asks for the layout fix-up only when its window keeps two or
// more panes.
func TestPropPlanTargetsTheWantedWindow(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var panes []tmux.Pane
		for i := range rapid.IntRange(1, 8).Draw(t, "n") {
			panes = append(panes, tmux.Pane{
				ID:     fmt.Sprintf("%%%d", i),
				Window: fmt.Sprintf("@%d", rapid.IntRange(0, 3).Draw(t, "win")),
				W:      rapid.IntRange(1, 200).Draw(t, "w"),
			})
		}
		self := rapid.SampledFrom(panes).Draw(t, "self")
		want := rapid.SampledFrom(append(slices.Clone(panes), tmux.Pane{ID: "%99"})).Draw(t, "want")
		mv, ok := plan(self.ID, want.ID, panes)
		if ok != (want.ID != "%99" && want.Window != self.Window) {
			t.Fatalf("move %v for want %+v, list %+v", ok, want, self)
		}
		if !ok {
			return
		}
		beside := 0
		for _, p := range panes {
			if p.Window == self.Window {
				beside++
			}
		}
		size := strconv.Itoa(self.W)
		if beside == 1 {
			size = listShare
		}
		if mv != (shift{to: want.ID, from: self.Window, size: size, fix: beside > 2}) {
			t.Fatalf("plan %+v for want %s, list %+v, %d in its window", mv, want.ID, self, beside)
		}
	})
}
