package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"overview/internal/fleet"
	"overview/internal/issues"
	"overview/internal/vcs"
	"overview/internal/world"
)

// The cockpit is the view's own pane zoomed to fill its window: the list
// down the left, as it always is, and cards about the selected entry's
// project beside it. tmux keeps every other pane of the
// window as it was under the zoom, so the session there comes back as it
// was when the cockpit closes. While it is open the list stays in its
// window, and the cards follow the cursor.

// cockpitList is the list's width in the cockpit when it has none of its
// own to keep, and cockpitCardsMin the narrowest the cards go beside it.
// A window too narrow for both shows only the cards.
const (
	cockpitList     = 40
	cockpitCardsMin = 50
)

// cockpit is the cockpit's state. pick is the issues card's cursor, which
// moves while the keys are in the card, which tab moves them to.
type cockpit struct {
	open   bool
	pick   int
	issues bool // the keys are in the issues card, not the list
}

// zoomedMsg answers zoomCmd.
type zoomedMsg struct{ err error }

// zoomCmd zooms the list's pane, or ends the zoom, in one tmux command
// that does nothing when the window is so already.
func zoomCmd(mux multiplexer, self string, on bool) tea.Cmd {
	return func() tea.Msg { return zoomedMsg{mux.SetZoom(self, on)} }
}

// zoomWant says whether the list's pane should be zoomed: while the
// cockpit or the task picker is open, or the digests show, as each wants
// the whole window.
func (m model) zoomWant() bool {
	return m.cockpit.open || m.taskPick.open || m.screen == digestScreen
}

// zoomWaits says whether a zoom is owed that waited for the list's move,
// or for the agents' pane, to land: the task picker's and the digests',
// which open at once and are zoomed when they can be.
func (m model) zoomWaits() bool { return m.taskPick.open || m.screen == digestScreen }

// syncZoom asks tmux for the zoom zoomWant says. One zoom is out at a time, so two never race in tmux. A wish made while
// one is out waits for it to land, and then the latest wish goes. The
// zoom counts as a spawn, so no listing from before it lands is taken to
// say the zoom has gone.
func (m model) syncZoom() (model, tea.Cmd) {
	if m.mux == nil {
		return m, nil
	}
	if m.zooming {
		m.rezoom = true
		return m, nil
	}
	m.spawns++
	m.zooming, m.rezoom = true, false
	return m, zoomCmd(m.mux, m.self, m.zoomWant())
}

// openCockpit opens the cockpit, zooming the list inside tmux. While the
// list is moving between windows it would zoom the window it is leaving,
// so the cockpit opens once the move lands. It waits in the same way for
// a split or a resize of the agents' pane, which would end the zoom.
func (m model) openCockpit() (model, tea.Cmd) {
	if m.cockpit.open {
		return m, nil
	}
	if m.staging || m.agentsBusy {
		m.cockpitSoon = true
		return m, nil
	}
	m.cockpitSoon = false
	m.cockpit, m.zoomPanes = cockpit{open: true}, m.inWindow()
	m.changeReads = stale(m.changeReads)
	return m.syncZoom()
}

// closeCockpit closes the cockpit and ends the zoom. The list goes back to
// following the cursor once the zoom has gone.
func (m model) closeCockpit() (model, tea.Cmd) {
	m.cockpitSoon = false
	if !m.cockpit.open {
		return m, nil
	}
	m.cockpit = cockpit{}
	return m.syncZoom()
}

// zoomLost reads a current listing for what became of the list's zoom,
// for the cockpit, the task picker or the digests. tmux ends a zoom when
// the user goes to another pane of the window, and also when a pane of
// it closes or opens. Only the first is the user leaving, and what wanted the zoom
// closes, without touching tmux: the list's window has the panes it had,
// unzoomed. When a pane has gone the zoom is asked for again if the list
// has company. A list alone in its window fills it unzoomed, as tmux
// cannot zoom it.
//
// A client that went to another window has left as well, and there tmux
// keeps the zoom, of a window it no longer shows. What wanted the zoom
// closes, and the zoom is ended, so that the list can follow the client.
func (m model) zoomLost() (model, tea.Cmd) {
	if !m.zoomWant() || m.zooming || m.mux == nil {
		return m, nil
	}
	if m.strayed() != "" {
		m = m.left()
		m.cockpit, m.taskPick, m.screen = cockpit{}, taskPick{}, sessionsScreen
		return m.syncZoom()
	}
	p, ok := m.pane(m.self)
	if !ok {
		return m, nil
	}
	n := m.inWindow()
	switch {
	case p.Zoomed || n == 1:
		m.zoomPanes = n
	case n < m.zoomPanes:
		m.zoomPanes = n
		return m.syncZoom()
	default:
		m.cockpit, m.taskPick, m.screen = cockpit{}, taskPick{}, sessionsScreen
	}
	return m, nil
}

// closesCockpit says whether an action leaves the cockpit before it acts:
// those that show another pane or take the list pane for something else.
func closesCockpit(v verb) bool {
	switch v {
	case open, newSession, newTerminal, openReview, addProject, toggleScreen, showScreen:
		return true
	}
	return false
}

// target is what the cockpit shows: the project at root, which is nil
// when the World has none there. root is where its reads run.
type target struct {
	root    string
	project *world.Project
}

// cockpitTarget is the cockpit's target for the selected entry in w: the
// project it belongs to, as rootOf finds it. ok says whether anything is
// selected.
func cockpitTarget(w *world.World, e entry, ok bool, rootOf func(entry, bool) string) target {
	root := rootOf(e, ok)
	return target{root: root, project: w.ProjectOf(root)}
}

// target is the cockpit's target now.
func (m model) target() target {
	e, _, ok := m.selected(m.ordered())
	return cockpitTarget(m.world(), e, ok, m.rootOf)
}

// sessions are the target's sessions: every one in its project.
func (t target) sessions() []*world.Session {
	if t.project != nil {
		return t.project.Members
	}
	return nil
}

// terminals are the target's terminals: every one in its project.
func (t target) terminals() []*world.Terminal {
	if t.project != nil {
		return t.project.Terminals
	}
	return nil
}

// cockpitListWidth is the list's width in the cockpit, 0 when the window
// has room only for the cards.
func cockpitListWidth(want, w int) int {
	if want <= 0 {
		want = cockpitList
	}
	if w < want+1+cockpitCardsMin {
		return 0
	}
	return want
}

// cockpitBody is the cockpit's lines above the footer: the list at its
// kept width, under a rule with its title, a line, then the cards. Where
// a rule meets the line between them, they join.
func (m model) cockpitBody() []string {
	h := m.listHeight()
	lw := cockpitListWidth(m.width.want, m.w)
	cw, _ := m.cardsSize()
	cards, rules, _ := layoutCards(m.cockpitRows(m.target()), cw, h, m.issuesLit())
	if lw == 0 {
		return cards
	}
	lm := m
	lm.w = lw
	secs := m.sections()
	col := lm.column(secs, m.marked(secs), h-1)
	left := append([]string{panelRule(m.titleText(m.listTitle()), "", lw, m.listHasKeys())}, col.lines...)
	out := make([]string, h)
	for i := range out {
		leftRule := i == 0 || i == col.rule+1
		join := "│"
		switch {
		case i == 0:
			join = "┬"
		case leftRule && rules[i]:
			join = "┼"
		case leftRule:
			join = "┤"
		case rules[i]:
			join = "├"
		}
		out[i] = left[i] + fg(pal.Border).Render(join) + cards[i]
	}
	return out
}

// cardsSize is the width and height of the cockpit's cards: the window
// beside the list and the line after it, above the footer.
func (m model) cardsSize() (w, h int) {
	if lw := cockpitListWidth(m.width.want, m.w); lw > 0 {
		return m.w - lw - 1, m.listHeight()
	}
	return m.w, m.listHeight()
}

// issuesShown says whether the cockpit has room for the target's issues
// card, the card whose cursor takes the keyboard.
func (m model) issuesShown() bool {
	w, h := m.cardsSize()
	_, _, places := layoutCards(m.cockpitRows(m.target()), w, h, "")
	// A card cut to its rule alone shows no issue to put the cursor on.
	return slices.ContainsFunc(places, func(p placed) bool { return p.id == issuesPanel && p.r.h >= 2 })
}

// inCockpitList says whether a click at column x lands on the list in the
// cockpit.
func (m model) inCockpitList(x int) bool {
	lw := cockpitListWidth(m.width.want, m.w)
	return lw > 0 && x < lw
}

// The cockpit's reads run in Cmds and land in caches by target: the
// changes, read on opening and when what the target's sessions are doing
// changes. The task picker's issue details land in a cache by issue, read
// again when its project's .issues changes. A timer would run jj, which snapshots the working copy, every
// few seconds for nothing.

// issueRead is an issue's details as last read, for the .issues stamp
// they were read at. pending is set while a read is out.
type issueRead struct {
	stamp   string
	pending bool
	info    issueInfo
}

// changesRead is a target's changes as last read. sig is what its sessions
// were doing when they were asked for. fresh says they were asked for
// since the cockpit opened.
type changesRead struct {
	sig     string
	fresh   bool
	pending bool
	kind    string // the repository's, "" when there is none
	info    changeInfo
}

type (
	issueReadMsg struct {
		key, stamp string
		info       issueInfo
	}
	changesReadMsg struct {
		key, sig, kind string
		info           changeInfo
	}
)

// cockpitKey names a target in the caches.
func cockpitKey(t target) string { return "project:" + t.root }

// sessionsSig is what rows are doing, so a change to any of them, as a
// hook reports it, reads the changes again.
func sessionsSig(rows []*world.Session) string {
	parts := make([]string, len(rows))
	for i, r := range rows {
		parts[i] = fmt.Sprint(r.SessionID, ":", r.State, ":", r.Since.UnixNano())
	}
	slices.Sort(parts)
	return strings.Join(parts, " ")
}

// withRead is a copy of mp with k set to v, so a map an older model holds
// never changes under it.
func withRead[V any](mp map[string]V, k string, v V) map[string]V {
	out := make(map[string]V, len(mp)+1)
	for kk, vv := range mp {
		out[kk] = vv
	}
	out[k] = v
	return out
}

// cockpitReads asks for the target's changes while the cockpit is open,
// when they are missing, not yet read since the cockpit opened, or from
// before its sessions last changed.
func (m model) cockpitReads() (model, tea.Cmd) {
	if !m.cockpit.open {
		return m, nil
	}
	t := m.target()
	key, sig := cockpitKey(t), sessionsSig(t.sessions())
	c, ok := m.changeReads[key]
	if c.pending || (ok && c.sig == sig && c.fresh) {
		return m, nil
	}
	c.sig, c.fresh, c.pending = sig, true, true
	m.changeReads = withRead(m.changeReads, key, c)
	return m, changesCmd(key, sig, t.root, m.deps)
}

// readIssue asks for the details of issue id of the project at root when
// they are missing or older than its .issues, for the task picker.
func (m model) readIssue(root string, id int) (model, tea.Cmd) {
	if m.issues == nil {
		return m, nil
	}
	key, stamp := taskKey(root, id), m.trackers[root].stamp
	r, ok := m.issueReads[key]
	if ok && (r.stamp == stamp || r.pending) {
		return m, nil
	}
	r.stamp, r.pending, r.info.loading = stamp, true, !ok
	m.issueReads = withRead(m.issueReads, key, r)
	run := m.issues
	return m, func() tea.Msg {
		i, err := issues.Show(run, root, id)
		if err != nil {
			return issueReadMsg{key, stamp, issueInfo{err: err}}
		}
		return issueReadMsg{key, stamp, issueInfo{details: i.Details}}
	}
}

// changesCmd reads what is uncommitted at root.
func changesCmd(key, sig, root string, d deps) tea.Cmd {
	return func() tea.Msg {
		msg := changesReadMsg{key: key, sig: sig}
		repo, ok := vcs.Find(root, d.exists)
		if !ok {
			msg.info.noRepo = true
			return msg
		}
		msg.kind = repo.Kind.String()
		fail := func(err error) tea.Msg {
			// The card has room for why, not for git's whole message.
			msg.info.err = errors.New(vcs.Short(err))
			return msg
		}
		parent, err := repo.Uncommitted(d.vcs)
		if err != nil {
			return fail(err)
		}
		open, err := repo.Diff(d.vcs, d.untracked, parent)
		if err != nil {
			return fail(err)
		}
		msg.info = changeInfo{files: fileStats(open)}
		return msg
	}
}

// landRead takes a cockpit read that came back.
func (m model) landRead(msg tea.Msg) model {
	switch msg := msg.(type) {
	case issueReadMsg:
		if r, ok := m.issueReads[msg.key]; ok && r.stamp == msg.stamp {
			m.issueReads = withRead(m.issueReads, msg.key, issueRead{stamp: msg.stamp, info: msg.info})
		}
	case changesReadMsg:
		c := m.changeReads[msg.key]
		c.pending, c.kind, c.info = false, msg.kind, msg.info
		m.changeReads = withRead(m.changeReads, msg.key, c)
	}
	return m
}

// stale is the changes with every read made old, so opening the cockpit
// reads its target's again while showing what it read last.
func stale(mp map[string]changesRead) map[string]changesRead {
	out := make(map[string]changesRead, len(mp))
	for k, v := range mp {
		v.fresh = false
		out[k] = v
	}
	return out
}

// members are a target's sessions and terminals as its header shows them.
func (m model) members(rows []*world.Session, terms []*world.Terminal) []member {
	var out []member
	for _, r := range rows {
		mark, markStyle, word, age, style := m.stateMark(r.Row)
		if r.State == fleet.Working {
			style = green
		}
		out = append(out, member{name: r.Name, word: word, age: age, mark: mark, markStyle: markStyle, style: style})
	}
	for _, t := range terms {
		out = append(out, member{name: t.Title, term: true})
	}
	return out
}

// others are the open issues of the target's tracker.
func (m model) others(t target) []issues.Issue {
	tr, _ := m.world().Tracker(t.root)
	return tr.Open
}

// cockpitRows is the cards for target t, in rows from the top.
func (m model) cockpitRows(t target) [][]card {
	ms := m.members(t.sessions(), t.terminals())
	// The header offers enter only as the footer does, from enterOn on
	// the selected entry, which the target is of. While the issues card
	// has the keys, enter acts on its issue, and the footer says so, so
	// the header offers none.
	var enter []hint
	if e, _, ok := m.selected(m.ordered()); ok && !m.inIssues() {
		enter = m.enterHint(e)
	}
	ch, ok := m.changeReads[cockpitKey(t)]
	info := ch.info
	if !ok {
		info.loading = true
	}
	tr, tracked := m.world().Tracker(t.root)
	others := m.others(t)
	pick := max(0, min(m.cockpit.pick, len(pickable(others))-1))
	issuesRow := []card{issuesCard(others, m.inIssues(), pick, tr.Err)}
	open := -1
	if tracked {
		open = len(others)
	} else {
		issuesRow = []card{tasksCard(m.short(t.root), m.issues != nil)}
	}
	kind := ch.kind
	if info.noRepo {
		kind = "no repository"
	}
	return [][]card{
		{projectHeader(m.name(t.root), m.short(t.root), kind, open, ms, enter)},
		{changesCard(info)},
		issuesRow,
	}
}

// startPicked starts a session on the issue under the issues card's
// cursor: claude at the project's root with the opening prompt that
// names the issue, shown as c shows a new session, once the cockpit has
// closed. Nothing says which session works on an issue already, so this
// is always a new one.
func (m model) startPicked() (model, tea.Cmd) {
	t, i, ok := m.picked()
	if !ok {
		return m.say("no issue to start"), nil
	}
	m, out := m.closeCockpit()
	m, cmd := m.startOn(t.root, i)
	return m, tea.Batch(out, cmd)
}

// picked is the issue under the issues card's cursor, and the cockpit's
// target it belongs to.
func (m model) picked() (target, issues.Issue, bool) {
	t := m.target()
	ps := pickable(m.others(t))
	if len(ps) == 0 {
		return t, issues.Issue{}, false
	}
	return t, ps[max(0, min(m.cockpit.pick, len(ps)-1))], true
}

// keepCockpit keeps the issues card's cursor on an issue it lists, and
// gives the keys back to the list when the card has gone.
func (m model) keepCockpit() model {
	if !m.inIssues() {
		return m
	}
	if _, tracked := m.world().Tracker(m.target().root); !tracked || !m.issuesShown() {
		m.cockpit.issues = false
		return m
	}
	m.cockpit.pick = max(0, min(m.cockpit.pick, len(pickable(m.others(m.target())))-1))
	return m
}
