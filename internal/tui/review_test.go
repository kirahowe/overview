package tui

import (
	"path/filepath"

	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"overview/internal/fleet"
	"overview/internal/review"
	"overview/internal/world"
)

// v opens a session's review in a split above its pane, with everything
// the review needs on its command line, and the keyboard goes into it. A
// second v goes back to that review rather than opening another.
func TestReviewOpensUnderTheSession(t *testing.T) {
	f := newFakeMux()
	row := fleet.Row{Live: fleet.Live{PID: 7, SessionID: "s-1", Name: "fix-parser", Cwd: "/code/app", StartedAt: 1792634408123},
		Transcript: "/t/s-1.jsonl", State: fleet.Idle}
	m := stagedModel(t, f, row)
	m.reviewArgs = []string{"--theme", "one-dark", "--config", "/cfg", "--socket-path", "/tmp/s"}
	m = pressSettled(t, m, "v")
	if len(f.splits) != 1 {
		t.Fatalf("splits %+v", f.splits)
	}
	session := paneOf(m, row)
	store := filepath.Join(m.home.Dir, "overview", "reviews", "s-1.jsonl")
	want := []string{"overview", "review", "view", "--store", store, "--dir", "/code/app", "--target", session,
		"--title", "fix-parser", "--transcript", "/t/s-1.jsonl", "--started", "1792634408",
		"--theme", "one-dark", "--config", "/cfg", "--socket-path", "/tmp/s"}
	if got := f.splits[0].command; !slices.Equal(got, want) {
		t.Fatalf("command\n got %q\nwant %q", got, want)
	}
	p := reviewPane(m.in.Panes, "s-1")
	if p == "" || f.panes[f.find(p)].Window != f.panes[f.find(session)].Window {
		t.Fatalf("the review should be marked and in the session's window: %+v", m.in.Panes)
	}
	if f.focus != p {
		t.Fatalf("the keyboard should go to the review %s, not %s", p, f.focus)
	}
	// Back in the list, v goes to the review again.
	f.focus = "%0"
	m = pressSettled(t, m, "v")
	if len(f.splits) != 1 {
		t.Fatalf("a second v opened another review: %+v", f.splits)
	}
	if f.focus != p {
		t.Fatalf("the second v should focus the review %s, not %s", p, f.focus)
	}
}

// A session with no pane in overview's tmux has nowhere to put a review.
func TestReviewNeedsASessionPane(t *testing.T) {
	f := newFakeMux()
	bg := fleet.Row{Live: fleet.Live{PID: 9, ID: "bg1", Kind: "background", SessionID: "s-2", Name: "bg", Cwd: "/code/app"}}
	m := stagedModel(t, f, bg)
	m = pressSettled(t, m, "v")
	if len(f.splits) != 0 || !strings.Contains(m.note, "above a session") {
		t.Fatalf("splits %+v note %q", f.splits, m.note)
	}
}

// A session's row says how many of its review's notes are open, and that
// a reply waits.
func TestReviewBadge(t *testing.T) {
	for _, c := range []struct {
		s    review.Summary
		want string
	}{
		{review.Summary{}, ""},
		{review.Summary{Open: 2}, "✎2"},
		{review.Summary{Open: 3, Waiting: 1}, "✎3 reply"},
	} {
		if got := reviewBadge(c.s); got != c.want {
			t.Errorf("%+v: %q, want %q", c.s, got, c.want)
		}
	}
	row := fleet.Row{Live: fleet.Live{PID: 7, SessionID: "s-1", Name: "fix-parser", Cwd: "/code/app"}, State: fleet.Idle}
	m := newTestModel(t, row)
	m = m.setInputs(func(in *world.Inputs) { in.Reviews = map[string]review.Summary{"s-1": {Open: 2, Waiting: 1}} })
	if !strings.Contains(m.View(), "✎2 reply") {
		t.Fatalf("no badge in\n%s", m.View())
	}
}

// The refresh reads each session's review from its log.
func TestReadReviews(t *testing.T) {
	home := fleet.Home{Dir: t.TempDir()}
	path, _ := review.PathFor(home.ReviewsDir(), "s-1")
	a := review.Anchor{Path: "x.go", Start: 1, End: 1, Lines: []string{"x"}}
	if err := (review.Store{Path: path}).Append(
		review.Event{Op: review.OpNote, ID: "aaaaa", Author: review.User, Body: "why?", Anchor: &a},
		review.Event{Op: review.OpReply, ID: "bbbbb", Note: "aaaaa", Author: review.Agent, Body: "because"},
	); err != nil {
		t.Fatal(err)
	}
	got := readReviews(home, []fleet.Row{{Live: fleet.Live{SessionID: "s-1"}}, {Live: fleet.Live{SessionID: "s-2"}}})
	if len(got) != 1 || got["s-1"] != (review.Summary{Open: 1, Waiting: 1, Unsent: 1}) {
		t.Fatalf("%+v", got)
	}
}

// v twice before the first review's pane lands still opens one review.
func TestReviewOpensOnceWhilePending(t *testing.T) {
	f := newFakeMux()
	row := fleet.Row{Live: fleet.Live{PID: 7, SessionID: "s-1", Name: "fix-parser", Cwd: "/code/app"}, State: fleet.Idle}
	m := stagedModel(t, f, row)
	m, first := step(m, keyMsg("v"))
	m, second := step(m, keyMsg("v"))
	m = settle(t, m, tea.Batch(first, second))
	if len(f.splits) != 1 {
		t.Fatalf("splits %+v", f.splits)
	}
	if len(m.opening) != 0 {
		t.Fatalf("still opening %v", m.opening)
	}
}
