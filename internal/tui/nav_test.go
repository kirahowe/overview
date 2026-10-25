package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"overview/internal/fleet"
)

// A window as the panels sit in it: the projects over the agents on the
// left, the session beside them with its review under it.
var navWindow = []panel{
	{projectsPanel, rect{0, 0, 40, 20}},
	{agentsPanel, rect{0, 21, 40, 8}},
	{stagePanel, rect{41, 0, 100, 15}},
	{"%7", rect{41, 16, 100, 13}},
}

func TestNavStep(t *testing.T) {
	for _, c := range []struct {
		from, prev panelID
		d          direction
		want       panelID
	}{
		{projectsPanel, "", rightward, stagePanel}, // both face it: the first
		{projectsPanel, "%7", rightward, "%7"},     // unless it came from the other
		{agentsPanel, "", rightward, "%7"},         // only the review faces it
		{agentsPanel, "", upward, projectsPanel},
		{projectsPanel, "", downward, agentsPanel},
		{stagePanel, "", downward, "%7"},
		{"%7", "", upward, stagePanel},
		{"%7", "", leftward, projectsPanel},
		{"%7", agentsPanel, leftward, agentsPanel},
		{stagePanel, "", leftward, projectsPanel}, // the agents do not face it
		{projectsPanel, "", upward, ""},
		{projectsPanel, "", leftward, ""},
		{stagePanel, "", rightward, ""},
		{"gone", "", rightward, ""},
	} {
		got, ok := navStep(navWindow, c.from, c.d, c.prev)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("from %s (prev %q) %d: %q %v, want %q", c.from, c.prev, c.d, got, ok, c.want)
		}
	}
}

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

// navModel is a staged model with two sessions, with the keys out on the
// panels as esc leaves them.
func navModel(t *testing.T, f *fakeMux) model {
	t.Helper()
	m := stagedModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	m.nav = nav{}
	m, cmd := step(m, noteMsg(""))
	return settle(t, m, cmd)
}

// The view starts with the keys in the list, where j moves the cursor.
func TestTheKeysStartInTheList(t *testing.T) {
	m := newModel(fleet.Home{}, "")
	if m.navAt() != projectsPanel || !m.nav.inside {
		t.Fatalf("at the start: nav %+v", m.nav)
	}
}

// From the panels, l selects the session beside the list, and enter moves
// the keyboard into it. Coming back puts the keys in the list, where j
// moves the cursor; esc goes out to the panels, and l returns to the
// session the keys came back from.
func TestTheKeysMoveBetweenPanelsAndIntoThem(t *testing.T) {
	f := newFakeMux()
	m := navModel(t, f)
	sel, stage := m.sel, m.stagePane()
	if m.navAt() != projectsPanel || m.nav.inside || f.selected["%0"] != "1" || stage == "" {
		t.Fatalf("at the start: nav %+v, the list's rule lit %q, stage %q", m.nav, f.selected["%0"], stage)
	}
	m = pressSettled(t, m, "l")
	if m.navAt() != stagePanel || f.selected[stage] != "1" || f.selected["%0"] != "" || m.sel != sel {
		t.Fatalf("after l: nav %+v, lit %v, sel %s", m.nav, f.selected, m.sel)
	}
	m = pressSettled(t, m, "enter")
	if f.focus != stage {
		t.Fatalf("enter on the session left the keyboard in %s, not %s", f.focus, stage)
	}
	m, cmd := step(m, tea.BlurMsg{})
	m = settle(t, m, cmd)
	if f.selected[stage] != "" {
		t.Fatal("the session's rule is lit by the view while the keys are in it; tmux lights it")
	}
	// ctrl+[ selects the list's pane in tmux, which reports the focus.
	m, cmd = step(m, listPanes(f, m.deps.roots, m.landed)())
	m = settle(t, m, cmd)
	f.focus = "%0"
	m, cmd = step(m, tea.FocusMsg{})
	m = settle(t, m, cmd)
	if m.navAt() != projectsPanel || !m.nav.inside || f.selected["%0"] != "1" || f.selected[stage] != "" {
		t.Fatalf("back from the session: nav %+v, lit %v", m.nav, f.selected)
	}
	m = pressSettled(t, m, "j")
	if m.sel == sel {
		t.Fatal("j back in the list did not move the cursor")
	}
	m = pressSettled(t, m, "k", "esc", "l")
	if m.navAt() != stagePanel || m.nav.inside {
		t.Fatalf("esc l: nav %+v", m.nav)
	}
	m = pressSettled(t, m, "h", "j")
	if m.navAt() != agentsPanel || m.nav.inside {
		t.Fatalf("h j: nav %+v", m.nav)
	}
	if !m.lit(agentsPanel) || m.lit(projectsPanel) {
		t.Fatal("the agents panel is not the one lit")
	}
}

// Out on the panels the footer says so, and the cursor's row loses its
// background, so a lit row always means the keys move it.
func TestThePanelsLookApartFromTheList(t *testing.T) {
	f := newFakeMux()
	m := navModel(t, f)
	m.nav.inside = true
	was := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(was) })
	text := []seg{{"auth", lipgloss.NewStyle()}}
	in, row := m.footer(), m.line(text, nil, false, true)
	m.nav.inside = false
	out := m.footer()
	if strings.Contains(in, "panels") || !strings.Contains(out, "panels") {
		t.Fatalf("footer in the list %q, on the panels %q", in, out)
	}
	bg := lipgloss.NewStyle().Background(pal.CursorDimBg).Render("x")
	bgCode := bg[:strings.Index(bg, "x")]
	if bare := m.line(text, nil, false, true); !strings.Contains(row, bgCode) || strings.Contains(bare, bgCode) {
		t.Fatalf("the cursor's row in the list %q, on the panels %q", row, bare)
	}
}

// n from the panels moves the cursor to what needs you and selects its
// pane, so enter goes straight into it.
func TestNSelectsThePaneOfWhatNeedsYou(t *testing.T) {
	f := newFakeMux()
	m := navModel(t, f)
	rows := m.rows
	rows[1].State = "blocked"
	m, cmd := step(m, rowsMsg{rows: rows, ttys: m.ttys})
	m = settle(t, m, cmd)
	m = pressSettled(t, m, "n")
	if m.navAt() != stagePanel || m.sel != rows[1].SessionID {
		t.Fatalf("after n: nav %+v, sel %s", m.nav, m.sel)
	}
	m = pressSettled(t, m, "enter")
	if f.focus != paneWithTTY(f, m.ttys[rows[1].PID]) {
		t.Fatalf("enter went to %s", f.focus)
	}
}

// In the cockpit, the panels are the list's and the cards that take keys:
// the header, and the issues, whose cursor enter puts the keys on. esc
// comes out of a card, and then closes the cockpit.
func TestTheKeysMoveBetweenTheCockpitsCards(t *testing.T) {
	f := newFakeMux()
	m := selectKey(t, taskModel(t, f), "s2")
	m.nav = nav{}
	m = resized(t, m, 150, 40)
	m = pressSettled(t, m, "o", "l")
	if m.navAt() != headerPanel {
		t.Fatalf("o l: nav %+v", m.nav)
	}
	m = pressSettled(t, m, "j")
	if m.navAt() != issuesPanel || m.inIssues() {
		t.Fatalf("j skips the cards that take no keys: nav %+v", m.nav)
	}
	m = pressSettled(t, m, "enter", "j")
	if !m.inIssues() || m.cockpit.pick != 1 {
		t.Fatalf("enter j: issues %v pick %d", m.inIssues(), m.cockpit.pick)
	}
	m = pressSettled(t, m, "esc")
	if m.inIssues() || !m.cockpit.open || m.navAt() != issuesPanel {
		t.Fatalf("esc: nav %+v open %v", m.nav, m.cockpit.open)
	}
	m = pressSettled(t, m, "esc")
	if m.cockpit.open || m.navAt() != projectsPanel {
		t.Fatalf("the second esc: open %v nav %+v", m.cockpit.open, m.nav)
	}
}

// A click on a row puts the keys in the list, in the panel clicked.
func TestAClickGoesIntoTheList(t *testing.T) {
	m := newTestModel(t, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	m.nav = nav{}
	m = update(m, tea.WindowSizeMsg{Width: 60, Height: 30})
	y := screenY(m, "agents/s1")
	m, _ = mouseClick(m, 5, y)
	if m.sel != "agents/s1" || m.navAt() != agentsPanel || !m.nav.inside {
		t.Fatalf("click on the agents: sel %s nav %+v", m.sel, m.nav)
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
	stage := m.stagePane()
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
