package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"overview/internal/issues"
)

// verdictModel is taskModel with an issues CLI that records the verdicts
// it is given, and fails them with err.
func verdictModel(t *testing.T, f *fakeMux, err error) (model, *[][]string) {
	t.Helper()
	m := taskModel(t, f)
	var calls [][]string
	m.issues = func(args ...string) ([]byte, error) {
		if slices.Contains(args, "list") || slices.Contains(args, "show") {
			return []byte("[]"), nil
		}
		calls = append(calls, args)
		return nil, err
	}
	return m, &calls
}

func TestApprovingATaskInReview(t *testing.T) {
	m, calls := verdictModel(t, newFakeMux(), nil)
	m = pressSettled(t, selectKey(t, m, "task:/code/app#4"), "m")
	text := ansi.Strip(m.View())
	for _, want := range []string{"app#4 show cost", "a approve   c send back   r reject"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the box lacks %q:\n%s", want, text)
		}
	}
	m = pressSettled(t, m, append([]string{"a"}, typed("looks right")...)...)
	if !strings.Contains(ansi.Strip(m.View()), "note, optional: looks right") {
		t.Fatalf("the note is not shown:\n%s", ansi.Strip(m.View()))
	}
	m = pressSettled(t, m, "enter")
	want := [][]string{{"--dir", "/code/app", "approve", "4", "--note", "looks right"}}
	if !slices.EqualFunc(*calls, want, slices.Equal) || m.verdict.open() || m.note != "approved app#4" {
		t.Fatalf("calls %v, open %v, note %q", *calls, m.verdict.open(), m.note)
	}
}

// An issue that is not in review has no work to send back: m offers done
// and drop, and c, like any other key, cancels.
func TestMarkingATaskInProgress(t *testing.T) {
	m, calls := verdictModel(t, newFakeMux(), nil)
	m = pressSettled(t, selectKey(t, m, "task:/code/app#6"), "m")
	text := ansi.Strip(m.View())
	if !strings.Contains(text, "a done   r drop") || strings.Contains(text, "send back") {
		t.Fatalf("the box:\n%s", text)
	}
	m = pressSettled(t, m, "c")
	if m.verdict.open() || len(*calls) != 0 || m.note != "left it as it was" {
		t.Fatalf("open %v, calls %v, note %q", m.verdict.open(), *calls, m.note)
	}
	m = pressSettled(t, m, "m", "r", "enter")
	if want := [][]string{{"--dir", "/code/app", "reject", "6"}}; !slices.EqualFunc(*calls, want, slices.Equal) || m.note != "dropped app#6" {
		t.Fatalf("calls %v, note %q", *calls, m.note)
	}
}

// Changes needs a note, and once given it, a session starts on the issue
// with the note in its prompt, and the cockpit closes for it.
func TestRequestingChangesStartsASession(t *testing.T) {
	f := newFakeMux()
	m, calls := verdictModel(t, f, nil)
	m = resized(t, selectKey(t, m, "task:/code/app#4"), 150, 40)
	m = pressSettled(t, m, "o", "m", "c", "enter")
	if !m.verdict.open() || len(*calls) != 0 || !strings.HasPrefix(m.note, "say what to change") {
		t.Fatalf("an empty note: open %v, calls %v, note %q", m.verdict.open(), *calls, m.note)
	}
	m = pressSettled(t, m, append(typed("cover the empty case"), "enter")...)
	if want := [][]string{{"--dir", "/code/app", "changes", "4", "--note", "cover the empty case"}}; !slices.EqualFunc(*calls, want, slices.Equal) {
		t.Fatalf("calls %v", *calls)
	}
	if len(f.spawned) != 1 || m.cockpit.open {
		t.Fatalf("spawned %+v, open %v", f.spawned, m.cockpit.open)
	}
	prompt := f.spawned[0].command[len(f.spawned[0].command)-1]
	if !strings.HasPrefix(prompt, "Work on app#4: show cost\n") || !strings.Contains(prompt, "cover the empty case") {
		t.Fatalf("prompt %q", prompt)
	}
	if !opens(prompt, "app", issues.Issue{ID: 4}) {
		t.Fatalf("prompt %q does not link its session to app#4", prompt)
	}
}

// In the cockpit's issues card, m marks the issue under the card's cursor.
func TestMarkingAnIssueInTheIssuesCard(t *testing.T) {
	m, calls := verdictModel(t, newFakeMux(), nil)
	m = withIssues(selectKey(t, m, "s2"), issues.Issue{ID: 8, Ref: "app#8", Title: "wait on tmux", Status: issues.Blocked, Priority: "p2"})
	m = resized(t, m, 150, 40)
	m = pressSettled(t, m, "o", "tab", "j", "j")
	_, picked, _ := m.picked()
	m = pressSettled(t, m, "m", "a", "enter")
	if want := [][]string{{"--dir", "/code/app", "approve", "8"}}; picked.ID != 8 || !slices.EqualFunc(*calls, want, slices.Equal) {
		t.Fatalf("picked #%d, calls %v", picked.ID, *calls)
	}
}

func TestAFailedVerdictSaysWhy(t *testing.T) {
	m, _ := verdictModel(t, newFakeMux(), errors.New("issues approve 4: exit status 1"))
	m = pressSettled(t, selectKey(t, m, "task:/code/app#4"), "m", "a", "enter")
	if !strings.HasPrefix(m.note, "couldn't mark app#4: ") {
		t.Fatalf("note %q", m.note)
	}
}

// m on anything but a task, outside the issues card, says what it is for.
func TestMarkOffATaskSaysWhatItIsFor(t *testing.T) {
	m, _ := verdictModel(t, newFakeMux(), nil)
	m = pressSettled(t, selectKey(t, m, "s2"), "m")
	if m.verdict.open() || !strings.HasPrefix(m.note, "m marks a task's issue") {
		t.Fatalf("open %v, note %q", m.verdict.open(), m.note)
	}
}
