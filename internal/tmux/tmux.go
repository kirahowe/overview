// Package tmux drives overview's own tmux server, which holds the view and
// every session started from it. Each method is one tmux command.
package tmux

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Socket names overview's server, so it never shares one with other tmux use.
// OVERVIEW_TMUX_SOCKET picks another, so a trial run keeps off the real one.
var Socket = socket()

func socket() string {
	if s := os.Getenv("OVERVIEW_TMUX_SOCKET"); s != "" {
		return s
	}
	return "overview"
}

// ListOption marks the pane the view runs in, so a relaunch can tell
// whether the view is still there.
const ListOption = "@overview-list"

// paneFormat is how Panes asks for each pane: one tab-separated line each.
const paneFormat = "#{pane_id}\t#{pane_tty}\t#{session_name}\t#{window_id}\t#{pane_width}\t#{pane_height}\t#{" + ListOption + "}"

// Pane is one tmux pane.
type Pane struct {
	ID      string // "%3", kept while the pane lives, wherever it moves
	TTY     string // "ttys012", as ps names it
	Session string
	Window  string // "@2"
	W, H    int
	List    bool // the view runs here
}

// Server is overview's tmux server. Run runs one tmux command and returns
// its output; tests replace it.
type Server struct {
	Run    func(args ...string) (string, error)
	socket string
}

// Socket is the socket name the server listens on.
func (s Server) Socket() string { return s.socket }

// New returns overview's real server. conf is read only by the call that
// starts the server; "" leaves tmux to its defaults.
func New(conf string) Server { return On(Socket, conf) }

// On returns the server on another socket. Tests use it to keep off the
// real one.
func On(socket, conf string) Server {
	base := []string{"-L", socket}
	if conf != "" {
		base = append(base, "-f", conf)
	}
	return Server{socket: socket, Run: func(args ...string) (string, error) {
		cmd := exec.Command("tmux", append(base, args...)...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("tmux %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
		}
		return string(out), nil
	}}
}

// ParsePanes reads list-panes output in paneFormat. Malformed lines are
// skipped.
func ParsePanes(out string) []Pane {
	var panes []Pane
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 7 || !strings.HasPrefix(f[0], "%") {
			continue
		}
		w, err1 := strconv.Atoi(f[4])
		h, err2 := strconv.Atoi(f[5])
		if err1 != nil || err2 != nil {
			continue
		}
		panes = append(panes, Pane{
			ID: f[0], TTY: strings.TrimPrefix(f[1], "/dev/"), Session: f[2], Window: f[3],
			W: w, H: h, List: f[6] == "1",
		})
	}
	return panes
}

// Panes lists every pane on the server. No server means no panes.
func (s Server) Panes() ([]Pane, error) {
	out, err := s.Run("list-panes", "-a", "-F", paneFormat)
	if err != nil {
		if noServer(err) {
			return nil, nil
		}
		return nil, err
	}
	return ParsePanes(out), nil
}

func noServer(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "no server running") || strings.Contains(msg, "error connecting")
}

// Spawn runs command in a new window of session, which it creates when
// missing, and leaves the window in the background. dir may be "".
func (s Server) Spawn(session, dir string, env []string, command ...string) (Pane, error) {
	var args []string
	if _, err := s.Run("has-session", "-t", "="+session); err == nil {
		args = []string{"new-window", "-d", "-t", "=" + session + ":"}
	} else {
		args = []string{"new-session", "-d", "-s", session}
	}
	return s.create(args, dir, env, command)
}

// Open runs command in a new window of session and makes it the session's
// current window.
func (s Server) Open(session string, env []string, command ...string) (Pane, error) {
	return s.create([]string{"new-window", "-t", "=" + session + ":"}, "", env, command)
}

func (s Server) create(args []string, dir string, env, command []string) (Pane, error) {
	if dir != "" {
		args = append(args, "-c", dir)
	}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	args = append(args, "-P", "-F", "#{pane_id}\t#{pane_tty}")
	out, err := s.Run(append(args, command...)...)
	if err != nil {
		return Pane{}, err
	}
	id, tty, ok := strings.Cut(strings.TrimSpace(out), "\t")
	if !ok || !strings.HasPrefix(id, "%") {
		return Pane{}, fmt.Errorf("tmux %s: unexpected output %q", args[0], out)
	}
	return Pane{ID: id, TTY: strings.TrimPrefix(tty, "/dev/")}, nil
}

// Move puts pane list in target's window as a full-height column on the
// left, size wide, and makes that window current and list its active pane.
// A window that list leaves empty closes.
func (s Server) Move(list, target, size string) error {
	_, err := s.Run("join-pane", "-h", "-b", "-f", "-l", size, "-s", list, "-t", target)
	return err
}

// Layout reads a window's layout, as select-layout takes it.
func (s Server) Layout(window string) (string, error) {
	out, err := s.Run("display-message", "-p", "-t", window, "#{window_layout}")
	return strings.TrimSpace(out), err
}

// SetLayout gives a window a layout that Layout read.
func (s Server) SetLayout(window, layout string) error {
	_, err := s.Run("select-layout", "-t", window, layout)
	return err
}

// MoveWindow moves a window to the end of session, in the background.
func (s Server) MoveWindow(window, session string) error {
	_, err := s.Run("move-window", "-d", "-s", window, "-t", "="+session+":")
	return err
}

// Select focuses a pane.
func (s Server) Select(pane string) error {
	_, err := s.Run("select-pane", "-t", pane)
	return err
}

// Detach detaches the client the calling pane is shown in.
func (s Server) Detach() error {
	_, err := s.Run("detach-client")
	return err
}

// Option reads a session option, "" when unset. The option commands do not
// take the "=" that pins a name exactly, but an exact name still wins.
func (s Server) Option(session, name string) string {
	out, err := s.Run("show-options", "-qv", "-t", session, name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// SetOption sets a session option.
func (s Server) SetOption(session, name, value string) error {
	_, err := s.Run("set-option", "-t", session, name, value)
	return err
}

// PaneOption reads a pane option, "" when unset.
func (s Server) PaneOption(pane, name string) string {
	out, err := s.Run("show-options", "-pqv", "-t", pane, name)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(out, "\n")
}

// SetPaneOption sets a pane option.
func (s Server) SetPaneOption(pane, name, value string) error {
	_, err := s.Run("set-option", "-p", "-t", pane, name, value)
	return err
}

// Respawn kills what runs in pane and runs command there instead. The pane
// keeps its place, size and options.
func (s Server) Respawn(pane string, env []string, command ...string) error {
	args := []string{"respawn-pane", "-k", "-t", pane}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	_, err := s.Run(append(args, command...)...)
	return err
}

// MarkList marks pane as the one the view runs in.
func (s Server) MarkList(pane string) error {
	_, err := s.Run("set-option", "-p", "-t", pane, ListOption, "1")
	return err
}
