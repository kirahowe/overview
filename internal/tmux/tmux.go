// Package tmux drives overview's own tmux server, which holds the view and
// every session started from it. Each method is one tmux command.
package tmux

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ListOption marks the pane the view runs in, so a relaunch can tell
// whether the view is still there.
const ListOption = "@overview-list"

// ProjectOption is the window option that names the project a window was
// started for, its root directory. Every pane in the window reads it.
const ProjectOption = "@overview-project"

// TaskOption is the window option that names the workstream a terminal was
// opened for: its project root and issue number, "/w/app#6".
const TaskOption = "@overview-task"

// AttachOption is the pane option set on a pane that runs claude attach,
// holding the background session's ID. It is a pane option, not a window
// one, so a split beside the attach is not taken for it.
const AttachOption = "@overview-attach"

// HolderOption marks the pane the view draws a session's details into. A
// pane option, so the listing says which pane it is.
const HolderOption = "@overview-holder"

// ReviewOption is the pane option on a review of a session's changes,
// holding the session's ID.
const ReviewOption = "@overview-review"

// ViewOption is the pane option on the view's pane that says what it runs:
// its settings and the binary, so a relaunch can tell a view that is out
// of date.
const ViewOption = "@overview-view"

// TitleOption is the pane option holding the title set into the rule along
// a pane's top, tmux styles and all, and SelectOption marks the pane the
// view has selected, whose rule is drawn in the accent colour. Frame says
// how they are drawn.
const (
	TitleOption  = "@overview-title"
	SelectOption = "@overview-select"
)

// paneFormat is how Panes asks for each pane: one tab-separated line each.
// The path comes last, so a tab in it cannot shift the other fields.
const paneFormat = "#{pane_id}\t#{pane_tty}\t#{session_name}\t#{window_id}\t#{pane_width}\t#{pane_height}\t#{window_width}\t#{" + ListOption + "}" +
	"\t#{pane_current_command}\t#{" + ProjectOption + "}\t#{" + AttachOption + "}\t#{" + HolderOption + "}\t#{" + ViewOption + "}" +
	"\t#{" + ReviewOption + "}\t#{" + TaskOption + "}\t#{window_zoomed_flag}\t#{pane_left}\t#{pane_top}\t#{pane_active}" +
	"\t#{pane-border-status}\t#{pane_current_path}"

// paneFields is how many fields a line of paneFormat has, the path's last.
const paneFields = 21

// Pane is one tmux pane.
type Pane struct {
	ID      string // "%3", kept while the pane lives, wherever it moves
	TTY     string // "ttys012", as ps names it
	Session string
	Window  string // "@2"
	W, H    int
	WinW    int    // the width of the pane's window
	List    bool   // the view runs here
	Command string // what runs in the foreground: "zsh", "node"
	Path    string // the foreground process's directory
	Project string // the window's ProjectOption, "" when unset
	Attach  string // the pane's AttachOption, "" when unset
	Holder  bool   // the pane's HolderOption is set
	View    string // the pane's ViewOption, "" when unset
	Review  string // the pane's ReviewOption, "" when unset
	Task    string // the window's TaskOption, "" when unset
	Zoomed  bool   // one pane of the window fills it, as Zoom does
	X, Y    int    // where the pane is in its window, its top-left cell
	Active  bool   // the pane is its window's active one
	Framed  bool   // the window draws a rule along the top of each pane
}

// Server is overview's tmux server. run runs one tmux command and returns
// its output, and feed does the same with stdin as the command's input.
type Server struct {
	run    func(args ...string) (string, error)
	feed   func(stdin string, args ...string) (string, error)
	socket string
}

// Socket is the path of the socket the server listens on.
func (s Server) Socket() string { return s.socket }

// At returns the server listening on the socket at path. conf is read only
// by the call that starts the server; "" leaves tmux to its defaults.
func At(path, conf string) Server {
	base := []string{"-S", path}
	if conf != "" {
		base = append(base, "-f", conf)
	}
	return Server{socket: path, run: runner(base), feed: feeder(base)}
}

// Named returns the server on the socket that tmux -L name would use, found
// once and then addressed by its path.
func Named(name, conf string) (Server, error) {
	path, err := SocketPath(name)
	if err != nil {
		return Server{}, err
	}
	return At(path, conf), nil
}

// SocketPath asks tmux where the socket that -L name uses lives. With no
// server running, start-server makes one only for the length of the call,
// since a server with no sessions exits, and -f /dev/null keeps that one
// from reading any settings.
func SocketPath(name string) (string, error) {
	out, err := runner([]string{"-L", name, "-f", "/dev/null"})("start-server", ";", "display-message", "-p", "#{socket_path}")
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(out)
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("tmux gave %q as the path of socket %s", out, name)
	}
	return path, nil
}

func runner(base []string) func(args ...string) (string, error) {
	feed := feeder(base)
	return func(args ...string) (string, error) { return feed("", args...) }
}

func feeder(base []string) func(stdin string, args ...string) (string, error) {
	return func(stdin string, args ...string) (string, error) {
		cmd := exec.Command("tmux", append(base, args...)...)
		cmd.Stdin = strings.NewReader(stdin)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("tmux %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
		}
		return string(out), nil
	}
}

// ParsePanes reads list-panes output in paneFormat. Malformed lines are
// skipped.
func ParsePanes(out string) []Pane {
	var panes []Pane
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, "\t", paneFields)
		if len(f) != paneFields || !strings.HasPrefix(f[0], "%") {
			continue
		}
		w, err1 := strconv.Atoi(f[4])
		h, err2 := strconv.Atoi(f[5])
		ww, err3 := strconv.Atoi(f[6])
		x, err4 := strconv.Atoi(f[16])
		y, err5 := strconv.Atoi(f[17])
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil {
			continue
		}
		panes = append(panes, Pane{
			ID: f[0], TTY: strings.TrimPrefix(f[1], "/dev/"), Session: f[2], Window: f[3],
			W: w, H: h, WinW: ww, List: f[7] == "1",
			Command: f[8], Project: f[9], Attach: f[10], Holder: f[11] == "1", View: f[12], Review: f[13], Task: f[14], Zoomed: f[15] == "1",
			X: x, Y: y, Active: f[18] == "1", Framed: f[19] == "top", Path: f[20],
		})
	}
	return panes
}

// PaneOf finds the pane whose process is pid in list-panes output in
// pidFormat. A process that tmux started directly, from an argument list
// rather than a shell command, is its pane's process.
func PaneOf(out string, pid int) (string, bool) {
	want := strconv.Itoa(pid)
	for _, line := range strings.Split(out, "\n") {
		id, p, ok := strings.Cut(line, "\t")
		if ok && p == want && strings.HasPrefix(id, "%") {
			return id, true
		}
	}
	return "", false
}

// pidFormat is how Self asks for each pane's process.
const pidFormat = "#{pane_id}\t#{pane_pid}"

// Self is the pane that pid, the caller's own process, runs as. ok is false
// when it runs in no pane on this server.
func (s Server) Self(pid int) (id string, ok bool, err error) {
	out, err := s.run("list-panes", "-a", "-F", pidFormat)
	if err != nil {
		return "", false, err
	}
	id, ok = PaneOf(out, pid)
	return id, ok, nil
}

// Panes lists every pane on the server. No server means no panes.
func (s Server) Panes() ([]Pane, error) {
	out, err := s.run("list-panes", "-a", "-F", paneFormat)
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
// missing, and leaves the window in the background. dir may be "". With no
// command the window runs tmux's default shell. The pane returned knows its
// ID, tty, session and window.
func (s Server) Spawn(session, dir string, env []string, command ...string) (Pane, error) {
	var args []string
	if _, err := s.run("has-session", "-t", "="+session); err == nil {
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
	args = append(args, "-P", "-F", "#{pane_id}\t#{pane_tty}\t#{session_name}\t#{window_id}")
	out, err := s.run(append(args, argv(command)...)...)
	if err != nil {
		return Pane{}, err
	}
	f := strings.Split(strings.TrimSpace(out), "\t")
	if len(f) != 4 || !strings.HasPrefix(f[0], "%") {
		return Pane{}, fmt.Errorf("tmux %s: unexpected output %q", args[0], out)
	}
	return Pane{ID: f[0], TTY: strings.TrimPrefix(f[1], "/dev/"), Session: f[2], Window: f[3], Path: dir}, nil
}

// Split runs command in a new pane above target, size tall ("40%" or a
// number of lines), in dir, and focuses it. The pane returned knows its
// ID, tty, session and window.
func (s Server) Split(target, dir, size string, env []string, command ...string) (Pane, error) {
	return s.create([]string{"split-window", "-v", "-b", "-l", size, "-t", target}, dir, env, command)
}

// Resize makes pane size tall, as Split takes it.
func (s Server) Resize(pane, size string) error {
	_, err := s.run("resize-pane", "-t", pane, "-y", size)
	return err
}

// SetWidth makes pane cols columns wide, taking the columns from or giving
// them to the panes beside it.
func (s Server) SetWidth(pane string, cols int) error {
	_, err := s.run("resize-pane", "-t", pane, "-x", strconv.Itoa(cols))
	return err
}

// Zoom toggles whether pane fills its window. The listing's Zoomed says
// whether a window is zoomed now.
func (s Server) Zoom(pane string) error {
	_, err := s.run("resize-pane", "-Z", "-t", pane)
	return err
}

// SetZoom zooms pane's window to pane when on, and ends the window's zoom
// when not, whatever it was before. tmux reads the window's zoom and acts
// on it in one command, so no listing taken in between can make it toggle
// the wrong way. A pane alone in its window cannot be zoomed, and then the
// window stays as it was. The zoomed pane draws its own frame, so the
// window's rules go while it is zoomed, as SetFrame has it, and come back
// with the zoom's end.
func (s Server) SetZoom(pane string, on bool) error {
	then, otherwise := "", "resize-pane -Z -t "+pane
	if !on {
		then, otherwise = otherwise, ""
	}
	_, err := s.run(append([]string{"if-shell", "-F", "-t", pane, "#{window_zoomed_flag}", then, otherwise, ";"}, frameArgs(pane, !on)...)...)
	return err
}

// SetFrame turns the rules along the tops of the panes of pane's window
// on or off.
func (s Server) SetFrame(pane string, on bool) error {
	_, err := s.run(frameArgs(pane, on)...)
	return err
}

func frameArgs(pane string, on bool) []string {
	status := "off"
	if on {
		status = "top"
	}
	return []string{"set-option", "-w", "-t", pane, "pane-border-status", status}
}

// Colours are the colours Frame draws the rules in, as tmux takes them:
// "#rrggbb" or "colour123".
type Colours struct{ Accent, Border, Text string }

// lit is a format that is true for a pane whose rule is drawn in the
// accent colour: the pane the view selected, and the pane with the
// keyboard when that is not the view's own, which lights only when it is
// the one selected.
const lit = "#{?#{" + SelectOption + "},1,#{?pane_active,#{?#{" + ListOption + "},0,1},0}}"

// FrameFormat and FrameStyle are what Frame sets pane-border-format and
// both pane border styles to. The title is the pane's TitleOption, or
// what runs in it while it has none.
const (
	FrameFormat = "#{?" + lit + ",#[fg=#{@overview-accent} bold],#[fg=#{@overview-text}]} " +
		"#{?#{" + TitleOption + "},#{" + TitleOption + "},#{pane_current_command}} "
	FrameStyle = "#{?" + lit + ",fg=#{@overview-accent},fg=#{@overview-border}}"
)

// Frame gives every pane on the server a rule along its top, the pane's
// title set into it, and single lines between panes. The colours are
// session's, so views of two config directories on one server can each
// draw in their own theme.
func (s Server) Frame(session string, c Colours) error {
	var args []string
	for _, o := range [][2]string{{"@overview-accent", c.Accent}, {"@overview-border", c.Border}, {"@overview-text", c.Text}} {
		args = append(args, "set-option", "-t", "="+session+":", o[0], o[1], ";")
	}
	for _, o := range [][2]string{
		{"pane-border-status", "top"}, {"pane-border-lines", "single"}, {"pane-border-format", FrameFormat},
		{"pane-border-style", FrameStyle}, {"pane-active-border-style", FrameStyle},
	} {
		args = append(args, "set-option", "-g", "-w", o[0], o[1], ";")
	}
	_, err := s.run(args[:len(args)-1]...)
	return err
}

// PaneSet is a pane option to set: its pane, name and value.
type PaneSet struct{ Pane, Name, Value string }

// SetPaneOptions sets several pane options in one tmux command, in order.
// tmux stops at the first that fails, as it does when a pane has gone.
func (s Server) SetPaneOptions(sets []PaneSet) error {
	if len(sets) == 0 {
		return nil
	}
	var args []string
	for _, ps := range sets {
		args = append(args, "set-option", "-p", "-t", ps.Pane, ps.Name, ps.Value, ";")
	}
	_, err := s.run(args[:len(args)-1]...)
	return err
}

// Live takes pane out of copy mode, where a paste would not be
// bracketed and Enter would not reach the program, and returns what runs
// there in the foreground and the text it shows, without colours. One
// tmux call does it all, so the screen is the one the program draws.
func (s Server) Live(pane string) (command, screen string, err error) {
	out, err := s.run("copy-mode", "-q", "-t", pane, ";",
		"display-message", "-p", "-t", pane, "#{pane_current_command}", ";",
		"capture-pane", "-p", "-t", pane)
	if err != nil {
		return "", "", err
	}
	command, screen, _ = strings.Cut(out, "\n")
	return command, screen, nil
}

// Paste types text into pane as one bracketed paste, then presses Enter.
// The text goes through stdin, since tmux limits how long a command's
// arguments may be, into a buffer named buffer that the paste deletes.
// tmux runs the three commands in order, so nothing typed in between
// lands inside the message.
func (s Server) Paste(pane, buffer, text string) error {
	_, err := s.feed(text, "load-buffer", "-b", buffer, "-", ";",
		"paste-buffer", "-p", "-d", "-b", buffer, "-t", pane, ";",
		"send-keys", "-t", pane, "Enter")
	return err
}

// Move puts pane list in target's window as a full-height column on the
// left, size wide, and makes that window current and list its active pane.
// A window that list leaves empty closes.
func (s Server) Move(list, target, size string) error {
	_, err := s.run("join-pane", "-h", "-b", "-f", "-l", size, "-s", list, "-t", target)
	return err
}

// Layout reads a window's layout, as select-layout takes it.
func (s Server) Layout(window string) (string, error) {
	out, err := s.run("display-message", "-p", "-t", window, "#{window_layout}")
	return strings.TrimSpace(out), err
}

// SetLayout gives a window a layout that Layout read.
func (s Server) SetLayout(window, layout string) error {
	_, err := s.run("select-layout", "-t", window, layout)
	return err
}

// Select focuses a pane.
func (s Server) Select(pane string) error {
	_, err := s.run("select-pane", "-t", pane)
	return err
}

// SelectBeside focuses the pane to the right of pane. Of several there,
// tmux picks the one used most recently.
func (s Server) SelectBeside(pane string) error {
	_, err := s.run("select-pane", "-R", "-t", pane)
	return err
}

// KillPane closes a pane and ends what runs in it.
func (s Server) KillPane(pane string) error {
	_, err := s.run("kill-pane", "-t", pane)
	return err
}

// KillWindow closes a window and ends everything that runs in it.
func (s Server) KillWindow(window string) error {
	_, err := s.run("kill-window", "-t", window)
	return err
}

// Detach detaches the client the calling pane is shown in.
func (s Server) Detach() error {
	_, err := s.run("detach-client")
	return err
}

// SetWindowOption sets an option on target's window. target may name the
// window or any pane in it.
func (s Server) SetWindowOption(target, name, value string) error {
	_, err := s.run("set-option", "-w", "-t", target, name, value)
	return err
}

// SetPaneOption sets a pane option.
func (s Server) SetPaneOption(pane, name, value string) error {
	_, err := s.run("set-option", "-p", "-t", pane, name, value)
	return err
}

// Respawn kills what runs in pane and runs command there instead. The pane
// keeps its place, size and options.
func (s Server) Respawn(pane string, env []string, command ...string) error {
	args := []string{"respawn-pane", "-k", "-t", pane}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	_, err := s.run(append(args, argv(command)...)...)
	return err
}

// argv is command as tmux runs it with nothing parsing it: tmux hands a
// lone argument to the shell as a command line, so one goes to sh as $0
// of a script that only execs it, in place and unsplit. With no command,
// tmux runs its default shell.
func argv(command []string) []string {
	if len(command) == 1 {
		return []string{"/bin/sh", "-c", `exec "$0"`, command[0]}
	}
	return command
}

// Focused says whether pane has the keyboard: it is the active pane of its
// session's current window, and a client is attached to the session. A
// pane learns this from no focus event when it turns focus reporting on,
// so the view asks once as it starts.
func (s Server) Focused(pane string) (bool, error) {
	out, err := s.run("display-message", "-p", "-t", pane, "#{pane_active}#{window_active}#{?session_attached,1,0}")
	return strings.TrimSpace(out) == "111", err
}

// MarkList marks pane as the one the view runs in.
func (s Server) MarkList(pane string) error {
	_, err := s.run("set-option", "-p", "-t", pane, ListOption, "1")
	return err
}
