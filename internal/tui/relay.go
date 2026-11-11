package tui

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The agents have a tmux pane of their own under the list's, so tmux moves
// the keyboard into it and lights its rule as it does for any pane. One
// process still decides what both panes show: the view. The program in
// the agents' pane, Relay, passes on what happens there (a key, a click,
// the keyboard arriving or leaving, the pane's size) and shows the screen
// the view sends back. The two talk over a unix socket, a line of JSON for
// each thing passed.

// wire is one line between the view and the agents' pane: something that
// happened in the pane, on its way to the view, or a screen for the pane
// to show, on its way back. Seq numbers the screens, so one that lands
// late is not shown over a newer one.
type wire struct {
	Key    *tea.Key        `json:"key,omitempty"`
	Mouse  *tea.MouseEvent `json:"mouse,omitempty"`
	Focus  *bool           `json:"focus,omitempty"`
	Size   *[2]int         `json:"size,omitempty"`
	Screen *string         `json:"screen,omitempty"`
	Seq    int             `json:"seq,omitempty"`
}

// toWire is the line for something that happened in the agents' pane. ok
// is false for what the view has no use for: a mouse that only moved, or
// a button let go.
func toWire(msg tea.Msg) (wire, bool) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		k := tea.Key(msg)
		return wire{Key: &k}, true
	case tea.MouseMsg:
		if msg.Action != tea.MouseActionPress {
			return wire{}, false
		}
		e := tea.MouseEvent(msg)
		return wire{Mouse: &e}, true
	case tea.FocusMsg:
		on := true
		return wire{Focus: &on}, true
	case tea.BlurMsg:
		on := false
		return wire{Focus: &on}, true
	case tea.WindowSizeMsg:
		return wire{Size: &[2]int{msg.Width, msg.Height}}, true
	}
	return wire{}, false
}

// fromWire is what a line from the agents' pane says happened there, as
// the message bubbletea would have given the view had it happened in the
// view's own pane. ok is false for a line that says nothing.
func fromWire(w wire) (tea.Msg, bool) {
	switch {
	case w.Key != nil:
		return tea.KeyMsg(*w.Key), true
	case w.Mouse != nil:
		return tea.MouseMsg(*w.Mouse), true
	case w.Focus != nil && *w.Focus:
		return tea.FocusMsg{}, true
	case w.Focus != nil:
		return tea.BlurMsg{}, true
	case w.Size != nil:
		return tea.WindowSizeMsg{Width: w.Size[0], Height: w.Size[1]}, true
	}
	return nil, false
}

// agentsSocket is where the view of session, on the tmux server at
// socket, listens for its agents' pane. It sits beside tmux's socket, so
// it is as private as that one, and its name is short, since a unix
// socket's path has about a hundred bytes.
func agentsSocket(socket, session string) string {
	sum := sha256.Sum256([]byte(socket + "\x00" + session))
	return filepath.Join(filepath.Dir(socket), fmt.Sprintf("ov-%x.sock", sum[:6]))
}

type (
	// agentsMsg is something that happened in the agents' pane, as its
	// program passed it on.
	agentsMsg struct{ msg tea.Msg }
	// agentsGone says the program in the agents' pane has hung up.
	agentsGone struct{}
)

// agentsLink is the view's end of the socket. It serves one pane at a
// time, the one that connected last.
type agentsLink struct {
	ln net.Listener

	mu   sync.Mutex
	conn net.Conn
	seq  int // the newest screen sent
}

// listenAgents listens at path, and gives send what each pane that
// connects passes on, and an agentsGone when it hangs up. A socket file
// left by a view that was killed is replaced.
func listenAgents(path string, send func(tea.Msg)) (*agentsLink, error) {
	os.Remove(path) //nolint:errcheck
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// A view that ends after the next one has started must not take that
	// one's socket away with it, so the file stays for whoever listens
	// here next to replace.
	if u, ok := ln.(*net.UnixListener); ok {
		u.SetUnlinkOnClose(false)
	}
	l := &agentsLink{ln: ln}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			l.mu.Lock()
			if l.conn != nil {
				l.conn.Close()
			}
			l.conn = conn
			l.mu.Unlock()
			go l.read(conn, send)
		}
	}()
	return l, nil
}

func (l *agentsLink) read(conn net.Conn, send func(tea.Msg)) {
	dec := json.NewDecoder(conn)
	for {
		var w wire
		if err := dec.Decode(&w); err != nil {
			break
		}
		if msg, ok := fromWire(w); ok {
			send(agentsMsg{msg})
		}
	}
	conn.Close()
	l.mu.Lock()
	current := l.conn == conn
	if current {
		l.conn = nil
	}
	l.mu.Unlock()
	if current {
		send(agentsGone{})
	}
}

// draw sends the pane the screen numbered seq. The view's Cmds run side
// by side, so two screens can reach here out of order, and the older is
// then dropped. With no pane connected there is nothing to do.
func (l *agentsLink) draw(seq int, screen string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil || seq <= l.seq {
		return nil
	}
	l.seq = seq
	return json.NewEncoder(l.conn).Encode(wire{Screen: &screen, Seq: seq})
}

// close stops listening and hangs up on the pane.
func (l *agentsLink) close() {
	l.ln.Close()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn != nil {
		l.conn.Close()
	}
}

// dialFor is how long Relay tries to reach the view, which listens before
// it starts the pane.
const dialFor = 3 * time.Second

// relay is the agents' pane's program: the screen the view last sent,
// and the way to pass things on to it.
type relay struct {
	screen string
	pass   func(wire) error
}

type screenMsg string

func (r relay) Init() tea.Cmd { return nil }

func (r relay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if s, ok := msg.(screenMsg); ok {
		r.screen = string(s)
		return r, nil
	}
	if w, ok := toWire(msg); ok {
		if err := r.pass(w); err != nil {
			return r, tea.Quit
		}
	}
	return r, nil
}

func (r relay) View() string { return r.screen }

// Relay is the program in the agents' pane. It reaches the view at the
// socket path, passes on what happens in the pane and shows what the view
// sends, and ends when the view hangs up, which closes the pane.
func Relay(path string) error {
	var conn net.Conn
	var err error
	for until := time.Now().Add(dialFor); ; time.Sleep(50 * time.Millisecond) {
		if conn, err = net.Dial("unix", path); err == nil || time.Now().After(until) {
			break
		}
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	enc := json.NewEncoder(conn)
	p := tea.NewProgram(relay{pass: func(w wire) error { return enc.Encode(w) }},
		tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithReportFocus())
	go func() {
		dec := json.NewDecoder(conn)
		for {
			var w wire
			if err := dec.Decode(&w); err != nil {
				p.Quit()
				return
			}
			if w.Screen != nil {
				p.Send(screenMsg(*w.Screen))
			}
		}
	}()
	_, err = p.Run()
	return err
}
