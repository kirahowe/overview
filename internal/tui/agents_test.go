package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"overview/internal/fleet"
	"overview/internal/tmux"
)

// screens are the screens the view sent the agents' pane, in order.
type screens struct{ sent []string }

// last is the newest screen's lines, without their colours.
func (s *screens) last() []string {
	if len(s.sent) == 0 {
		return nil
	}
	return strings.Split(ansi.Strip(s.sent[len(s.sent)-1]), "\n")
}

// relist gives the model a fresh listing of the fake tmux's panes, as the
// view's tick does, and settles what it starts.
func relist(t tester, m model, f *fakeMux) model {
	t.Helper()
	panes, _ := f.Panes()
	m, cmd := step(m, panesMsg{panes: panes, gen: m.spawns})
	return settle(t, m, cmd)
}

// fromPane passes the model something that happened in the agents' pane,
// as its program would, and settles what it starts.
func fromPane(t tester, m model, msgs ...tea.Msg) model {
	t.Helper()
	for _, msg := range msgs {
		var cmd tea.Cmd
		m, cmd = step(m, agentsMsg{msg})
		m = settle(t, m, cmd)
	}
	return m
}

// agentsPaneOf is the fake tmux's agents' pane, its ID "" for none.
func agentsPaneOf(f *fakeMux) tmux.Pane {
	for _, p := range f.panes {
		if p.Agents {
			return p
		}
	}
	return tmux.Pane{}
}

// agentsModel is a staged model that starts an agents' pane, as the view
// does in tmux: it listens for one, a listing has opened it, and its
// program has said how big it is. The screens the view sends it are kept.
func agentsModel(t *testing.T, f *fakeMux, rows ...fleet.Row) (model, *screens) {
	t.Helper()
	m := stagedModel(t, f, rows...)
	s := &screens{}
	m.agentsSock = "/tmp/ov-test.sock"
	m.deps.drawAgents = func(seq int, screen string) error {
		s.sent = append(s.sent, screen)
		return nil
	}
	m = relist(t, m, f)
	p := agentsPaneOf(f)
	if p.ID == "" {
		t.Fatalf("no agents' pane was started: %+v", f.panes)
	}
	m = fromPane(t, m, tea.WindowSizeMsg{Width: p.W, Height: p.H})
	// The next listing shows the list as tall as the pane leaves it.
	return relist(t, m, f), s
}

// With sessions listed, the agents get a pane under the list: a line for
// each and one for the footer. The list's own pane then shows neither the
// agents nor a footer, and the pane's screen has both.
func TestTheAgentsGetAPaneOfTheirOwn(t *testing.T) {
	f := newFakeMux()
	m, s := agentsModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	p := agentsPaneOf(f)
	list, _ := m.pane(m.self)
	if len(f.unders) != 1 || !slices.Equal(f.unders[0].command, []string{"overview", "agents", "/tmp/ov-test.sock"}) {
		t.Fatalf("the pane was started with %+v", f.unders)
	}
	if p.H != 3 || p.Window != list.Window || p.Y != list.H+1 || p.W != list.W || !m.apart() {
		t.Fatalf("the column is list %+v over agents %+v, apart %v", list, p, m.apart())
	}
	if f.focus == p.ID {
		t.Fatal("starting the pane took the keyboard")
	}
	if f.titles[p.ID] != agentsTitle || !strings.Contains(f.notes[p.ID], "priority") {
		t.Fatalf("the pane's rule says %q %q", f.titles[p.ID], f.notes[p.ID])
	}
	m = resized(t, m, list.W, list.H)
	own := screenText(m)
	if len(own) != list.H || strings.Contains(strings.Join(own, "\n"), "── agents") || strings.Contains(own[len(own)-1], "claude") {
		t.Fatalf("the list's pane shows %d lines:\n%s", len(own), strings.Join(own, "\n"))
	}
	got := s.last()
	if len(got) != 3 || !strings.Contains(got[0]+got[1], "red") || !strings.Contains(got[0]+got[1], "blue") || !strings.Contains(got[2], "c claude") {
		t.Fatalf("the agents' pane shows:\n%s", strings.Join(got, "\n"))
	}
	// The list is no terminal for having the pane under it.
	for _, e := range m.ordered() {
		if e.kind == terminalEntry {
			t.Fatalf("a terminal is listed: %s", e.key())
		}
	}
}

// With the agents apart, j and k stop at the end of the side the cursor
// is on, and g and G go to that side's ends. The keyboard arriving in the
// agents' pane takes the cursor to the same session there, so the list's
// column stays where it is, and coming back takes it to that session in
// the list.
func TestTheCursorKeepsToItsSide(t *testing.T) {
	f := newFakeMux()
	m, s := agentsModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	es := m.ordered()
	last := ""
	for _, e := range es {
		if e.alias != agentsPrefix {
			last = e.key()
		}
	}
	m = pressSettled(t, m, "j", "j", "j", "j", "j", "j")
	if m.sel != last {
		t.Fatalf("j walked on to %s, past the last entry %s", m.sel, last)
	}
	if m = pressSettled(t, m, "G"); m.sel != last {
		t.Fatalf("G went to %s, want %s", m.sel, last)
	}
	e, _, _ := m.selected(m.ordered())
	sess, moves := e.sess.SessionID, len(f.moves)

	m = fromPane(t, m, tea.FocusMsg{})
	if !m.inAgents || m.blurred || m.sel != agentsPrefix+"/"+sess || len(f.moves) != moves {
		t.Fatalf("in the agents' pane: inAgents %v blurred %v sel %s moves %d, want the cursor on %s and %d moves", m.inAgents, m.blurred, m.sel, len(f.moves), sess, moves)
	}
	// The list's blur may arrive after the pane's focus, and changes
	// nothing.
	m, _ = step(m, tea.BlurMsg{})
	if m.blurred || !m.inAgents {
		t.Fatalf("the list's late blur: blurred %v inAgents %v", m.blurred, m.inAgents)
	}
	if got := s.last(); !slices.ContainsFunc(got, func(l string) bool { return strings.HasPrefix(l, "▌") }) {
		t.Fatalf("no cursor among the agents:\n%s", strings.Join(got, "\n"))
	}
	if own := strings.Join(screenText(m), "\n"); strings.Contains(own, "▌") {
		t.Fatalf("the list's pane still shows a cursor:\n%s", own)
	}

	// g and G, j and k, typed in the pane, keep to the agents.
	first, end := agentsPrefix+"/"+agentEntries(m.sections())[0].sess.SessionID, agentsPrefix+"/"+agentEntries(m.sections())[1].sess.SessionID
	for _, c := range []struct{ key, want string }{{"g", first}, {"k", first}, {"j", end}, {"j", end}, {"G", end}, {"g", first}} {
		if m = fromPane(t, m, keyMsg(c.key)); m.sel != c.want {
			t.Fatalf("%s among the agents went to %s, want %s", c.key, m.sel, c.want)
		}
	}

	// Back up in the list, on the same session.
	on, _, _ := m.selected(m.ordered())
	m, cmd := step(m, tea.FocusMsg{})
	m = settle(t, m, cmd)
	if m.inAgents || m.blurred || m.sel != on.sess.SessionID {
		t.Fatalf("back in the list: inAgents %v blurred %v sel %s, want %s", m.inAgents, m.blurred, m.sel, on.sess.SessionID)
	}
	// The pane's blur may arrive after the list's focus.
	if m = fromPane(t, m, tea.BlurMsg{}); m.blurred || m.inAgents {
		t.Fatalf("the pane's late blur: blurred %v inAgents %v", m.blurred, m.inAgents)
	}
	// And one that does mean the keyboard has left the view.
	m = fromPane(t, m, tea.FocusMsg{}, tea.BlurMsg{})
	if !m.blurred || m.inAgents {
		t.Fatalf("the keyboard left the pane: blurred %v inAgents %v", m.blurred, m.inAgents)
	}
}

// The list's column follows the cursor among the agents into another
// session's window: both panes go, the agents' at the height it had, and
// the keyboard stays in the agents' pane.
func TestTheColumnFollowsTheCursorAmongTheAgents(t *testing.T) {
	f := newFakeMux()
	m, _ := agentsModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	p := agentsPaneOf(f)
	m = fromPane(t, m, tea.FocusMsg{})
	f.focus = p.ID
	was := f.stage()
	on := m.sel
	m = fromPane(t, m, keyMsg("j"))
	if m.sel == on {
		m = fromPane(t, m, keyMsg("k"))
	}
	e, _, _ := m.selected(m.ordered())
	want := paneWithTTY(f, m.in.TTYs[e.sess.PID])
	if f.stage() == was || !f.staged(want) {
		t.Fatalf("the list is in %s, want the window of %s", f.stage(), want)
	}
	if got := f.carried[len(f.carried)-1]; got != (tmux.Carried{Pane: p.ID, Lines: 3, Focus: true}) {
		t.Fatalf("the move carried %+v", got)
	}
	now := agentsPaneOf(f)
	if now.Window != f.stage() || f.focus != p.ID || !m.apart() || !m.inAgents {
		t.Fatalf("after the move: agents %+v in stage %s, focus %s, apart %v", now, f.stage(), f.focus, m.apart())
	}
	if lay := f.set[was]; strings.Contains(lay, ","+strings.TrimPrefix(p.ID, "%")+"}") {
		t.Fatalf("the window left keeps the agents' pane in its layout: %s", lay)
	}
}

// The mouse in the agents' pane: a click puts the cursor on the row, two
// act on it, and the wheel moves the cursor among the agents.
func TestTheMouseInTheAgentsPane(t *testing.T) {
	f := newFakeMux()
	m, _ := agentsModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	agents := agentEntries(m.sections())
	click := func(y int) tea.MouseMsg {
		return tea.MouseMsg{X: 3, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
	}
	m = fromPane(t, m, click(1))
	if m.sel != agents[1].key() {
		t.Fatalf("a click on the second row selected %s, want %s", m.sel, agents[1].key())
	}
	m = fromPane(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	if m.sel != agents[0].key() {
		t.Fatalf("the wheel went to %s, want %s", m.sel, agents[0].key())
	}
	// The footer's line is no row.
	if m = fromPane(t, m, click(2)); m.sel != agents[0].key() {
		t.Fatalf("a click on the footer selected %s", m.sel)
	}
	m = fromPane(t, m, click(1), click(1))
	if got := paneWithTTY(f, m.in.TTYs[agents[1].sess.PID]); f.focus != got {
		t.Fatalf("a double click left the keyboard in %s, want the session's pane %s", f.focus, got)
	}
}

// n among the agents stops at the sessions that need you, and wraps.
func TestNStopsAtTheAgentsThatNeedYou(t *testing.T) {
	f := newFakeMux()
	blocked := row("s2", "blue", "billing", 12)
	blocked.State = fleet.Blocked
	m, _ := agentsModel(t, f, row("s1", "red", "auth", 11), blocked, row("s3", "green", "cart", 13))
	m = fromPane(t, m, tea.FocusMsg{}, keyMsg("G"), keyMsg("n"))
	if m.sel != agentsPrefix+"/s2" {
		t.Fatalf("n went to %s, want the blocked session among the agents", m.sel)
	}
	if m = fromPane(t, m, keyMsg("n")); m.sel != agentsPrefix+"/s2" {
		t.Fatalf("n again went to %s", m.sel)
	}
}

// The cockpit zooms the list over the agents' pane, so the list draws the
// agents itself there, with a footer of its own, and the cursor walks on
// into them. Closing the cockpit gives them their pane back.
func TestTheCockpitDrawsTheAgentsItself(t *testing.T) {
	f := newFakeMux()
	m, s := agentsModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	m = resized(t, m, 150, 40)
	m = pressSettled(t, m, "o")
	if !f.zoomed("%0") || m.apart() {
		t.Fatalf("the cockpit: zoomed %v apart %v", f.zoomed("%0"), m.apart())
	}
	own := screenText(m)
	if !strings.Contains(strings.Join(own, "\n"), "── agents") || !strings.Contains(own[len(own)-1], "o close") {
		t.Fatalf("the cockpit shows:\n%s", strings.Join(own, "\n"))
	}
	if len(f.heights) != 0 || len(f.unders) != 1 {
		t.Fatalf("the pane was touched under the zoom: heights %v, started %d", f.heights, len(f.unders))
	}
	m = pressSettled(t, m, "G")
	if e, _, _ := m.selected(m.ordered()); e.alias != agentsPrefix {
		t.Fatalf("G in the cockpit went to %s, not the last of the agents", m.sel)
	}
	sent := len(s.sent)
	m = pressSettled(t, m, "o")
	m = relist(t, m, f)
	if f.zoomed("%0") || !m.apart() {
		t.Fatalf("the cockpit closed: zoomed %v apart %v", f.zoomed("%0"), m.apart())
	}
	if len(s.sent) == sent {
		t.Fatal("the agents' pane was not sent the screen it has now")
	}
}

// The pane is as tall as its lines need, and closes when no session is
// left, which gives the list its footer back.
func TestTheAgentsPaneFitsItsLines(t *testing.T) {
	f := newFakeMux()
	rows := []fleet.Row{row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12)}
	m, _ := agentsModel(t, f, rows...)
	ttys := m.in.TTYs
	ttys[13] = "ttys013"
	f.add("ov", "ttys013")
	m, cmd := step(m, rowsMsg{rows: append(rows, row("s3", "green", "cart", 13)), ttys: ttys})
	m = settle(t, m, cmd)
	m = relist(t, m, f)
	if p := agentsPaneOf(f); p.H != 4 || !slices.Equal(f.heights, []int{4}) {
		t.Fatalf("with a third session the pane is %d lines tall, resized %v", p.H, f.heights)
	}
	// Asked once: the same listing again changes nothing.
	m = relist(t, m, f)
	if len(f.heights) != 1 {
		t.Fatalf("the pane was resized again: %v", f.heights)
	}
	m, cmd = step(m, rowsMsg{})
	m = settle(t, m, cmd)
	m = relist(t, m, f)
	m = relist(t, m, f)
	if p := agentsPaneOf(f); p.ID != "" || m.apart() {
		t.Fatalf("with no session left the pane is %+v, apart %v", p, m.apart())
	}
	list, _ := m.pane(m.self)
	if list.H != 40 {
		t.Fatalf("the list is %d lines tall, want the whole column", list.H)
	}
	m = resized(t, m, list.W, list.H)
	if own := screenText(m); !strings.Contains(own[len(own)-1], "c claude") {
		t.Fatalf("the list has no footer:\n%s", strings.Join(own, "\n"))
	}
}

// A pane that tmux has no room for is asked for once, and the list goes
// on drawing the agents itself.
func TestAnAgentsPaneThatCannotOpenIsNotAskedForAgain(t *testing.T) {
	f := newFakeMux()
	f.underErr = errors.New("no space for new pane")
	m := stagedModel(t, f, row("s1", "red", "auth", 11))
	m.agentsSock = "/tmp/ov-test.sock"
	m = relist(t, m, f)
	if !strings.Contains(m.note, "no space for new pane") || m.apart() || m.agentsBusy || m.spawns != m.landed {
		t.Fatalf("after the refusal: note %q, apart %v, spawning %v", m.note, m.apart(), m.agentsBusy)
	}
	if own := strings.Join(screenText(m), "\n"); !strings.Contains(own, "── agents") {
		t.Fatalf("the list does not draw the agents:\n%s", own)
	}
	f.underErr = nil
	m = relist(t, m, f)
	if len(f.unders) != 0 {
		t.Fatal("the pane was asked for again with nothing changed")
	}
}

// An agents' pane that is not under the list, left by a move that failed
// halfway, closes, and a new one opens under the list.
func TestAStrayAgentsPaneCloses(t *testing.T) {
	f := newFakeMux()
	m, _ := agentsModel(t, f, row("s1", "red", "auth", 11))
	stray := agentsPaneOf(f)
	i := f.find(stray.ID)
	f.panes[i].Window = "@1" // the holder's window
	f.lay(f.stage())
	m = relist(t, m, f)
	if f.find(stray.ID) >= 0 || m.apart() {
		t.Fatalf("the stray pane is still there: %+v", f.panes)
	}
	m = relist(t, m, f)
	if p := agentsPaneOf(f); p.ID == "" || p.Window != f.stage() || len(f.unders) != 2 {
		t.Fatalf("no new pane under the list: %+v, started %d", p, len(f.unders))
	}
}

// When the agents' pane goes while the keyboard is in it, the keyboard is
// taken to be outside the view, until a pane says it has it.
func TestAnAgentsPaneThatGoesTakesNoKeysWithIt(t *testing.T) {
	f := newFakeMux()
	m, _ := agentsModel(t, f, row("s1", "red", "auth", 11))
	m = fromPane(t, m, tea.FocusMsg{})
	m, _ = step(m, agentsGone{})
	if m.inAgents || !m.blurred || m.apart() {
		t.Fatalf("after the pane's program hung up: inAgents %v blurred %v apart %v", m.inAgents, m.blurred, m.apart())
	}
	m, _ = step(m, tea.FocusMsg{})
	if m.blurred {
		t.Fatal("the list's focus did not bring the keyboard back")
	}
}

func TestAgentsHeight(t *testing.T) {
	for _, c := range []struct{ projects, agents, total, want int }{
		{10, 4, 40, 5},   // both fit: a line each and the footer
		{10, 0, 40, 0},   // no sessions, no pane
		{60, 30, 40, 16}, // neither fits: two fifths, and the footer
		{5, 5, 3, 0},     // too short for a list, a rule, a row and a footer
		{5, 5, 4, 2},
	} {
		if got := agentsHeight(c.projects, c.agents, c.total); got != c.want {
			t.Errorf("agentsHeight(%d, %d, %d) = %d, want %d", c.projects, c.agents, c.total, got, c.want)
		}
	}
}

// A pane whose program never reaches the view closes by itself, and is
// not opened again and again. One whose program did reach it, and that
// then closed, is.
func TestAPaneThatNeverConnectsIsNotReopened(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11))
	m.agentsSock = "/tmp/ov-test.sock"
	m = relist(t, m, f)
	f.exit(agentsPaneOf(f).ID)
	m = relist(t, relist(t, m, f), f)
	if len(f.unders) != 1 || agentsPaneOf(f).ID != "" {
		t.Fatalf("the pane was started %d times", len(f.unders))
	}

	f = newFakeMux()
	m, _ = agentsModel(t, f, row("s1", "red", "auth", 11))
	f.exit(agentsPaneOf(f).ID)
	m = relist(t, relist(t, m, f), f)
	if len(f.unders) != 2 || agentsPaneOf(f).ID == "" || m.apart() {
		t.Fatalf("a pane that had run was started %d times, apart %v", len(f.unders), m.apart())
	}
}

// tmux can report the keyboard gone from the agents' pane and back while
// the column is on its way to another window. Neither report undoes the
// move: the cursor stays where j put it, and the column goes there once.
func TestFocusReportsDuringAMoveDoNotUndoIt(t *testing.T) {
	f := newFakeMux()
	m, _ := agentsModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	m = fromPane(t, m, tea.FocusMsg{}, keyMsg("g"))
	f.focus = agentsPaneOf(f).ID
	moves := len(f.moves)
	m, move := step(m, agentsMsg{keyMsg("j")})
	want := m.sel
	if !m.staging || want != agentsPrefix+"/"+agentEntries(m.sections())[1].sess.SessionID {
		t.Fatalf("j among the agents: staging %v, sel %s", m.staging, m.sel)
	}
	m, blur := step(m, agentsMsg{tea.BlurMsg{}})
	m, focus := step(m, agentsMsg{tea.FocusMsg{}})
	if m.sel != want || !m.inAgents || m.blurred {
		t.Fatalf("the reports moved the cursor to %s, want %s (inAgents %v, blurred %v)", m.sel, want, m.inAgents, m.blurred)
	}
	m = settle(t, m, tea.Batch(move, blur, focus))
	e, _, _ := m.selected(m.ordered())
	if m.sel != want || len(f.moves) != moves+1 || !f.staged(paneWithTTY(f, m.in.TTYs[e.sess.PID])) {
		t.Fatalf("after the move: sel %s, want %s, %d moves, want %d", m.sel, want, len(f.moves), moves+1)
	}
}

// enter among the agents on a session in another window: the reports of
// the move do not cancel the keyboard's way into that session.
func TestFocusReportsDuringAMoveKeepEnterGoing(t *testing.T) {
	f := newFakeMux()
	m, _ := agentsModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	m = fromPane(t, m, tea.FocusMsg{}, keyMsg("g"))
	f.focus = agentsPaneOf(f).ID
	m, move := step(m, agentsMsg{keyMsg("j")})
	m, enter := step(m, agentsMsg{keyMsg("enter")})
	m, blur := step(m, agentsMsg{tea.BlurMsg{}})
	m, focus := step(m, agentsMsg{tea.FocusMsg{}})
	m = settle(t, m, tea.Batch(move, enter, blur, focus))
	e, _, _ := m.selected(m.ordered())
	if got := paneWithTTY(f, m.in.TTYs[e.sess.PID]); f.focus != got {
		t.Fatalf("the keyboard is in %s, want the session's pane %s", f.focus, got)
	}
}

// A search typed in the agents' pane keeps the cursor among the agents
// whenever one of them matches, also after a stretch where none did.
func TestASearchTypedAmongTheAgentsStaysThere(t *testing.T) {
	f := newFakeMux()
	m, s := agentsModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	m = fromPane(t, m, tea.FocusMsg{}, keyMsg("/"), keyMsg("b"), keyMsg("l"))
	if m.sel != agentsPrefix+"/s2" {
		t.Fatalf("/bl among the agents put the cursor on %s", m.sel)
	}
	m = fromPane(t, m, keyMsg("q"))
	if got := strings.Join(s.last(), "\n"); !strings.Contains(got, "nothing matches /blq") {
		t.Fatalf("with no match the agents' pane shows:\n%s", got)
	}
	m = fromPane(t, m, keyMsg("backspace"))
	if m.sel != agentsPrefix+"/s2" {
		t.Fatalf("with a match again the cursor is on %s, not among the agents", m.sel)
	}
}

// While tmux is still opening the agents' pane, the list does not move
// and the cockpit does not zoom it: either would cut across the split.
// Both happen once the pane has landed.
func TestTheColumnWaitsForTheAgentsPane(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	m.agentsSock = "/tmp/ov-test.sock"
	panes, _ := f.Panes()
	m, opening := step(m, panesMsg{panes: panes, gen: m.spawns})
	if !m.agentsBusy {
		t.Fatal("the listing started no agents' pane")
	}
	moves := len(f.moves)
	on := m.sel
	m, cmd := step(m, keyMsg("j"))
	if m.sel == on {
		m, cmd = step(m, keyMsg("k"))
	}
	if m.staging || len(f.moves) != moves {
		t.Fatalf("the list moved with the pane still opening: staging %v, moves %d", m.staging, len(f.moves))
	}
	m = settle(t, m, tea.Batch(opening, cmd))
	p := agentsPaneOf(f)
	if len(f.moves) != moves+1 || p.ID == "" || p.Window != f.stage() || f.carried[len(f.carried)-1].Pane != p.ID {
		t.Fatalf("after the pane landed: %d moves, pane %+v, stage %s, carried %+v", len(f.moves), p, f.stage(), f.carried)
	}

	// The same for the cockpit, with the pane being resized.
	m = fromPane(t, m, tea.WindowSizeMsg{Width: p.W, Height: p.H})
	m = resized(t, relist(t, m, f), 150, 40)
	i := f.find(p.ID)
	f.panes[i].H = 7
	f.lay(f.stage())
	panes, _ = f.Panes()
	m, sizing := step(m, panesMsg{panes: panes, gen: m.spawns})
	if !m.agentsBusy {
		t.Fatal("the listing resized nothing")
	}
	m, cmd = step(m, keyMsg("o"))
	if m.cockpit.open || !m.cockpitSoon || len(f.zooms) != 0 {
		t.Fatalf("the cockpit opened over a resize: open %v soon %v zooms %v", m.cockpit.open, m.cockpitSoon, f.zooms)
	}
	m = settle(t, m, tea.Batch(sizing, cmd))
	if !m.cockpit.open || !f.zoomed("%0") || agentsPaneOf(f).H != 3 {
		t.Fatalf("after the resize landed: open %v zoomed %v pane %+v", m.cockpit.open, f.zoomed("%0"), agentsPaneOf(f))
	}
}

// An agents' pane whose program has ended, and that tmux holds open, is
// closed at the next listing whatever else is in the window.
func TestADeadAgentsPaneIsClosed(t *testing.T) {
	f := newFakeMux()
	m, _ := agentsModel(t, f, row("s1", "red", "auth", 11))
	p := agentsPaneOf(f)
	f.panes[f.find(p.ID)].Dead = true
	m = relist(t, m, f)
	if f.find(p.ID) >= 0 || m.apart() {
		t.Fatalf("the dead pane is still there: %+v", f.panes)
	}
}
