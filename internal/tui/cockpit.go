package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"overview/internal/fleet"
	"overview/internal/issues"
	"overview/internal/review"
	"overview/internal/vcs"
	"overview/internal/world"
)

// The cockpit is the view's own pane zoomed to fill its window: the list
// down the left, as it always is, and cards about the selected entry's
// workstream or project beside it. tmux keeps every other pane of the
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
// cockpit or the task picker is open, as both want the whole window.
func (m model) zoomWant() bool { return m.cockpit.open || m.taskPick.open }

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
// so the cockpit opens once the move lands.
func (m model) openCockpit() (model, tea.Cmd) {
	if m.cockpit.open {
		return m, nil
	}
	if m.staging {
		m.cockpitSoon = true
		return m, nil
	}
	m.cockpitSoon = false
	m.cockpit, m.zoomPanes = cockpit{open: true}, m.inStage()
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

// inStage counts the panes in the list's window.
func (m model) inStage() int {
	w, n := m.windowOf(m.self), 0
	for _, p := range m.in.Panes {
		if p.Window == w && w != "" {
			n++
		}
	}
	return n
}

// zoomLost reads a current listing for what became of the list's zoom,
// for the cockpit or the task picker. tmux ends a zoom when the user goes
// to another pane of the window, and also when a pane of it closes or
// opens. Only the first is the user leaving, and what wanted the zoom
// closes, without touching tmux: the list's window has the panes it had,
// unzoomed. When a pane has gone the zoom is asked for again if the list
// has company. A list alone in its window fills it unzoomed, as tmux
// cannot zoom it.
func (m model) zoomLost() (model, tea.Cmd) {
	if !m.zoomWant() || m.zooming || m.mux == nil {
		return m, nil
	}
	p, ok := m.pane(m.self)
	if !ok {
		return m, nil
	}
	n := m.inStage()
	switch {
	case p.Zoomed || n == 1:
		m.zoomPanes = n
	case n < m.zoomPanes:
		m.zoomPanes = n
		return m.syncZoom()
	default:
		m.cockpit, m.taskPick = cockpit{}, taskPick{}
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

// idleStream says whether e is a workstream that no session works on.
// The holder shows its cockpit's cards, and enter opens the cockpit.
func idleStream(es []entry, e entry) bool {
	return e.kind == workstreamEntry && !slices.ContainsFunc(children(es, e.task()), func(c entry) bool { return c.kind == sessionEntry })
}

// idleSelected says whether the cursor is on an idle workstream.
func (m model) idleSelected() bool {
	es := m.ordered()
	e, _, ok := m.selected(es)
	return ok && idleStream(es, e)
}

// target is what the cockpit shows: a workstream, when stream is set, or
// else the project at root, which is nil when the World has none there.
// root is where its reads run: the workstream's tracker's root, or the
// project's.
type target struct {
	root    string
	stream  *world.Stream
	project *world.Project
}

// cockpitTarget is the cockpit's target for the selected entry in w: the
// workstream it is or sits under, or else the project it belongs to, as
// rootOf finds it. ok says whether anything is selected.
func cockpitTarget(w *world.World, e entry, ok bool, rootOf func(entry, bool) string) target {
	if ok && e.stream != nil {
		return target{root: e.stream.Root, stream: e.stream, project: e.stream.Project}
	}
	root := rootOf(e, ok)
	return target{root: root, project: w.ProjectOf(root)}
}

// target is the cockpit's target now.
func (m model) target() target {
	e, _, ok := m.selected(m.ordered())
	return cockpitTarget(m.world(), e, ok, m.rootOf)
}

// sessions are the target's sessions: those linked to its workstream, or
// every one in its project.
func (t target) sessions() []*world.Session {
	switch {
	case t.stream != nil:
		return t.stream.Sessions
	case t.project != nil:
		return t.project.Members
	}
	return nil
}

// terminals are the target's terminals: those opened for its workstream,
// or every one in its project.
func (t target) terminals() []*world.Terminal {
	switch {
	case t.stream != nil:
		return t.stream.Terminals
	case t.project != nil:
		return t.project.AllTerminals
	}
	return nil
}

// start is when a workstream's work began, in Unix milliseconds, 0 when
// nothing says or for a project, whose changes are its working copy's.
func (t target) start() int64 {
	if t.stream == nil || t.stream.Started.IsZero() {
		return 0
	}
	return t.stream.Started.UnixMilli()
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
	e, _, _ := m.selected(flatten(secs))
	col := lm.column(secs, e.key(), h-1)
	left := append([]string{panelRule(m.titleText(m.listTitle(), m.listHasKeys()), "", lw, m.listHasKeys())}, col.lines...)
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
	return slices.ContainsFunc(places, func(p placed) bool { return p.id == issuesPanel })
}

// inCockpitList says whether a click at column x lands on the list in the
// cockpit.
func (m model) inCockpitList(x int) bool {
	lw := cockpitListWidth(m.width.want, m.w)
	return lw > 0 && x < lw
}

// The cockpit's reads run in Cmds and land in caches by target: the
// issue's details, read again when its project's .issues changes, and the
// changes, read on opening and when what the target's sessions are doing
// changes. A timer would run jj, which snapshots the working copy, every
// few seconds for nothing.

// issueRead is an issue's details as last read, for the .issues stamp
// they were read at. pending is set while a read is out.
type issueRead struct {
	stamp   string
	pending bool
	info    issueInfo
}

// changesRead is a target's changes as last read. sig is what its sessions
// were doing when they were asked for, and base the commit a workstream's
// changes are read from, found once for its start. fresh says they were
// asked for since the cockpit opened.
type changesRead struct {
	sig     string
	start   int64
	base    string
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
		key, sig, base, kind string
		start                int64
		info                 changeInfo
	}
)

// cockpitKey names a target in the caches.
func cockpitKey(t target) string {
	if t.stream != nil {
		return taskPrefix + t.stream.Key.String()
	}
	return "project:" + t.root
}

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

// cockpitReads asks for what the cockpit's target lacks, while it is open
// or the holder shows its cards: its issue's
// details when they are missing or older than its .issues, and its
// changes when they are missing, not yet read since the cockpit opened,
// or from before its sessions last changed. A workstream's base is kept
// once found, and found again only when its start moves earlier, as when
// a session that took it on sooner is listed: a session ending must not
// move it later.
func (m model) cockpitReads() (model, tea.Cmd) {
	if !m.cockpit.open && !m.idleSelected() {
		return m, nil
	}
	t := m.target()
	var cmds []tea.Cmd
	if t.stream != nil {
		var read tea.Cmd
		m, read = m.readIssue(t.root, t.stream.ID)
		cmds = append(cmds, read)
	}
	key := cockpitKey(t)
	sig, start := sessionsSig(t.sessions()), t.start()
	c, ok := m.changeReads[key]
	if c.pending || (ok && c.sig == sig && c.fresh) {
		return m, tea.Batch(cmds...)
	}
	stream := t.stream != nil
	if stream && start == 0 && c.base == "" {
		c.sig, c.fresh, c.info = sig, true, changeInfo{noStart: true}
		m.changeReads = withRead(m.changeReads, key, c)
		return m, tea.Batch(cmds...)
	}
	base := c.base
	switch {
	case base == "" || (start > 0 && start < c.start):
		base = ""
	default:
		start = c.start
	}
	c.sig, c.fresh, c.pending = sig, true, true
	c.info.noStart = false
	m.changeReads = withRead(m.changeReads, key, c)
	return m, tea.Batch(append(cmds, changesCmd(key, sig, t.root, start, base, stream, m.deps))...)
}

// readIssue asks for the details of issue id of the project at root when
// they are missing or older than its .issues, for the cockpit's issue
// card and the task picker.
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
		return issueReadMsg{key, stamp, issueInfo{summary: issueSummary(i.Details), criteria: issues.Criteria(i.Details), details: i.Details}}
	}
}

// changesCmd reads what changed at root: since start, from base when it is
// known, for a workstream, and in the working copy for a project.
func changesCmd(key, sig, root string, start int64, base string, stream bool, d deps) tea.Cmd {
	return func() tea.Msg {
		msg := changesReadMsg{key: key, sig: sig, start: start, base: base}
		repo, ok := vcs.Find(root, d.exists)
		if !ok {
			msg.info.noRepo = true
			return msg
		}
		msg.kind = repo.Kind.String()
		fail := func(err error) tea.Msg {
			msg.info.err = err
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
		if !stream {
			msg.info = changeInfo{files: fileStats(open), uncommitted: -1}
			return msg
		}
		if msg.base == "" {
			if msg.base, err = repo.BaseAt(d.vcs, time.UnixMilli(start)); err != nil {
				return fail(err)
			}
		}
		files, err := repo.Diff(d.vcs, d.untracked, msg.base)
		if err != nil {
			return fail(err)
		}
		msg.info = changeInfo{files: fileStats(files), uncommitted: len(open)}
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
		if msg.info.err == nil {
			c.start, c.base = msg.start, msg.base
		}
		m.changeReads = withRead(m.changeReads, msg.key, c)
	}
	return m
}

// stale is the changes with every read made old, so opening the cockpit
// reads its target's again while showing what it read last. A new
// cockpit finds each workstream's base afresh.
func stale(mp map[string]changesRead) map[string]changesRead {
	out := make(map[string]changesRead, len(mp))
	for k, v := range mp {
		v.fresh, v.base, v.start = false, "", 0
		out[k] = v
	}
	return out
}

// members are a target's sessions and terminals as its header shows them.
func (m model) members(rows []*world.Session, terms []*world.Terminal) []member {
	var out []member
	for _, r := range rows {
		_, _, word, age, style := m.stateMark(r.Row)
		if r.State == fleet.Working {
			style = green
		}
		out = append(out, member{name: r.Name, word: word, age: age, style: style})
	}
	for _, t := range terms {
		out = append(out, member{name: t.Title, term: true})
	}
	return out
}

// others are the open issues of the target's tracker other than its own,
// and how many sessions work on each, by number, as its workstream lists
// them.
func (m model) others(t target) ([]issues.Issue, map[int]int) {
	var out []issues.Issue
	linked := map[int]int{}
	count := m.sessionsOn(t.root)
	tr, _ := m.world().Tracker(t.root)
	for _, i := range tr.Open {
		if t.stream == nil || i.ID != t.stream.ID {
			out = append(out, i)
			if n := count(i); n > 0 {
				linked[i.ID] = n
			}
		}
	}
	return out, linked
}

// changeStart is when the target's changes are counted from: the start
// its base was found for, once it has one, and else its start now.
func (m model) changeStart(t target) int64 {
	if c := m.changeReads[cockpitKey(t)]; c.base != "" && c.start > 0 {
		return c.start
	}
	return t.start()
}

// cockpitRows is the cards for target t, in rows from the top.
func (m model) cockpitRows(t target) [][]card {
	rows := t.sessions()
	ms := m.members(rows, t.terminals())
	ch := m.changeReads[cockpitKey(t)]
	info := ch.info
	if _, ok := m.changeReads[cockpitKey(t)]; !ok {
		info.loading = true
	}
	_, tracked := m.world().Tracker(t.root)
	others, linked := m.others(t)
	pick := max(0, min(m.cockpit.pick, len(pickable(others))-1))
	issuesRow := []card{issuesCard(others, linked, m.inIssues(), pick, m.trackers[t.root].err)}
	if !tracked {
		issuesRow = []card{tasksCard(m.short(t.root), m.issues != nil)}
	}
	if t.stream == nil {
		kind := ch.kind
		if info.noRepo {
			kind = "no repository"
		}
		open := -1
		if tracked {
			open = len(others)
		}
		return [][]card{
			{projectHeader(m.name(t.root), m.short(t.root), kind, open, ms)},
			issuesRow,
			{changesCard("Uncommitted", "", info)},
		}
	}
	since := ""
	if s := m.changeStart(t); s > 0 {
		since = Ago(time.UnixMilli(s))
	}
	ir, ok := m.issueReads[t.stream.Key.String()]
	details := ir.info
	if !ok {
		details.loading = true
	}
	var sum review.Summary
	for _, r := range rows {
		s := m.reviews[r.SessionID]
		sum.Open, sum.Waiting, sum.Unsent = sum.Open+s.Open, sum.Waiting+s.Waiting, sum.Unsent+s.Unsent
	}
	return [][]card{
		{streamHeader(issueRef(m.projectID(t.root), t.stream.Issue), t.stream.Issue.Status, t.stream.Issue.Priority, t.stream.Issue.Title, since, ms)},
		{issueCard(t.stream.ID, details)},
		{changesCard("Changes", since, info), reviewCard(sum)},
		issuesRow,
	}
}

// startPicked acts on the issue under the issues card's cursor. An issue
// that is a workstream already, in the list as one, gets the cursor, and
// the cockpit follows it there, so enter never starts a second session on
// work under way. A ready or inbox issue gets a workstream: claude at the
// project's root with the opening prompt that names the issue, shown as
// c shows a new session, once the cockpit has closed.
func (m model) startPicked() (model, tea.Cmd) {
	t, i, ok := m.picked()
	if !ok {
		return m.say("no issue to start"), nil
	}
	if m.pickStarts() {
		m, out := m.closeCockpit()
		m, cmd := m.startOn(t.root, i)
		return m, tea.Batch(out, cmd)
	}
	es := m.ordered()
	j := streamAt(es, taskKey(t.root, i.ID))
	if j < 0 && m.filter != "" {
		// The filter hides it: show everything, so the cursor can go.
		m.filter, m.filtering = "", false
		es = m.ordered()
		j = streamAt(es, taskKey(t.root, i.ID))
	}
	if j >= 0 {
		m = m.moveTo(es, j)
	}
	m.cockpit.pick = 0
	m.cockpit.issues = false
	return m, nil
}

// streamAt is where the workstream key is in es, -1 when it is not.
func streamAt(es []entry, key string) int {
	return slices.IndexFunc(es, func(e entry) bool { return e.kind == workstreamEntry && e.task() == key })
}

// pickStarts says whether enter in the issues card starts a session: no
// session works on the issue under its cursor yet, whatever its status, so
// a paused task is resumed. With one working, enter goes to it instead.
// The task picker decides the same way, through taskGoes.
func (m model) pickStarts() bool {
	t, i, ok := m.picked()
	return ok && !taskGoes(taskRow{sessions: m.sessionsOn(t.root)(i)})
}

// picked is the issue under the issues card's cursor, and the cockpit's
// target it belongs to.
func (m model) picked() (target, issues.Issue, bool) {
	t := m.target()
	others, _ := m.others(t)
	ps := pickable(others)
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
	others, _ := m.others(m.target())
	m.cockpit.pick = max(0, min(m.cockpit.pick, len(pickable(others))-1))
	return m
}
