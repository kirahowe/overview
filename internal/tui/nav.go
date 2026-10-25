package tui

// Focus is tmux's. The window is a set of panels: the projects and the
// agents down the left, in the view's own pane, and beside them the tmux
// panes of the window, or in the cockpit the cards. Each has a rule along
// its top with its title in it. tmux draws the rule of each of its panes,
// lit in the accent colour for the active one, which is the one pane that
// has the keyboard. The view lights what it draws itself: the agents'
// rule while the cursor is in the agents, and in the cockpit the list's
// rule or the issues card's, whichever has the keys.
//
// The keyboard starts in the list, where the keys move the cursor and act
// on its entry, and comes back to it from a tmux pane however it comes
// back: ctrl+[, where esc belongs to the program, a tmux key or a click.
// esc in the list puts the client in the panels mode, a key table of tmux
// (tmux.PanelsTable) in which h, j, k and l move tmux's focus and enter
// leaves the mode, so the keys land in the pane that has it. The view
// knows the mode from the listing of the panes, and at once when it puts
// the client there itself.

// panelID names a card of the cockpit that can take the keys.
type panelID string

// issuesPanel is the cockpit's issues card, whose cursor tab moves the
// keys to.
const issuesPanel panelID = "card:issues"

type rect struct{ x, y, w, h int }

// splitList shares h lines of the list between the projects, which take
// p lines, and the agents panel, a rule and a lines. Both fit when they
// can, the agents at the bottom. Otherwise the agents get up to two
// fifths, and each scrolls on its own. The projects always keep a line.
// A list too short for a rule and a line of each, or with no agents, is
// all projects.
func splitList(p, a, h int) (proj, agents int) {
	if a == 0 || h < 3 {
		return h, 0
	}
	if max(p, 1)+1+a <= h {
		return h - 1 - a, a
	}
	agents = min(a, max((h-1)*2/5, 1))
	return h - 1 - agents, agents
}

// inPanels says whether a client of the view's session is in the panels
// mode, whichever pane has the focus: the keys go to tmux, so the footer
// shows the mode's keys and not the list's.
func (m model) inPanels() bool { return m.panels }

// listHasKeys says whether the keys move the list's cursor: the keyboard
// is in the view and not in the panels mode or the cockpit's issues card.
func (m model) listHasKeys() bool { return !m.blurred && !m.panels && !m.inIssues() }

// inIssues says whether the keys move the cockpit's issues card's cursor.
func (m model) inIssues() bool { return m.cockpit.open && m.cockpit.issues }

// issuesLit is the cockpit card drawn lit: the issues card, while the keys
// are in it. The card is named for layoutCards, "" for none.
func (m model) issuesLit() panelID {
	if m.inIssues() && !m.blurred {
		return issuesPanel
	}
	return ""
}

// agentsLit says whether the agents' rule is lit: the keys move the
// list's cursor, and it is in the agents.
func (m model) agentsLit() bool { return m.listHasKeys() && m.cursorInAgents() }

// cursorInAgents says whether the cursor's entry is one of the agents.
func (m model) cursorInAgents() bool {
	e, _, ok := m.selected(m.ordered())
	return ok && e.alias == agentsPrefix
}
