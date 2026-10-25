package tui

import (
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"overview/internal/fleet"
	"overview/internal/issues"
)

func TestOOpensTheCockpitZoomedAndEscPutsItBack(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	first := paneWithTTY(f, m.in.TTYs[m.ordered()[0].sess.PID])
	if !f.staged(first) {
		t.Fatalf("the list is in %s, want beside %s", f.stage(), first)
	}
	m = pressSettled(t, m, "o")
	if !m.cockpit.open || !slices.Equal(f.zooms, []string{"%0 true"}) || !f.zoomed("%0") {
		t.Fatalf("after o: open %v zooms %v", m.cockpit.open, f.zooms)
	}
	// The cursor moves, and the list stays in its window.
	stage, moves := f.stage(), len(f.moves)
	m = pressSettled(t, m, "j")
	m, cmd := step(m, listPanes(f, m.deps.roots, m.landed)())
	m = settle(t, m, cmd)
	if m.sel != m.ordered()[1].key() || f.stage() != stage || !m.cockpit.open || len(f.moves) != moves {
		t.Fatalf("after j: sel %s, list in %s (was %s), open %v, moves %v", m.sel, f.stage(), stage, m.cockpit.open, f.moves)
	}
	m = pressSettled(t, m, "esc")
	second := paneWithTTY(f, m.in.TTYs[m.ordered()[1].sess.PID])
	if m.cockpit.open || f.zoomed("%0") || len(f.zooms) != 2 {
		t.Fatalf("after esc: open %v zoomed %v zooms %v", m.cockpit.open, f.zoomed("%0"), f.zooms)
	}
	if !f.staged(second) {
		t.Fatalf("after esc the list is in %s, want beside s2's %s", f.stage(), second)
	}
	// o closes it too.
	m = pressSettled(t, m, "o", "o")
	if m.cockpit.open || f.zoomed("%0") || !slices.Equal(f.zooms[2:], []string{"%0 true", "%0 false"}) {
		t.Fatalf("after o o: open %v zooms %v", m.cockpit.open, f.zooms)
	}
}

func TestTheCockpitClosesWhenTmuxEndsTheZoom(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11))
	m = pressSettled(t, m, "o")
	// The user goes to the pane beside the list, and tmux ends the zoom.
	f.setZoom(f.stage(), false)
	m, cmd := step(m, listPanes(f, m.deps.roots, m.landed)())
	m = settle(t, m, cmd)
	if m.cockpit.open || len(f.zooms) != 1 || f.zoomed("%0") {
		t.Fatalf("open %v zooms %v zoomed %v", m.cockpit.open, f.zooms, f.zoomed("%0"))
	}
}

// cmdMsgs runs cmd's tree of Cmds, in the order given, and returns the
// messages they answer with, without feeding them back.
func cmdMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case nil:
		return nil
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range msg {
			out = append(out, cmdMsgs(c)...)
		}
		return out
	default:
		return []tea.Msg{msg}
	}
}

// feed steps m through msgs and settles what each asks for.
func feed(t *testing.T, m model, msgs []tea.Msg) model {
	t.Helper()
	for _, msg := range msgs {
		var cmd tea.Cmd
		m, cmd = step(m, msg)
		m = settle(t, m, cmd)
	}
	return m
}

// o o pressed faster than tmux answers sends one zoom at a time, and the
// window ends as the cockpit does: closed and unzoomed. With two zooms out
// at once, the close could run first and leave the window zoomed.
func TestOOFastLeavesTheWindowUnzoomed(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11))
	m, first := press(m, "o")
	m, second := press(m, "o")
	if late := cmdMsgs(second); len(late) != 0 {
		t.Fatalf("a second zoom went out while the first was: %v", late)
	}
	m = feed(t, m, cmdMsgs(first))
	m, cmd := step(m, listPanes(f, m.deps.roots, m.landed)())
	m = settle(t, m, cmd)
	if m.cockpit.open || f.zoomed("%0") || m.zooming {
		t.Fatalf("open %v zoomed %v zooming %v zooms %v", m.cockpit.open, f.zoomed("%0"), m.zooming, f.zooms)
	}
}

// v from the cockpit splits the session's window for the review, which
// ends the zoom in tmux. The cockpit's own unzoom must not zoom it again,
// whichever of the two runs first.
func TestVFromTheCockpitLeavesTheReviewShown(t *testing.T) {
	for _, splitFirst := range []bool{true, false} {
		f := newFakeMux()
		m := stagedModel(t, f, row("s1", "red", "auth", 11))
		m = pressSettled(t, m, "o")
		m, cmd := press(m, "v")
		msgs := cmdMsgs(cmd)
		if splitFirst {
			slices.Reverse(msgs)
		}
		m = feed(t, m, msgs)
		m, cmd = step(m, listPanes(f, m.deps.roots, m.landed)())
		m = settle(t, m, cmd)
		if m.cockpit.open || f.zoomed("%0") || len(f.splits) != 1 {
			t.Fatalf("split first %v: open %v zoomed %v splits %d zooms %v", splitFirst, m.cockpit.open, f.zoomed("%0"), len(f.splits), f.zooms)
		}
	}
}

// o while the list is moving to another window waits for the move, so
// the zoom lands on the window the list went to, not the one it left.
func TestOWhileTheListMovesOpensWhereItLands(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	m, move := press(m, "j")
	if !m.staging {
		t.Fatal("j started no move")
	}
	m, open := press(m, "o")
	if m.cockpit.open || len(cmdMsgs(open)) != 0 || len(f.zooms) != 0 {
		t.Fatalf("o during the move: open %v zooms %v", m.cockpit.open, f.zooms)
	}
	m = feed(t, m, cmdMsgs(move))
	second := paneWithTTY(f, m.in.TTYs[m.ordered()[1].sess.PID])
	if !m.cockpit.open || !f.staged(second) || !f.zoomed("%0") {
		t.Fatalf("open %v, list in %s beside %s %v, zoomed %v", m.cockpit.open, f.stage(), second, f.staged(second), f.zoomed("%0"))
	}
}

// A list alone in its window cannot be zoomed, and fills it anyway: the
// cockpit opens and stays open.
func TestTheCockpitOpensWithTheListAlone(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11))
	// The session beside the list exits while the user is elsewhere, so
	// the list stays where it was, alone.
	m.blurred = true
	f.exit(paneWithTTY(f, m.in.TTYs[11]))
	m, cmd := step(m, listPanes(f, m.deps.roots, m.landed)())
	m = settle(t, m, cmd)
	if n := f.inWindow(f.stage()); n != 1 {
		t.Fatalf("the list shares its window with %d panes", n-1)
	}
	m = pressSettled(t, m, "o")
	m, cmd = step(m, listPanes(f, m.deps.roots, m.landed)())
	m = settle(t, m, cmd)
	if !m.cockpit.open {
		t.Fatal("the cockpit closed with the list alone")
	}
}

// A pane closing in the list's window ends tmux's zoom without the user
// going anywhere: the cockpit stays, zoomed again while the list has
// company, and unzoomed once it is alone.
func TestAPaneClosingKeepsTheCockpit(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11))
	split := f.split(f.panes[f.find(paneWithTTY(f, m.in.TTYs[11]))].ID)
	m, cmd := step(m, listPanes(f, m.deps.roots, m.landed)())
	m = settle(t, m, cmd)
	m = pressSettled(t, m, "o")
	if !f.zoomed("%0") {
		t.Fatal("not zoomed")
	}
	f.exit(split.ID)
	m, cmd = step(m, listPanes(f, m.deps.roots, m.landed)())
	m = settle(t, m, cmd)
	if !m.cockpit.open || !f.zoomed("%0") {
		t.Fatalf("after a split closed: open %v zoomed %v", m.cockpit.open, f.zoomed("%0"))
	}
	f.exit(paneWithTTY(f, m.in.TTYs[11]))
	m, cmd = step(m, listPanes(f, m.deps.roots, m.landed)())
	m = settle(t, m, cmd)
	if !m.cockpit.open || f.inWindow(f.stage()) != 1 {
		t.Fatalf("after the session closed: open %v, %d panes", m.cockpit.open, f.inWindow(f.stage()))
	}
}

func TestAListingFromBeforeTheZoomDoesNotCloseTheCockpit(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11))
	stale := listPanes(f, m.deps.roots, m.landed)() // taken before the zoom
	m, cmd := press(m, "o")
	m = settle(t, m, cmd)
	m, cmd = step(m, stale)
	m = settle(t, m, cmd)
	if !m.cockpit.open {
		t.Fatal("a listing from before the zoom closed the cockpit")
	}
}

func TestActionsThatShowAPaneCloseTheCockpit(t *testing.T) {
	for _, k := range []string{"enter", "c", "t", "d", "v"} {
		f := newFakeMux()
		m := stagedModel(t, f, row("s1", "red", "auth", 11))
		m = pressSettled(t, m, "o")
		m = pressSettled(t, m, k)
		if m.cockpit.open || f.zoomed("%0") {
			t.Errorf("%s: open %v zoomed %v", k, m.cockpit.open, f.zoomed("%0"))
		}
	}
}

// esc in the cockpit clears a filter applied to the list first, and
// closes the cockpit on the next press.
func TestEscClearsTheFilterBeforeClosingTheCockpit(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	m = pressSettled(t, m, "o")
	// A filter left applied to the list, as a search that matched
	// nothing leaves it.
	m.filter = "b"
	m = pressSettled(t, m, "esc")
	if m.filter != "" || !m.cockpit.open {
		t.Fatalf("after esc: filter %q open %v", m.filter, m.cockpit.open)
	}
	m = pressSettled(t, m, "esc")
	if m.cockpit.open {
		t.Fatal("the second esc left the cockpit open")
	}
}

// The wheel over the cards scrolls nothing and leaves the list's cursor
// where it was; over the list it moves the cursor as ever.
func TestTheWheelOverTheCardsDoesNothing(t *testing.T) {
	m := newTestModel(t, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	m = update(m, tea.WindowSizeMsg{Width: 140, Height: 20})
	m = pressSettled(t, m, "o")
	sel := m.sel
	wheel := func(x int) tea.MouseMsg {
		return tea.MouseMsg{X: x, Y: 3, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown}
	}
	if m = update(m, wheel(cockpitList+10)); m.sel != sel {
		t.Fatalf("the wheel over the cards moved the cursor to %s", m.sel)
	}
	if m = update(m, wheel(5)); m.sel == sel {
		t.Fatal("the wheel over the list did not move the cursor")
	}
}

func TestTheCockpitOutsideTmuxOnlyDraws(t *testing.T) {
	m := newTestModel(t, row("s1", "red", "auth", 11))
	m = update(m, tea.WindowSizeMsg{Width: 140, Height: 20})
	m = pressSettled(t, m, "o")
	if !m.cockpit.open || m.zooming {
		t.Fatalf("open %v zooming %v", m.cockpit.open, m.zooming)
	}
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 20 {
		t.Fatalf("%d lines", len(lines))
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w != 140 {
			t.Fatalf("line %d is %d wide: %q", i, w, ansi.Strip(l))
		}
	}
	if body := ansi.Strip(lines[2]); []rune(body)[cockpitList] != '│' {
		t.Fatalf("no border after the list: %q", body)
	}
	m, _ = press(m, "esc")
	if m.cockpit.open {
		t.Fatal("esc left it open")
	}
}

func TestCockpitTarget(t *testing.T) {
	m := taskModel(t, newFakeMux())
	for _, c := range []struct{ key, root, task string }{
		{"task:/code/app#6", "/code/app", "/code/app#6"},
		{"s1", "/code/app", "/code/app#6"},
		{"s2", "/code/app", ""},
		{"agents/s1", "/code/app", ""},
	} {
		got := selectKey(t, m, c.key).target()
		if got.root != c.root || targetTask(got) != c.task {
			t.Errorf("%s: target %+v, want root %s task %s", c.key, got, c.root, c.task)
		}
	}
}

func TestCockpitListWidth(t *testing.T) {
	for _, c := range []struct{ want, w, got int }{
		{0, 200, cockpitList},
		{30, 200, 30},
		{30, 81, 30},
		{30, 80, 0},
		{0, 90, 0},
	} {
		if got := cockpitListWidth(c.want, c.w); got != c.got {
			t.Errorf("cockpitListWidth(%d, %d) = %d, want %d", c.want, c.w, got, c.got)
		}
	}
}

func TestTabMovesTheKeyboardToTheIssuesCard(t *testing.T) {
	f := newFakeMux()
	m := selectKey(t, taskModel(t, f), "s2")
	m = resized(t, m, 150, 40)
	m = pressSettled(t, m, "o", "tab")
	// The first issue listed has no session working on it, so enter would
	// start one.
	if !m.inIssues() || !strings.Contains(ansi.Strip(m.footer()), "enter start") {
		t.Fatalf("after tab: issues %v, footer %q", m.inIssues(), ansi.Strip(m.footer()))
	}
	// j moves the card's cursor, not the list's, and stops at the end.
	m = pressSettled(t, m, "j", "j", "j")
	if m.sel != "s2" || m.cockpit.pick != 1 {
		t.Fatalf("after j: sel %s, pick %d", m.sel, m.cockpit.pick)
	}
	if !strings.Contains(ansi.Strip(m.View()), "▸ in progress  #6 keep width") {
		t.Fatalf("the cursor is not on #6:\n%s", ansi.Strip(m.View()))
	}
	m = pressSettled(t, m, "k", "tab")
	if m.inIssues() || m.cockpit.pick != 0 || !strings.Contains(ansi.Strip(m.footer()), "o close") {
		t.Fatalf("after k tab: issues %v pick %d footer %q", m.inIssues(), m.cockpit.pick, ansi.Strip(m.footer()))
	}
	m = pressSettled(t, m, "j")
	if m.sel == "s2" {
		t.Fatal("j with the list focused did not move the cursor")
	}
}

// withIssues is m with more of /code/app's issues.
func withIssues(m model, more ...issues.Issue) model {
	tr := m.trackers["/code/app"]
	tr.issues = append(slices.Clone(tr.issues), more...)
	m.trackers = trackers{"/code/app": tr}
	m = m.setInputs(nil)
	return m
}

func TestEnterOnAnIssueStartsAWorkstream(t *testing.T) {
	f := newFakeMux()
	m := withIssues(selectKey(t, taskModel(t, f), "s2"), issues.Issue{ID: 9, Ref: "app#9", Title: "add threads", Status: issues.Ready, Priority: "p2"})
	m = resized(t, m, 150, 40)
	// in review #4, in progress #6, up next #9.
	m = pressSettled(t, m, "o", "tab", "j", "j")
	if text := ansi.Strip(m.View()); !strings.Contains(text, "▸ up next      #9 add threads") {
		t.Fatalf("the cursor is not on #9:\n%s", text)
	}
	if !strings.Contains(ansi.Strip(m.footer()), "enter start") {
		t.Fatalf("footer %q", ansi.Strip(m.footer()))
	}
	m = pressSettled(t, m, "enter")
	if len(f.spawned) != 1 || f.spawned[0].dir != "/code/app" || !slices.Equal(f.spawned[0].command[1:], []string{"Work on app#9: add threads"}) {
		t.Fatalf("spawned %+v", f.spawned)
	}
	if m.cockpit.open || f.zoomed("%0") {
		t.Fatalf("the cockpit is still open (%v) or zoomed (%v)", m.cockpit.open, f.zoomed("%0"))
	}
	if m.opened.ID == "" || !f.staged(m.opened.ID) || f.focus != m.opened.ID {
		t.Fatalf("the new session %s is not shown and focused: list in %s, focus %s", m.opened.ID, f.stage(), f.focus)
	}
}

// enter on an issue already in progress starts nothing: the list's cursor
// goes to its workstream, and the cockpit stays open on it.
func TestEnterOnAnIssueInProgressGoesToItsWorkstream(t *testing.T) {
	f := newFakeMux()
	m := selectKey(t, taskModel(t, f), "s2")
	m = resized(t, m, 150, 40)
	m = pressSettled(t, m, "o", "tab", "j", "enter")
	if len(f.spawned) != 0 {
		t.Fatalf("started a session on #6: %+v", f.spawned)
	}
	if m.sel != "task:/code/app#6" || !m.cockpit.open || m.inIssues() || targetTask(m.target()) != "/code/app#6" {
		t.Fatalf("sel %s open %v issues %v target %+v", m.sel, m.cockpit.open, m.inIssues(), m.target())
	}
}

func TestTheProjectCockpitWithoutIssues(t *testing.T) {
	m := newTestModel(t, row("s1", "red", "auth", 11))
	m = update(m, tea.WindowSizeMsg{Width: 140, Height: 30})
	m = pressSettled(t, m, "o")
	text := ansi.Strip(m.View())
	for _, want := range []string{"─ /w/red ", "no issues", "This project doesn't track its tasks.", "Install the issues tool", "Uncommitted", "not in a repository"} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q:\n%s", want, text)
		}
	}
	if m, _ = press(m, "tab"); m.inIssues() {
		t.Fatal("tab went to an issues card that is not there")
	}
	// With the tool, it says how to start.
	m.issues = func(...string) ([]byte, error) { return nil, nil }
	if text := ansi.Strip(m.View()); !strings.Contains(text, "Start with issues init in /w/red.") {
		t.Fatalf("with the tool:\n%s", text)
	}
}

// A blocked issue is listed after those in progress. No session works on
// it, so enter resumes it with a new one, as the task picker does.
func TestEnterOnABlockedIssueWithNoSessionResumesIt(t *testing.T) {
	f := newFakeMux()
	m := withIssues(selectKey(t, taskModel(t, f), "s2"), issues.Issue{ID: 8, Ref: "app#8", Title: "wait on tmux", Status: issues.Blocked, Priority: "p2"})
	m = resized(t, m, 150, 40)
	m = pressSettled(t, m, "o", "tab", "j", "j")
	if text := ansi.Strip(m.View()); !strings.Contains(text, "▸ blocked      #8 wait on tmux") {
		t.Fatalf("the cursor is not on #8:\n%s", text)
	}
	if !strings.Contains(ansi.Strip(m.footer()), "enter start") {
		t.Fatalf("footer %q, want enter start", ansi.Strip(m.footer()))
	}
	m = pressSettled(t, m, "enter")
	if len(f.spawned) != 1 || !slices.Contains(f.spawned[0].command, "Work on app#8: wait on tmux") || m.cockpit.open {
		t.Fatalf("spawned %+v, open %v", f.spawned, m.cockpit.open)
	}
}

// resized is m at w by h, with the restage that follows settled, so the
// list is where the cursor wants it.
func resized(t *testing.T, m model, w, h int) model {
	t.Helper()
	m, cmd := step(m, tea.WindowSizeMsg{Width: w, Height: h})
	return settle(t, m, cmd)
}

func TestTheCockpitSaysWhatEnterDoesAsTheListDoes(t *testing.T) {
	bg := fleet.Row{Live: fleet.Live{SessionID: "b1", ID: "b1", Name: "bg", Kind: "background"}, State: fleet.Idle}
	m := stagedModel(t, newFakeMux(), bg)
	m.cockpit.open = true
	hs := m.panelHints()
	if i := slices.IndexFunc(hs, func(h hint) bool { return h.key == "enter" }); i < 0 || hs[i].label != "attach" {
		t.Fatalf("hints %v, want enter attach for a background session with no pane", hs)
	}
}

// An o that waits for the list's move is cancelled by whatever would close
// an open cockpit, so it cannot open one afterwards. Pressing o again, or
// esc, cancels it too.
func TestAPendingOIsCancelled(t *testing.T) {
	second := func(f *fakeMux, m model) string { return paneWithTTY(f, m.in.TTYs[m.ordered()[1].sess.PID]) }
	for _, c := range []struct {
		name  string
		keys  []string
		focus bool // enter moves the focus into the pane
	}{
		{"enter", []string{"enter"}, true},
		{"o again", []string{"o"}, false},
		{"esc", []string{"esc"}, false},
		{"new terminal", []string{"t"}, false},
		{"digest screen", []string{"d"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeMux()
			m := stagedModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
			m, move := press(m, "j")
			if !m.staging {
				t.Fatal("j started no move")
			}
			m, _ = press(m, "o")
			if !m.cockpitSoon {
				t.Fatal("o during the move is not pending")
			}
			m, cmd := press(m, c.keys...)
			if m.cockpitSoon {
				t.Fatalf("%v left the o pending", c.keys)
			}
			m = settle(t, m, cmd)
			m = feed(t, m, cmdMsgs(move))
			if m.cockpit.open || f.zoomed("%0") || m.zooming || len(f.zooms) != 0 {
				t.Fatalf("open %v zoomed %v zooms %v", m.cockpit.open, f.zoomed("%0"), f.zooms)
			}
			if want := second(f, m); c.focus && (len(f.selects) == 0 || f.focus != want) {
				t.Fatalf("selects %v, focus %s, want %s", f.selects, f.focus, want)
			}
		})
	}
}
