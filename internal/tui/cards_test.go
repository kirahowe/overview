package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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

func TestStreamHeaderCard(t *testing.T) {
	ms := []member{{name: "list squeeze", word: "working", age: "3m", mark: "⠋", markStyle: green, style: green}, {name: "go", term: true}}
	got := drawn(streamHeader("overview#6", "in-progress", "p1", "keep the list's width", "2h", ms), 60)
	wantLines(t, "header", got, []string{
		"── overview#6 ────────────────────────── in-progress · p1 ──",
		" keep the list's width                       started 2h ago",
		" ⠋ list squeeze working 3m   $ go",
		"  enter  attach   v  review   c  claude   t  term",
	})
	// No sessions yet, and no start.
	got = drawn(streamHeader("app#4", "review", "p2", "show cost", "", nil), 40)
	if strings.TrimSpace(got[1]) != "show cost" || !strings.Contains(got[2], "no sessions") {
		t.Fatalf("empty header: %q", got)
	}
}

func TestProjectHeaderCard(t *testing.T) {
	got := drawn(projectHeader("website", "~/code/website", "jj", -1, nil), 50)
	wantLines(t, "untracked", got, []string{
		"── website ────────────────────────── no issues ──",
		" ~/code/website                                jj",
		" no sessions",
		"  c  claude   t  term",
	})
	if got := drawn(projectHeader("app", "/w/app", "git", 3, nil), 50); !strings.HasSuffix(got[0], " 3 open ──") {
		t.Fatalf("tracked: %q", got[0])
	}
}

func TestIssueSummary(t *testing.T) {
	for _, c := range []struct{ details, want string }{
		{"# Title\n\n## Context\n\nFirst line\nsecond line.\n\nLater.", "First line second line."},
		{"# Title\n\nStraight in.\n## Acceptance criteria\n- [ ] x", "Straight in."},
		{"First.\n\nSecond.", "First."},
		{"# Title\n\n## Acceptance criteria\n\n- [ ] x", ""},
		{"", ""},
	} {
		if got := issueSummary(c.details); got != c.want {
			t.Errorf("issueSummary(%q) = %q, want %q", c.details, got, c.want)
		}
	}
}

func TestIssueCard(t *testing.T) {
	info := issueInfo{summary: "Keep the list at the width it was dragged to.",
		criteria: []issues.Criterion{{Text: "keeps its width", Done: true}, {Text: "e2e covers it"}}}
	got := drawn(issueCard(6, info), 50)
	wantLines(t, "issue", got, []string{
		"── Issue ──────────────────────── issues show 6 ──",
		" Keep the list at the width it was dragged to.",
		"",
		" acceptance  1 of 2",
		" ○ e2e covers it",
		" ✓ keeps its width",
	})
	long := issueInfo{summary: strings.Repeat("word ", 80)}
	if got := drawn(issueCard(6, long), 30); len(got) != issueSummaryLines+1 || !strings.Contains(got[issueSummaryLines], "…") {
		t.Fatalf("long summary: %q", got)
	}
	if got := drawn(issueCard(6, issueInfo{loading: true}), 30); !strings.Contains(got[1], "reading…") {
		t.Fatalf("loading: %q", got)
	}
	if got := drawn(issueCard(6, issueInfo{err: errors.New("issues: boom")}), 30); !strings.Contains(got[1], "issues: boom") {
		t.Fatalf("error: %q", got)
	}
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
	got := drawn(changesCard("Changes", "2h", changeInfo{files: files, uncommitted: 2}), 50)
	wantLines(t, "changes", got, []string{
		"── Changes ─────────────────────────── since 2h ──",
		" +163 −35 in 3 files                2 uncommitted",
		" internal/tui/view.go                    +120 − 4",
		" internal/tui/stage.go                   + 40 −30",
		" README.md                               +  3 − 1",
	})
	var many []fileStat
	for i := range 9 {
		many = append(many, fileStat{path: string(rune('a'+i)) + ".go", add: 10 - i})
	}
	got = drawn(changesCard("Uncommitted", "", changeInfo{files: many, uncommitted: -1}), 40)
	if len(got) != 1+1+shownFiles || strings.TrimSpace(got[len(got)-1]) != "… 4 more files" || strings.Contains(got[1], "uncommitted") {
		t.Fatalf("many: %q", got)
	}
	for _, c := range []struct {
		info changeInfo
		want string
	}{
		{changeInfo{noRepo: true}, "not in a repository"},
		{changeInfo{noStart: true}, "No session has worked on it yet."},
		{changeInfo{loading: true}, "reading…"},
		{changeInfo{err: errors.New("jj: boom")}, "jj: boom"},
		{changeInfo{}, "no changes"},
	} {
		if got := drawn(changesCard("Changes", "", c.info), 50); !strings.Contains(got[1], c.want) {
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
	got := drawn(issuesCard(others, map[int]int{7: 2}, true, 2, ""), 50)
	wantLines(t, "issues", got, []string{
		"── Issues ────────────────────────────── 9 open ──",
		"   in review    #4 show cost",
		"   in progress  #7 tidy · 2 sessions",
		" ▸ blocked      #14 stuck",
		"   up next      #9 urgent",
		"                #5 threads",
		"                #10 later",
		"                … 1 more",
		"   inbox 2",
	})
	ids := []int{}
	for _, i := range pickable(others) {
		ids = append(ids, i.ID)
	}
	if !slices.Equal(ids, []int{4, 7, 14, 9, 5, 10}) {
		t.Fatalf("pickable %v", ids)
	}
	// Each group lists a few, so the card stays short enough to show.
	var many []issues.Issue
	for n, st := range map[string]int{issues.Review: 5, issues.InProgress: 6, issues.Blocked: 4, issues.Ready: 9} {
		for j := 0; j < st; j++ {
			many = append(many, issues.Issue{ID: 100 + len(many), Title: "x", Status: n})
		}
	}
	text := ansi.Strip(strings.Join(drawn(issuesCard(many, nil, false, 0, ""), 50), "\n"))
	for _, want := range []string{"… 2 more", "… 2 more", "… 2 more", "… 6 more"} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q:\n%s", want, text)
		}
	}
	if n := len(pickable(many)); n != 3+4+2+3 {
		t.Fatalf("%d pickable, want those listed", n)
	}
	if got := drawn(issuesCard(nil, nil, false, 0, ""), 40); !strings.Contains(got[1], "no other open issues") {
		t.Fatalf("empty: %q", got)
	}
}

// An issue's card lists every criterion, those still to do before the
// done ones, each in the order the issue gives them.
func TestIssueCardListsPendingCriteriaFirst(t *testing.T) {
	var crit []issues.Criterion
	for i := 0; i < 12; i++ {
		crit = append(crit, issues.Criterion{Text: fmt.Sprint("c", i), Done: i%3 == 0})
	}
	got := drawn(issueCard(6, issueInfo{criteria: crit}), 40)
	var order []string
	for _, l := range got[2:] {
		order = append(order, strings.TrimSpace(ansi.Strip(l)))
	}
	want := []string{"○ c1", "○ c2", "○ c4", "○ c5", "○ c7", "○ c8", "○ c10", "○ c11", "✓ c0", "✓ c3", "✓ c6", "✓ c9"}
	if !slices.Equal(order, want) || !strings.Contains(ansi.Strip(got[1]), "acceptance  4 of 12") {
		t.Errorf("criteria:\n%s", strings.Join(append(got[1:2], order...), "\n"))
	}
}

func TestTasksCard(t *testing.T) {
	got := drawn(tasksCard("~/code/site", true), 60)
	text := strings.Join(got, "\n")
	for _, want := range []string{"This project doesn't track its tasks.", "each task becomes a workstream", "Start with issues init in ~/code/site."} {
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
	lines, _, places := layoutCards([][]card{{issuesCard(others, nil, true, 3, "")}}, 60, 4, issuesPanel)
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
		{80, 7, 2},  // a 2, b 3, then c and d need 4
		{80, 10, 4}, // c and d side by side, 4 high; e needs 2 more
		{60, 10, 4}, // stacked: c 2, then d needs 4 and is shortened to 3
		{60, 9, 3},  // stacked: c 2, then 2 lines are too few for d
		{60, 1, 0},
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

// The cockpit of a workstream, read through the fake issues and a git
// repository whose commands a fake answers.
func TestTheCockpitReadsTheIssueAndTheChanges(t *testing.T) {
	m := taskModel(t, newFakeMux())
	m = update(m, keyMsg("r"))
	started := time.Unix(1_700_000_000, 0)
	rows := slices.Clone(m.in.Rows)
	for i := range rows {
		rows[i].StartedAt = started.UnixMilli()
	}
	m = update(m, rowsMsg{rows: rows, ttys: m.in.TTYs})
	// The project tracks its issues and is a git repository. The issues
	// CLI lists the tracker's issues, as each refresh reads them, and
	// shows #6's details.
	shows := 0
	m.issues = func(args ...string) ([]byte, error) {
		if slices.Contains(args, "list") {
			return []byte(`{"issues": [{"id": 6, "ref": "app#6", "project": "app", "title": "keep width", "status": "in-progress", "priority": "p1"},` +
				`{"id": 4, "ref": "app#4", "project": "app", "title": "show cost", "status": "review", "priority": "p2"}]}`), nil
		}
		shows++
		return []byte(`{"id": 6, "title": "keep width", "details-text": "# keep width\n\nKeep it.\n\n## Acceptance criteria\n\n- [x] kept\n- [ ] tested\n"}`), nil
	}
	var asked [][]string
	m.deps.exists = func(p string) bool { return p == "/code/app/.git" || p == "/code/app/.issues" }
	// The tracker was read from the .issues there is.
	tr := m.trackers["/code/app"]
	tr.stamp = issueStamp(m.deps.glob, m.deps.modTime, "/code/app")
	m.trackers = trackers{"/code/app": tr}
	m = m.setInputs(nil)
	m.deps.vcs = func(dir string, args ...string) ([]byte, error) {
		asked = append(asked, args)
		switch {
		case slices.Contains(args, "reflog"):
			return []byte("bbb HEAD@{1700000100}\naaa HEAD@{1699999000}\n"), nil
		case slices.Contains(args, "rev-parse"):
			return []byte("bbb\n"), nil
		case slices.Contains(args, "ls-files"):
			return nil, nil
		case slices.Contains(args, "aaa"):
			return []byte("diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1,2 @@\n-old\n+new\n+more\n" +
				"diff --git a/y.go b/y.go\n--- a/y.go\n+++ b/y.go\n@@ -1 +1 @@\n-a\n+b\n"), nil
		case slices.Contains(args, "bbb"):
			return []byte("diff --git a/y.go b/y.go\n--- a/y.go\n+++ b/y.go\n@@ -1 +1 @@\n-a\n+b\n"), nil
		}
		return nil, errors.New("unexpected " + strings.Join(args, " "))
	}
	m = selectKey(t, m, "s1")
	m = resized(t, m, 150, 40)
	m = pressSettled(t, m, "o")
	text := ansi.Strip(m.View())
	for _, want := range []string{"Keep it.", "acceptance  1 of 2", "✓ kept", "○ tested", "+3 −2 in 2 files", "1 uncommitted", "x.go"} {
		if !strings.Contains(text, want) {
			t.Errorf("the cockpit lacks %q:\n%s", want, text)
		}
	}
	if shows != 1 {
		t.Fatalf("issues show ran %d times", shows)
	}
	// Soon after, nothing is read again; once a session's state changes,
	// the changes are, from the base found before.
	n := len(asked)
	m = pressSettled(t, m, "r")
	if len(asked) != n {
		t.Fatalf("read again with nothing changed: %v", asked[n:])
	}
	rows[0].State = fleet.Working
	m, cmd := step(m, rowsMsg{rows: rows, ttys: m.in.TTYs})
	m = settle(t, m, cmd)
	if targetTask(m.target()) != "/code/app#6" || len(m.trackers["/code/app"].issues) != 2 {
		t.Fatalf("the refresh lost the workstream: target %+v, tracker %+v", m.target(), m.trackers["/code/app"])
	}
	if len(asked) == n || slices.ContainsFunc(asked[n:], func(a []string) bool { return slices.Contains(a, "reflog") }) {
		t.Fatalf("after a state change: %v", asked[n:])
	}
	if shows != 1 {
		t.Fatalf("issues show ran %d times with .issues unchanged", shows)
	}
}

// A workstream's changes count from when it was first taken on, the
// earliest link among its sessions, and that base holds: a session ending
// does not move it later, time passing reads nothing again, and only a
// session that took it on sooner moves it earlier.
func TestTheChangesBaseKeepsToTheEarliestLink(t *testing.T) {
	m := taskModel(t, newFakeMux())
	linked := func(r fleet.Row, at int64) fleet.Row {
		r.StartedAt = 1_700_000_300_000
		r.Tasks = []fleet.TaskRef{{Project: "app", Dir: "/code/app", ID: 6, Linked: time.UnixMilli(at)}}
		return r
	}
	s1, s2 := linked(m.in.Rows[0], 1_700_000_000_000), linked(m.in.Rows[1], 1_700_000_200_000)
	clock := time.Unix(1_700_001_000, 0)
	m.deps.now = func() time.Time { return clock }
	reflogs, reads := 0, 0
	m.deps.exists = func(p string) bool { return p == "/code/app/.git" }
	m.deps.vcs = func(dir string, args ...string) ([]byte, error) {
		reads++
		switch {
		case slices.Contains(args, "reflog"):
			reflogs++
			return []byte("bbb HEAD@{1700000100}\naaa HEAD@{1699999000}\n"), nil
		case slices.Contains(args, "rev-parse"):
			return []byte("bbb\n"), nil
		}
		return nil, nil
	}
	set := func(rows ...fleet.Row) {
		t.Helper()
		var cmd tea.Cmd
		m, cmd = step(m, rowsMsg{rows: rows, ttys: m.in.TTYs})
		m = settle(t, m, cmd)
	}
	set(s1, s2)
	m = selectKey(t, m, "task:/code/app#6")
	m = resized(t, m, 150, 40)
	m = pressSettled(t, m, "o")
	key := taskPrefix + "/code/app#6"
	if c := m.changeReads[key]; c.base != "aaa" || c.start != 1_700_000_000_000 || reflogs != 1 {
		t.Fatalf("base %q start %d after %d reflogs", c.base, c.start, reflogs)
	}
	// Time passes with nothing changed: nothing is read.
	n := reads
	clock = clock.Add(time.Minute)
	set(s1, s2)
	if reads != n {
		t.Fatalf("read again with nothing changed: %d", reads-n)
	}
	// The session that took it on first ends: the base stays.
	set(s2)
	if c := m.changeReads[key]; c.base != "aaa" || c.start != 1_700_000_000_000 || reflogs != 1 || reads == n {
		t.Fatalf("after s1 ended: base %q start %d, %d reflogs, %d reads", c.base, c.start, reflogs, reads-n)
	}
	// One that took it on sooner moves it earlier.
	set(s2, linked(s1, 1_699_999_500_000))
	if c := m.changeReads[key]; c.start != 1_699_999_500_000 || reflogs != 2 {
		t.Fatalf("after an earlier link: start %d, %d reflogs", c.start, reflogs)
	}
}

func TestIssuesCardSaysWhyTheReadFailed(t *testing.T) {
	others := []issues.Issue{{ID: 4, Title: "show cost", Status: issues.Review}}
	got := drawn(issuesCard(others, nil, false, 0, issues.OneLine("issues --dir /w/app list --json: exit status 2:\nno such\tthing")), 40)
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
	c := changesCard("Changes", "now", changeInfo{})
	if got := ansi.Strip(c.right); got != "just started" {
		t.Errorf("changes card right = %q", got)
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
	msg := changesCmd("k", "", "/code/app", 0, "", false, d)().(changesReadMsg)
	got := strings.TrimSpace(ansi.Strip(strings.Join(drawn(changesCard("Uncommitted", "", msg.info), 40), "\n")))
	if !strings.Contains(got, "not a git repository") || strings.Contains(got, "rev-parse") || strings.Contains(got, "fatal") {
		t.Fatalf("the card reads:\n%s", got)
	}
}
