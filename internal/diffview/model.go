// Package diffview is the review viewer: a session's changes as a diff, side
// by side or inline, with notes on lines that are sent to the session with
// one key. It runs as `overview review view` in a pane above the session.
//
// The model is a value. Update decides what happens and returns any work as
// a Cmd; the layout and the frame are pure functions of the model.
package diffview

import (
	"fmt"
	"io"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"

	"overview/internal/diff"
	"overview/internal/review"
	"overview/internal/tui"
	"overview/internal/vcs"
)

const (
	pollEvery = 500 * time.Millisecond
	// settled is how long the transcript must stay unchanged before the
	// diff is reloaded, and stale the longest a change waits regardless.
	settled = time.Second
	stale   = 4 * time.Second
	noteFor = 5 * time.Second
	// splitAt is the width from which the diff is side by side, until the
	// user picks a view.
	splitAt = 140
	// editorRows is the most rows the note editor takes.
	editorRows = 8
)

// Config is what the viewer runs with, every part of it from its command
// line.
type Config struct {
	Store      string    // the review's log
	Dir        string    // the session's directory
	Started    time.Time // when the session started; zero for none
	Transcript string    // the session's transcript, "" for none
	Target     string    // the session's pane, "" when there is none to send to
	Self       string    // the viewer's own pane, "" outside tmux
	Title      string    // what to call the session
	Exe        string    // the overview the agent runs to answer
	Theme      tui.Theme
	Profile    termenv.Profile
}

// panes is the part of tmux the viewer uses.
type panes interface {
	Live(pane string) (command, screen string, err error)
	Paste(pane, buffer, text string) error
	Resize(pane, size string) error
	Zoom(pane string) error
	Copy(text string) error
}

// deps are the viewer's ways out to the system, each run inside a Cmd.
type deps struct {
	run   vcs.Runner
	read  func(path string) (vcs.Untracked, error)
	stat  func(path string) stamp
	now   func() time.Time
	rnd   io.Reader
	mux   panes // nil outside tmux
	copy  func(text string) error
	store review.Store
	// find locates the repository of a directory.
	find func(dir string) (vcs.Repo, bool)
}

// stamp is what a file looked like when last checked.
type stamp struct {
	mod  time.Time
	size int64
	ok   bool
}

// scope is which changes the diff shows.
type scope int

const (
	sinceStart  scope = iota // since the session started
	uncommitted              // the working copy against its parent
)

func (s scope) label() string {
	if s == sinceStart {
		return "since the session started"
	}
	return "uncommitted"
}

// composing is a note or reply being written, and what it is for: a new
// note on anchor, a reply to note, or an edit of entry.
type composing struct {
	open    bool
	ed      editor
	anchor  review.Anchor
	reply   string // the note replied to
	edit    string // the entry edited
	discard bool   // esc was pressed once on text
}

type model struct {
	cfg  Config
	pal  palette
	deps deps

	repo    vcs.Repo
	hasRepo bool
	scope   scope
	base    string // the commit the diff is from
	// startBase is the commit the session started on, found once: the
	// operation log it comes from grows as the session works.
	startBase string
	files     []diff.File
	loaded    bool
	loadErr   error
	gen       int  // counts loads asked for; a result from an older one is dropped
	loading   bool // a load is running
	again     bool // another load is wanted once it lands

	events []review.Event
	rev    review.Review
	places map[string]review.Place

	items   []item
	laidOut []diff.File // the files items index into
	opts    layoutOpts
	chosen  bool // the user picked split or unified, so width no longer decides
	lit     map[int]lit
	litting map[int]bool // files whose highlighting is asked for, this gen

	cur      int // the cursor's item
	top, off int // the first item drawn, and how many of its rows are above the screen
	mark     int // where a range began, -1 for none

	comp composing
	help bool
	// writing counts the appends to the log not yet landed, and sending
	// says a send is under way, so s neither misses a note being saved
	// nor sends the same notes twice.
	writing int
	sending bool
	size    int // the pane's size: 0 peek, 1 tall, 2 zoomed

	storeAt, transAt stamp
	// changedAt is when the transcript last changed and pendingAt when it
	// first did since the diff was last loaded, both zero when no reload
	// is owed.
	changedAt, pendingAt time.Time

	note   string
	warn   bool
	noteAt time.Time

	w, h int
}

func newModel(cfg Config, d deps) model {
	m := model{cfg: cfg, pal: newPalette(cfg.Theme), deps: d, mark: -1, lit: map[int]lit{}, litting: map[int]bool{}}
	m.repo, m.hasRepo = d.find(cfg.Dir)
	if cfg.Started.IsZero() {
		m.scope = uncommitted
	}
	// The first load is asked for here, since Init cannot change the
	// model; Init only starts it.
	m, _ = m.load()
	return m
}

type (
	tickMsg   time.Time
	stampsMsg struct{ store, trans stamp }
	loadedMsg struct {
		gen   int
		base  string
		files []diff.File
		err   error
	}
	storeMsg struct {
		events []review.Event
		at     stamp
		err    error
	}
	litMsg struct {
		gen  int
		file int
		lit  lit
	}
	// doneMsg reports an action: a note to show, whether the review's
	// log changed, and whether it was a write or a send that finished.
	doneMsg struct {
		note  string
		err   error
		wrote bool
		write bool
		send  bool
	}
)

func (m model) Init() tea.Cmd {
	return tea.Batch(m.loadCmd(), m.loadStore(), tick())
}

func tick() tea.Cmd {
	return tea.Tick(pollEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// load asks for the diff again, or for another load once the running one
// lands.
func (m model) load() (model, tea.Cmd) {
	if !m.hasRepo {
		m.loadErr, m.loaded = fmt.Errorf("%s is in no git or jj repository", m.cfg.Dir), true
		return m, nil
	}
	if m.loading {
		m.again = true
		return m, nil
	}
	m.gen++
	m.loading, m.again = true, false
	m.changedAt, m.pendingAt = time.Time{}, time.Time{}
	return m, m.loadCmd()
}

// loadCmd resolves the base and reads the diff for load m.gen.
func (m model) loadCmd() tea.Cmd {
	if !m.loading {
		return nil
	}
	gen, repo, sc, started, known, d := m.gen, m.repo, m.scope, m.cfg.Started, m.startBase, m.deps
	return func() tea.Msg {
		var base string
		var err error
		switch {
		case sc == sinceStart && known != "":
			base = known
		case sc == sinceStart:
			base, err = repo.BaseAt(d.run, started)
		default:
			base, err = repo.Uncommitted(d.run)
		}
		if err != nil {
			return loadedMsg{gen: gen, err: err}
		}
		files, err := repo.Diff(d.run, d.read, base)
		return loadedMsg{gen, base, files, err}
	}
}

func (m model) loadStore() tea.Cmd {
	d := m.deps
	return func() tea.Msg {
		at := d.stat(d.store.Path)
		es, err := d.store.Load()
		return storeMsg{es, at, err}
	}
}

func (m model) checkStamps() tea.Cmd {
	d, trans := m.deps, m.cfg.Transcript
	return func() tea.Msg {
		msg := stampsMsg{store: d.stat(d.store.Path)}
		if trans != "" {
			msg.trans = d.stat(trans)
		}
		return msg
	}
}

func (m model) say(note string) model {
	m.note, m.warn, m.noteAt = note, false, m.deps.now()
	return m
}

func (m model) complain(err error) model {
	m.note, m.warn, m.noteAt = err.Error(), true, m.deps.now()
	return m
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m, cmd := m.update(msg)
	switch msg.(type) {
	case tickMsg, stampsMsg:
		// Nothing on screen moved, so no file came into view.
		return m, cmd
	}
	m, hl := m.highlightVisible()
	return m, tea.Batch(cmd, hl)
}

func (m model) update(msg tea.Msg) (model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		if !m.chosen {
			m.opts.split = m.w >= splitAt
		}
		return m.relayout(), nil
	case tickMsg:
		now := time.Time(msg)
		if m.note != "" && now.Sub(m.noteAt) > noteFor {
			m.note = ""
		}
		return m, tea.Batch(tick(), m.checkStamps())
	case stampsMsg:
		var cmds []tea.Cmd
		if msg.store != m.storeAt {
			cmds = append(cmds, m.loadStore())
		}
		// The diff reloads once the transcript has been still for a
		// moment, or has kept changing for a while. The first look only
		// records it.
		now := m.deps.now()
		if msg.trans != m.transAt {
			if m.transAt.ok {
				m.changedAt = now
				if m.pendingAt.IsZero() {
					m.pendingAt = now
				}
			}
			m.transAt = msg.trans
		}
		if due(m.changedAt, m.pendingAt, now) {
			var load tea.Cmd
			m, load = m.load()
			cmds = append(cmds, load)
		}
		return m, tea.Batch(cmds...)
	case loadedMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.loading, m.loaded = false, true
		var cmd tea.Cmd
		if m.again {
			m, cmd = m.load()
		}
		if msg.err != nil {
			m.loadErr = msg.err
			if len(m.files) > 0 {
				// The diff on screen stays; the error goes in the status bar.
				m = m.complain(fmt.Errorf("could not reload: %w", msg.err))
			}
			return m.relayout(), cmd
		}
		m.loadErr, m.base, m.files = nil, msg.base, msg.files
		if m.scope == sinceStart {
			m.startBase = msg.base
		}
		m.lit, m.litting = map[int]lit{}, map[int]bool{}
		return m.relayout(), cmd
	case storeMsg:
		if msg.err != nil {
			return m.complain(msg.err), nil
		}
		// The log only grows, so a read with fewer events than the model
		// has began before a write that has since landed: it is stale.
		if len(msg.events) < len(m.events) {
			return m, nil
		}
		m.storeAt, m.events = msg.at, msg.events
		m.rev = review.Fold(msg.events)
		return m.relayout(), nil
	case litMsg:
		if msg.gen == m.gen {
			m.lit = with(m.lit, msg.file, msg.lit)
		}
		return m, nil
	case doneMsg:
		if msg.write {
			m.writing = max(0, m.writing-1)
		}
		if msg.send {
			m.sending = false
		}
		switch {
		case msg.err != nil:
			m = m.complain(msg.err)
		case msg.note != "":
			m = m.say(msg.note)
		}
		if msg.wrote {
			return m, m.loadStore()
		}
		return m, nil
	case tea.KeyMsg:
		return m.key(msg)
	case tea.MouseMsg:
		return m.mouse(msg)
	}
	return m, nil
}

// due says whether a reload is owed: the transcript changed at changed,
// first changed at pending, and has settled or waited long enough.
func due(changed, pending, now time.Time) bool {
	if pending.IsZero() {
		return false
	}
	return now.Sub(changed) >= settled || now.Sub(pending) >= stale
}

// with is a copy of mp with k set to v, so a map the model shares with an
// older model is never changed under it.
func with[K comparable, V any](mp map[K]V, k K, v V) map[K]V {
	out := make(map[K]V, len(mp)+1)
	for kk, vv := range mp {
		out[kk] = vv
	}
	out[k] = v
	return out
}

// relayout rebuilds the items after the diff, the notes or the options
// changed, and keeps the cursor and the top of the screen on the same
// spots.
func (m model) relayout() model {
	var at, from, mark spot
	hadItems := len(m.items) > 0
	if hadItems {
		at = spotOf(m.laidOut, m.items[m.cur])
		from = spotOf(m.laidOut, m.items[m.top])
		if m.mark >= 0 {
			mark = spotOf(m.laidOut, m.items[m.mark])
		}
	}
	m.places = review.Places(m.rev, m.files)
	m.items = layout(m.files, m.rev, m.places, m.opts)
	m.laidOut = m.files
	if !hadItems {
		m.cur, m.top, m.off = settle(m.items, 0), 0, 0
		return m.scrollToCursor()
	}
	if i, ok := find(m.items, m.files, at); ok {
		m.cur = settle(m.items, i)
	} else {
		m.cur = settle(m.items, min(m.cur, len(m.items)-1))
	}
	if i, ok := find(m.items, m.files, from); ok {
		m.top, m.off = i, 0
	} else {
		m.top, m.off = min(m.top, len(m.items)-1), 0
	}
	if m.mark >= 0 {
		m.mark = -1
		if i, ok := find(m.items, m.files, mark); ok && m.items[i].kind == rowItem {
			m.mark = i
		}
	}
	return m.scrollToCursor()
}
