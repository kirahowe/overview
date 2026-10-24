package tui

import tea "github.com/charmbracelet/bubbletea"

// The view's window is a set of panels: the projects and the agents down
// the left, in the view's own pane, and beside them the tmux panes of the
// window, or in the cockpit the cards. Each has a rule along its top with
// its title in it, which the panel that is selected draws in the accent
// colour.
//
// The keyboard starts on the panels: h, j, k and l move the selection to
// the nearest panel that way, and enter goes into the selected one. In
// the projects or the agents, the keys move the cursor and act on its
// entry. In a tmux pane the keys go to what runs there. esc comes back
// out to the panels, and ctrl+[ from a tmux pane, where esc belongs to
// the program, or any other way back to the view's pane. The keys that
// act on the cursor's entry, such as c or o, work from the panels too.

// panelID names a panel. A tmux pane other than the one that shows the
// cursor's entry is named by its pane id, "%5".
type panelID string

const (
	projectsPanel panelID = "projects"
	agentsPanel   panelID = "agents"
	// stagePanel is the pane that shows the cursor's entry, whichever it
	// is: the session's own pane, the terminal, or the holder.
	stagePanel  panelID = "stage"
	headerPanel panelID = "card:header" // the cockpit's first card
	issuesPanel panelID = "card:issues" // the cockpit's issues card
)

// isPane says whether a panel is a tmux pane named by its id.
func (p panelID) isPane() bool { return len(p) > 0 && p[0] == '%' }

// listPanel says whether a panel is one of the list's own.
func (p panelID) listPanel() bool { return p == projectsPanel || p == agentsPanel }

// nav is where the keyboard is: on panel, choosing between panels, or in
// it when inside is set. prev is the panel it was on before, which a move
// back the way it came returns to.
type nav struct {
	panel  panelID
	prev   panelID
	inside bool
}

type rect struct{ x, y, w, h int }

// panel is a panel and where it is, in its window's cells.
type panel struct {
	id panelID
	r  rect
}

type direction int

const (
	leftward direction = iota
	rightward
	upward
	downward
)

// beyond says how far b lies past a in direction d, and whether it lies
// wholly that way; and how much the two overlap across it.
func beyond(a, b rect, d direction) (gap, overlap int, ok bool) {
	span := func(lo1, n1, lo2, n2 int) int { return min(lo1+n1, lo2+n2) - max(lo1, lo2) }
	switch d {
	case leftward:
		return a.x - (b.x + b.w), span(a.y, a.h, b.y, b.h), b.x+b.w <= a.x
	case rightward:
		return b.x - (a.x + a.w), span(a.y, a.h, b.y, b.h), b.x >= a.x+a.w
	case upward:
		return a.y - (b.y + b.h), span(a.x, a.w, b.x, b.w), b.y+b.h <= a.y
	}
	return b.y - (a.y + a.h), span(a.x, a.w, b.x, b.w), b.y >= a.y+a.h
}

// navStep is the panel a move from panel from in direction d lands on: of
// the panels wholly that way, those that face it, overlapping it across
// the move, before those that do not, and of those the nearest. The
// panel the selection came from, prev, wins a tie of facing panels, so a
// move back returns where it was. ok is false when no panel lies that
// way, or from is not among ps.
func navStep(ps []panel, from panelID, d direction, prev panelID) (panelID, bool) {
	var at rect
	found := false
	for _, p := range ps {
		if p.id == from {
			at, found = p.r, true
		}
	}
	if !found {
		return "", false
	}
	best, bestFaces, bestGap, bestPrev := panelID(""), false, 0, false
	for _, p := range ps {
		if p.id == from {
			continue
		}
		gap, overlap, ok := beyond(at, p.r, d)
		if !ok {
			continue
		}
		faces := overlap > 0
		if !faces {
			gap += -overlap // how far it sits off to the side, too
		}
		isPrev := p.id == prev
		better := best == "" ||
			(faces && !bestFaces) ||
			(faces == bestFaces && gap < bestGap) ||
			(faces == bestFaces && gap == bestGap && isPrev && !bestPrev)
		if better {
			best, bestFaces, bestGap, bestPrev = p.id, faces, gap, isPrev
		}
	}
	return best, best != ""
}

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

// lit says whether panel p is drawn lit: it is the one selected, or the
// one with the keys, while the keyboard is in the view.
func (m model) lit(p panelID) bool { return !m.blurred && m.navAt() == p }

// navAt is the selected panel, the projects until one is chosen.
func (m model) navAt() panelID {
	if m.nav.panel == "" {
		return projectsPanel
	}
	return m.nav.panel
}

// listHasKeys says whether the keys move the list's cursor: the keyboard
// is in the view and inside the projects or the agents.
func (m model) listHasKeys() bool { return !m.blurred && m.nav.inside && m.navAt().listPanel() }

// inIssues says whether the keys move the cockpit's issues card's cursor.
func (m model) inIssues() bool { return m.cockpit.open && m.nav.inside && m.navAt() == issuesPanel }

// cursorPanel is the list's panel the cursor's entry is in.
func (m model) cursorPanel() panelID {
	if e, _, ok := m.selected(m.ordered()); ok && e.alias == agentsPrefix {
		return agentsPanel
	}
	return projectsPanel
}

// panelEntries are the entries of es in list panel p, in order.
func panelEntries(es []entry, p panelID) []entry {
	var out []entry
	for _, e := range es {
		if (e.alias == agentsPrefix) == (p == agentsPanel) {
			out = append(out, e)
		}
	}
	return out
}

// cursorInto puts the cursor in list panel p when it is in the other:
// on the same session's entry there, its primary one in the projects, or
// else on the panel's first entry.
func (m model) cursorInto(p panelID) model {
	es := m.ordered()
	e, _, ok := m.selected(es)
	in := panelEntries(es, p)
	if !ok || len(in) == 0 || (e.alias == agentsPrefix) == (p == agentsPanel) {
		return m
	}
	if r, isSession := e.session(); isSession {
		for j, c := range in {
			if c.kind == sessionEntry && c.row.SessionID == r.SessionID && (p == agentsPanel || c.alias == "") {
				return m.moveTo(in, j)
			}
		}
	}
	return m.moveTo(in, 0)
}

// stagePane is the pane that shows the cursor's entry, when it is in the
// list's window: the stage panel.
func (m model) stagePane() string {
	if w := m.want(); w != "" && m.windowOf(w) == m.windowOf(m.self) {
		return w
	}
	return ""
}

// panelOfPane is the panel a tmux pane of the list's window is.
func (m model) panelOfPane(id string) panelID {
	if id == m.stagePane() {
		return stagePanel
	}
	return panelID(id)
}

// paneOfPanel is the tmux pane a panel is, "" for one of the view's own.
func (m model) paneOfPanel(p panelID) string {
	switch {
	case p == stagePanel:
		return m.stagePane()
	case p.isPane() && m.windowOf(string(p)) == m.windowOf(m.self) && string(p) != m.self:
		return string(p)
	}
	return ""
}

// panels are the panels of the list's window and where they are: the
// list's own two, then the tmux panes beside it, or in the cockpit the
// cards that take the keyboard.
func (m model) panels() []panel {
	self, _ := m.pane(m.self)
	x, y, w := self.X, self.Y, m.w
	if self.W > 0 {
		w = self.W // as tmux has it, which the other panes are placed by
	}
	var out []panel
	if m.cockpit.open {
		w = cockpitListWidth(m.width.want, m.w)
		y++ // the cockpit draws the list's rule itself
	}
	if w > 0 {
		h := m.listHeight()
		if m.cockpit.open {
			h--
		}
		secs := m.sections()
		e, _, _ := m.selected(flatten(secs))
		lm := m
		lm.w = w
		c := lm.column(secs, e.key(), h)
		out = append(out, panel{projectsPanel, rect{x, y, w, c.projH}})
		if c.agentsH > 0 {
			out = append(out, panel{agentsPanel, rect{x, y + c.rule + 1, w, c.agentsH}})
		}
	}
	if m.cockpit.open {
		cx := x
		if w > 0 {
			cx += w + 1
		}
		cw, ch := m.cardsSize()
		_, _, places := layoutCards(m.cockpitRows(m.target()), cw, ch, m.navAt())
		for _, pl := range places {
			if pl.id != "" {
				out = append(out, panel{pl.id, rect{cx + pl.r.x, self.Y + pl.r.y, pl.r.w, pl.r.h}})
			}
		}
		return out
	}
	for _, p := range m.panes {
		if p.ID != m.self && p.Window == self.Window && self.Window != "" {
			out = append(out, panel{m.panelOfPane(p.ID), rect{p.X, p.Y, p.W, p.H}})
		}
	}
	return out
}

// keepNav keeps the selection on a panel that is there. One that has gone
// gives way to the stage, for a pane, or else to the projects, and the
// keyboard comes out of it. Inside the list, the selection follows the
// cursor from one of its panels to the other.
func (m model) keepNav() model {
	at := m.navAt()
	if m.nav.inside && at.listPanel() && m.cursorPanel() != at {
		if at = m.cursorPanel(); at == agentsPanel && len(agentEntries(m.sections())) == 0 {
			at = projectsPanel
		}
		m.nav.panel = at
	}
	if m.hasPanel(at) {
		return m
	}
	m.nav.inside = false
	switch {
	case (at.isPane() || at == stagePanel) && m.hasPanel(stagePanel):
		m.nav.panel = stagePanel
	default:
		m.nav.panel = projectsPanel
	}
	return m
}

// hasPanel says whether panel p is in the window now.
func (m model) hasPanel(p panelID) bool {
	switch {
	case p == projectsPanel:
		return true
	case p == agentsPanel:
		return len(agentEntries(m.sections())) > 0 && (!m.cockpit.open || cockpitListWidth(m.width.want, m.w) > 0)
	case p == stagePanel:
		// The list may be on its way to the pane: the selection waits.
		return !m.cockpit.open && m.want() != ""
	case p == headerPanel:
		return m.cockpit.open
	case p == issuesPanel:
		_, tracked := m.trackers[m.target().root]
		return m.cockpit.open && tracked && m.issuesShown()
	case m.cockpit.open:
		return false
	}
	return m.paneOfPanel(p) != ""
}

// navMove moves the selection to the nearest panel in direction d.
func (m model) navMove(d direction) model {
	if next, ok := navStep(m.panels(), m.navAt(), d, m.nav.prev); ok {
		m.nav = nav{panel: next, prev: m.navAt()}
	}
	return m
}

// navEnter goes into the selected panel: the keys move the cursor of the
// list or the issues card, enter acts on the cursor's entry from the
// stage or the cockpit's header, as it does in the list, and the keyboard
// goes to any other pane.
func (m model) navEnter() (model, tea.Cmd) {
	switch at := m.navAt(); {
	case at.listPanel():
		m.nav.inside = true
		return m.cursorInto(at), nil
	case at == issuesPanel:
		m.nav.inside = true
		return m, nil
	case at == stagePanel || at == headerPanel:
		return m.apply(action{do: open})
	case m.paneOfPanel(at) != "":
		m.entering = m.paneOfPanel(at)
	}
	return m, nil
}

// backFrom is the model with the keyboard back in the view from the pane
// it was in, as the latest listing has it: that pane is selected, and
// the keys move between panels again. Focus that comes back from outside
// tmux finds the view's own pane active, and changes nothing.
func (m model) backFrom() model {
	if !m.blurred {
		return m
	}
	self, _ := m.pane(m.self)
	for _, p := range m.panes {
		if p.Active && p.ID != m.self && p.Window == self.Window && self.Window != "" {
			m.nav = nav{panel: m.panelOfPane(p.ID), prev: m.navAt()}
		}
	}
	return m
}
