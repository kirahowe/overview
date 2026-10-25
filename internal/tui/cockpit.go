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

// cockpit is the cockpit's state. issues says the issues card has the
// keyboard, and pick is its cursor there. panes is how many panes the
// list's window had when a listing last showed the cockpit as it should
// be, so zoomLost can tell the user leaving from a pane going away.
type cockpit struct {
	open   bool
	issues bool
	pick   int
	panes  int
}

// zoomedMsg answers zoomCmd.
type zoomedMsg struct{ err error }

// zoomCmd zooms the list's pane, or ends the zoom, in one tmux command
// that does nothing when the window is so already.
func zoomCmd(mux multiplexer, self string, on bool) tea.Cmd {
	return func() tea.Msg { return zoomedMsg{mux.SetZoom(self, on)} }
}

// syncZoom asks tmux for the zoom the cockpit wants: on while it is open.
// One zoom is out at a time, so two never race in tmux. A wish made while
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
	return m, zoomCmd(m.mux, m.self, m.cockpit.open)
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
	m.cockpit = cockpit{open: true, panes: m.inStage()}
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
	for _, p := range m.panes {
		if p.Window == w && w != "" {
			n++
		}
	}
	return n
}

// zoomLost reads a current listing for what became of the cockpit's
// zoom. tmux ends a zoom when the user goes to another pane of the
// window, and also when a pane of it closes or opens. Only the first is
// the user leaving the cockpit, and it closes, without touching tmux:
// the list's window has the panes it had, unzoomed. When a pane has gone
// the cockpit stays, zoomed again if the list has company. A list alone
// in its window fills it unzoomed, as tmux cannot zoom it.
func (m model) zoomLost() (model, tea.Cmd) {
	if !m.cockpit.open || m.zooming || m.mux == nil {
		return m, nil
	}
	p, ok := m.pane(m.self)
	if !ok {
		return m, nil
	}
	n := m.inStage()
	switch {
	case p.Zoomed || n == 1:
		m.cockpit.panes = n
	case n < m.cockpit.panes:
		m.cockpit.panes = n
		return m.syncZoom()
	default:
		m.cockpit = cockpit{}
	}
	return m, nil
}

// closesCockpit says whether an action leaves the cockpit before it acts:
// those that show another pane or take the list pane for something else.
func closesCockpit(v verb) bool {
	switch v {
	case open, newSession, newTerminal, openReview, addProject, startTask, toggleScreen, showScreen:
		return true
	}
	return false
}

// target is what the cockpit shows: a workstream, when task is set, or
// else the project at root.
type target struct {
	root  string
	task  string
	issue issues.Issue
}

// cockpitTarget is the cockpit's target for the selected entry: the
// workstream it is or sits under, or else the project it belongs to, as
// rootOf finds it. ok says whether anything is selected.
func cockpitTarget(e entry, ok bool, rootOf func(entry, bool) string) target {
	if root, i, on := e.workstream(); ok && on {
		return target{root: root, task: e.task, issue: i}
	}
	return target{root: rootOf(e, ok)}
}

// target is the cockpit's target now.
func (m model) target() target {
	e, _, ok := m.selected(m.ordered())
	return cockpitTarget(e, ok, m.rootOf)
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

// cockpitBody is the cockpit's lines between the header and the footer:
// the list at its kept width and a border, then the cards.
func (m model) cockpitBody() []string {
	h := m.listHeight()
	lw := cockpitListWidth(m.width.want, m.w)
	cw, _ := m.cardsSize()
	var left []string
	if lw > 0 {
		lm := m
		lm.w = lw
		left = pad(lm.sessionsView(), h)
		for i, l := range left {
			if l == "" {
				left[i] = lm.plain("")
			}
		}
	}
	cards := m.cockpitCards(cw, h)
	out := make([]string, h)
	edge := fg(pal.Border).Render("│")
	for i := range out {
		if lw > 0 {
			out[i] = left[i] + edge + cards[i]
		} else {
			out[i] = cards[i]
		}
	}
	return out
}

// cardsSize is the width and height of the cockpit's cards: the window
// beside the list and its border, between the header and the footer.
func (m model) cardsSize() (w, h int) {
	if lw := cockpitListWidth(m.width.want, m.w); lw > 0 {
		return m.w - lw - 1, m.listHeight()
	}
	return m.w, m.listHeight()
}

// issuesShown says whether the cockpit has room for the target's issues
// card, the only card that takes the keyboard. layoutCards drops cards
// from the end, so it is shown when fewer cards come before it than are
// shown.
func (m model) issuesShown() bool {
	rows := m.cockpitRows(m.target())
	w, h := m.cardsSize()
	_, shown := layoutCards(rows, w, h)
	before := 0
	for _, r := range rows {
		for _, c := range r {
			if c.keys {
				return before < shown
			}
			before++
		}
	}
	return false
}

// cockpitCards is the cards for the target, h lines of exactly w cells.
func (m model) cockpitCards(w, h int) []string {
	lines, _ := layoutCards(m.cockpitRows(m.target()), w, h)
	return lines
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
	if t.task != "" {
		return taskPrefix + t.task
	}
	return "project:" + t.root
}

// targetRows are the target's sessions: those linked to its workstream,
// or those in its project.
func (m model) targetRows(t target) []fleet.Row {
	links := m.tasks().links
	var out []fleet.Row
	for _, r := range m.rows {
		switch {
		case r.Name == fleet.OrchestratorName:
		case t.task != "" && slices.Contains(links[r.SessionID], t.task):
			out = append(out, r)
		case t.task == "" && m.root(r.Cwd) == t.root:
			out = append(out, r)
		}
	}
	sortActive(out)
	return out
}

// targetTerms are the target's terminals: those opened for its
// workstream, or those of its project.
func (m model) targetTerms(t target) []terminal {
	var out []terminal
	for _, tm := range m.terminals() {
		if (t.task != "" && tm.Task == t.task) || (t.task == "" && tm.Project == t.root) {
			out = append(out, tm)
		}
	}
	return out
}

// streamStart is when the workstream key was taken on, in Unix
// milliseconds: the earliest time any of rows linked to it, or for a
// session whose refs have no time, when it started. 0 when none says.
func streamStart(rows []fleet.Row, key string, linked map[string]map[string]time.Time) int64 {
	var first int64
	for _, r := range rows {
		at := r.StartedAt
		if t := linked[r.SessionID][key]; !t.IsZero() {
			at = t.UnixMilli()
		}
		if at > 0 && (first == 0 || at < first) {
			first = at
		}
	}
	return first
}

// targetStart is when the target's work began: a workstream's start, as
// streamStart has it, or for a project when its first session started.
func (m model) targetStart(t target, rows []fleet.Row) int64 {
	if t.task == "" {
		return started(rows)
	}
	return streamStart(rows, t.task, m.tasks().linked)
}

// started is when the earliest of rows started, in Unix milliseconds, 0
// when none says.
func started(rows []fleet.Row) int64 {
	var first int64
	for _, r := range rows {
		if r.StartedAt > 0 && (first == 0 || r.StartedAt < first) {
			first = r.StartedAt
		}
	}
	return first
}

// sessionsSig is what rows are doing, so a change to any of them, as a
// hook reports it, reads the changes again.
func sessionsSig(rows []fleet.Row) string {
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

// cockpitReads asks for what the open cockpit's target lacks: its issue's
// details when they are missing or older than its .issues, and its
// changes when they are missing, not yet read since the cockpit opened,
// or from before its sessions last changed. A workstream's base is kept
// once found, and found again only when its start moves earlier, as when
// a session that took it on sooner is listed: a session ending must not
// move it later.
func (m model) cockpitReads() (model, tea.Cmd) {
	if !m.cockpit.open {
		return m, nil
	}
	t := m.target()
	var cmds []tea.Cmd
	if t.task != "" && m.issues != nil {
		stamp := m.trackers[t.root].stamp
		if r, ok := m.issueReads[t.task]; !ok || (r.stamp != stamp && !r.pending) {
			r.stamp, r.pending, r.info.loading = stamp, true, !ok
			m.issueReads = withRead(m.issueReads, t.task, r)
			run, root, id, key := m.issues, t.root, t.issue.ID, t.task
			cmds = append(cmds, func() tea.Msg {
				i, err := issues.Show(run, root, id)
				if err != nil {
					return issueReadMsg{key, stamp, issueInfo{err: err}}
				}
				return issueReadMsg{key, stamp, issueInfo{summary: issueSummary(i.Details), criteria: issues.Criteria(i.Details)}}
			})
		}
	}
	key := cockpitKey(t)
	rows := m.targetRows(t)
	sig, start := sessionsSig(rows), m.targetStart(t, rows)
	c, ok := m.changeReads[key]
	if c.pending || (ok && c.sig == sig && c.fresh) {
		return m, tea.Batch(cmds...)
	}
	stream := t.task != ""
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
func (m model) members(rows []fleet.Row, terms []terminal) []member {
	var out []member
	for _, r := range rows {
		_, _, word, age, style := m.stateMark(r)
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

// others are the open issues of the target's project other than its own,
// and how many sessions work on each, by number.
func (m model) others(t target) ([]issues.Issue, map[int]int) {
	var out []issues.Issue
	for _, i := range m.trackers[t.root].issues {
		if t.task == "" || i.ID != t.issue.ID {
			out = append(out, i)
		}
	}
	linked := map[int]int{}
	for _, keys := range m.tasks().links {
		for _, k := range keys {
			for _, i := range out {
				if k == taskKey(t.root, i.ID) {
					linked[i.ID]++
				}
			}
		}
	}
	return out, linked
}

// changeStart is when the target's changes are counted from: the start
// its base was found for, once it has one, and else its start now.
func (m model) changeStart(t target, rows []fleet.Row) int64 {
	if c := m.changeReads[cockpitKey(t)]; c.base != "" && c.start > 0 {
		return c.start
	}
	return m.targetStart(t, rows)
}

// cockpitRows is the cards for target t, in rows from the top.
func (m model) cockpitRows(t target) [][]card {
	rows, terms := m.targetRows(t), m.targetTerms(t)
	ms := m.members(rows, terms)
	ch := m.changeReads[cockpitKey(t)]
	info := ch.info
	if _, ok := m.changeReads[cockpitKey(t)]; !ok {
		info.loading = true
	}
	_, tracked := m.trackers[t.root]
	others, linked := m.others(t)
	pick := max(0, min(m.cockpit.pick, len(pickable(others))-1))
	issuesRow := []card{issuesCard(others, linked, m.cockpit.issues, pick, m.trackers[t.root].err)}
	if !tracked {
		issuesRow = []card{tasksCard(m.short(t.root), m.issues != nil)}
	}
	if t.task == "" {
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
	if s := m.changeStart(t, rows); s > 0 {
		since = Ago(time.UnixMilli(s))
	}
	ir, ok := m.issueReads[t.task]
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
		{streamHeader(issueRef(m.projectID(t.root), t.issue), t.issue.Status, t.issue.Priority, t.issue.Title, since, ms)},
		{issueCard(t.issue.ID, details)},
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
	m.cockpit.issues, m.cockpit.pick = false, 0
	return m, nil
}

// streamAt is where the workstream key is in es, -1 when it is not.
func streamAt(es []entry, key string) int {
	return slices.IndexFunc(es, func(e entry) bool { return e.kind == workstreamEntry && e.task == key })
}

// pickStarts says whether enter in the issues card starts a session: no
// session works on the issue under its cursor yet, whatever its status, so
// a paused task is resumed. With one working, enter goes to it instead.
// The task picker decides the same way, through taskGoes.
func (m model) pickStarts() bool {
	t, i, ok := m.picked()
	return ok && !taskGoes(taskRow{sessions: streamSessions(m.rows, m.tasks().links, t.root)[i.ID]})
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

// keepCockpit gives the keyboard back to the list when the cursor has
// moved to a project with no issues card, or the window has no room to
// show it, and keeps the card's cursor on
// an issue it lists.
func (m model) keepCockpit() model {
	if !m.cockpit.open || !m.cockpit.issues {
		return m
	}
	t := m.target()
	if _, tracked := m.trackers[t.root]; !tracked || !m.issuesShown() {
		m.cockpit.issues, m.cockpit.pick = false, 0
		return m
	}
	others, _ := m.others(t)
	m.cockpit.pick = max(0, min(m.cockpit.pick, len(pickable(others))-1))
	return m
}
