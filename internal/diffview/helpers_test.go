package diffview

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"

	"overview/internal/review"
	"overview/internal/tui"
	"overview/internal/vcs"
)

// The fakes stand in for everything the viewer reaches outside itself:
// the vcs runner, the clock, the tmux panes and the clipboard. Only the
// review's log is real, in a t.TempDir().

// fakeVCS answers the commands vcs.Repo runs for a jj repository.
type fakeVCS struct {
	diff  string
	calls [][]string
	fail  error // returned for a diff, when set
}

func (f *fakeVCS) run(_ string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, slices.Clone(args))
	switch {
	case slices.Contains(args, "--at-op"):
		return []byte("startcommit0000\n"), nil
	case slices.Contains(args, "op"):
		return []byte("op2 " + strconv.FormatInt(time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC).Unix(), 10) + "\nop1 0\n"), nil
	case slices.Contains(args, "diff"):
		if f.fail != nil {
			return nil, f.fail
		}
		return []byte(f.diff), nil
	case slices.Contains(args, "-r"):
		return []byte("parentcommit00\n"), nil
	}
	return nil, fmt.Errorf("fake vcs: unexpected %q", args)
}

// count is how many calls had every one of words among their args.
func (f *fakeVCS) count(words ...string) int {
	n := 0
	for _, c := range f.calls {
		all := true
		for _, w := range words {
			if !slices.Contains(c, w) {
				all = false
			}
		}
		if all {
			n++
		}
	}
	return n
}

type pasted struct{ pane, buffer, text string }

// fakePanes is tmux as the viewer sees it.
type fakePanes struct {
	command  string // what runs in the pane, "claude" when empty
	screen   string
	captured []string
	pastes   []pasted
	calls    []string // Resize and Zoom, in order
}

func (f *fakePanes) Live(pane string) (string, string, error) {
	f.captured = append(f.captured, pane)
	command := f.command
	if command == "" {
		command = "claude"
	}
	return command, f.screen, nil
}

func (f *fakePanes) Paste(pane, buffer, text string) error {
	f.pastes = append(f.pastes, pasted{pane, buffer, text})
	return nil
}

func (f *fakePanes) Resize(pane, size string) error {
	f.calls = append(f.calls, "resize "+pane+" "+size)
	return nil
}

func (f *fakePanes) Zoom(pane string) error {
	f.calls = append(f.calls, "zoom "+pane)
	return nil
}

func (f *fakePanes) Copy(text string) error {
	f.calls = append(f.calls, "copy "+text)
	return nil
}

// counter is a deterministic random source.
type counter struct{ n byte }

func (c *counter) Read(p []byte) (int, error) {
	for i := range p {
		c.n++
		p[i] = c.n
	}
	return len(p), nil
}

// fixture is one viewer's world.
type fixture struct {
	store  review.Store
	vcs    *fakeVCS
	panes  *fakePanes // nil for no tmux
	copied []string
	now    time.Time
	trans  map[string]stamp
	repo   bool
}

func newFixture(dir, diffText string) *fixture {
	return &fixture{
		store: review.Store{Path: filepath.Join(dir, "reviews", "s1.jsonl")},
		vcs:   &fakeVCS{diff: diffText},
		now:   time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC),
		trans: map[string]stamp{},
		repo:  true,
	}
}

func (f *fixture) deps() deps {
	d := deps{
		run: f.vcs.run,
		read: func(string) (vcs.Untracked, error) {
			return vcs.Untracked{}, errors.New("fake: no files")
		},
		stat: func(path string) stamp {
			if path == f.store.Path {
				return statFile(path)
			}
			return f.trans[path]
		},
		now:   func() time.Time { return f.now },
		rnd:   &counter{},
		copy:  func(text string) error { f.copied = append(f.copied, text); return nil },
		store: f.store,
		find: func(string) (vcs.Repo, bool) {
			return vcs.Repo{Root: "/fake/repo", Kind: vcs.JJ}, f.repo
		},
	}
	if f.panes != nil {
		d.mux = f.panes
	}
	return d
}

func testTheme(t testing.TB) tui.Theme {
	t.Helper()
	th, err := tui.LoadTheme("one-dark", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return th
}

func baseConfig(th tui.Theme, f *fixture) Config {
	return Config{
		Store:   f.store.Path,
		Dir:     "/fake/repo",
		Title:   "the session",
		Exe:     "/usr/local/bin/overview",
		Theme:   th,
		Profile: termenv.Ascii,
	}
}

// open starts a viewer as Run would, sized w by h, with its first loads
// done.
func open(t tester, f *fixture, cfg Config, w, h int) model {
	t.Helper()
	m := newModel(cfg, f.deps())
	m, cmd := step(m, tea.WindowSizeMsg{Width: w, Height: h})
	m = drain(t, m, tea.Batch(cmd, m.loadCmd(), m.loadStore()))
	return m
}

func step(m model, msg tea.Msg) (model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(model), cmd
}

type tester interface {
	Helper()
	Fatal(args ...any)
	Fatalf(format string, args ...any)
}

// drain runs commands and feeds their messages back, as the runtime
// would, until nothing is left to do. A tick is never run: it would only
// sleep and ask for the stamps, which tests deliver themselves.
func drain(t tester, m model, cmd tea.Cmd) model {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for n := 0; len(queue) > 0; n++ {
		if n > 500 {
			t.Fatal("the viewer never settled")
		}
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg := runCmd(t, c)
		switch msg := msg.(type) {
		case nil, tea.QuitMsg:
		case tea.BatchMsg:
			queue = append(queue, msg...)
		default:
			var next tea.Cmd
			m, next = step(m, msg)
			queue = append(queue, next)
		}
	}
	return m
}

// runCmd runs c, dropping a tick: a Cmd that has not answered in a
// moment is the poll timer.
func runCmd(t tester, c tea.Cmd) tea.Msg {
	t.Helper()
	done := make(chan tea.Msg, 1)
	go func() { done <- c() }()
	select {
	case msg := <-done:
		if _, ok := msg.(tickMsg); ok {
			return nil
		}
		return msg
	case <-time.After(5 * time.Second):
		// Only tea.Tick sleeps, for pollEvery; anything else this slow
		// is a hang.
		t.Fatal("a command never returned")
	}
	return nil
}

func keyMsg(s string) tea.KeyMsg {
	types := map[string]tea.KeyType{
		"enter": tea.KeyEnter, "esc": tea.KeyEsc, "backspace": tea.KeyBackspace,
		"delete": tea.KeyDelete, "up": tea.KeyUp, "down": tea.KeyDown,
		"left": tea.KeyLeft, "right": tea.KeyRight, "home": tea.KeyHome, "end": tea.KeyEnd,
		"ctrl+d": tea.KeyCtrlD, "ctrl+u": tea.KeyCtrlU, "ctrl+j": tea.KeyCtrlJ,
		"ctrl+w": tea.KeyCtrlW, "ctrl+a": tea.KeyCtrlA, "ctrl+e": tea.KeyCtrlE,
		"pgdown": tea.KeyPgDown, "pgup": tea.KeyPgUp,
	}
	if k, ok := types[s]; ok {
		return tea.KeyMsg{Type: k}
	}
	if s == " " {
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// press sends keys one by one, settling after each.
func press(t tester, m model, keys ...string) model {
	t.Helper()
	for _, k := range keys {
		var cmd tea.Cmd
		m, cmd = step(m, keyMsg(k))
		m = drain(t, m, cmd)
	}
	return m
}

// typeText types s into the editor rune by rune.
func typeText(t tester, m model, s string) model {
	t.Helper()
	for _, r := range s {
		var cmd tea.Cmd
		m, cmd = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = drain(t, m, cmd)
	}
	return m
}

func (f *fixture) events(t testing.TB) []review.Event {
	t.Helper()
	es, err := f.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func readFixture(t testing.TB, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "review", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// screen is the frame as plain text, one row per line.
func screen(m model) string {
	rows, _ := m.frame()
	var b strings.Builder
	for _, r := range rows {
		for _, s := range r {
			b.WriteString(s.Text)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// cursorSpot is where the cursor is: a file, side and line, or a note.
// cursorSpot is where the cursor is, by number; its text is left out so
// tests can name a spot by its line.
func cursorSpot(m model) spot {
	s := spotOf(m.laidOut, m.items[m.cur])
	s.text = ""
	return s
}
