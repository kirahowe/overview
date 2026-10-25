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
	in      world.Inputs
	gen     int
	memo    *memo
	rowsErr error
	loaded  bool

	screen    screen
	sel       string // the key of the entry under the cursor, kept across refreshes
	listTop   int    // first line of the projects shown, kept so clicks do not shift it
	agentsTop int    // and of the agents
	filter    string
	filtering bool
	confirm   string // the key of a session to stop or a terminal to close, waiting on a yes
	help      bool
	frame     int

	digests      []string
	digestIdx    int
	digest       string
	digestAt     time.Time
	digesting    bool
	digestScroll int

	note   string
	noteAt time.Time

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
	// written is when each row's transcript was last written, as the
	// latest refresh found it. turns holds the selected session's last
	// turns, read in a Cmd, and turnsAsked the transcript last asked for.
	written    map[string]time.Time
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
	if err := srv.Frame(session, frameColours(v.Theme)); err != nil {
		return err
	}
	if err := srv.BindPanels(); err != nil {
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
	_, err = tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithReportFocus()).Run()
	return err
}

func newModel(home fleet.Home, llm string) model {
	return model{home: home, llm: llm, cache: &detailCache{}, memo: &memo{}, transcripts: fleet.NewTranscripts(), deps: systemDeps()}
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
	exists   func(path string) bool      // whether anything is at path
	resolve  func(path string) string    // path with its symlinks resolved
	modTime  func(path string) time.Time // when a file was last written, zero if unknown
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
	k := turnsKey{e.sess.Transcript, m.written[e.sess.Transcript]}
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
	m.note, m.noteAt = note, m.deps.now()
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
	return es[0], 0, true
}

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

// keepSel puts the cursor on the first entry if it is on none that is
// shown.
func (m model) keepSel() model {
	es := m.ordered()
	if len(es) == 0 || m.shown(m.sel) {
		return m
	}
	m.sel, m.confirm = es[0].key(), ""
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
// when the entry under the cursor is no longer shown.
func (m model) keepNear(before model) model {
	if m.shown(m.sel) {
		return m
	}
	if key, ok := nearKey(before.ordered(), m.ordered(), m.sel); ok {
		m.sel, m.confirm = key, ""
	}
	return m
}

// nearKey is where the cursor goes when sel, the key of an entry in old,
// is not in now. A session's entry that has gone goes to another entry
// of the same session, as samePlace finds it, and a workstream that has
// gone to the first session under it that is still listed, so the cursor
// stays with the session it was on. Otherwise it is nearest: a cursor on
// an entry of its own stays among those, out of the agents list, while
// there are any.
func nearKey(old, now []entry, sel string) (string, bool) {
	listed := map[string]bool{}
	for _, e := range now {
		listed[e.key()] = true
	}
	if key, ok := samePlace(old, now, sel); ok {
		return key, true
	}
	var keys []string
	alias := map[string]bool{}
	for _, e := range old {
		keys = append(keys, e.key())
		alias[e.key()] = e.alias != ""
	}
	key, ok := nearest(keys, sel, func(k string) bool { return listed[k] && (alias[sel] || !alias[k]) })
	if !ok {
		key, ok = nearest(keys, sel, func(k string) bool { return listed[k] })
	}
	return key, ok
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
		if key, ok := follow(m.placed(m.ordered()), m.stageOf(m.in.Panes), m.sel); ok && key != m.sel {
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
	if m.w > 0 && m.screen == sessionsScreen {
		col := m.shownColumn()
		m.listTop, m.agentsTop = col.projTop, col.agentsTop
	}
	return m.keepCockpit()
}

// effects compares what tmux and the reads should be with what is in
// flight, and sends the differences as Cmds: the list's place and the
// holder, the selected session's turns, the cockpit's and the task
// picker's reads, the trackers after a refresh, and the panes' rules.
func (m model) effects() (model, tea.Cmd) {
	m, stage := m.restage()
	m, turns := m.askTurns()
	m, reads := m.cockpitReads()
	m, taskRead := m.taskReads()
	m, trackers := m.readTrackers()
	m, frame := m.reframe()
	return m, tea.Batch(stage, turns, reads, taskRead, trackers, frame)
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
	case tea.KeyMsg:
		// A key that reaches the list proves the keyboard is back, whether
		// or not tmux said so, since a focus event can go missing. It also
		// proves the client is out of the panels mode, which keeps every
		// key for itself.
		m.panels = false
		return m.input(keysBack, func(m model) []action { return m.keyActions(msg) })
	case tea.MouseMsg:
		// tmux's panels mode does not bind the mouse, so a mouse event
		// that reaches the view has taken the client out of it.
		m.panels = false
		// The input's time is read here, at the door, so apply stays pure.
		// The mouse proves the user is at the list, but a wheel over it
		// leaves the keyboard where it was.
		at := m.deps.now()
		return m.input(pointerHere, func(m model) []action { return m.mouseActions(msg, at) })
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
		// A refresh that failed keeps what the last one found.
		if m.rowsErr = msg.err; msg.err != nil {
			return m, nil
		}
		m.rowRoots, m.written, m.loaded = msg.roots, msg.written, true
		pins := m.pins == m.pinsLanded && msg.pinGen == m.pinsLanded
		m = m.setInputs(func(in *world.Inputs) {
			in.Rows, in.TTYs, in.Hosts = msg.rows, msg.ttys, msg.hosts
			in.Reviews, in.Orchestrator = msg.reviews, msg.health
			if pins {
				in.Pinned = msg.pinned
			}
		})
		m.refreshes++
		// Keep the cursor on a visible row after sessions come and go.
		return m.adopt().dropTrackers(), nil
	case trackerMsg:
		m = m.landTracker(msg)
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
			return m.say("could not unpin " + m.name(msg.root) + ": " + msg.err.Error()), nil
		case msg.err != nil:
			return m.say("could not add the project: " + msg.err.Error()), nil
		case msg.unpin:
			m = m.setInputs(func(in *world.Inputs) {
				in.Pinned = slices.DeleteFunc(slices.Clone(in.Pinned), func(r string) bool { return r == msg.root })
			})
			return m.say("unpinned " + m.name(msg.root)), nil
		}
		if !slices.Contains(m.in.Pinned, msg.root) {
			m = m.setInputs(func(in *world.Inputs) { in.Pinned = slices.Concat(in.Pinned, []string{msg.root}) })
		}
		return m.toProject(msg.root).say("pinned " + m.name(msg.root)), nil
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
// results, in order.
func (m model) input(arrival verb, translate func(model) []action) (tea.Model, tea.Cmd) {
	m, first := m.apply(action{do: arrival})
	next, rest := m.handle(translate(m))
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
	case keysBack:
		return m.unblur(), nil
	case keysLeft:
		m.blurred = true
	case pointerHere:
		return m.unblur(), nil
	case quit:
		return m, tea.Quit
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
		m.help = true
	case dismiss:
		if m.confirm != "" {
			m.confirm = ""
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
				return m.say("closing " + c.term.Title + "…"), closeCmd(m.mux, m.deps.roots, m.self, m.holder(), c.term, m.in.Panes, m.width.want, m.landed)
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
		return m.say("writing a digest…"), m.runDigest
	case refresh:
		return m, m.refresh
	case move:
		return m.moveTo(es, i+a.n), nil
	case moveTo:
		return m.moveTo(es, a.n), nil
	case nextNeedy:
		// Wraps round the list.
		for d := 1; d <= len(es); d++ {
			j := ((i+a.n*d)%len(es) + len(es)) % len(es)
			if needy(es[j]) {
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
		// New search text puts the cursor on the first match.
		if m.filter != before {
			return m.moveTo(m.ordered(), 0), nil
		}
	case cancelSearch:
		m.filter, m.filtering = "", false
	case clearFilter:
		m.filter = ""
	case open:
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
		return m.act(e)
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
		case ok && e.kind == projectEntry:
			m.pins++
			home, root := m.home, e.pin()
			return m, func() tea.Msg { return pinnedMsg{root: root, err: home.UnpinProject(root), unpin: true} }
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
		m.pick, m.pins = picker{}, m.pins+1
		return m.say("adding " + path + "…"), pinCmd(m.deps.readDirs, m.home.PinProject, path, typed, m.deps.resolve, m.deps.exists)
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
// start: a session's, a terminal's, or a pinned project's. With nothing
// selected it is the project overview was started in.
func (m model) rootOf(e entry, ok bool) string {
	switch {
	case !ok:
		return m.root(m.cwd)
	case e.kind == terminalEntry:
		return e.term.Root
	case e.kind == projectEntry:
		return e.pin()
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

// copyText puts text on the clipboard.
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

// act does what enter means for an entry: it moves the focus beside the
// list. On a workstream it acts on the workstream's lead, the entry the
// stage shows beside it, and with no session under it, it opens the
// cockpit, the keyboard staying in the list. A background session gets an
// attach pane first. One that runs inside another app brings that app
// forward, since that is where it is used. A pinned project with nothing in it only says how to start one.
func (m model) act(e entry) (model, tea.Cmd) {
	switch e.kind {
	case workstreamEntry:
		es := m.ordered()
		if c, ok := lead(children(es, e.task())); ok {
			return m.act(c)
		}
		if m.cockpit.open {
			return m.say("No session works on #" + strconv.Itoa(e.stream.ID) + ". c starts one, t a terminal."), nil
		}
		return m.openCockpit()
	case projectEntry:
		if e.proj.Tracked {
			return m.say("Nothing runs in " + e.proj.Name + " yet. s starts a task, c Claude, t a terminal."), nil
		}
		return m.say("Nothing runs in " + e.proj.Name + " yet. c starts Claude here, t a terminal."), nil
	case terminalEntry:
		if m.mux == nil {
			return m.say("terminals open only inside overview's tmux"), nil
		}
		m.entering = e.term.Pane
		return m, nil
	}
	row := e.sess
	if h := row.Host; h != nil {
		if h.Path == "" || m.deps.reveal == nil {
			return m.say(awayNote(row.Name, *h)), nil
		}
		reveal := m.deps.reveal
		return m.say("opening " + h.Name + "…"), func() tea.Msg {
			return doneMsg{row.Name + " is in " + h.Name, reveal(h.Path)}
		}
	}
	if m.mux == nil {
		return m.say("sessions open only inside overview's tmux"), nil
	}
	if p := row.Pane; p != "" {
		m.entering = p
		return m, nil
	}
	if !row.Background() {
		return m.say(m.stageHint(row)), nil
	}
	attach := m.home.AttachCmd(row.Live)
	return m.say("attaching to " + row.Name + "…").startPane(spawn{kind: spawnAttach, key: row.ID, sel: m.sel, name: row.Name,
		session: m.session, dir: row.Cwd, env: m.claudeEnv(), command: attach.Args})
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
	return m.say("starting a session in " + where + "…").startPane(spawn{kind: spawnSession, name: "a session in " + where,
		session: m.session, dir: dir, env: m.claudeEnv(), command: m.home.NewCmd().Args})
}

// startOn runs a new claude at root on a workstream's issue: its first
// prompt names the issue, which links the session to it once its
// transcript is read.
func (m model) startOn(root string, i issues.Issue) (model, tea.Cmd) {
	if m.mux == nil {
		return m.say("sessions start only inside overview's tmux"), nil
	}
	ref := "#" + strconv.Itoa(i.ID)
	prompt := opening(m.projectID(root), i)
	note := "starting a session on " + ref + "…"
	if !opens(prompt, m.projectID(root), i) {
		note = "starting a session on " + ref + ", which won't link by itself: the project id has a space or a #"
	}
	return m.say(note).startPane(spawn{kind: spawnSession, name: "a session on " + ref,
		session: m.session, dir: root, env: m.claudeEnv(), command: m.home.NewCmdWith(prompt).Args})
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
	return m.say("opening a terminal in " + where + "…").startPane(spawn{kind: spawnTerminal, name: "a terminal in " + where,
		session: m.session, dir: dir, env: m.claudeEnv(), task: task})
}

// awayNote says where a session that runs inside another app is.
func awayNote(name string, h world.Host) string {
	if h.Name == "" {
		return name + " runs without a terminal, so there is nowhere to go."
	}
	return name + " runs inside " + h.Name + ". Switch to " + h.Name + " to use it."
}
