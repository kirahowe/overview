package tui

import (
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"overview/internal/tmux"
)

// The agents have a pane of their own, under the list's and as wide: the
// foot of the list's column. It is a tmux pane, so the keys that move
// tmux's focus reach it like any other (down from the list, in the panels
// mode or with tmux's own bindings), and tmux draws its rule and lights it
// while the keyboard is there. relay.go has the program that runs in it.
//
// The view starts the pane once sessions are listed, keeps it as tall as
// its lines need, takes it along when the list moves to another window and
// closes it when no session is left. The footer is the column's last line,
// so with the pane open it is the pane's last line and the list's pane has
// none.
//
// The cursor is still one cursor. With the agents apart, j and k stop at
// the end of the side they are on, and the cursor changes sides when the
// keyboard does: it goes to the same session's entry on the other side,
// so the pane beside the list does not change under it.
//
// Whenever the pane is not showing the agents, the list's own pane draws
// them as its last lines, and the cursor walks on into them: before the
// pane has opened, while the cockpit or the task picker has the list
// zoomed over it, and in a window too short for both.

type (
	// agentsOpenedMsg answers the Cmd that started the agents' pane.
	agentsOpenedMsg struct {
		pane tmux.Pane
		err  error
	}
	// agentsKeptMsg answers a Cmd that resized it, or closed the panes
	// named.
	agentsKeptMsg struct {
		closed []string
		err    error
	}
)

// agentsPane is the agents' pane: the pane so marked in the list's
// window. ok is false when there is none.
func (m model) agentsPane() (tmux.Pane, bool) {
	win := m.windowOf(m.self)
	for _, p := range m.in.Panes {
		if p.Agents && p.Session == m.session && p.Window == win && win != "" {
			return p, true
		}
	}
	return tmux.Pane{}, false
}

// apart says whether the agents' pane shows the agents: it is open beside
// the list, its program has said how big it is, and nothing has the list
// zoomed over it. Otherwise the list's pane draws the agents itself.
func (m model) apart() bool {
	if m.mux == nil || m.aw <= 0 || m.ah < 2 || m.zoomWant() {
		return false
	}
	list, ok := m.pane(m.self)
	if !ok || list.Zoomed {
		return false
	}
	_, ok = m.agentsPane()
	return ok
}

// side is the run of es that j and k walk from the cursor's entry, the
// ith: all of es while the list draws the agents itself, and with the
// agents apart only the cursor's own side of it, the agents or the rest.
// The agents come last in es.
func side(es []entry, i int, apart bool) (lo, hi int) {
	if !apart || i < 0 || i >= len(es) {
		return 0, len(es)
	}
	first := len(es)
	for j, e := range es {
		if e.alias == agentsPrefix {
			first = j
			break
		}
	}
	if es[i].alias == agentsPrefix {
		return first, len(es)
	}
	return 0, first
}

// across is where the cursor goes from the ith entry of es when the
// keyboard arrives on one side of the list: among the agents when agents
// is set, and else among the rest. From a session it goes to that
// session's entry on the other side, and from a workstream to its lead's,
// so the pane the stage shows stays as it is. With no such entry it goes
// to the first on that side. ok is false when the cursor is on that side
// already, or the side has nothing.
func across(es []entry, i int, agents bool) (string, bool) {
	if i < 0 || i >= len(es) || (es[i].alias == agentsPrefix) == agents {
		return "", false
	}
	from := es[i]
	if from.kind == workstreamEntry {
		if c, ok := lead(children(es, from.task())); ok {
			from = c
		}
	}
	first, any, other := "", "", ""
	for _, e := range es {
		if (e.alias == agentsPrefix) != agents {
			continue
		}
		if any == "" {
			any = e.key()
		}
		if first == "" && landing(e) {
			first = e.key()
		}
		if s, ok := from.session(); ok && e.kind == sessionEntry && e.sess.SessionID == s.SessionID {
			if e.alias == "" || agents {
				return e.key(), true
			}
			if other == "" {
				other = e.key()
			}
		}
	}
	switch {
	case other != "":
		return other, true
	case first != "":
		return first, true
	}
	// Only headings are left there, as with nothing but full projects.
	return any, any != ""
}

// cross puts the cursor on the side of the list the keyboard or the mouse
// has arrived on, as across has it. It cancels a move of the keyboard that
// was on its way to a pane, since the user has gone somewhere else.
func (m model) cross(agents bool) model {
	if !m.apart() {
		return m
	}
	es := m.ordered()
	_, i, _ := m.selected(es)
	if key, ok := across(es, i, agents); ok {
		m.sel, m.entering = key, ""
	}
	return m
}

// needs says whether n stops at an entry among the agents: a session that
// needs you. Every entry there is an alias, which needy passes over.
func needs(e entry) bool {
	r, ok := e.session()
	return ok && r.NeedsYou()
}

// agentsHeight is how tall the agents' pane is in a column total lines
// high, under a list of projects lines: a line for each of agents and one
// for the footer, as splitList shares the column, and 0 for no pane.
func agentsHeight(projects, agents, total int) int {
	// The footer has the column's last line, and tmux's rule between the
	// two panes is the line splitList gives the agents' rule.
	if _, rows := splitList(projects, agents, total-1); rows > 0 {
		return rows + 1
	}
	return 0
}

// agentsStep is what keepAgents does to the agents' pane.
type agentsStep int

const (
	agentsStay  agentsStep = iota // it is as it should be
	agentsOpen                    // start it, lines tall
	agentsSize                    // make it lines tall
	agentsClose                   // close the panes named
)

// agentsPlan decides what to do about the agents' pane, given the panes of
// the view's session as tmux lists them, the list's pane, how many lines
// the projects and the agents have, and what was last asked of tmux and
// refused. An agents' pane that is not the one under the list was left
// behind by a move that failed halfway, or by a view that has gone, and
// closes first. Then the pane under the list opens, closes or is resized
// to the height agentsHeight gives it.
func agentsPlan(panes []tmux.Pane, session string, list tmux.Pane, projects, agents int, refused agentsAsk) (agentsStep, []string, agentsAsk) {
	var under tmux.Pane
	var strays []string
	for _, p := range panes {
		switch {
		case !p.Agents || p.Session != session:
		case p.Window == list.Window && under.ID == "":
			under = p
		default:
			strays = append(strays, p.ID)
		}
	}
	if len(strays) > 0 {
		return agentsClose, strays, agentsAsk{}
	}
	total := list.H
	if under.ID != "" {
		total += 1 + under.H
	}
	ask := agentsAsk{total: total, lines: agentsHeight(projects, agents, total), had: under.H}
	switch {
	case under.ID == "" && ask.lines == 0, under.ID != "" && under.H == ask.lines:
		return agentsStay, nil, agentsAsk{}
	case ask == refused:
		return agentsStay, nil, refused // tmux has no room for it
	case under.ID == "":
		return agentsOpen, nil, ask
	case ask.lines == 0:
		return agentsClose, []string{under.ID}, ask
	}
	return agentsSize, []string{under.ID}, ask
}

// agentsAsk is a height asked of tmux for the agents' pane: lines, in a
// column total high, where the pane had had lines. keepAgents remembers
// the last one and does not ask it twice running. tmux would refuse again
// what it had no room for, and a pane whose program could not reach the
// view, and so closed, would open and close for ever. The memory goes
// once a pane's program has said its size.
type agentsAsk struct{ total, lines, had int }

// keepAgents reads the listing just taken, which must be current, and
// brings the agents' pane in line with it, one step at a time, as
// agentsPlan decides. Each step counts as a spawn, so no listing asked
// for before it lands is taken to show the column as it was. Nothing is
// done while the list is moving or zoomed: a split or a resize would end
// the zoom. For the same reason the list does not move, and the cockpit
// and the task picker do not zoom it, until a step that is out has
// landed: agentsBusy says one is.
func (m model) keepAgents() (model, tea.Cmd) {
	list, ok := m.pane(m.self)
	if !ok || m.mux == nil || m.agentsSock == "" || m.agentsBusy || m.staging || m.zooming || m.zoomWant() || list.Zoomed {
		return m, nil
	}
	// The pane is as tall as the whole list needs, whatever a search
	// hides, so it does not jump about while one is typed.
	all := m
	all.filter = ""
	secs := all.sections()
	projects, _ := all.projectLines(secs, "")
	step, panes, ask := agentsPlan(m.in.Panes, m.session, list, len(projects), len(agentEntries(secs)), m.agentsRefused)
	if step == agentsStay {
		return m, nil
	}
	m.spawns++
	m.agentsBusy = true
	mux, self := m.mux, m.self
	switch step {
	case agentsOpen:
		m.agentsRefused = ask
		exe, sock := m.exe, m.agentsSock
		return m, func() tea.Msg {
			p, err := mux.Under(self, ask.lines, exe, "agents", sock)
			return agentsOpenedMsg{p, err}
		}
	case agentsSize:
		m.agentsRefused = ask
		return m, func() tea.Msg { return agentsKeptMsg{err: mux.Resize(panes[0], strconv.Itoa(ask.lines))} }
	}
	return m, func() tea.Msg {
		for _, id := range panes {
			mux.KillPane(id) //nolint:errcheck
		}
		return agentsKeptMsg{closed: panes}
	}
}

// keepColumn keeps the list's column as it should be, on a listing that
// is current: its width first, and then the agents' pane at its foot.
func (m model) keepColumn() (model, tea.Cmd) {
	m, cmd := m.keepWidth()
	if cmd != nil {
		return m, cmd
	}
	return m.keepAgents()
}

// agentsLanded takes the answers to keepAgents' Cmds: the pane that
// opened is known at once, and so is that the ones closed have gone. A
// cockpit or a task picker that was opened meanwhile gets its zoom now.
func (m model) agentsLanded(msg tea.Msg) (model, tea.Cmd) {
	m.landed++
	m.agentsBusy = false
	switch msg := msg.(type) {
	case agentsOpenedMsg:
		if msg.pane.ID == "" {
			// The list goes on drawing the agents itself, and tmux is
			// not asked again until the column has changed.
			m = m.say("could not open the agents' pane: " + msg.err.Error())
		} else {
			m = m.addPane(msg.pane)
		}
	case agentsKeptMsg:
		if len(msg.closed) > 0 {
			m = m.keepPanes(slices.DeleteFunc(slices.Clone(m.in.Panes), func(p tmux.Pane) bool { return slices.Contains(msg.closed, p.ID) }))
			if _, ok := m.agentsPane(); !ok {
				m = m.lostAgents()
			}
		}
	}
	switch {
	case m.staging:
	case m.cockpitSoon:
		return m.openCockpit()
	case m.taskPick.open:
		m.zoomPanes = m.inWindow()
		return m.syncZoom()
	}
	return m, nil
}

// lostAgents is the model once a listing shows no agents' pane: its size
// is forgotten, and had the keyboard been in it, it is taken to be outside
// the view until a pane says otherwise.
func (m model) lostAgents() model {
	m.aw, m.ah, m.agentsShown = 0, 0, ""
	if m.inAgents {
		m.inAgents, m.blurred = false, true
	}
	return m
}

// fromAgents handles what the agents' pane's program passed on, as update
// handles the same from the view's own pane.
func (m model) fromAgents(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// A pane that has just connected, or changed size, has nothing
		// that fits it on its screen. Its program runs, so should the
		// pane close it may be opened again.
		m.aw, m.ah, m.agentsShown, m.agentsRefused = msg.Width, msg.Height, "", agentsAsk{}
	case tea.FocusMsg:
		return m.handle([]action{{do: keysBack, n: 1}})
	case tea.BlurMsg:
		return m.handle([]action{{do: keysLeft, n: 1}})
	case tea.KeyMsg:
		m.panels = false
		return m.input(action{do: keysBack, n: 1}, true, func(m model) []action { return m.keyActions(msg) })
	case tea.MouseMsg:
		m.panels = false
		at := m.now
		wheel := msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown
		return m.input(action{do: pointerHere, n: 1}, !wheel, func(m model) []action { return m.mouseActions(msg, at, true) })
	}
	return m, nil
}

// inPane is the model as the agents' pane draws it: at that pane's size,
// and with the cursor's row lit only while the keyboard is in that pane.
func (m model) inPane() model {
	m.w, m.h, m.other = m.aw, m.ah, !m.inAgents
	return m
}

// agentsBody is what the agents' pane shows above the footer: a line for
// each of the agents, scrolled as little as the cursor needs from
// agentsTop, with the key of the entry each line shows, "" for none, and
// the first line shown.
func (m model) agentsBody(secs []section, sel string) (lines, owners []string, top int) {
	am := m.inPane()
	h := max(m.ah-1, 0)
	var all, keys []string
	for _, e := range agentEntries(secs) {
		all, keys = append(all, am.entryLine(e, e.key() == sel)), append(keys, e.key())
	}
	if len(all) == 0 && m.filter != "" {
		all, keys = []string{am.plain(dim.Render("nothing matches /" + m.filter))}, []string{""}
	}
	top = scrollTo(m.agentsTop, keys, sel, h)
	end := min(top+h, len(all))
	lines, owners = append(lines, all[top:end]...), append(owners, keys[top:end]...)
	for len(lines) < h {
		lines, owners = append(lines, am.plain("")), append(owners, "")
	}
	return lines, owners, top
}

// agentsScreen is the agents' pane's whole screen: its lines and, last,
// the footer.
func (m model) agentsScreen() string {
	secs := m.sections()
	e, _, _ := m.selected(flatten(secs))
	lines, _, _ := m.agentsBody(secs, e.key())
	return strings.Join(append(lines, m.inPane().footer()), "\n")
}

// redrawAgents sends the agents' pane its screen when it has changed, as
// restage redraws the holder. The screens are numbered, since two Cmds
// can land in either order.
func (m model) redrawAgents() (model, tea.Cmd) {
	if m.deps.drawAgents == nil || !m.apart() {
		return m, nil
	}
	screen := m.agentsScreen()
	if screen == m.agentsShown {
		return m, nil
	}
	m.agentsShown = screen
	m.agentsSeq++
	draw, seq := m.deps.drawAgents, m.agentsSeq
	return m, func() tea.Msg {
		draw(seq, screen) //nolint:errcheck // a pane that has gone says so itself
		return nil
	}
}

// agentsMouse translates a click or the wheel in the agents' pane, once
// mouseActions has dealt with what is the same in both panes.
func (m model) agentsMouse(msg tea.MouseMsg, step int, at time.Time) []action {
	if step != 0 {
		return doN(move, step)
	}
	secs := m.sections()
	cur, _, _ := m.selected(flatten(secs))
	_, owners, _ := m.agentsBody(secs, cur.key())
	if y := msg.Y; y >= 0 && y < len(owners) && owners[y] != "" {
		return []action{{do: click, id: owners[y], at: at}}
	}
	return nil
}
