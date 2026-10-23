package tui

import (
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

	"github.com/alecthomas/chroma/v2/styles"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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

// fakeShort shows a directory as it is.
func fakeShort(dir string) string { return dir }

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

// sessionList is buildList with sessions only.
func sessionList(rows []fleet.Row, query string, project func(string) string, host func(fleet.Row) (string, bool)) []section {
	return buildList(rows, nil, nil, query, project, fakeShort, host)
}

func ids(secs []section) []string {
	var out []string
	for _, e := range flatten(secs) {
		out = append(out, e.key())
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
		got := ids(sessionList(rows, q, fakeProject, fakeHost))
		sort.Strings(want)
		sort.Strings(got)
		if strings.Join(want, ",") != strings.Join(got, ",") {
			t.Fatalf("query %q: got %v want %v", q, got, want)
		}
	})
}

func TestPropSectionsHoldTheRightRows(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		secs := sessionList(genRows(t), genQuery(t), fakeProject, fakeHost)
		seen := map[string]bool{}
		var prev *section
		for i := range secs {
			s := &secs[i]
			if len(s.Entries) == 0 {
				t.Fatalf("empty section %q", s.Title)
			}
			if s.NeedsYou && (i != 0 || s.Title != needsYouTitle) {
				t.Fatalf("needs-you section at %d titled %q, want first and %q", i, s.Title, needsYouTitle)
			}
			for j, e := range s.Entries {
				r := e.row
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
				p := s.Entries[j-1].row
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
		a := sessionList(rows, q, fakeProject, fakeHost)
		b := sessionList(shuffled, q, fakeProject, fakeHost)
		if !reflect.DeepEqual(ids(a), ids(b)) || len(a) != len(b) {
			t.Fatalf("order changed: %v vs %v", ids(a), ids(b))
		}
		if !reflect.DeepEqual(ids(a), ids(sessionList(rows, q, fakeProject, fakeHost))) {
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
		if got := len(ids(sessionList(rows, q, fakeProject, fakeHost))); got != want {
			t.Fatalf("got %d rows, want %d", got, want)
		}
	})
}

func TestPropLongerQueryNeverAddsRows(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rows, q := genRows(t), genQuery(t)
		extra := rapid.SampledFrom([]string{"a", "x", "e", " fix", " ", "  api", "\tzz"}).Draw(t, "extra")
		base := map[string]bool{}
		for _, id := range ids(sessionList(rows, q, fakeProject, fakeHost)) {
			base[id] = true
		}
		for _, id := range ids(sessionList(rows, q+extra, fakeProject, fakeHost)) {
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
			if len(sessionList([]fleet.Row{r}, term, func(string) string { return "" }, fakeHost)) != 0 {
				t.Fatalf("%q matched across fields %q|%q", term, a, b)
			}
		}
		// Whitespace splits terms, so a "term" with a space is two terms and
		// each may match a different field.
		r := fleet.Row{SessionID: "x", Title: a, LastPrompt: b}
		if len(sessionList([]fleet.Row{r}, a+" "+b, func(string) string { return "" }, fakeHost)) != 1 {
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
		got := len(sessionList([]fleet.Row{r}, q, fakeProject, fakeHost)) == 1
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

// genTerms draws terminals in distinct windows, in projects named like the
// rows' (fakeProject takes the last path element).
func genTerms(t *rapid.T) []terminal {
	n := rapid.IntRange(0, 6).Draw(t, "terms")
	wins := rapid.SliceOfNDistinct(rapid.IntRange(0, 30), n, n, rapid.ID[int]).Draw(t, "wins")
	out := make([]terminal, n)
	for i, w := range wins {
		out[i] = terminal{
			Window:  fmt.Sprintf("@%d", w),
			Pane:    fmt.Sprintf("%%%d", 100+w),
			Project: rapid.SampledFrom(cwds).Draw(t, "tproject"),
			Title:   rapid.SampledFrom([]string{"zsh", "node", "vite, node", "make"}).Draw(t, "title"),
			Path:    rapid.SampledFrom([]string{"/w/alpha/web", "/x/api", "/"}).Draw(t, "path"),
		}
	}
	return out
}

func genPinned(t *rapid.T) []string {
	return rapid.SliceOfDistinct(rapid.SampledFrom([]string{"/w/alpha", "/w/delta", "/w/Beta", "/p/omega", "/w/gamma"}), rapid.ID[string]).Draw(t, "pinned")
}

// With no query, every session but the orchestrator, every terminal and
// every pinned project shows exactly once: a pinned project as a
// placeholder exactly when no session or terminal is in it. A project's
// terminals come after its sessions, by window number, and a placeholder
// is alone in its section.
func TestPropEveryEntryShowsOnce(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rows, terms, pinned := genRows(t), genTerms(t), genPinned(t)
		secs := buildList(rows, terms, pinned, "", fakeProject, fakeShort, fakeHost)
		count := map[string]int{}
		for _, s := range secs {
			sawTerm := false
			for j, e := range s.Entries {
				count[e.key()]++
				switch e.kind {
				case terminalEntry:
					if s.NeedsYou || s.Away || s.Title != fakeProject(e.term.Project) {
						t.Fatalf("terminal %s (project %q) in section %q", e.key(), e.term.Project, s.Title)
					}
					if sawTerm && !windowLess(s.Entries[j-1].term.Window, e.term.Window) {
						t.Fatalf("terminals out of order in %q", s.Title)
					}
					sawTerm = true
				case projectEntry:
					if len(s.Entries) != 1 || s.Title != fakeProject(e.project) {
						t.Fatalf("placeholder %s in %+v", e.key(), s)
					}
				case sessionEntry:
					if sawTerm {
						t.Fatalf("session %s after a terminal in %q", e.key(), s.Title)
					}
				}
			}
		}
		used := map[string]bool{}
		for _, r := range rows {
			if r.Name != fleet.OrchestratorName {
				used[fakeProject(r.Cwd)] = true
				if count[r.SessionID] != 1 {
					t.Fatalf("session %s shows %d times", r.SessionID, count[r.SessionID])
				}
			}
		}
		for _, tm := range terms {
			used[fakeProject(tm.Project)] = true
			if count[tm.Window] != 1 {
				t.Fatalf("terminal %s shows %d times", tm.Window, count[tm.Window])
			}
		}
		// Pinned roots with one name are one project, shown once.
		holders := map[string]int{}
		for _, p := range pinned {
			if count["project:"+p] > 0 {
				holders[fakeProject(p)]++
			}
		}
		for _, p := range pinned {
			name := fakeProject(p)
			if used[name] && count["project:"+p] != 0 {
				t.Fatalf("pinned %s has entries, yet a placeholder", p)
			}
			if !used[name] && holders[name] != 1 {
				t.Fatalf("pinned %s is empty, and has %d placeholders", p, holders[name])
			}
		}
	})
}

func TestPropEntriesIndependentOfInputOrder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rows, terms, pinned, q := genRows(t), genTerms(t), genPinned(t), genQuery(t)
		a := buildList(rows, terms, pinned, q, fakeProject, fakeShort, fakeHost)
		b := buildList(rapid.Permutation(rows).Draw(t, "rows"), rapid.Permutation(terms).Draw(t, "terms"),
			rapid.Permutation(pinned).Draw(t, "pinned"), q, fakeProject, fakeShort, fakeHost)
		if !reflect.DeepEqual(ids(a), ids(b)) || len(a) != len(b) {
			t.Fatalf("order changed: %v vs %v", ids(a), ids(b))
		}
	})
}

// A search shows a placeholder only when every term is in its project's
// name, and never brings back a project with entries as a placeholder.
func TestPropSearchShowsPlaceholdersByName(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rows, terms, pinned, q := genRows(t), genTerms(t), genPinned(t), genQuery(t)
		all := map[string]bool{}
		for _, id := range ids(buildList(rows, terms, pinned, "", fakeProject, fakeShort, fakeHost)) {
			all[id] = true
		}
		for _, e := range flatten(buildList(rows, terms, pinned, q, fakeProject, fakeShort, fakeHost)) {
			if e.kind != projectEntry {
				continue
			}
			if !all[e.key()] {
				t.Fatalf("%q shows placeholder %s that the full list lacks", q, e.key())
			}
			name := strings.ToLower(fakeProject(e.project))
			for _, w := range strings.Fields(strings.ToLower(q)) {
				if !strings.Contains(name, w) {
					t.Fatalf("%q shows the placeholder for %q", q, name)
				}
			}
		}
	})
}

func TestTerminalSearch(t *testing.T) {
	term := terminal{Window: "@4", Pane: "%9", Project: "/w/shop", Title: "node", Path: "/x/shop/web"}
	for q, want := range map[string]bool{
		"": true, "shop": true, "NODE": true, "web": true, "node web": true, "shop/web": true,
		"nodeweb": false, "zsh": false, "terminal": false,
	} {
		got := len(buildList(nil, []terminal{term}, nil, q, fakeProject, fakeShort, fakeHost)) == 1
		if got != want {
			t.Errorf("%q: got %v want %v", q, got, want)
		}
	}
}

func TestCountsSkipTerminalsAndPlaceholders(t *testing.T) {
	r := fleet.Row{Live: fleet.Live{SessionID: "s", Cwd: "/w/a"}, State: fleet.Working}
	secs := buildList([]fleet.Row{r}, []terminal{{Window: "@1", Project: "/w/a"}}, []string{"/w/b"}, "", fakeProject, fakeShort, fakeHost)
	if need, working, idle := counts(secs); need != 0 || working != 1 || idle != 0 {
		t.Fatalf("counts %d %d %d", need, working, idle)
	}
}

// genPanes draws a tmux listing: panes in a few windows of the view's
// session "ov" and of another, with the list, the holder, attaches and
// claude ttys among them.
func genPanes(t *rapid.T) []tmux.Pane {
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
// pane besides the list and none of the holder, an attach, a listed
// claude, or the pane c just opened. Each comes once, in window order,
// named after its first such pane.
func TestPropTerminals(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		panes := genPanes(t)
		pick := func(label string) string {
			if len(panes) == 0 || rapid.Bool().Draw(t, label+"none") {
				return ""
			}
			return rapid.SampledFrom(panes).Draw(t, label).ID
		}
		self, holder, opened := pick("self"), pick("holder"), pick("opened")
		for i := range panes {
			panes[i].Holder = panes[i].ID == holder
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
			if p.Session != "ov" || p.ID == self {
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
			if i > 0 && !windowLess(got[i-1].Window, tm.Window) {
				t.Fatalf("out of order: %s then %s", got[i-1].Window, tm.Window)
			}
			first := ps[0]
			wantProject := first.Project
			if wantProject == "" {
				wantProject = root(first.Path)
			}
			if tm.Pane != first.ID || tm.Path != first.Path || tm.Project != wantProject || tm.Title != termTitle(ps) {
				t.Fatalf("terminal %+v from panes %+v", tm, ps)
			}
		}
	})
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

// stuck moves the cursor to the window the session was in only when the
// session has left it and the window is listed as a terminal.
func TestPropStuck(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		es := flatten(buildList(genRows(t), genTerms(t), genPinned(t), "", fakeProject, fakeShort, fakeHost))
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
			ps = append(ps, place{fmt.Sprintf("k%d", i), rapid.SampledFrom([]string{"", "@1", "@2", "@3"}).Draw(t, "window")})
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
		}
	})
}

// The view fits whatever pane tmux gives it, down to none at all: no line
// is wider than the pane and there are no more lines than it has rows.
// tmux can squeeze the list to a column, and a view that panics there
// takes its pane down with it.
func TestPropViewFitsAnySize(t *testing.T) {
	root := t.TempDir()
	keys := []string{"?", "/", "a", "x", "j", "k", "g", "G", "esc", "tab", "enter", "s"}
	rapid.Check(t, func(t *rapid.T) {
		m := newModel(caseHome(t, root), "")
		m.deps = fakeDeps(nil)
		m = update(m, tea.WindowSizeMsg{Width: 120, Height: 30})
		m = update(m, rowsMsg{rows: genRows(t)})
		for _, k := range rapid.SliceOfN(rapid.SampledFrom(keys), 0, 4).Draw(t, "keys") {
			m = update(m, keyMsg(k))
		}
		w := rapid.OneOf(rapid.IntRange(0, 12), rapid.IntRange(0, 200)).Draw(t, "w")
		h := rapid.OneOf(rapid.IntRange(0, 6), rapid.IntRange(0, 60)).Draw(t, "h")
		m = update(m, tea.WindowSizeMsg{Width: w, Height: h})
		lines := strings.Split(m.View(), "\n")
		if len(lines) > max(h, 1) {
			t.Fatalf("%d lines in a pane %d high", len(lines), h)
		}
		for i, l := range lines {
			if got := ansi.StringWidth(l); got > w {
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
