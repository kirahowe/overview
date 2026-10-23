package tui

import (
	"errors"
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
	for _, l := range c.render(body, w, len(body)+2) {
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
	ms := []member{{name: "list squeeze", word: "working", age: "3m", style: green}, {name: "go", term: true}}
	got := drawn(streamHeader("overview#6", "in-progress", "p1", "keep the list's width", "2h", ms), 60)
	wantLines(t, "header", got, []string{
		"┌─ overview#6 ────────────────────────── in-progress · p1 ─┐",
		"│ keep the list's width                     started 2h ago │",
		"│ ● list squeeze working 3m   $ go                         │",
		"│  enter  attach   v  review   c  claude   t  term         │",
		"└──────────────────────────────────────────────────────────┘",
	})
	// No sessions yet, and no start.
	got = drawn(streamHeader("app#4", "review", "p2", "show cost", "", nil), 40)
	if got[1] != "│ show cost                            │" || !strings.Contains(got[2], "no sessions") {
		t.Fatalf("empty header: %q", got)
	}
}

func TestProjectHeaderCard(t *testing.T) {
	got := drawn(projectHeader("website", "~/code/website", "jj", -1, nil), 50)
	wantLines(t, "untracked", got, []string{
		"┌─ website ────────────────────────── no issues ─┐",
		"│ ~/code/website                              jj │",
		"│ no sessions                                    │",
		"│  c  claude   t  term                           │",
		"└────────────────────────────────────────────────┘",
	})
	if got := drawn(projectHeader("app", "/w/app", "git", 3, nil), 50); !strings.HasSuffix(got[0], " 3 open ─┐") {
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
		"┌─ Issue ──────────────────────── issues show 6 ─┐",
		"│ Keep the list at the width it was dragged to.  │",
		"│                                                │",
		"│ acceptance  1 of 2                             │",
		"│ ✓ keeps its width                              │",
		"│ ○ e2e covers it                                │",
		"└────────────────────────────────────────────────┘",
	})
	long := issueInfo{summary: strings.Repeat("word ", 80)}
	if got := drawn(issueCard(6, long), 30); len(got) != issueSummaryLines+2 || !strings.Contains(got[issueSummaryLines], "…") {
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
		"┌─ Changes ─────────────────────────── since 2h ─┐",
		"│ +163 −35 in 3 files              2 uncommitted │",
		"│ internal/tui/view.go                  +120 − 4 │",
		"│ internal/tui/stage.go                 + 40 −30 │",
		"│ README.md                             +  3 − 1 │",
		"└────────────────────────────────────────────────┘",
	})
	var many []fileStat
	for i := range 9 {
		many = append(many, fileStat{path: string(rune('a'+i)) + ".go", add: 10 - i})
	}
	got = drawn(changesCard("Uncommitted", "", changeInfo{files: many, uncommitted: -1}), 40)
	if len(got) != 2+1+shownFiles || got[len(got)-2] != "│ … 4 more files                       │" || strings.Contains(got[1], "uncommitted") {
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
		"┌─ Review ─────────────────────────────┐",
		"│ 3 open                1 replies wait │",
		"│  v  open   s  send                   │",
		"└──────────────────────────────────────┘",
	})
	if got := drawn(reviewCard(review.Summary{}), 40); !strings.Contains(got[1], "no notes") || len(got) != 3 {
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
	got := drawn(issuesCard(others, map[int]int{7: 2}, true, 2, nil), 50)
	wantLines(t, "issues", got, []string{
		"┌─ Issues ────────────────────────────── 9 open ─┐",
		"│   in review    #4 show cost                    │",
		"│   in progress  #7 tidy · 2 sessions            │",
		"│ ▸ blocked      #14 stuck                       │",
		"│   up next      #9 urgent                       │",
		"│                #5 threads                      │",
		"│                #10 later                       │",
		"│   inbox 2                                      │",
		"└────────────────────────────────────────────────┘",
	})
	ids := []int{}
	for _, i := range pickable(others) {
		ids = append(ids, i.ID)
	}
	if !slices.Equal(ids, []int{4, 7, 14, 9, 5, 10}) {
		t.Fatalf("pickable %v", ids)
	}
	if got := drawn(issuesCard(nil, nil, false, 0, nil), 40); !strings.Contains(got[1], "no other open issues") {
		t.Fatalf("empty: %q", got)
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

func TestLayoutCardsDropsWhatDoesNotFit(t *testing.T) {
	box := func(name string, n int) card {
		return card{title: name, body: func(int) []string { return make([]string, n) }}
	}
	rows := [][]card{{box("a", 1)}, {box("b", 2)}, {box("c", 1), box("d", 3)}, {box("e", 1)}}
	for _, c := range []struct{ w, h, shown int }{
		{80, 40, 5},
		{80, 7, 2},  // a 3, b 4, then c and d need 5
		{80, 12, 4}, // c and d side by side, 5 high; e needs 3 more
		{60, 15, 4}, // stacked: c 3, d 5
		{60, 2, 0},
	} {
		lines, shown := layoutCards(rows, c.w, c.h)
		if shown != c.shown || len(lines) != c.h {
			t.Errorf("%dx%d: shown %d in %d lines, want %d", c.w, c.h, shown, len(lines), c.shown)
		}
	}
	lines, _ := layoutCards(rows, 80, 40)
	if l := ansi.Strip(lines[7]); !strings.HasPrefix(l, "┌─ c ") || !strings.Contains(l, "┐ ┌─ d ") {
		t.Fatalf("c and d are not side by side: %q", l)
	}
}

// The cockpit of a workstream, read through the fake issues and a git
// repository whose commands a fake answers.
func TestTheCockpitReadsTheIssueAndTheChanges(t *testing.T) {
	m := taskModel(t, newFakeMux())
	m = update(m, keyMsg("r"))
	started := time.Unix(1_700_000_000, 0)
	rows := slices.Clone(m.rows)
	for i := range rows {
		rows[i].StartedAt = started.UnixMilli()
	}
	m = update(m, rowsMsg{rows: rows, ttys: m.ttys})
	shows := 0
	m.issues = func(args ...string) ([]byte, error) {
		shows++
		return []byte(`{"id": 6, "title": "keep width", "details-text": "# keep width\n\nKeep it.\n\n## Acceptance criteria\n\n- [x] kept\n- [ ] tested\n"}`), nil
	}
	var asked [][]string
	m.deps.exists = func(p string) bool { return p == "/code/app/.git" }
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
	m = update(m, tea.WindowSizeMsg{Width: 150, Height: 40})
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
	m, cmd := step(m, rowsMsg{rows: rows, ttys: m.ttys})
	settle(t, m, cmd)
	if len(asked) == n || slices.ContainsFunc(asked[n:], func(a []string) bool { return slices.Contains(a, "reflog") }) {
		t.Fatalf("after a state change: %v", asked[n:])
	}
	if shows != 1 {
		t.Fatalf("issues show ran %d times with .issues unchanged", shows)
	}
}

// A failed read says why at the top of the card, on lines that fit, and
// the card still lists what the last good read found.
func TestIssuesCardSaysWhyTheReadFailed(t *testing.T) {
	others := []issues.Issue{{ID: 4, Title: "show cost", Status: issues.Review}}
	got := drawn(issuesCard(others, nil, false, 0, errors.New("issues --dir /w/app list --json: exit status 2:\nno such\tthing")), 40)
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
