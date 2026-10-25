package tui

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"overview/internal/issues"
)

func iss(id int, status, prio, title string) issues.Issue {
	return issues.Issue{ID: id, Status: status, Priority: prio, Title: title}
}

func rowIDs(rs []taskRow) []int {
	var out []int
	for _, r := range rs {
		out = append(out, r.issue.ID)
	}
	return out
}

func TestTaskRows(t *testing.T) {
	open := []issues.Issue{
		iss(1, issues.Review, "p0", "show cost"),
		iss(2, issues.Inbox, "p1", "fix scroll"),
		iss(3, issues.Ready, "p2", "add threads"),
		iss(4, issues.Ready, "p0", "keep width"),
		iss(5, issues.InProgress, "p1", "rework list"),
		iss(6, issues.Blocked, "p0", "wait on tmux"),
		iss(7, issues.Done, "p0", "old"),
		iss(8, issues.Ready, "p2", "add search"),
	}
	cases := []struct {
		query string
		want  []int
	}{
		// ready, inbox, then review, in progress, blocked; by priority, then number.
		{"", []int{4, 3, 8, 2, 1, 5, 6}},
		{"add", []int{3, 8}},
		{"ADD Thr", []int{3}},
		{"#5", []int{5}},
		{"app#6", []int{6}},
		{"app#", []int{4, 3, 8, 2, 1, 5, 6}},
		{"add #1", nil},
		{"old", nil},
	}
	for _, c := range cases {
		if got := rowIDs(taskRows(open, "app", counted(nil), c.query)); !slices.Equal(got, c.want) {
			t.Errorf("%q: %v, want %v", c.query, got, c.want)
		}
	}
	rs := taskRows(open, "app", counted(map[int]int{5: 2, 1: 1}), "")
	counts := map[int]int{}
	for _, r := range rs {
		counts[r.issue.ID] = r.sessions
	}
	if counts[5] != 2 || counts[1] != 1 || counts[3] != 0 {
		t.Fatalf("sessions %v", counts)
	}
}

func TestTaskListNotes(t *testing.T) {
	some := tracked("app", iss(3, issues.Ready, "p2", "add threads"))
	cases := []struct {
		name                   string
		t                      tracker
		tracked, reading, tool bool
		query                  string
		note                   string
		rows                   int
	}{
		{"no tool", tracker{}, false, false, false, "", "Install the issues tool to track tasks.", 0},
		{"untracked", tracker{}, false, false, true, "", "app doesn't track its tasks. issues init in its root starts it.", 0},
		{"unread", tracker{}, false, true, true, "", "reading…", 0},
		{"failed", tracker{id: "app", err: errors.New("issues: boom")}, true, false, true, "", "issues failed: issues: boom", 0},
		{"failed with the last list", tracker{id: "app", issues: some.issues, err: errors.New("boom")}, true, false, true, "", "issues failed: boom", 1},
		{"none open", tracked("app"), true, false, true, "", "No open issues in app.", 0},
		{"only done", tracked("app", iss(1, issues.Done, "p1", "x")), true, false, true, "", "No open issues in app.", 0},
		{"no match", some, true, false, true, "zzz", "Nothing matches.", 0},
		{"rows", some, true, true, true, "", "", 1},
	}
	for _, c := range cases {
		l := taskListOf("app", c.t, c.tracked, c.reading, c.tool, counted(nil), c.query)
		if l.note != c.note || len(l.rows) != c.rows {
			t.Errorf("%s: note %q rows %d, want %q and %d", c.name, l.note, len(l.rows), c.note, c.rows)
		}
	}
}

func TestTaskOpens(t *testing.T) {
	if open, note := taskOpens("app", true, false, false); open || note != "Install the issues tool to track tasks." {
		t.Errorf("no tool: %v %q", open, note)
	}
	if open, note := taskOpens("app", false, false, true); open || !strings.HasPrefix(note, "app doesn't track") {
		t.Errorf("untracked: %v %q", open, note)
	}
	if open, _ := taskOpens("app", false, true, true); !open {
		t.Error("a project being read does not open")
	}
	if open, _ := taskOpens("app", true, false, true); !open {
		t.Error("a tracked project does not open")
	}
}

// counted counts the sessions on an issue from n, by number.
func counted(n map[int]int) func(issues.Issue) int {
	return func(i issues.Issue) int { return n[i.ID] }
}

// pinnedTasksModel has /code/todo pinned, tracked and with nothing
// running, /code/plain pinned without .issues, and the issues CLI.
func pinnedTasksModel(t *testing.T, f *fakeMux) model {
	t.Helper()
	fi := newFakeIssues()
	fi.project("/code/todo", "todo", "3 ready add threads", "4 inbox fix scroll", "8 ready add search", "9 done old")
	m := stagedModel(t, f)
	d := fi.deps()
	m.deps.exists, m.deps.glob, m.deps.modTime, m.deps.readFile = d.exists, d.glob, d.modTime, d.readFile
	m.issues = fi.run
	m, cmd := step(m, rowsMsg{pinned: []string{"/code/plain", "/code/todo"}})
	m = settle(t, m, cmd)
	return resized(t, m, 60, 30)
}

func TestSOnAPinnedProjectStartsATask(t *testing.T) {
	f := newFakeMux()
	m := selectKey(t, pinnedTasksModel(t, f), "project:/code/todo")
	text := ansi.Strip(m.View())
	if !strings.Contains(text, "+ s task · c claude · t terminal") || !strings.Contains(ansi.Strip(m.footer()), "s task • c claude") {
		t.Fatalf("the placeholder does not offer s:\n%s", text)
	}
	m = pressSettled(t, m, "s")
	if !m.taskPick.open || m.taskPick.root != "/code/todo" {
		t.Fatalf("picker %+v", m.taskPick)
	}
	text = ansi.Strip(m.View())
	for _, want := range []string{"› ▏", "#3 add threads", "ready p2", "#4 fix scroll", "inbox p2", "start a task in /code/todo"} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "#9") || strings.Index(text, "#8") > strings.Index(text, "#4") {
		t.Errorf("a done issue, or inbox before ready:\n%s", text)
	}
	m = pressSettled(t, m, typed("thr")...)
	if l := m.taskList(); !slices.Equal(rowIDs(l.rows), []int{3}) {
		t.Fatalf("typing narrowed to %v", rowIDs(l.rows))
	}
	m = pressSettled(t, m, "enter")
	if len(f.spawned) != 1 || f.spawned[0].dir != "/code/todo" || !slices.Equal(f.spawned[0].command[1:], []string{"Work on todo#3: add threads"}) {
		t.Fatalf("spawned %+v", f.spawned)
	}
	if m.taskPick.open {
		t.Fatal("the picker stayed open")
	}
	if m.opened.ID == "" || !f.staged(m.opened.ID) || f.focus != m.opened.ID {
		t.Fatalf("the new session %s is not shown and focused: list in %s, focus %s", m.opened.ID, f.stage(), f.focus)
	}
}

func TestTaskPickerKeysAndMouse(t *testing.T) {
	f := newFakeMux()
	m := selectKey(t, pinnedTasksModel(t, f), "project:/code/todo")
	m = pressSettled(t, m, "s", "down")
	if m.taskPick.sel != 1 {
		t.Fatalf("down: sel %d", m.taskPick.sel)
	}
	m, _ = step(m, tea.KeyMsg{Type: tea.KeyCtrlP})
	if m.taskPick.sel != 0 {
		t.Fatalf("ctrl+p: sel %d", m.taskPick.sel)
	}
	m = wheel(m, 5, 5, true)
	if m.taskPick.sel != 1 {
		t.Fatalf("wheel: sel %d", m.taskPick.sel)
	}
	// Lines: query, blank, then #3, #8, #4 with a blank line between
	// each, under the rule tmux draws.
	y := 2 + 4
	m, _ = step(m, tea.MouseMsg{X: 5, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m.taskPick.sel != 2 || len(f.spawned) != 0 {
		t.Fatalf("a click: sel %d, spawned %v", m.taskPick.sel, f.spawned)
	}
	m, cmd := step(m, tea.MouseMsg{X: 5, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = settle(t, m, cmd)
	if len(f.spawned) != 1 || !slices.Equal(f.spawned[0].command[1:], []string{"Work on todo#4: fix scroll"}) || m.taskPick.open {
		t.Fatalf("a double click: spawned %+v, open %v", f.spawned, m.taskPick.open)
	}
	m = selectKey(t, m, "project:/code/todo")
	m = pressSettled(t, m, "s", "x", "esc")
	if m.taskPick.open || len(f.spawned) != 1 {
		t.Fatalf("esc: open %v, spawned %d", m.taskPick.open, len(f.spawned))
	}
}

func TestSOnAnUntrackedProjectOnlySays(t *testing.T) {
	f := newFakeMux()
	m := selectKey(t, pinnedTasksModel(t, f), "project:/code/plain")
	if strings.Contains(ansi.Strip(m.footer()), "s task") || !strings.Contains(ansi.Strip(m.View()), "▌+ c claude · t terminal") {
		t.Fatalf("an untracked project offers s:\n%s", ansi.Strip(m.View()))
	}
	m = pressSettled(t, m, "s")
	if m.taskPick.open || m.note != "/code/plain doesn't track its tasks. issues init in its root starts it." {
		t.Fatalf("open %v, note %q", m.taskPick.open, m.note)
	}
	m.issues = nil
	m = pressSettled(t, m, "s")
	if m.taskPick.open || m.note != "Install the issues tool to track tasks." {
		t.Fatalf("without the tool: open %v, note %q", m.taskPick.open, m.note)
	}
}

// enter on an issue that sessions already work on goes to its workstream
// and starts nothing.
func TestTaskPickerGoesToALiveWorkstream(t *testing.T) {
	f := newFakeMux()
	m := withIssues(selectKey(t, taskModel(t, f), "s2"), issues.Issue{ID: 9, Ref: "app#9", Title: "add threads", Status: issues.Ready, Priority: "p2"})
	m.issues = func(...string) ([]byte, error) { return nil, errors.New("not run") }
	m = resized(t, m, 60, 30)
	m = pressSettled(t, m, "s")
	text := ansi.Strip(m.View())
	if !strings.Contains(text, "in-progress · 1 session p1") {
		t.Fatalf("no session count:\n%s", text)
	}
	m = pressSettled(t, m, typed("width")...)
	if !strings.Contains(ansi.Strip(m.footer()), "enter go to") {
		t.Fatalf("footer %q", ansi.Strip(m.footer()))
	}
	m = pressSettled(t, m, "enter")
	if len(f.spawned) != 0 || m.taskPick.open || m.sel != "task:/code/app#6" {
		t.Fatalf("spawned %+v, open %v, sel %s", f.spawned, m.taskPick.open, m.sel)
	}
}

// s in the cockpit closes it, then opens the picker in the list's place,
// keeping the zoom.
func TestSClosesTheCockpit(t *testing.T) {
	f := newFakeMux()
	m := selectKey(t, taskModel(t, f), "s2")
	m.issues = func(...string) ([]byte, error) { return nil, errors.New("not run") }
	m = resized(t, m, 150, 40)
	m = pressSettled(t, m, "o")
	if !m.cockpit.open {
		t.Fatal("no cockpit")
	}
	m = pressSettled(t, m, "s")
	if m.cockpit.open || !f.zoomed("%0") || !m.taskPick.open {
		t.Fatalf("cockpit %v zoomed %v picker %v", m.cockpit.open, f.zoomed("%0"), m.taskPick.open)
	}
	if !strings.Contains(ansi.Strip(m.View()), "#4 show cost") {
		t.Fatalf("the picker is not shown:\n%s", ansi.Strip(m.View()))
	}
}

func TestTheHolderOfAnIdleTrackedProjectOffersS(t *testing.T) {
	f := newFakeMux()
	m := selectKey(t, pinnedTasksModel(t, f), "project:/code/todo")
	if got := ansi.Strip(m.holderText(80, 10)); !strings.Contains(got, "s starts one of its tasks") {
		t.Errorf("holder for a tracked project:\n%s", got)
	}
	m = selectKey(t, m, "project:/code/plain")
	if got := ansi.Strip(m.holderText(80, 10)); strings.Contains(got, "s starts") || !strings.Contains(got, "c starts Claude here") {
		t.Errorf("holder for an untracked project:\n%s", got)
	}
}

// A title too long for its line wraps under itself rather than being
// cut, and a click on any of its lines picks it.
func TestTaskPickerWrapsLongTitles(t *testing.T) {
	f := newFakeMux()
	fi := newFakeIssues()
	fi.project("/code/todo", "todo", "3 ready add threads", "19 inbox add test paths from a reloaded tests.edn file when the watcher restarts")
	m := stagedModel(t, f)
	d := fi.deps()
	m.deps.exists, m.deps.glob, m.deps.modTime, m.deps.readFile = d.exists, d.glob, d.modTime, d.readFile
	m.issues = fi.run
	m, cmd := step(m, rowsMsg{pinned: []string{"/code/todo"}})
	m = settle(t, m, cmd)
	m = resized(t, m, 40, 30)
	m = pressSettled(t, selectKey(t, m, "project:/code/todo"), "s")
	lines, owners := m.taskPickLines()
	var text []string
	for _, l := range lines {
		text = append(text, strings.TrimRight(ansi.Strip(l), " "))
	}
	joined := strings.Join(text, "\n")
	if strings.Contains(joined, "…") {
		t.Fatalf("a title is cut:\n%s", joined)
	}
	at := slices.IndexFunc(text, func(s string) bool { return strings.Contains(s, "#19") })
	if at < 0 || !strings.HasSuffix(text[at], "inbox p2") || text[at-1] != "" {
		t.Fatalf("#19 is not on its own line with its status after a blank one:\n%s", joined)
	}
	var title []string
	for j := at; j < len(owners) && owners[j] == 1; j++ {
		s := strings.TrimSuffix(strings.TrimPrefix(text[j], "   #19 "), "inbox p2")
		title = append(title, strings.TrimSpace(s))
	}
	if len(title) < 2 || strings.Join(title, " ") != "add test paths from a reloaded tests.edn file when the watcher restarts" {
		t.Fatalf("the title shows as %q:\n%s", title, joined)
	}
	m, _ = step(m, tea.MouseMsg{X: 5, Y: at + 1, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m.taskPick.sel != 1 {
		t.Fatalf("a click on the title's second line: sel %d", m.taskPick.sel)
	}
}

// The picker zooms the list and shows the selected issue's details beside
// its rows, read as the cursor reaches each, and scrolled with pgdn when
// they are long. esc puts the pane back.
func TestTaskPickerShowsDetails(t *testing.T) {
	f := newFakeMux()
	fi := newFakeIssues()
	fi.project("/code/todo", "todo", "3 ready add threads", "4 inbox fix scroll")
	var long []string
	for n := 1; n <= 60; n++ {
		long = append(long, "- [ ] step "+strconv.Itoa(n))
	}
	fi.details["/code/todo#3"] = "# Add threads\n\nReplies nest under the\nmessage they answer.\n"
	fi.details["/code/todo#4"] = "# Fix scroll\n\n## Acceptance criteria\n\n" + strings.Join(long, "\n") + "\n"
	m := stagedModel(t, f)
	d := fi.deps()
	m.deps.exists, m.deps.glob, m.deps.modTime, m.deps.readFile = d.exists, d.glob, d.modTime, d.readFile
	m.issues = fi.run
	m, cmd := step(m, rowsMsg{pinned: []string{"/code/todo"}})
	m = settle(t, m, cmd)
	m = selectKey(t, m, "project:/code/todo")
	m = pressSettled(t, m, "s")
	if !m.taskPick.open || !f.zoomed(m.self) {
		t.Fatalf("picker %v, zoomed %v", m.taskPick.open, f.zoomed(m.self))
	}
	m = resized(t, m, 150, 30)
	m = settle(t, m, nil)
	text := ansi.Strip(m.View())
	for _, want := range []string{"#3 · ready · p2", "Replies nest under the message they answer.", "#3 add threads", "pgup/pgdn details"} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q:\n%s", want, text)
		}
	}
	m = pressSettled(t, m, "down")
	text = ansi.Strip(m.View())
	if !strings.Contains(text, "#4 · inbox · p2") || !strings.Contains(text, "- [ ] step 1 ") || strings.Contains(text, "step 60") {
		t.Fatalf("the long details:\n%s", text)
	}
	for range 10 {
		m = pressSettled(t, m, "pgdown")
	}
	text = ansi.Strip(m.View())
	if !strings.Contains(text, "step 60") || strings.Contains(text, "step 1 ") || !strings.Contains(text, "of 64") {
		t.Fatalf("pgdn does not reach the end:\n%s", text)
	}
	m = pressSettled(t, m, "pgup")
	if m.taskPick.top >= 64-28 {
		t.Fatalf("pgup after the end: top %d", m.taskPick.top)
	}
	m = pressSettled(t, m, "esc")
	if m.taskPick.open || f.zoomed(m.self) {
		t.Fatalf("esc: picker %v, zoomed %v", m.taskPick.open, f.zoomed(m.self))
	}
}

// enter ends the zoom before the session it starts is shown.
func TestTaskPickerEnterEndsTheZoom(t *testing.T) {
	f := newFakeMux()
	m := selectKey(t, pinnedTasksModel(t, f), "project:/code/todo")
	m = pressSettled(t, m, "s")
	if !f.zoomed(m.self) {
		t.Fatal("not zoomed")
	}
	m = pressSettled(t, m, "enter")
	if len(f.spawned) != 1 || f.zoomed(m.self) || m.opened.ID == "" || !f.staged(m.opened.ID) {
		t.Fatalf("spawned %d, zoomed %v, the session shown %v", len(f.spawned), f.zoomed(m.self), f.staged(m.opened.ID))
	}
}

func TestDetailLines(t *testing.T) {
	got := detailLines("# Title here\n\nA paragraph\nwrapped in the file.\n\n\n## Acceptance criteria\n\n- [ ] first item that\n      goes on\n2. second\n\n```\nlet x = 1\n```\n", 20)
	var plain []string
	for _, l := range got {
		plain = append(plain, ansi.Strip(l))
	}
	want := []string{
		"Title here",
		"",
		"A paragraph wrapped",
		"in the file.",
		"",
		"Acceptance criteria",
		"",
		"- [ ] first item",
		"      that goes on",
		"2. second",
		"",
		"let x = 1",
	}
	if !slices.Equal(plain, want) {
		t.Fatalf("got\n%q\nwant\n%q", plain, want)
	}
}

// Going to the pane beside the list ends the zoom in tmux, and the picker
// closes with it rather than zooming the list back over the user.
func TestTaskPickerClosesWhenTmuxEndsTheZoom(t *testing.T) {
	f := newFakeMux()
	m := selectKey(t, pinnedTasksModel(t, f), "project:/code/todo")
	m = pressSettled(t, m, "s")
	zooms := len(f.zooms)
	f.setZoom(f.stage(), false)
	m, cmd := step(m, listPanes(f, m.deps.roots, m.landed)())
	m = settle(t, m, cmd)
	if m.taskPick.open || len(f.zooms) != zooms || f.zoomed(m.self) {
		t.Fatalf("open %v zooms %v zoomed %v", m.taskPick.open, f.zooms, f.zoomed(m.self))
	}
}
