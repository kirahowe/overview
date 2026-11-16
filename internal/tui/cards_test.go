package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"overview/internal/diff"
	"overview/internal/fleet"
	"overview/internal/issues"
	"overview/internal/review"
)

// drawn is a card at width w, as plain text, its height its body's.
func drawn(c card, w int) []string {
	body := c.body(cardInner(w))
	var out []string
	for _, l := range c.render(body, w, len(body)+1, false) {
		out = append(out, ansi.Strip(l))
	}
	return out
}

func wantLines(t *testing.T, what string, got, want []string) {
	t.Helper()
	trimmed := make([]string, len(got))
	for i, l := range got {
		trimmed[i] = strings.TrimRight(l, " ")
	}
	if !slices.Equal(trimmed, want) {
		t.Fatalf("%s:\n%s\nwant\n%s", what, strings.Join(trimmed, "\n"), strings.Join(want, "\n"))
	}
}

func TestProjectHeaderCard(t *testing.T) {
	got := drawn(projectHeader("website", "~/code/website", "jj", -1, nil, nil), 50)
	wantLines(t, "untracked", got, []string{
		"── website ────────────────────────── no issues ──",
		" ~/code/website                                jj",
		" no sessions",
		"  c  claude   t  term",
	})
	if got := drawn(projectHeader("app", "/w/app", "git", 3, nil, nil), 50); !strings.HasSuffix(got[0], " 3 open ──") {
		t.Fatalf("tracked: %q", got[0])
	} // A long path keeps its end in the title and on the line under it,
	// so projects that share a parent still look apart.
	long := "/private/tmp/qa2/work/alpha-project"
	got = drawn(projectHeader(long, long, "git", -1, nil, nil), 34)
	wantLines(t, "long", got[:2], []string{
		"── …k/alpha-project ─ no issues ──",
		" …/tmp/qa2/work/alpha-project git",
	})
}

func TestFileStats(t *testing.T) {
	files := []diff.File{
		{NewPath: "a.go", Hunks: []diff.Hunk{{Lines: []diff.Line{{Kind: diff.Added}, {Kind: diff.Context}, {Kind: diff.Removed}, {Kind: diff.Added}}}}},
		{OldPath: "gone.go", Status: diff.Deleted, Hunks: []diff.Hunk{{Lines: []diff.Line{{Kind: diff.Removed}}}}},
		{NewPath: "logo.png", Binary: true},
	}
	want := []fileStat{{path: "a.go", add: 2, del: 1}, {path: "gone.go", del: 1}, {path: "logo.png", binary: true}}
	if got := fileStats(files); !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestChangesCard(t *testing.T) {
	files := []fileStat{{path: "internal/tui/view.go", add: 120, del: 4}, {path: "README.md", add: 3, del: 1},
		{path: "internal/tui/stage.go", add: 40, del: 30}}
	got := drawn(changesCard(changeInfo{files: files}), 50)
	wantLines(t, "changes", got, []string{
		"── Uncommitted " + strings.Repeat("─", 50-ansi.StringWidth("── Uncommitted ")),
		" +163 −35 in 3 files",
		" internal/tui/view.go                    +120 − 4",
		" internal/tui/stage.go                   + 40 −30",
		" README.md                               +  3 − 1",
	})
	var many []fileStat
	for i := range 9 {
		many = append(many, fileStat{path: string(rune('a'+i)) + ".go", add: 10 - i})
	}
	got = drawn(changesCard(changeInfo{files: many}), 40)
	if len(got) != 1+1+shownFiles || strings.TrimSpace(got[len(got)-1]) != "… 4 more files" {
		t.Fatalf("many: %q", got)
	}
	for _, c := range []struct {
		info changeInfo
		want string
	}{
		{changeInfo{noRepo: true}, "not in a repository"},
		{changeInfo{loading: true}, "reading…"},
		{changeInfo{err: errors.New("jj: boom")}, "jj: boom"},
		{changeInfo{}, "no changes"},
	} {
		if got := drawn(changesCard(c.info), 50); !strings.Contains(got[1], c.want) {
			t.Errorf("%+v: %q", c.info, got)
		}
	}
}

func TestReviewCard(t *testing.T) {
	got := drawn(reviewCard(review.Summary{Open: 3, Waiting: 1}), 40)
	wantLines(t, "review", got, []string{
		"── Review ──────────────────────────────",
		" 3 open                  1 replies wait",
		"  v  open",
	})
	if got := drawn(reviewCard(review.Summary{}), 40); !strings.Contains(got[1], "no notes") || len(got) != 2 {
		t.Fatalf("no notes: %q", got)
	}
}

func TestIssuesCard(t *testing.T) {
	others := []issues.Issue{
		{ID: 4, Title: "show cost", Status: issues.Review, Priority: "p2"},
		{ID: 7, Title: "tidy", Status: issues.InProgress, Priority: "p2"},
		{ID: 5, Title: "threads", Status: issues.Ready, Priority: "p2"},
		{ID: 9, Title: "urgent", Status: issues.Ready, Priority: "p0"},
		{ID: 10, Title: "later", Status: issues.Ready, Priority: "p3"},
		{ID: 11, Title: "much later", Status: issues.Ready, Priority: "p3"},
		{ID: 12, Title: "an idea", Status: issues.Inbox},
		{ID: 13, Title: "another", Status: issues.Inbox},
		{ID: 14, Title: "stuck", Status: issues.Blocked},
	}
	got := drawn(issuesCard(others, true, 2, ""), 50)
	wantLines(t, "issues", got, []string{
		"── Issues ────────────────────────────── 9 open ──",
		"   in review    #4 show cost",
		"   in progress  #7 tidy",
		" ▸ blocked      #14 stuck",
		"   up next      #9 urgent",
		"                #5 threads",
		"                #10 later",
		"                #11 much later",
		"   inbox 2",
	})
	ids := []int{}
	for _, i := range pickable(others) {
		ids = append(ids, i.ID)
	}
	if !slices.Equal(ids, []int{4, 7, 14, 9, 5, 10, 11}) {
		t.Fatalf("pickable %v", ids)
	}
	// Every issue is listed, however many a group has; the cockpit
	// shortens the card around its cursor when it does not fit.
	var many []issues.Issue
	for n, st := range map[string]int{issues.Review: 5, issues.InProgress: 6, issues.Blocked: 4, issues.Ready: 9} {
		for j := 0; j < st; j++ {
			many = append(many, issues.Issue{ID: 100 + len(many), Title: "x", Status: n})
		}
	}
	text := ansi.Strip(strings.Join(drawn(issuesCard(many, false, 0, ""), 50), "\n"))
	for _, i := range many {
		if want := fmt.Sprintf("#%d x", i.ID); !strings.Contains(text, want) {
			t.Errorf("lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "more") {
		t.Errorf("elides issues:\n%s", text)
	}
	if n := len(pickable(many)); n != len(many) {
		t.Fatalf("%d pickable, want all %d", n, len(many))
	}
	if got := drawn(issuesCard(nil, false, 0, ""), 40); !strings.Contains(got[1], "no open issues") {
		t.Fatalf("empty: %q", got)
	}
}

func TestTasksCard(t *testing.T) {
	got := drawn(tasksCard("~/code/site", true), 60)
	text := strings.Join(strings.Fields(strings.Join(got, " ")), " ")
	for _, want := range []string{"This project doesn't track its tasks.", "s picks one to start a session on", "the list says how many wait on you in review", "Start with issues init in ~/code/site."} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q:\n%s", want, text)
		}
	}
	if text := strings.Join(drawn(tasksCard("~/code/site", false), 60), "\n"); !strings.Contains(text, "Install the issues tool to track tasks here.") || strings.Contains(text, "issues init") {
		t.Fatalf("without the tool:\n%s", text)
	}
}

// A card shortened to fit keeps the line its at names, such as the issues
// card's cursor, and says how many lines it leaves out.
func TestAShortenedCardKeepsItsCursor(t *testing.T) {
	var others []issues.Issue
	for i := 1; i <= 6; i++ {
		others = append(others, issues.Issue{ID: i, Title: fmt.Sprint("issue ", i), Status: issues.Ready})
	}
	others = append(others, issues.Issue{ID: 9, Title: "the last", Status: issues.InProgress})
	lines, _, places := layoutCards([][]card{{issuesCard(others, true, 3, "")}}, 60, 4, issuesPanel)
	var got []string
	for _, l := range lines {
		got = append(got, strings.TrimSpace(ansi.Strip(l)))
	}
	want := []string{"── Issues ──────────────────────────────────────── 7 open ──", "↑ 3 more issues", "▸ up next      #3 issue 3", "↓ 3 more issues"}
	if len(places) != 1 || !slices.Equal(got, want) {
		t.Fatalf("the shortened issues card:\n%s", strings.Join(got, "\n"))
	}
}

func TestLayoutCardsDropsWhatDoesNotFit(t *testing.T) {
	box := func(name string, n int) card {
		return card{title: name, body: func(int) []string { return make([]string, n) }}
	}
	rows := [][]card{{box("a", 1)}, {box("b", 2)}, {box("c", 1), box("d", 3)}, {box("e", 1)}}
	for _, c := range []struct{ w, h, shown int }{
		{80, 40, 5},
		{80, 7, 4},  // a 2, b 3, then c and d need 4 and get 2
		{80, 10, 5}, // c and d side by side, 4 high; e needs 2 and gets its rule
		{60, 10, 4}, // stacked: c 2, then d needs 4 and is shortened to 3
		{60, 9, 4},  // stacked: c 2, then d gets its rule and a line
		{60, 1, 1},  // a's rule alone
		{60, 0, 0},
	} {
		lines, rules, places := layoutCards(rows, c.w, c.h, "")
		if len(places) != c.shown || len(lines) != c.h || len(rules) != c.h {
			t.Errorf("%dx%d: shown %d in %d lines, want %d", c.w, c.h, len(places), len(lines), c.shown)
		}
	}
	lines, rules, places := layoutCards(rows, 80, 40, "")
	if l := ansi.Strip(lines[5]); !strings.HasPrefix(l, "── c ") || !strings.Contains(l, "┬── d ") {
		t.Fatalf("c and d are not side by side: %q", l)
	}
	if l := ansi.Strip(lines[9]); !strings.HasPrefix(l, "── e ") || []rune(l)[80*pairShare/100] != '┴' {
		t.Fatalf("the line between c and d does not end on e's rule: %q", l)
	}
	var at []int
	for i, r := range rules {
		if r {
			at = append(at, i)
		}
	}
	if want := []int{0, 2, 5, 9}; !slices.Equal(at, want) {
		t.Fatalf("rules at %v, want %v", at, want)
	}
	if d := places[3]; d.r != (rect{80*pairShare/100 + 1, 5, 80 - 80*pairShare/100 - 1, 4}) {
		t.Fatalf("d is at %+v", d.r)
	}
}

func TestIssuesCardSaysWhyTheReadFailed(t *testing.T) {
	others := []issues.Issue{{ID: 4, Title: "show cost", Status: issues.Review}}
	got := drawn(issuesCard(others, false, 0, issues.OneLine("issues --dir /w/app list --json: exit status 2:\nno such\tthing")), 40)
	text := strings.Join(got, "\n")
	for _, want := range []string{"issues failed: issues --dir", "exit status 2: no such thing", "#4 show cost"} {
		if !strings.Contains(strings.Join(strings.Fields(strings.ReplaceAll(text, "│", " ")), " "), want) {
			t.Errorf("card lacks %q:\n%s", want, text)
		}
	}
	for _, l := range got {
		if w := ansi.StringWidth(l); w != 40 {
			t.Fatalf("line %q is %d wide", l, w)
		}
	}
}

func TestAgesReadAsPhrases(t *testing.T) {
	if got := agoPhrase("now"); got != "just now" {
		t.Errorf("agoPhrase(now) = %q", got)
	}
	if got := agoPhrase("3m"); got != "3m ago" {
		t.Errorf("agoPhrase(3m) = %q", got)
	}
}

// A project whose .git is no repository says so in a few words, without
// git's message, under Uncommitted.
func TestTheChangesCardSaysShortlyWhyGitFailed(t *testing.T) {
	d := fakeDeps(nil)
	d.exists = func(p string) bool { return p == "/code/app/.git" }
	d.vcs = func(dir string, args ...string) ([]byte, error) {
		return nil, errors.New("git rev-parse --verify: exit status 128: fatal: not a git repository (or any of the parent directories): .git")
	}
	msg := changesCmd("k", "", "/code/app", d)().(changesReadMsg)
	got := strings.TrimSpace(ansi.Strip(strings.Join(drawn(changesCard(msg.info), 40), "\n")))
	if !strings.Contains(got, "not a git repository") || strings.Contains(got, "rev-parse") || strings.Contains(got, "fatal") {
		t.Fatalf("the card reads:\n%s", got)
	}
}

// The cockpit of a project reads what is uncommitted in it, through a
// git repository whose commands a fake answers, and reads again only
// once what its sessions are doing changes.
func TestTheCockpitReadsWhatIsUncommitted(t *testing.T) {
	m := taskModel(t, newFakeMux())
	var asked [][]string
	m.deps.exists = func(p string) bool { return p == "/code/app/.git" || p == "/code/app/.issues" }
	m.deps.vcs = func(dir string, args ...string) ([]byte, error) {
		asked = append(asked, args)
		switch {
		case slices.Contains(args, "rev-parse"):
			return []byte("bbb\n"), nil
		case slices.Contains(args, "ls-files"):
			return nil, nil
		case slices.Contains(args, "bbb"):
			return []byte("diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1,2 @@\n-old\n+new\n+more\n"), nil
		}
		return nil, errors.New("unexpected " + strings.Join(args, " "))
	}
	m = selectKey(t, m, "s1")
	m = resized(t, m, 150, 40)
	m = pressSettled(t, m, "o")
	text := ansi.Strip(m.View())
	for _, want := range []string{"Uncommitted", "+2 −1 in 1 file", "x.go"} {
		if !strings.Contains(text, want) {
			t.Errorf("the cockpit lacks %q:\n%s", want, text)
		}
	}
	// Soon after, nothing is read again; once a session's state changes,
	// the changes are.
	n := len(asked)
	m = pressSettled(t, m, "r")
	if len(asked) != n {
		t.Fatalf("read again with nothing changed: %v", asked[n:])
	}
	rows := slices.Clone(m.in.Rows)
	rows[0].State = fleet.Working
	m, cmd := step(m, rowsMsg{rows: rows, ttys: m.in.TTYs})
	m = settle(t, m, cmd)
	if len(asked) == n {
		t.Fatal("a session's state changed and the changes were not read again")
	}
	if got := m.target(); got.root != "/code/app" || len(m.trackers["/code/app"].issues) != 2 {
		t.Fatalf("the refresh lost the project: target %+v, tracker %+v", got, m.trackers["/code/app"])
	}
}
