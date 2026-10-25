package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"overview/internal/fleet"
)

func TestSplitList(t *testing.T) {
	for _, c := range []struct{ p, a, h, proj, agents int }{
		{10, 4, 30, 25, 4}, // both fit: the agents at the bottom
		{10, 0, 30, 30, 0}, // no sessions, no agents panel
		{40, 20, 30, 18, 11},
		{40, 2, 30, 27, 2},
		{5, 5, 2, 2, 0}, // too short for both
		{5, 5, 3, 1, 1},
	} {
		if proj, agents := splitList(c.p, c.a, c.h); proj != c.proj || agents != c.agents {
			t.Errorf("splitList(%d, %d, %d) = %d, %d, want %d, %d", c.p, c.a, c.h, proj, agents, c.proj, c.agents)
		}
	}
}

// navModel is a staged model with two sessions and the keys in the list.
func navModel(t *testing.T, f *fakeMux) model {
	t.Helper()
	return stagedModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
}

// The view starts with the keys in the list, where j moves the cursor.
func TestTheKeysStartInTheList(t *testing.T) {
	m := newModel(fleet.Home{}, "")
	if m.panels || m.cockpit.issues || !m.listHasKeys() {
		t.Fatalf("at the start: panels %v, issues %v", m.panels, m.cockpit.issues)
	}
}

// esc in the list puts the client in tmux's panels mode, through the mux,
// and the view knows it at once. Its Cmd is the only thing esc does.
func TestEscPutsTheClientInThePanelsMode(t *testing.T) {
	f := newFakeMux()
	m := navModel(t, f)
	sel := m.sel
	m = pressSettled(t, m, "esc")
	if f.entered != 1 || !m.panels || !m.inPanels() || m.listHasKeys() || m.sel != sel {
		t.Fatalf("after esc: entered %d, panels %v, sel %s", f.entered, m.panels, m.sel)
	}
	// ctrl+[ in the list arrives as esc.
	m = pressSettled(t, m, "esc")
	if f.entered != 2 {
		t.Fatalf("a second esc entered %d times", f.entered)
	}
}

// esc keeps its other uses: it clears a filter first, and the overlays and
// the cockpit take it for themselves.
func TestEscDoesNotEnterThePanelsWhereItMeansSomethingElse(t *testing.T) {
	f := newFakeMux()
	m := navModel(t, f)
	m = pressSettled(t, m, "/", "b", "esc")
	if f.entered != 0 || m.panels || m.filtering {
		t.Fatalf("esc in a search: entered %d panels %v filtering %v", f.entered, m.panels, m.filtering)
	}
	m.filter = "b"
	m = pressSettled(t, m, "esc")
	if f.entered != 0 || m.filter != "" {
		t.Fatalf("esc with a filter applied: entered %d filter %q", f.entered, m.filter)
	}
	m = pressSettled(t, m, "?", "esc")
	if f.entered != 0 || m.help {
		t.Fatalf("esc in the help: entered %d help %v", f.entered, m.help)
	}
	m = pressSettled(t, m, "a", "esc")
	if f.entered != 0 || m.pick.open {
		t.Fatalf("esc in the picker: entered %d open %v", f.entered, m.pick.open)
	}
	m = pressSettled(t, m, "o", "esc")
	if f.entered != 0 || m.cockpit.open {
		t.Fatalf("esc in the cockpit: entered %d open %v", f.entered, m.cockpit.open)
	}
}

// Outside tmux there is no mode to enter.
func TestEscWithoutTmuxDoesNothing(t *testing.T) {
	m := newTestModel(t, row("s1", "red", "auth", 11))
	m, cmd := press(m, "esc")
	if m.panels || cmd != nil {
		t.Fatalf("panels %v, cmd %v", m.panels, cmd != nil)
	}
}

// When tmux refuses, the view does not think it is in the mode.
func TestAFailedEntryLeavesNoMode(t *testing.T) {
	f := newFakeMux()
	f.panelsErr = errors.New("no client")
	m := navModel(t, f)
	m = pressSettled(t, m, "esc")
	if m.panels || !strings.Contains(m.note, "no client") {
		t.Fatalf("panels %v, note %q", m.panels, m.note)
	}
}

// The mode is read from the listing of the panes, and a key that reaches
// the view means the client has left it.
func TestTheViewReadsTheMode(t *testing.T) {
	f := newFakeMux()
	m := navModel(t, f)
	f.panels = true
	m, cmd := step(m, listPanes(f, m.deps.roots, m.landed)())
	m = settle(t, m, cmd)
	if !m.panels || !m.inPanels() {
		t.Fatalf("the listing has a client in the mode, the view panels %v", m.panels)
	}
	// With the keys in another pane the view's own look is the blurred one.
	m, _ = step(m, tea.BlurMsg{})
	if m.inPanels() {
		t.Fatal("the panels look shows while the keys are in another pane")
	}
	m, _ = step(m, tea.FocusMsg{})
	if !m.panels {
		t.Fatal("the keys coming back ended the mode, which tmux still has")
	}
	f.panels = false
	m, cmd = step(m, listPanes(f, m.deps.roots, m.landed)())
	m = settle(t, m, cmd)
	if m.panels {
		t.Fatal("a listing without the mode left it on")
	}
	m.panels = true
	m = pressSettled(t, m, "x")
	if m.panels {
		t.Fatal("a key that reached the view left the mode on")
	}
}

// A listing asked for before esc cannot undo it: the Cmd that enters the
// mode makes older listings stale.
func TestAnOlderListingDoesNotUndoTheMode(t *testing.T) {
	f := newFakeMux()
	m := navModel(t, f)
	// A listing read before the mode, answered while the Cmd that enters
	// it is still out, and again after that Cmd has landed.
	old := listPanes(f, m.deps.roots, m.landed)()
	m, enter := press(m, "esc")
	m, _ = step(m, old)
	if !m.panels {
		t.Fatal("a listing from before the mode turned it off while it was being entered")
	}
	m = settle(t, m, enter)
	m, _ = step(m, old)
	if !m.panels {
		t.Fatal("a listing from before the mode turned it off once it was entered")
	}
}

// In the panels mode the footer says so, and the cursor's row loses its
// background, so a lit row always means the keys move it.
func TestThePanelsLookApartFromTheList(t *testing.T) {
	f := newFakeMux()
	m := navModel(t, f)
	was := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(was) })
	text := []seg{{"auth", lipgloss.NewStyle()}}
	// The list's own hints include "esc panels", which says nothing of
	// the mode, so it is taken out first.
	m = resized(t, m, 160, 30)
	leads := func(s string) bool {
		return strings.Contains(strings.ReplaceAll(ansi.Strip(s), "esc panels", ""), "panels")
	}
	in, row := m.footer(), m.line(text, nil, false, true)
	m.panels = true
	out := m.footer()
	if leads(in) || !leads(out) {
		t.Fatalf("footer in the list %q, in the mode %q", in, out)
	}
	m.note = "copied s1"
	if out := m.footer(); !leads(out) || !strings.Contains(ansi.Strip(out), "copied s1") {
		t.Fatalf("footer in the mode with a note %q", out)
	}
	m.note = ""
	bg := lipgloss.NewStyle().Background(pal.CursorDimBg).Render("x")
	bgCode := bg[:strings.Index(bg, "x")]
	if bare := m.line(text, nil, false, true); !strings.Contains(row, bgCode) || strings.Contains(bare, bgCode) {
		t.Fatalf("the cursor's row in the list %q, in the mode %q", row, bare)
	}
}

// However the keys come back into the view's pane, by a tmux key, by
// ctrl+[ or by a click, they are in the list on the cursor, where j moves
// it, and nothing has to be selected first.
func TestTheKeysComeBackToTheListOnTheCursor(t *testing.T) {
	f := newFakeMux()
	m := navModel(t, f)
	sel := m.sel
	m = pressSettled(t, m, "enter")
	m, cmd := step(m, tea.BlurMsg{})
	m = settle(t, m, cmd)
	if !m.blurred || m.listHasKeys() {
		t.Fatal("the keys are in the session, and the list does not know")
	}
	f.focus = "%0"
	m, cmd = step(m, tea.FocusMsg{})
	m = settle(t, m, cmd)
	if m.blurred || !m.listHasKeys() || m.sel != sel {
		t.Fatalf("back from the session: blurred %v, sel %s, want %s", m.blurred, m.sel, sel)
	}
	m = pressSettled(t, m, "j")
	if m.sel == sel {
		t.Fatal("j back in the list did not move the cursor")
	}
}

// The agents' rule is lit while the cursor is in the agents and the keys
// are in the view. The projects' rule is tmux's.
func TestTheAgentsRuleLightsWithTheCursor(t *testing.T) {
	m := newTestModel(t, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	if m.agentsLit() {
		t.Fatal("the agents are lit with the cursor in the projects")
	}
	m = selectKey(t, m, "agents/s1")
	if !m.agentsLit() {
		t.Fatal("the agents are not lit with the cursor in them")
	}
	m.blurred = true
	if m.agentsLit() {
		t.Fatal("the agents are lit while the keys are in another pane")
	}
	m.blurred, m.panels = false, true
	if m.agentsLit() {
		t.Fatal("the agents are lit in the panels mode, where the keys move the focus")
	}
	m.panels, m.cockpit.open, m.cockpit.issues = false, true, true
	if m.agentsLit() {
		t.Fatal("the agents are lit while the keys are in the cockpit's issues card")
	}
}

// In the cockpit, tab moves the keys to the issues card and back, and the
// cockpit has no panels mode: esc closes it. A click on the list puts the
// keys in the list.
func TestTheKeysMoveBetweenTheCockpitsListAndCard(t *testing.T) {
	f := newFakeMux()
	m := selectKey(t, taskModel(t, f), "s2")
	m = resized(t, m, 150, 40)
	m = pressSettled(t, m, "o")
	if !m.listHasKeys() || m.issuesLit() != "" {
		t.Fatalf("in the cockpit: list %v lit %q", m.listHasKeys(), m.issuesLit())
	}
	m = pressSettled(t, m, "tab", "j")
	if !m.inIssues() || m.listHasKeys() || m.issuesLit() != issuesPanel || m.cockpit.pick != 1 {
		t.Fatalf("tab j: issues %v list %v pick %d", m.inIssues(), m.listHasKeys(), m.cockpit.pick)
	}
	m = pressSettled(t, m, "tab")
	if m.inIssues() || !m.listHasKeys() {
		t.Fatalf("tab again: issues %v", m.inIssues())
	}
	m = pressSettled(t, m, "tab", "esc")
	if m.cockpit.open || f.entered != 0 || m.inIssues() {
		t.Fatalf("esc: open %v entered %d issues %v", m.cockpit.open, f.entered, m.inIssues())
	}
	m = pressSettled(t, m, "o", "tab")
	m, _ = mouseClick(m, 5, 3)
	if m.inIssues() {
		t.Fatal("a click on the list left the keys in the issues card")
	}
}

// A click on a row puts the keys in the list.
func TestAClickGoesIntoTheList(t *testing.T) {
	m := newTestModel(t, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	m = update(m, tea.WindowSizeMsg{Width: 60, Height: 30})
	y := screenY(m, "agents/s1")
	m.panels = true // a click leaves tmux's panels mode, which binds no mouse
	m, _ = mouseClick(m, 5, y)
	if m.sel != "agents/s1" || !m.listHasKeys() || !m.agentsLit() || m.panels {
		t.Fatalf("click on the agents: sel %s panels %v", m.sel, m.panels)
	}
}

// Each pane's rule names what runs there, and a # in a title stays a #
// once tmux has read it.
func TestThePanesRulesSayWhatRunsThere(t *testing.T) {
	f := newFakeMux()
	m := navModel(t, f)
	if got := f.titles["%0"]; !strings.Contains(got, "◆ ") || !strings.Contains(got, "○ 2") {
		t.Fatalf("the list's title %q", got)
	}
	stage := m.want()
	if got := f.titles[stage]; !strings.HasPrefix(got, m.ordered()[0].row.Name) || !strings.Contains(got, "idle") {
		t.Fatalf("the session's title %q", got)
	}
	if got := tmuxTitle([]titleSeg{{text: "#6 a"}, {"  b", lipgloss.Color("#abc")}}); got != "##6 a#[fg=#aabbcc nobold]  b" {
		t.Fatalf("tmuxTitle %q", got)
	}
	for c, want := range map[lipgloss.Color]string{"#abc": "#aabbcc", "#a1b2c3": "#a1b2c3", "141": "colour141"} {
		if got := tmuxColour(c); got != want {
			t.Errorf("tmuxColour(%s) = %s, want %s", c, got, want)
		}
	}
	// The cockpit draws its own rules: the window's go while it is open.
	m = pressSettled(t, m, "o")
	if p, _ := m.pane("%0"); p.Framed {
		t.Fatal("the cockpit left the window's rules on")
	}
	m = pressSettled(t, m, "o")
	if p, _ := m.pane("%0"); !p.Framed {
		t.Fatal("closing the cockpit left the window without rules")
	}
}
