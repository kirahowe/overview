package tui

import (
	"path"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"overview/internal/fleet"
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
		Label:      rapid.SampledFrom([]string{"", "", "staff-7", "Ops"}).Draw(t, "label"),
		Title:      phrase(t, "title"),
		LastPrompt: phrase(t, "prompt"),
		LastReply:  rapid.SampledFrom(replies).Draw(t, "reply"),
	}
	r.SessionID = id
	r.Name = rapid.SampledFrom([]string{"red-fox", "Blue-owl", "overview-orchestrator", "quiet-elk", ""}).Draw(t, "name")
	r.Cwd = rapid.SampledFrom(cwds).Draw(t, "cwd")
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
		w := strings.ToLower(rapid.SampledFrom(append(words, "fox", "OWL", "alpha", "beta", "staff", "q")).Draw(t, "term"))
		// A prefix, or the whole word in odd case.
		w = w[:rapid.IntRange(1, len(w)).Draw(t, "cut")]
		if rapid.Bool().Draw(t, "upper") {
			w = strings.ToUpper(w)
		}
		terms = append(terms, w)
	}
	return strings.Join(terms, rapid.SampledFrom([]string{" ", "  ", "\t"}).Draw(t, "sep"))
}

// refMatch is an independent statement of the matching rule.
func refMatch(r fleet.Row, query string) bool {
	fields := []string{fakeProject(r.Cwd), r.Title, r.LastPrompt, r.Name, r.Label}
	for _, term := range strings.Fields(strings.ToLower(query)) {
		hit := false
		for _, f := range fields {
			hit = hit || strings.Contains(strings.ToLower(f), term)
		}
		if !hit {
			return false
		}
	}
	return true
}

func ids(secs []section) []string {
	var out []string
	for _, r := range flatten(secs) {
		out = append(out, r.SessionID)
	}
	return out
}

func TestPropEveryMatchAppearsOnce(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rows, q := genRows(t), genQuery(t)
		var want []string
		for _, r := range rows {
			if r.Name != fleet.OrchestratorName && refMatch(r, q) {
				want = append(want, r.SessionID)
			}
		}
		got := ids(buildList(rows, q, fakeProject))
		sort.Strings(want)
		g := append([]string(nil), got...)
		sort.Strings(g)
		if !reflect.DeepEqual(append([]string{}, want...), append([]string{}, g...)) {
			t.Fatalf("query %q: got %v want %v", q, g, want)
		}
	})
}

func TestPropSectionsHoldTheRightRows(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		secs := buildList(genRows(t), genQuery(t), fakeProject)
		seen := map[string]bool{}
		var prev *section
		for i := range secs {
			s := &secs[i]
			if len(s.Rows) == 0 {
				t.Fatalf("empty section %q", s.Title)
			}
			if s.NeedsYou != (i == 0 && s.Title == needsYouTitle && s.NeedsYou) {
				t.Fatalf("section %d %q: NeedsYou=%v", i, s.Title, s.NeedsYou)
			}
			for j, r := range s.Rows {
				if seen[r.SessionID] {
					t.Fatalf("%s appears twice", r.SessionID)
				}
				seen[r.SessionID] = true
				if r.NeedsYou() != s.NeedsYou {
					t.Fatalf("%s: NeedsYou=%v in section needs=%v", r.SessionID, r.NeedsYou(), s.NeedsYou)
				}
				if !s.NeedsYou && fakeProject(r.Cwd) != s.Title {
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
			if !s.NeedsYou {
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
		a := buildList(rows, q, fakeProject)
		b := buildList(shuffled, q, fakeProject)
		if !reflect.DeepEqual(ids(a), ids(b)) || len(a) != len(b) {
			t.Fatalf("order changed: %v vs %v", ids(a), ids(b))
		}
		if !reflect.DeepEqual(ids(a), ids(buildList(rows, q, fakeProject))) {
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
		if got := len(ids(buildList(rows, q, fakeProject))); got != want {
			t.Fatalf("got %d rows, want %d", got, want)
		}
	})
}

func TestPropLongerQueryNeverAddsRows(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rows, q := genRows(t), genQuery(t)
		extra := rapid.SampledFrom([]string{"a", "x", "e", " fix", " ", "  api", "\tzz"}).Draw(t, "extra")
		base := map[string]bool{}
		for _, id := range ids(buildList(rows, q, fakeProject)) {
			base[id] = true
		}
		for _, id := range ids(buildList(rows, q+extra, fakeProject)) {
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
			if refMatch(r, term) != (strings.Contains(a, term) || strings.Contains(b, term)) {
				t.Fatal("test oracle broken")
			}
			if len(buildList([]fleet.Row{r}, term, func(string) string { return "" })) != 0 {
				t.Fatalf("%q matched across fields %q|%q", term, a, b)
			}
		}
		// Whitespace splits terms, so a "term" with a space is two terms and
		// each may match a different field.
		r := fleet.Row{SessionID: "x", Title: a, LastPrompt: b}
		if len(buildList([]fleet.Row{r}, a+" "+b, func(string) string { return "" })) != 1 {
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
		got := len(buildList([]fleet.Row{r}, q, fakeProject)) == 1
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
