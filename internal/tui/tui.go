// Package tui is the live terminal view: the sessions by project, and by
// task where a project tracks its issues, every session again by priority
// at the bottom, the one under the cursor live in the tmux pane beside it,
// and the digests a key away.
package tui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"overview/internal/fleet"
	"overview/internal/issues"
	"overview/internal/review"
	"overview/internal/tmux"
	"overview/internal/vcs"
	"overview/internal/world"
)

const (
	refreshEvery = 2 * time.Second
	frameEvery   = 120 * time.Millisecond
	noteFor      = 6 * time.Second
	recentTurns  = 6
	// doubleClick is how close two clicks on a row must be to act on it.
	doubleClick = 400 * time.Millisecond
	// wheelLines is how far one wheel notch scrolls a pane.
	wheelLines = 3
)

type screen int

const (
	sessionsScreen screen = iota
	digestScreen
)

type (
	tickMsg  time.Time
	frameMsg time.Time
	// rowsMsg is a refresh: the sessions, where they run, the pinned
	// projects, and the roots of their directories. On an error only err
	// is set.
	rowsMsg struct {
		rows   []fleet.Row
		ttys   map[int]string
		hosts  map[int]world.Host
		pinned []string
		pinGen int // the model's pinsLanded when it was asked for
		roots  roots
		// written is when each row's transcript was last written, by path.
		written map[string]time.Time
		reviews map[string]review.Summary
		health  fleet.Health // the orchestrator's
		err     error
	}
	digestMsg struct {
		d   fleet.Digest
		err error
	}
	// noteMsg is something to tell the user.
	noteMsg string
	// doneMsg reports how an action on a session went.
	doneMsg struct {
		note string
		err  error
	}
)

type model struct {
	home fleet.Home
	llm  string
	// issues runs the issues CLI, nil when overview was started without one.
	// trackers holds what it said of each project, as the latest read of
	// each found it, and goes into every read so an unchanged project is
	// not read again. reading holds the roots whose read is out.
	// refreshes counts the refreshes that landed, and trackersAsked the
	// one the trackers were last asked for after.
	issues                   issues.Runner
	trackers                 trackers
	reading                  map[string]bool
	refreshes, trackersAsked int

	// in holds the sources' latest values, unjoined: the rows, ttys,
	// hosts, panes, pins, roots and trackers. Only setInputs changes it,
	// and each change gets a new generation, gen, which the World and the
	// list are memoized on. memo is shared by every copy of the model.
	in   world.Inputs
	gen  int
	memo *memo

	screen    screen
	sel       string // the key of the entry under the cursor, kept across refreshes
	listTop   int    // first line of the projects shown, kept so clicks do not shift it
	agentsTop int    // and of the agents
	filter    string
	filtering bool
	confirm   string // the key of a session to stop or a terminal to close, waiting on a yes
	verdict   verdictPrompt
	help      bool
	helpTop   int // the first line of the help shown, when it is taller than its pane
	frame     int

	digests      []string
	digestIdx    int
	digest       string
	digestAt     time.Time
	digesting    bool
	digestScroll int

	note   string
	noteAt time.Time
	// notePath is the directory note names, "" for none. It gives way
	// first when the footer is short, cut from its left, so the message
	// around it stays whole.
	notePath string
	// notePathAt is where notePath starts in note, in bytes.
	notePathAt int
	// adding is the add-project picker as it was when enter added from
	// it, reopened when the add fails so what was typed is not lost.
	adding picker
	// now is when the message being handled arrived. Update reads the
	// clock once, at the door, so nothing after it reads the time itself.
	now time.Time

	// The details are rebuilt only when what they show changes, not on
	// every spinner frame. A pointer, because the model is passed by value.
	cache *detailCache
	// transcripts keeps what the refreshes have read of the transcripts. A
	// pointer, because the model is passed by value.
	transcripts *fleet.Transcripts

	deps deps
	// cwd is where c starts a session when the list is empty.
	cwd string
	// user is the user's home directory, symlinks resolved.
	user string
	// turns holds the selected session's last turns, read in a Cmd, and
	// turnsAsked the transcript last asked for.
	turns      recent
	turnsAsked turnsKey
	// rowRoots holds the project roots of the latest refresh's
	// directories, and paneRoots those of the latest pane listing's.
	rowRoots, paneRoots roots

	// tmux, as stage.go describes. mux is nil outside tmux, and then
	// nothing is staged. The view runs in pane self of session. exe starts
	// the holder.
	mux            multiplexer
	self, session  string
	exe            string
	failed         string
	staging        bool
	spawningHolder bool
	holderShown    string
	// entering is the pane an action asked to move the keyboard into, ""
	// for none. It is cleared when the cursor moves or the move there
	// fails, so it never focuses a pane the user has since left.
	entering string
	// spawns counts the panes asked for, which all get options set, and
	// the list's resizes, and landed those whose Cmd has come back. A
	// listing is taken only when none is pending and it was asked for after
	// the latest one landed: any other may show a pane before its options
	// were set, or the list at a width it no longer has.
	spawns, landed int
	// width is the list's width, as keepWidth keeps it.
	width listWidth
	// pins counts the writes to the pinned projects asked for, and
	// pinsLanded those done. A refresh that read the file before the
	// latest write landed is not taken for the pins.
	pins, pinsLanded int
	// dead are the session's panes whose programs have exited, which
	// tmux keeps open until reap closes them: see tmux.HoldExits. The
	// rest of the view takes them as closed.
	dead []tmux.Pane
	// opened is the pane c started, shown until its session is listed.
	opened tmux.Pane
	// blurred is set while the focus is in another pane. The list then
	// stays where it is, and the cursor follows its window.
	blurred bool
	// panels is set while a client of the session is in the panels mode,
	// as nav.go describes: from the latest listing, and at once when the
	// view puts the client there. A key that reaches the view clears it.
	panels bool
	// framed is what the rules of the session's panes were last asked to
	// show, by pane, as frame.go describes. framing is set while that Cmd
	// is out, and frameStale after one failed, until a listing shows the
	// panes as they are now.
	framed              map[string]paneFrame
	framing, frameStale bool

	// The agents' pane, as agents.go describes. agentsSock is the socket
	// its program reaches the view on, "" when the view starts no such
	// pane. aw and ah are the pane's size as its program last said, 0
	// until it has. inAgents is set while the keyboard is in that pane,
	// and other, on a copy of the model that draws one of the two panes,
	// while it is in the other one. agentsBusy is set while a Cmd that
	// starts, sizes or closes the pane is out, and agentsRefused is the last height
	// asked of tmux for it, which is not asked twice running.
	// agentsShown is the screen last sent to the pane, and agentsSeq its
	// number.
	agentsSock    string
	aw, ah        int
	inAgents      bool
	other         bool
	agentsBusy    bool
	agentsRefused agentsAsk
	agentsShown   string
	agentsSeq     int

	// pick is the add-project picker, open while it takes the list pane.
	pick picker
	// taskPick is the task picker, open while it takes the list pane.
	taskPick taskPick

	// cockpit is the cockpit, as cockpit.go describes. zooming is set while
	// a Cmd that zooms the list or puts it back is out, and rezoom when
	// zoomWant changed meanwhile, to be sent when it lands. zoomPanes is
	// how many panes the list's window had when a listing last showed the
	// zoom as it should be, so zoomLost can tell the user leaving from a
	// pane going away. cockpitSoon is an o that came while the list was
	// moving, and opens the cockpit once the move lands.
	cockpit         cockpit
	zooming, rezoom bool
	zoomPanes       int
	cockpitSoon     bool
	// issueReads and changeReads are the cockpit's reads, by cockpitKey.
	issueReads  map[string]issueRead
	changeReads map[string]changesRead

	// reviewArgs are the flags every review is started with.
	reviewArgs []string
	// opening is the sessions whose review is being opened, so a second
	// v before the pane lands does not open another.
	opening map[string]bool

	// The last click on a list row, to tell a double click.
	clickID string
	clickAt time.Time

	w, h int
}

// View is what the view runs with, every part of it from its command line.
type View struct {
	Home  fleet.Home
	Model string // the model a digest is written with
	Theme Theme
	// Socket is the path of the tmux server's socket, and Session the tmux
	// session the view runs in.
	Socket, Session string
	User            string // the user's home directory
	Cwd             string // where overview was started
	// ThemeArg and ConfigDir are the --theme and --config the view was
	// started with, which a review is started with too.
	ThemeArg, ConfigDir string
	// Issues is the path of the issues binary, "" when there is none.
	Issues string
}

// Run opens the view and blocks until the user quits. It runs in the pane
// Launch made for it in v.Session, on the tmux server at v.Socket, and acts
// on no other server.
func Run(v View) error {
	useTheme(v.Theme)
	m := newModel(v.Home, v.Model)
	m.user, m.cwd = resolvePath(v.User), v.Cwd
	if v.Issues != "" {
		m.issues = issues.Exec(v.Issues)
	}
	m.reviewArgs = []string{"--theme", v.ThemeArg, "--config", v.ConfigDir, "--socket-path", v.Socket}
	socket, session := v.Socket, v.Session
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	srv := tmux.At(socket, "")
	self, ok, err := srv.Self(os.Getpid())
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("the view runs inside overview's tmux: start it with plain `overview`")
	}
	if err := srv.MarkList(self); err != nil {
		return err
	}
	m.mux, m.self, m.session, m.exe = srv, self, session, exe
	m.deps.copyText = tmux.CopyAll(copyText, srv.Copy)
	if err := srv.Frame(session, frameColours(v.Theme)); err != nil {
		return err
	}
	if err := srv.BindPanels(); err != nil {
		return err
	}
	if err := srv.HoldExits(self); err != nil {
		return err
	}
	focused, err := srv.Focused(self)
	if err != nil {
		return err
	}
	m.blurred = !focused
	panes, rs, err := readPanes(srv, m.deps.roots)
	if err != nil {
		return err
	}
	m = m.setPanes(panes, rs)
	// The agents' pane reaches the view here. A pane left by a view that
	// has just gone may connect before this one's program is made, so
	// what it passes on waits for that, and Send then waits for the
	// program to start. A view that cannot listen starts no such pane,
	// and draws the agents in the list's own.
	var p *tea.Program
	made := make(chan struct{})
	sock := agentsSocket(socket, session)
	if link, err := listenAgents(sock, func(msg tea.Msg) { <-made; p.Send(msg) }); err != nil {
		m = m.say("the agents have no pane of their own: " + err.Error())
	} else {
		defer link.close()
		m.agentsSock, m.deps.drawAgents = sock, link.draw
	}
	p = tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithReportFocus())
	close(made)
	_, err = p.Run()
	return err
}

func newModel(home fleet.Home, llm string) model {
	d := systemDeps()
	return model{home: home, llm: llm, cache: &detailCache{}, memo: &memo{}, transcripts: fleet.NewTranscripts(), deps: d, now: d.now()}
}

// deps are the model's ways out to the system. Each runs inside a Cmd,
// apart from now, which stamps input as it arrives. Tests replace them.
type deps struct {
	now      func() time.Time
	reveal   func(path string) error // brings an app to the front
	copyText func(text string) error // puts text on the clipboard
	psTable  func() ([]byte, error)
	readDirs func(dir string) ([]string, error)
	writeTTY func(tty string, b []byte) error
	// drawAgents sends the agents' pane the screen numbered seq, nil
	// when the view has no such pane.
	drawAgents func(seq int, screen string) error
	exists     func(path string) bool      // whether anything is at path
	resolve    func(path string) string    // path with its symlinks resolved
	modTime    func(path string) time.Time // when a file was last written, zero if unknown
	// recentTurns reads the last n turns of a transcript.
	recentTurns func(path string, n int) []fleet.Turn
	// reviews reads the reviews of rows' sessions, by session ID.
	reviews func(home fleet.Home, rows []fleet.Row) map[string]review.Summary
	glob    func(pattern string) []string // the paths that match pattern
	// readFile reads a file whole.
	readFile func(path string) ([]byte, error)
	// vcs runs git and jj, and untracked reads a file git does not track,
	// for the cockpit's changes.
	vcs       vcs.Runner
	untracked func(path string) (vcs.Untracked, error)
	// derive joins the inputs into a World. It is world.Derive, which is
	// pure; tests count its calls.
	derive func(world.Inputs) world.World
}

// roots finds the project roots of dirs.
func (d deps) roots(dirs ...string) roots { return findRoots(d.exists, d.resolve, dirs...) }

func systemDeps() deps {
	return deps{now: time.Now, reveal: openApp, copyText: copyText, psTable: psTable, readDirs: readDirs, writeTTY: writeTTY,
		exists: exists, resolve: resolvePath, modTime: modTime, recentTurns: fleet.RecentTurns, reviews: readReviews,
		glob: glob, readFile: os.ReadFile, vcs: vcs.Exec, untracked: vcs.ReadUntracked, derive: world.Derive}
}

func glob(pattern string) []string {
	out, _ := filepath.Glob(pattern)
	return out
}

func modTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func openApp(path string) error { return exec.Command("open", path).Run() }

// remote says whether the user types from another machine: whether the
// tmux client they typed in last runs over ssh or mosh. An app opens on
// this machine's screen, so it cannot reach them. Outside tmux, or when
// tmux or ps cannot say, they are taken to be here. It runs inside a Cmd.
func remote(mux multiplexer, ps func() ([]byte, error)) bool {
	if mux == nil {
		return false
	}
	pid, err := mux.LatestClient()
	if err != nil || pid == 0 {
		return false
	}
	out, err := ps()
	return err == nil && overNetwork(parseProcs(string(out)), pid)
}

type (
	// digestsMsg is the list of digest files. newest asks to show the
	// newest whatever the reader was on.
	digestsMsg struct {
		files  []string
		newest bool
	}
	// digestReadMsg is digest idx, read from path.
	digestReadMsg struct {
		idx        int
		path, text string
		at         time.Time
		err        error
	}
)

// listDigests reads the list of digest files.
func (m model) listDigests(newest bool) tea.Cmd {
	home := m.home
	return func() tea.Msg { return digestsMsg{home.DigestFiles(), newest} }
}

// showDigest reads digest i to show it, when there is one.
func (m model) showDigest(i int) tea.Cmd {
	if i < 0 || i >= len(m.digests) {
		return nil
	}
	path := m.digests[i]
	return func() tea.Msg {
		b, err := os.ReadFile(path)
		msg := digestReadMsg{idx: i, path: path, text: string(b), err: err}
		if info, serr := os.Stat(path); err == nil && serr == nil {
			msg.at = info.ModTime()
		}
		return msg
	}
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.refresh, tick(), nextFrame(), m.listDigests(true)}
	if m.mux != nil {
		// The first listing also starts a holder when there is none.
		cmds = append(cmds, listPanes(m.mux, m.deps.roots, m.landed), panesEveryTick())
	}
	return tea.Batch(cmds...)
}

func tick() tea.Cmd {
	return tea.Tick(refreshEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func nextFrame() tea.Cmd {
	return tea.Tick(frameEvery, func(t time.Time) tea.Msg { return frameMsg(t) })
}

func (m model) refresh() tea.Msg {
	rows, err := m.home.Rows(m.transcripts)
	if err != nil {
		return rowsMsg{err: err}
	}
	out, _ := m.deps.psTable()
	ttys, hosts := places(parseProcs(string(out)), rows)
	return m.rowsMsg(rows, ttys, hosts, m.home.Projects())
}

// rowsMsg is a refresh of rows, with the roots of the directories it
// names: the sessions', their task refs', the pinned projects' and the one
// overview was started in. It reads the disk, so it runs in a Cmd.
func (m model) rowsMsg(rows []fleet.Row, ttys map[int]string, hosts map[int]world.Host, pinned []string) rowsMsg {
	dirs := append(slices.Clone(pinned), m.cwd)
	for _, r := range rows {
		dirs = append(dirs, r.Cwd)
		for _, ref := range r.Tasks {
			dirs = append(dirs, ref.Dir)
		}
	}
	written := map[string]time.Time{}
	for _, r := range rows {
		if r.Transcript != "" {
			written[r.Transcript] = m.deps.modTime(r.Transcript)
		}
	}
	rs := m.deps.roots(dirs...)
	return rowsMsg{rows, ttys, hosts, pinned, m.pinsLanded, rs, written, m.deps.reviews(m.home, rows), m.home.HealthOf(rows), nil}
}

// landRows takes a refresh. One that failed keeps what the last one
// found. The pins it read are taken only when no write to them is out or
// landed since it read them.
func (m model) landRows(msg rowsMsg) model {
	if msg.err != nil {
		return m.setInputs(func(in *world.Inputs) { in.RefreshErr = msg.err })
	}
	m.rowRoots = msg.roots
	pins := m.pins == m.pinsLanded && msg.pinGen == m.pinsLanded
	m = m.setInputs(func(in *world.Inputs) {
		in.Rows, in.TTYs, in.Hosts = msg.rows, msg.ttys, msg.hosts
		in.Written, in.Loaded, in.RefreshErr = msg.written, true, nil
		in.Reviews, in.Orchestrator = msg.reviews, msg.health
		if pins {
			in.Pinned = msg.pinned
		}
	})
	m.refreshes++
	// Keep the cursor on a visible row after sessions come and go.
	return m.adopt().dropTrackers()
}

// turnsKey names a transcript as it was when written at a moment.
type turnsKey struct {
	path string
	at   time.Time
}

// recent is a transcript's last turns, as read when it was written at
// key.at.
type recent struct {
	key   turnsKey
	turns []fleet.Turn
}

// turnsMsg answers askTurns.
type turnsMsg struct{ recent }

// askTurns asks for the selected session's last turns, which the holder
// shows, when the model has none for its transcript as the latest refresh
// found it and has not asked already.
func (m model) askTurns() (model, tea.Cmd) {
	e, _, ok := m.selected(m.ordered())
	if !ok || e.kind != sessionEntry || e.sess.Transcript == "" {
		return m, nil
	}
	k := turnsKey{e.sess.Transcript, e.sess.Written}
	if k == m.turns.key || k == m.turnsAsked {
		return m, nil
	}
	m.turnsAsked = k
	read := m.deps.recentTurns
	return m, func() tea.Msg { return turnsMsg{recent{k, read(k.path, recentTurns)}} }
}

// turnsOf is the last turns of a transcript, if the model has read them.
func (m model) turnsOf(path string) []fleet.Turn {
	if path == "" || m.turns.key.path != path {
		return nil
	}
	return m.turns.turns
}

func (m model) runDigest() tea.Msg {
	d, err := m.home.RunDigest(m.llm)
	return digestMsg{d, err}
}

func (m model) say(note string) model {
	m.note, m.noteAt, m.notePath = note, m.now, ""
	return m
}

// sayAbout is say for the note before+path+after, which names the
// directory path, the part that gives way first when the footer is short.
func (m model) sayAbout(before, path, after string) model {
	m = m.say(before + path + after)
	m.notePath, m.notePathAt = path, len(before)
	return m
}

// ordered is the list flattened in the order the cursor walks it.
func (m model) ordered() []entry { return flatten(m.sections()) }

// selected returns the entry under the cursor and its place in es, falling
// back to the first entry when the selected one has gone.
func (m model) selected(es []entry) (entry, int, bool) {
	for i, e := range es {
		if e.key() == m.sel {
			return e, i, true
		}
	}
	if len(es) == 0 {
		return entry{}, -1, false
	}
	i := first(es)
	return es[i], i, true
}

// first is where the cursor starts in es: the first entry it lands on,
// or the first entry when it lands on none.
func first(es []entry) int {
	return max(slices.IndexFunc(es, landing), 0)
}

// landing says whether the cursor lands on e when it is put somewhere
// rather than moved there: anything but the heading of a project with
// something under it, which j and k and a click still reach.
func landing(e entry) bool { return e.kind != projectEntry || e.proj.Empty() }

// printable cleans typed or pasted filter text: tabs become spaces and other
// control characters, which would have no width and break the layout, go.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
}

// keepSel puts the cursor where it starts, as first has it, if it is on
// none that is shown.
func (m model) keepSel() model {
	es := m.ordered()
	if len(es) == 0 || m.shown(m.sel) {
		return m
	}
	m.sel, m.confirm = es[first(es)].key(), ""
	return m
}

// moveTo puts the cursor on the ith entry. Moving it also stops showing a
// pane c started, so the stage follows the cursor again.
func (m model) moveTo(es []entry, i int) model {
	if len(es) == 0 {
		return m
	}
	i = max(0, min(i, len(es)-1))
	if k := es[i].key(); k != m.sel {
		m.sel, m.confirm, m.entering = k, "", ""
		m = m.setOpened(tmux.Pane{})
	}
	return m
}

// stuck is where the cursor goes when the session it was on had a pane in
// window win and has none there now: to that window's terminal, if the
// window is listed as one. Claude may have exited and left a split behind,
// and the user may be typing in it, so the list must not hop to another
// window. ok is false when the cursor should stay where it is.
func stuck(win string, stillThere bool, es []entry) (string, bool) {
	if win == "" || stillThere {
		return "", false
	}
	for _, e := range es {
		if e.kind == terminalEntry && e.term.Window == win {
			return e.key(), true
		}
	}
	return "", false
}

// nearest is where the cursor goes when the entry it was on, sel, has gone:
// to the first entry after sel in the old order that is still listed, or
// else to the last one before it. ok is false when sel was not in the old
// order, or nothing from it is left.
func nearest(old []string, sel string, listed func(string) bool) (string, bool) {
	i := slices.Index(old, sel)
	if i < 0 {
		return "", false
	}
	for _, k := range old[i+1:] {
		if listed(k) {
			return k, true
		}
	}
	for j := i - 1; j >= 0; j-- {
		if listed(old[j]) {
			return old[j], true
		}
	}
	return "", false
}

// keepNear applies nearKey across one update, from the model before it,
// when the entry under the cursor is no longer shown, and moves it off
// the heading of a pinned project that had nothing in it and has now, to
// what started there.
func (m model) keepNear(before model) model {
	es := m.ordered()
	if m.shown(m.sel) {
		if key, ok := filled(before.ordered(), es, m.sel); ok {
			m.sel, m.confirm = key, ""
		}
		return m
	}
	if key, ok := nearKey(before.ordered(), es, m.sel); ok {
		m.sel, m.confirm = key, ""
	}
	return m
}

// filled is the first entry under the heading sel in now when that
// heading's project was empty in old and is not in now. ok is false
// otherwise.
func filled(old, now []entry, sel string) (string, bool) {
	at := func(es []entry) int { return slices.IndexFunc(es, func(e entry) bool { return e.key() == sel }) }
	i, j := at(old), at(now)
	if i < 0 || j < 0 || old[i].kind != projectEntry || !old[i].proj.Empty() || now[j].proj.Empty() || j+1 >= len(now) {
		return "", false
	}
	return now[j+1].key(), true
}

// nearKey is where the cursor goes when sel, the key of an entry in old,
// is not in now. A session's entry that has gone goes to another entry
// of the same session, as samePlace finds it, and a workstream that has
// gone to the first session under it that is still listed, so the cursor
// stays with the session it was on. Otherwise it stays in its project,
// as sameProject finds, and else it is nearest: a cursor on an entry of
// its own stays among those, out of the agents list, while there are
// any. It lands on a heading only when nothing else is left.
func nearKey(old, now []entry, sel string) (string, bool) {
	listed, stops := map[string]bool{}, map[string]bool{}
	for _, e := range now {
		listed[e.key()], stops[e.key()] = true, landing(e)
	}
	if key, ok := samePlace(old, now, sel); ok {
		return key, true
	}
	if key, ok := sameProject(old, now, sel); ok {
		return key, true
	}
	var keys []string
	alias := map[string]bool{}
	for _, e := range old {
		keys = append(keys, e.key())
		alias[e.key()] = e.alias != ""
	}
	for _, ok := range []func(string) bool{
		func(k string) bool { return stops[k] && (alias[sel] || !alias[k]) },
		func(k string) bool { return stops[k] },
		func(k string) bool { return listed[k] },
	} {
		if key, found := nearest(keys, sel, ok); found {
			return key, true
		}
	}
	return "", false
}

// sameProject is where the cursor goes when sel, an entry of old in a
// project's section, is not in now: to the nearest entry of old in that
// project still listed, after it and then before it, or else to the
// first entry now listed in the project, its heading last. So the last
// terminal of a pinned project leaves the cursor on its heading. An
// agents alias has no section to stay in.
func sameProject(old, now []entry, sel string) (string, bool) {
	i := slices.IndexFunc(old, func(e entry) bool { return e.key() == sel })
	if i < 0 || old[i].alias == agentsPrefix {
		return "", false
	}
	name := old[i].projectName()
	if name == "" {
		return "", false
	}
	in := func(e entry) bool { return e.alias != agentsPrefix && e.projectName() == name }
	listed, stops := map[string]bool{}, map[string]bool{}
	for _, e := range now {
		if in(e) {
			listed[e.key()], stops[e.key()] = true, landing(e)
		}
	}
	var keys []string
	for _, e := range old {
		keys = append(keys, e.key())
	}
	for _, ok := range []func(string) bool{
		func(k string) bool { return stops[k] },
		func(k string) bool { return listed[k] },
	} {
		if key, found := nearest(keys, sel, ok); found {
			return key, true
		}
	}
	for _, ok := range []func(entry) bool{func(e entry) bool { return in(e) && landing(e) }, in} {
		if i := slices.IndexFunc(now, ok); i >= 0 {
			return now[i].key(), true
		}
	}
	return "", false
}

// samePlace is the entry in now for the session the entry sel in old
// showed: the one in the same place, under the same workstream or in the
// agents list, if there is one, else its primary entry, else any. For a
// workstream sel it is the primary entry of the first session that was
// under it and is still listed. ok is false when there is none, or sel
// is neither.
func samePlace(old, now []entry, sel string) (string, bool) {
	i := slices.IndexFunc(old, func(e entry) bool { return e.key() == sel })
	if i < 0 {
		return "", false
	}
	was := old[i]
	if was.kind == workstreamEntry {
		for _, c := range children(old, was.task()) {
			if c.kind != sessionEntry {
				continue
			}
			for _, e := range now {
				if e.kind == sessionEntry && e.alias == "" && e.sess.SessionID == c.sess.SessionID {
					return e.key(), true
				}
			}
		}
		return "", false
	}
	if was.kind != sessionEntry {
		return "", false
	}
	agents := was.alias == agentsPrefix
	primary, other := "", ""
	for _, e := range now {
		if e.kind != sessionEntry || e.sess.SessionID != was.sess.SessionID {
			continue
		}
		switch {
		case (e.alias == agentsPrefix) == agents && (agents || e.stream == was.stream):
			return e.key(), true
		case e.alias == "" && primary == "":
			primary = e.key()
		case other == "":
			other = e.key()
		}
	}
	if primary == "" {
		primary = other
	}
	return primary, primary != ""
}

// stick applies stuck across one update, from the model before it.
func (m model) stick(before model) model {
	e, _, ok := before.selected(before.ordered())
	s, isSession := e.session()
	if !ok || !isSession {
		return m
	}
	was, _ := before.pane(s.Pane)
	now := tmux.Pane{}
	if s := m.world().Sessions[s.SessionID]; s != nil {
		now, _ = m.pane(s.Pane)
	}
	if key, ok := stuck(was.Window, now.Window == was.Window, m.ordered()); ok {
		m.sel = key
	}
	return m
}

// keepCursor puts the cursor where it belongs once rows and panes have
// come and gone. While the keyboard is outside the list, the cursor
// follows the list's window, which stays where the user is typing: it
// goes to an entry that is there, if one is. Otherwise, as while the list
// has the keyboard, it stays on a shown entry, near where it was, and in
// the window it was in when its session ended there.
func (m model) keepCursor(before model) model {
	if m.blurred {
		// While the list is on its way to another window, the one it is
		// in says nothing of where the cursor belongs. tmux can report
		// the keyboard gone and back in the middle of such a move.
		if m.staging {
			return m
		}
		if key, ok := follow(m.placed(m.ordered()), m.windowOf(m.self), m.sel); ok && key != m.sel {
			m.sel, m.confirm = key, ""
		}
		// With no entry there, the cursor stays as it is, even on an entry
		// that is not shown, so nothing moves when the keyboard comes back.
		return m
	}
	return m.stick(before).keepNear(before).keepSel()
}

// unblur is the model with the keyboard back in the list. A search that
// hid the cursor's entry while the user was elsewhere is cleared, so the
// cursor and the list stay where they were.
func (m model) unblur() model {
	if !m.blurred {
		return m
	}
	m.blurred = false
	if !m.shown(m.sel) && m.filter != "" {
		all := m
		all.filter = ""
		if all.shown(m.sel) {
			m.filter, m.filtering = "", false
		}
	}
	return m
}

// needy says whether n stops at an entry: a session's primary entry when
// the session needs you, or a workstream in review.
func needy(e entry) bool {
	if e.kind == workstreamEntry {
		return e.stream.Issue.Status == issues.Review
	}
	r, ok := e.session()
	return ok && e.alias == "" && r.NeedsYou()
}

// place is an entry's key and the window its pane is in, "" when it has
// no pane on this server. alias marks an entry that shows another's pane:
// a session's entry that is not its primary one, or a workstream.
type place struct {
	key, window string
	alias       bool
}

// placed is where each entry's pane is.
func (m model) placed(es []entry) []place {
	out := make([]place, len(es))
	for i, e := range es {
		out[i].key, out[i].alias = e.key(), e.alias != "" || e.kind == workstreamEntry
		if e.kind == terminalEntry {
			out[i].window = e.term.Window
		} else if p, ok := m.pane(entryPane(es, e)); ok {
			out[i].window = p.Window
		}
	}
	return out
}

// follow is the entry the cursor takes while the keyboard is outside the
// list: one whose pane is in the list's window, stage, preferring sel when
// it is one of them, and else the first that is no alias. ok is false
// when none is there.
func follow(ps []place, stage, sel string) (string, bool) {
	if stage == "" {
		return "", false
	}
	found, alias := "", ""
	for _, p := range ps {
		if p.window != stage {
			continue
		}
		switch {
		case p.key == sel:
			return sel, true
		case p.alias && alias == "":
			alias = p.key
		case !p.alias && found == "":
			found = p.key
		}
	}
	if found == "" {
		found = alias
	}
	return found, found != ""
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// The time is read here, at the door, so the steps after it are pure
	// in it: a note, a click and a tracker read are all stamped with it.
	m.now = m.deps.now()
	// reduce: the message changes the inputs or the UI state. The World is
	// derived when settle first asks for it, once per generation.
	before := m
	next, cmd := m.update(msg)
	m = next.(model).settle(before)
	m, effects := m.effects()
	return m, tea.Batch(cmd, effects)
}

// settle makes the UI state valid for the list as it is now: the cursor
// on an entry that is shown, the list scrolled only as far as the cursor
// needs, and the issues card's cursor on an issue it lists.
func (m model) settle(before model) model {
	m = m.keepCursor(before)
	switch {
	case m.w > 0 && m.screen == sessionsScreen:
		col := m.shownColumn()
		m.listTop, m.agentsTop = col.projTop, col.agentsTop
	case m.apart():
		// The agents' pane shows them whatever the list's pane shows.
		m.agentsTop = m.shownColumn().agentsTop
	}
	if m.help {
		m.helpTop = max(0, min(m.helpTop, m.helpMax()))
	}
	return m.keepCockpit()
}

// effects compares what tmux and the reads should be with what is in
// flight, and sends the differences as Cmds: the list's place and the
// holder, the selected session's turns, the cockpit's and the task
// picker's reads, the trackers after a refresh, the panes' rules, and
// the agents' pane's screen.
func (m model) effects() (model, tea.Cmd) {
	m, stage := m.restage()
	m, turns := m.askTurns()
	m, reads := m.cockpitReads()
	m, taskRead := m.taskReads()
	m, trackers := m.readTrackers()
	m, frame := m.reframe()
	m, agents := m.redrawAgents()
	return m, tea.Batch(stage, turns, reads, taskRead, trackers, frame, agents)
}

func (m model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tea.FocusMsg:
		return m.handle(do(keysBack))
	case tea.BlurMsg:
		return m.handle(do(keysLeft))
	case panesTick, panesMsg, stagedMsg, spawnedMsg, closedMsg, reviewOpenedMsg, widthMsg, zoomedMsg, framedMsg, panelsMsg:
		return m.stageUpdate(msg)
	case agentsOpenedMsg, agentsKeptMsg:
		return m.agentsLanded(msg)
	case agentsMsg:
		return m.fromAgents(msg.msg)
	case agentsGone:
		return m.lostAgents(), nil
	case tea.KeyMsg:
		// A key that reaches the list proves the keyboard is back, whether
		// or not tmux said so, since a focus event can go missing. It also
		// proves the client is out of the panels mode, which keeps every
		// key for itself.
		m.panels = false
		return m.input(action{do: keysBack}, true, func(m model) []action { return m.keyActions(msg) })
	case tea.MouseMsg:
		// tmux's panels mode does not bind the mouse, so a mouse event
		// that reaches the view has taken the client out of it.
		m.panels = false
		// The mouse proves the user is at the list, but a wheel over it
		// leaves the keyboard where it was. A click is stamped with the
		// time Update read at the door, so apply stays pure.
		at := m.now
		wheel := msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown
		return m.input(action{do: pointerHere}, !wheel, func(m model) []action { return m.mouseActions(msg, at, false) })
	case noteMsg:
		m = m.say(string(msg))
	case frameMsg:
		m.frame++
		return m, nextFrame()
	case tickMsg:
		if !m.noteAt.IsZero() && time.Time(msg).Sub(m.noteAt) > noteFor {
			m.note = ""
		}
		return m, tea.Batch(m.refresh, tick(), m.listDigests(false))
	case digestsMsg:
		// The orchestrator publishes digests on its own schedule. Follow the
		// newest one unless the reader has gone back to an older one.
		if len(msg.files) == len(m.digests) && !msg.newest {
			return m, nil
		}
		atNewest := msg.newest || len(m.digests) == 0 || m.digestIdx == len(m.digests)-1
		m.digests = msg.files
		if atNewest {
			return m, m.showDigest(len(msg.files) - 1)
		}
	case digestReadMsg:
		if msg.err == nil && msg.idx < len(m.digests) && m.digests[msg.idx] == msg.path {
			m.digestIdx, m.digest, m.digestAt, m.digestScroll = msg.idx, msg.text, msg.at, 0
		}
	case turnsMsg:
		m.turns = msg.recent
	case issueReadMsg, changesReadMsg:
		m = m.landRead(msg)
	case rowsMsg:
		return m.landRows(msg), nil
	case trackerMsg:
		m = m.landTracker(msg)
	case verdictMsg:
		return m.landVerdict(msg)
	case digestMsg:
		m.digesting = false
		switch {
		case msg.err != nil:
			m = m.say("digest failed: " + msg.err.Error())
		case msg.d.Quiet:
			m = m.say("no activity since " + msg.d.Since.Local().Format("15:04"))
		default:
			return m.say("new digest written"), m.listDigests(true)
		}
	case pickedMsg:
		if m.pick.open && msg.source == m.pick.from {
			m.pick.paths, m.pick.read = msg.paths, true
			if m.pick.sel >= 0 {
				m.pick.sel = max(0, min(m.pick.sel, len(m.pickMatches())-1))
			}
		}
	case pinnedMsg:
		m.pinsLanded++
		switch {
		case msg.err != nil && msg.unpin:
			return m.sayAbout("could not unpin ", m.name(msg.root), ": "+pathless(msg.err)), nil
		case msg.err != nil:
			// The picker comes back as it was, so the path can be fixed.
			if !m.pick.open && m.adding.open {
				m.pick = m.adding
			}
			m.adding = picker{}
			where := m.short(msg.root)
			return m.sayAbout("could not add ", where, ": "+pathless(msg.err)), nil
		case msg.unpin:
			m = m.setInputs(func(in *world.Inputs) {
				in.Pinned = slices.DeleteFunc(slices.Clone(in.Pinned), func(r string) bool { return r == msg.root })
			})
			return m.sayAbout("unpinned ", m.name(msg.root), ""), nil
		}
		m.adding = picker{}
		m = m.setInputs(func(in *world.Inputs) {
			if !slices.Contains(in.Pinned, msg.root) {
				in.Pinned = slices.Concat(in.Pinned, []string{msg.root})
			}
		})
		return m.toProject(msg.root).sayAbout("pinned ", m.name(msg.root), ""), nil
	case doneMsg:
		if msg.err != nil {
			m = m.say(msg.err.Error())
		} else if msg.note != "" {
			m = m.say(msg.note)
		}
		return m, m.refresh
	}
	return m, nil
}

// input applies arrival, what an input says of where the user is, and
// then the actions translate makes of the input for the model that
// results, in order. A key or a click that means something has read the
// note, so heard clears it first. The wheel only looks, so read is false
// for it and the note stays.
func (m model) input(arrival action, read bool, translate func(model) []action) (tea.Model, tea.Cmd) {
	m, first := m.apply(arrival)
	as := translate(m)
	if read && len(as) > 0 {
		as = append([]action{{do: heard}}, as...)
	}
	next, rest := m.handle(as)
	return next, tea.Batch(first, rest)
}

// handle applies actions in order.
func (m model) handle(actions []action) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	for _, a := range actions {
		var cmd tea.Cmd
		m, cmd = m.apply(a)
		cmds = append(cmds, cmd)
	}
	// Whatever happened, the cursor stays on a row that is shown.
	return m.keepSel(), tea.Batch(cmds...)
}

// apply carries out one action. It is the only place input changes the
// model, and it runs nothing itself: any work comes back as a Cmd.
func (m model) apply(a action) (model, tea.Cmd) {
	// An o waiting for the list's move is cancelled by what would close
	// an open cockpit, by esc, and by another o.
	if m.cockpitSoon && (closesCockpit(a.do) || a.do == clearFilter || a.do == toCockpit || a.do == outCockpit || a.do == enterPanels) {
		m.cockpitSoon = false
		if a.do == toCockpit {
			return m, nil
		}
	}
	// enter on an idle workstream opens the cockpit, so it stays open.
	if m.cockpit.open && closesCockpit(a.do) && (a.do != open || !m.idleSelected()) {
		m, out := m.closeCockpit()
		m, cmd := m.apply(a)
		return m, tea.Batch(out, cmd)
	}
	es := m.ordered()
	e, i, ok := m.selected(es)
	row, isSession := e.session()
	switch a.do {
	case heard:
		m.note = ""
	case keysBack, pointerHere:
		// n says which of the view's panes: 1 for the agents', 0 for the
		// list's. The cursor goes to that pane's side of the list.
		m = m.unblur()
		if a.do == keysBack {
			m.inAgents = a.n == 1
		}
		return m.cross(a.n == 1), nil
	case keysLeft:
		// The pane the keyboard went to may have said so first, when it
		// is the view's other pane, and then this one is old news.
		if (a.n == 1) == m.inAgents {
			m.blurred, m.inAgents = true, false
		}
	case detach:
		if m.mux == nil {
			return m, tea.Quit
		}
		mux := m.mux
		return m, func() tea.Msg {
			if err := mux.Detach(); err != nil {
				return noteMsg(err.Error())
			}
			return nil
		}
	case openHelp:
		m.help, m.helpTop = true, 0
	case scrollHelp, scrollHelpTo:
		if a.do == scrollHelp {
			a.n += m.helpTop
		}
		m.helpTop = max(0, min(a.n, m.helpMax()))
	case dismiss:
		if m.confirm != "" {
			unpin := strings.HasPrefix(m.confirm, "project:")
			m.confirm = ""
			if unpin {
				return m.say("left it pinned"), nil
			}
			return m.say("left it running"), nil
		}
		m.help = false
	case confirmStop:
		key := m.confirm
		m.confirm = ""
		for _, c := range es {
			if c.key() != key {
				continue
			}
			if c.kind == terminalEntry && m.mux != nil {
				return m.say("closing " + c.term.Title + "…"), m.closeTerminal(c.term)
			}
			if c.kind == projectEntry {
				m.pins++
				home, root := m.home, c.pin()
				return m, func() tea.Msg { return pinnedMsg{root: root, err: home.UnpinProject(root), unpin: true} }
			}
		}
		if s := m.world().Sessions[key]; s != nil {
			home, l := m.home, s.Live
			return m.say("stopping " + s.Name + "…"), func() tea.Msg {
				return doneMsg{"stopped " + l.Name, home.Stop(l)}
			}
		}
		return m.say("left it running"), nil
	case showScreen:
		m.screen = screen(a.n)
	case toggleScreen:
		m.screen = 1 - m.screen
	case writeDigest:
		if m.digesting {
			return m, nil
		}
		m.digesting = true
		return m, m.runDigest // the footer says so while it runs
	case refresh:
		return m, m.refresh
	case move, moveTo:
		// The cursor stays on its side of the list while the agents have
		// a pane of their own.
		lo, hi := side(es, i, m.apart())
		if hi == lo {
			return m, nil
		}
		to := i + a.n
		if a.do == moveTo {
			to = lo + a.n
			// g goes where the cursor starts, under the top project's
			// rule.
			if a.n == 0 {
				to = lo + first(es[lo:hi])
			}
		}
		return m.moveTo(es, max(lo, min(to, hi-1))), nil
	case nextNeedy:
		// Wraps round the list, or the cursor's side of it.
		lo, hi := side(es, i, m.apart())
		stops := needy
		if lo > 0 {
			stops = needs
		}
		for d, n := 1, hi-lo; d <= n; d++ {
			j := lo + ((i-lo+a.n*d)%n+n)%n
			if stops(es[j]) {
				return m.moveTo(es, j), nil
			}
		}
		return m.say("nothing needs you"), nil
	case scrollDigest, scrollDigestTo:
		if a.do == scrollDigest {
			a.n += m.digestScroll
		}
		m.digestScroll = max(0, min(a.n, len(m.digestLines())-m.listHeight()))
	case stepDigest:
		return m, m.showDigest(m.digestIdx + a.n)
	case search:
		m.filtering = true
	case typeText, backspace:
		before := m.filter
		if a.do == typeText {
			m.filter += a.text
		} else if r := []rune(m.filter); len(r) > 0 {
			m.filter = string(r[:len(r)-1])
		}
		// New search text puts the cursor on the first match, among the
		// agents when it is typed in their pane and one of them matches.
		if m.filter != before {
			agents := m.inAgents && m.apart()
			es := m.ordered()
			to := first(es)
			if j := slices.IndexFunc(es, func(e entry) bool { return e.alias == agentsPrefix }); agents && j >= 0 {
				to = j
			}
			return m.moveTo(es, to), nil
		}
	case cancelSearch:
		m.filter, m.filtering = "", false
	case clearFilter:
		m.filter = ""
	case open:
		// What enter does is decided on the list as the user sees it, as
		// the footer's hint is, before a search is cleared.
		var k enterKind
		if ok {
			e, k = m.enterOn(e)
		}
		if m.filtering {
			// Act on the match under the cursor, then show everything
			// again so coming back finds the full list.
			m.filtering = false
			if ok {
				m.filter = ""
			}
		}
		if !ok {
			return m, nil
		}
		return m.carry(e, k)
	case click:
		double := a.id == m.clickID && a.at.Sub(m.clickAt) < doubleClick
		m.clickID, m.clickAt = a.id, a.at
		for j, c := range es {
			if c.key() == a.id {
				m = m.moveTo(es, j)
			}
		}
		// A click puts the keys in the list.
		m.cockpit.issues = false
		if double {
			m.clickID = ""
			return m.apply(action{do: open})
		}
	case stop:
		switch {
		case ok && e.kind == terminalEntry:
			m.confirm = e.key()
		case ok && e.kind == projectEntry && e.pin() != "":
			m.confirm = e.key()
		case ok && e.kind == projectEntry:
			return m.say("x unpins a pinned project, and " + e.proj.Name + " is not pinned"), nil
		case ok && e.kind == workstreamEntry:
			return m.say(m.moveToSay(e, "x stops a background session or closes a terminal")), nil
		case !isSession:
			return m.say("nothing to stop"), nil
		default:
			if row.Host != nil {
				return m.say(row.Name + " belongs to " + row.Host.Label() + ". Close it there."), nil
			}
			if !row.Background() {
				return m.say("only background sessions can be stopped from here"), nil
			}
			m.confirm = row.SessionID
		}
	case newSession:
		if ok && e.stream != nil {
			return m.startOn(e.stream.Root, e.stream.Issue)
		}
		return m.start(m.rootOf(e, ok))
	case newTerminal:
		task := ""
		if ok {
			task = e.task()
		}
		return m.openTerminal(m.rootOf(e, ok), task)
	case openReview:
		if ok && e.kind == workstreamEntry {
			return m.say(m.moveToSay(e, "v reviews a session's changes")), nil
		}
		if !isSession {
			return m.say("v reviews a session's changes: move to a session"), nil
		}
		return m.openReview(row)
	case addProject:
		m.pick = picker{open: true}
		return m.readPicks()
	case pickType, pickBack:
		q := m.pick.query
		if a.do == pickType {
			q += a.text
		} else if r := []rune(q); len(r) > 0 {
			q = string(r[:len(r)-1])
		}
		return m.setPickQuery(q)
	case pickMove:
		n := len(m.pickMatches())
		m.pick.sel = max(0, min(m.pick.sel+a.n, n-1))
	case pickTab:
		if ms, i := m.pickMatches(), max(m.pick.sel, 0); i < len(ms) {
			return m.setPickQuery(pickComplete(m.pick.query, m.user, ms[i]))
		}
	case pickEnter:
		path, typed := pickTarget(m.pick.query, m.user, m.pickMatches(), m.pick.sel)
		if path == "" {
			return m.say("nothing to add"), nil
		}
		m.pick, m.adding, m.pins = picker{}, m.pick, m.pins+1
		return m.sayAbout("adding ", m.short(path), "…"), pinCmd(m.deps.readDirs, m.home.PinProject, path, typed, m.deps.resolve, m.deps.exists)
	case pickCancel:
		m.pick = picker{}
	case startTask:
		return m.openTasks(m.rootOf(e, ok))
	case taskType, taskBack:
		q := m.taskPick.query
		if a.do == taskType {
			q += a.text
		} else if r := []rune(q); len(r) > 0 {
			q = string(r[:len(r)-1])
		}
		m.taskPick.query, m.taskPick.sel, m.taskPick.top = q, 0, 0
	case taskMove:
		n := len(m.taskList().rows)
		m.taskPick.sel, m.taskPick.top = max(0, min(m.taskPick.sel+a.n, n-1)), 0
	case taskScroll:
		_, body, _ := m.taskDetailLines(cardInner(m.w - m.taskSplit() - 1))
		room := m.listHeight() - 1
		m.taskPick.top = taskDetailTop(taskDetailTop(m.taskPick.top, len(body), room)+a.n, len(body), room)
	case taskEnter:
		return m.taskEnter()
	case taskCancel:
		m.taskPick = taskPick{}
		return m.syncZoom()
	case taskClick:
		id := "taskpick:" + strconv.Itoa(a.n)
		double := id == m.clickID && a.at.Sub(m.clickAt) < doubleClick
		m.clickID, m.clickAt = id, a.at
		if m.taskPick.sel != a.n {
			m.taskPick.sel, m.taskPick.top = a.n, 0
		}
		if double {
			m.clickID = ""
			return m.taskEnter()
		}
	case toCockpit:
		return m.openCockpit()
	case outCockpit:
		return m.closeCockpit()
	case cockpitTab:
		if into := a.n > 0 || (a.n == 0 && !m.inIssues()); !into {
			m.cockpit.issues = false
			return m, nil
		}
		if _, tracked := m.world().Tracker(m.target().root); !tracked {
			return m.say("this project does not track its issues"), nil
		}
		if !m.issuesShown() {
			return m.say("the window has no room for the issues card"), nil
		}
		m.cockpit.issues = true
	case enterPanels:
		if m.mux == nil {
			return m, nil
		}
		m.panels = true
		m.spawns++ // listings wait for it, as they do for a spawn: see model.spawns
		mux := m.mux
		return m, func() tea.Msg { return panelsMsg{mux.EnterPanels()} }
	case cockpitMove:
		others, _ := m.others(m.target())
		m.cockpit.pick = max(0, min(m.cockpit.pick+a.n, len(pickable(others))-1))
	case cockpitPick:
		return m.startPicked()
	case markIssue:
		return m.markIssue(e, ok), nil
	case verdictPick:
		m.verdict.kind = a.text
	case verdictType:
		m.verdict.note += a.text
	case verdictBack:
		if r := []rune(m.verdict.note); len(r) > 0 {
			m.verdict.note = string(r[:len(r)-1])
		}
	case verdictSend:
		return m.sendVerdict()
	case verdictCancel:
		m.verdict = verdictPrompt{}
		return m.say("left it as it was"), nil
	case copyID:
		if ok && e.kind == workstreamEntry {
			return m.say(m.moveToSay(e, "y copies a session's attach command")), nil
		}
		if !isSession {
			return m, nil
		}
		text := row.SessionID
		if row.Background() {
			text = "claude attach " + row.ID
		}
		put := m.deps.copyText
		return m, func() tea.Msg { return copied(text, put(text)) }
	}
	return m, nil
}

// shownRoots are the roots of the projects in the list.
func (m model) shownRoots() []string {
	var roots []string
	for _, p := range m.world().Projects {
		roots = append(roots, p.Roots...)
	}
	return roots
}

// pickMatches is what the picker offers now.
func (m model) pickMatches() []string {
	shown := map[string]bool{}
	for _, r := range m.shownRoots() {
		shown[r] = true
	}
	return pickMatches(m.pick.query, m.user, m.pick.paths, shown, m.short)
}

// setPickQuery changes the picker's query, and reads the directories it
// needs when they are not the ones read already.
func (m model) setPickQuery(q string) (model, tea.Cmd) {
	m.pick.query, m.pick.sel = q, 0
	if _, prefix := splitPathQuery(q, m.user); isPathQuery(q) && prefix == "" {
		m.pick.sel = -1 // enter adds the directory itself
	}
	if src := pickSource(q, m.user); src != m.pick.from {
		m.pick.from, m.pick.paths, m.pick.read = src, nil, false
		return m.readPicks()
	}
	return m, nil
}

// readPicks reads what the picker's source needs: the folders beside the
// known projects and the one overview was started in, or a path query's
// directory.
func (m model) readPicks() (model, tea.Cmd) {
	dirs := []string{m.pick.from}
	if m.pick.from == "" {
		dirs = pickParents(append(m.shownRoots(), m.root(m.cwd)))
	}
	return m, readPathsCmd(m.deps.readDirs, m.pick.from, dirs)
}

// toProject puts the cursor on a project's first entry, clearing a search
// that hides it.
func (m model) toProject(root string) model {
	name := m.name(root)
	find := func() (string, bool) {
		for _, s := range m.sections() {
			if !s.Away && !s.Agents && s.Title == name && len(s.Entries) > 0 {
				return s.Entries[0].key(), true
			}
		}
		return "", false
	}
	key, ok := find()
	if !ok && m.filter != "" {
		m.filter, m.filtering = "", false
		key, ok = find()
	}
	if ok {
		m.sel, m.confirm = key, ""
		m = m.setOpened(tmux.Pane{})
	}
	return m
}

// rootOf is the root of the project an entry belongs to, where c and t
// start: a session's, a terminal's, or a heading's project's. With
// nothing selected it is the project overview was started in.
func (m model) rootOf(e entry, ok bool) string {
	switch {
	case !ok:
		return m.root(m.cwd)
	case e.kind == terminalEntry:
		return e.term.Root
	case e.kind == projectEntry:
		return e.proj.Root
	case e.stream != nil:
		return e.stream.Root
	}
	return m.root(e.sess.Cwd)
}

// moveToSay is what a key that acts on a session or terminal says on a
// workstream: what it does, and to move to an entry under it.
func (m model) moveToSay(e entry, does string) string {
	return does + ": move to one under #" + strconv.Itoa(e.stream.ID)
}

// copyText puts text on this machine's clipboard. In tmux the view also
// copies through it, to reach a terminal on another machine.
func copyText(text string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

func copied(text string, err error) tea.Msg {
	if err != nil {
		return noteMsg("copy failed: " + err.Error())
	}
	return noteMsg("copied " + text)
}

// enterKind is what enter does on an entry. enterOn decides it, act
// carries it out and the footer names it, so the hint cannot say one
// thing while enter does another.
type enterKind int

const (
	enterSays    enterKind = iota // only a note: how to start something, or why nothing opens
	enterType                     // the keyboard goes to the entry's pane beside the list
	enterAttach                   // a background session gets an attach pane first
	enterApp                      // the app a session runs inside comes forward
	enterCockpit                  // the cockpit opens, the keyboard staying in the list
)

// label is how the footer names what enter does, "" when it only says
// something, since a note is not worth a key's hint.
func (k enterKind) label() string {
	switch k {
	case enterType:
		return "type"
	case enterAttach:
		return "attach"
	case enterApp:
		return "open app"
	case enterCockpit:
		return "cockpit"
	}
	return ""
}

// does is a sentence that says what enter does to what, as a note under
// a heading puts it, "" when enter only says something.
func (k enterKind) does(what string) string {
	switch k {
	case enterType:
		return " enter types in " + what + "."
	case enterAttach:
		return " enter attaches to " + what + "."
	case enterApp:
		return " enter brings " + what + " forward in its app."
	case enterCockpit:
		return " enter opens the cockpit."
	}
	return ""
}

// enterOn is what enter does on an entry, and the entry it does it to.
// On a workstream it acts on the workstream's lead, the entry the stage
// shows beside it, and with nothing under it it opens the cockpit, or in
// the cockpit says how to start a session. A background session gets an
// attach pane first. One that runs inside another app brings that app
// forward, since that is where it is used. A project's heading only says
// how to start something there.
func (m model) enterOn(e entry) (entry, enterKind) {
	switch e.kind {
	case workstreamEntry:
		if c, ok := lead(children(m.ordered(), e.task())); ok {
			return m.enterOn(c)
		}
		if m.cockpit.open {
			return e, enterSays
		}
		return e, enterCockpit
	case projectEntry:
		return e, enterSays
	case terminalEntry:
		if m.mux == nil {
			return e, enterSays
		}
		return e, enterType
	}
	row := e.sess
	switch {
	case row.Host != nil:
		if row.Host.Path == "" || m.deps.reveal == nil {
			return e, enterSays
		}
		return e, enterApp
	case m.mux == nil:
		return e, enterSays
	case row.Pane != "":
		return e, enterType
	case !row.Background():
		return e, enterSays
	}
	return e, enterAttach
}

// carry does k, what enterOn decided, to e, the entry it decided on.
func (m model) carry(e entry, k enterKind) (model, tea.Cmd) {
	switch k {
	case enterCockpit:
		return m.openCockpit()
	case enterType:
		if e.kind == terminalEntry {
			m.entering = e.term.Pane
		} else {
			m.entering = e.sess.Pane
		}
		return m, nil
	case enterApp:
		row, h, reveal, mux, ps := e.sess, e.sess.Host, m.deps.reveal, m.mux, m.deps.psTable
		return m.say("opening " + h.Name + "…"), func() tea.Msg {
			if remote(mux, ps) {
				return doneMsg{row.Name + " is in " + h.Name + ", on the machine overview runs on, which cannot open it from here", nil}
			}
			return doneMsg{row.Name + " is in " + h.Name, reveal(h.Path)}
		}
	case enterAttach:
		row := e.sess
		attach := m.home.AttachArgs(row.Live)
		return m.say("attaching to " + row.Name + "…").startPane(spawn{kind: spawnAttach, key: row.ID, sel: m.sel, name: row.Name,
			session: m.session, dir: row.Cwd, env: m.claudeEnv(), command: attach})
	}
	return m.say(m.enterNote(e)), nil
}

// enterNote is what enter says on an entry where it opens nothing.
func (m model) enterNote(e entry) string {
	switch e.kind {
	case workstreamEntry:
		return "No session works on #" + strconv.Itoa(e.stream.ID) + ". c starts one, t a terminal."
	case projectEntry:
		p := e.proj
		switch {
		case p.Empty() && p.Tracked:
			return "Nothing runs in " + p.Name + " yet. s starts a task, c Claude, t a terminal."
		case p.Empty():
			return "Nothing runs in " + p.Name + " yet. c starts Claude here, t a terminal."
		case p.Tracked:
			return "c starts Claude in " + p.Name + " on no task, s one on a task, t a terminal."
		}
		return "c starts Claude in " + p.Name + ", t a terminal."
	case terminalEntry:
		return "terminals open only inside overview's tmux"
	}
	switch row := e.sess; {
	case row.Host != nil:
		return awayNote(row.Name, *row.Host)
	case m.mux == nil:
		return "sessions open only inside overview's tmux"
	default:
		return m.stageHint(row)
	}
}

// claudeEnv is what a pane overview starts is given, so a claude in it,
// started by overview or typed by the user, joins this config directory.
func (m model) claudeEnv() []string { return []string{m.home.ClaudeEnv()} }

// start runs a new claude at dir, a project's root, in a window of its own
// that the stage shows until the session joins the list.
func (m model) start(dir string) (model, tea.Cmd) {
	if dir == "" {
		return m.say("there is no directory to start a session in"), nil
	}
	if m.mux == nil {
		return m.say("sessions start only inside overview's tmux"), nil
	}
	where := m.name(dir)
	return m.sayAbout("starting a session in ", where, "…").startPane(spawn{kind: spawnSession, name: "a session in " + where,
		session: m.session, dir: dir, env: m.claudeEnv(), command: m.home.NewArgs()})
}

// startOn runs a new claude at root on a workstream's issue: its first
// prompt names the issue, which links the session to it once its
// transcript is read.
func (m model) startOn(root string, i issues.Issue) (model, tea.Cmd) {
	return m.startWith(root, i, opening(m.world().IssuesID(root), i))
}

// startChanges runs a new claude at root on the changes asked for in issue
// i, with note, the verdict that sent it back.
func (m model) startChanges(root string, i issues.Issue, note string) (model, tea.Cmd) {
	return m.startWith(root, i, changesPrompt(m.world().IssuesID(root), i, note))
}

// startWith runs a new claude at root on issue i with prompt, which opens
// with the line that links it to the issue.
func (m model) startWith(root string, i issues.Issue, prompt string) (model, tea.Cmd) {
	if m.mux == nil {
		return m.say("sessions start only inside overview's tmux"), nil
	}
	ref := "#" + strconv.Itoa(i.ID)
	id := m.world().IssuesID(root)
	note := "starting a session on " + ref + "…"
	if !opens(prompt, id, i) {
		note = "starting a session on " + ref + ", which won't link by itself: the project id has a space or a #"
	}
	return m.say(note).startPane(spawn{kind: spawnSession, name: "a session on " + ref,
		session: m.session, dir: root, env: m.claudeEnv(), command: m.home.NewArgs(prompt)})
}

// openTerminal starts tmux's default shell at dir, a project's root, in a
// window of its own, and the cursor goes to it. task is the workstream the
// terminal is for, "" for none.
func (m model) openTerminal(dir, task string) (model, tea.Cmd) {
	if dir == "" {
		return m.say("there is no directory to open a terminal in"), nil
	}
	if m.mux == nil {
		return m.say("terminals open only inside overview's tmux"), nil
	}
	where := m.name(dir)
	return m.sayAbout("opening a terminal in ", where, "…").startPane(spawn{kind: spawnTerminal, name: "a terminal in " + where,
		session: m.session, dir: dir, env: m.claudeEnv(), task: task})
}

// awayNote says where a session that runs inside another app is.
func awayNote(name string, h world.Host) string {
	if h.Name == "" {
		return name + " runs without a terminal, so there is nowhere to go."
	}
	return name + " runs inside " + h.Name + ". Switch to " + h.Name + " to use it."
}
