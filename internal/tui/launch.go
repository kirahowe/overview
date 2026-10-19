package tui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"overview/internal/fleet"
	"overview/internal/tmux"
)

// defaultConf is written to the config directory on the first launch, and
// is the user's to change from then on.
const defaultConf = `# overview's tmux. The server reads this when it starts. To apply a change
# to a running server, press ctrl+b : inside overview and run source-file
# with this file's path.

set -g default-terminal "tmux-256color"
set -as terminal-features ",xterm-ghostty:RGB:extkeys:clipboard:focus:sync:hyperlinks"
# always: tmux asks the terminal for extended keys even when the focused app
# has not, which is what lets ctrl+[ arrive apart from a plain Esc.
set -s extended-keys always
set -s extended-keys-format csi-u
set -s escape-time 10
set -g focus-events on
set -g mouse on
set -g status off
set -s set-clipboard on
set -g allow-passthrough on
set -g history-limit 50000

# ctrl+[ moves from the list to the session beside it, and back to the list
# from any pane, however split. The list is always the top-left pane. A
# terminal that cannot send extended keys sends Esc for it instead, which
# goes to the pane.
bind -n C-[ if -F "#{@overview-list}" "select-pane -R" "select-pane -t '{top-left}'"

# Splits open in the directory of the pane they split.
bind % split-window -h -c "#{pane_current_path}"
bind '"' split-window -v -c "#{pane_current_path}"
`

// writeConf writes the default tmux settings to path when there is no file
// there. A file that exists is the user's and is left as it is.
func writeConf(path string) error {
	if _, err := os.Stat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(defaultConf), 0o644)
}

// Launch puts the view in overview's tmux server, on the socket named
// socket, and attaches this terminal to it. dir is overview's config
// directory, settings the flags the view runs with, every one resolved,
// and cwd where overview was started, where c and t start when the list is
// empty. A view running with those settings and this build of overview is
// kept, so a plain relaunch only reattaches. One running with other
// settings, or from another build, is restarted in its pane, and the
// sessions beside it keep running. On success Launch does not return.
//
// Run inside another tmux, the attach fails with tmux's own message about
// nesting, which says what to do.
func Launch(home fleet.Home, socket, dir string, settings []string, cwd string) error {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return errors.New("overview runs its sessions in tmux, and tmux is not installed")
	}
	conf := filepath.Join(dir, "tmux.conf")
	if err := writeConf(conf); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	build, err := buildOf(exe)
	if err != nil {
		return err
	}
	// From here on the server is addressed by its path, which every
	// overview process started below is given.
	srv, err := tmux.Named(socket, conf)
	if err != nil {
		return err
	}
	session := SessionName(home)
	if err := ensureView(srv, session, viewCommand(exe, srv.Socket(), session, cwd, settings), viewKey(settings, build)); err != nil {
		return err
	}
	// The client passes the terminal's environment, TERM and the rest, on
	// to tmux unread.
	return syscall.Exec(tmuxPath, []string{"tmux", "-S", srv.Socket(), "attach-session", "-t", "=" + session}, os.Environ())
}

// buildOf names the build of the binary at exe by its path, size and
// modification time, so a rebuilt overview tells its view is out of date.
func buildOf(exe string) (string, error) {
	info, err := os.Stat(exe)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %d %d", exe, info.Size(), info.ModTime().UnixNano()), nil
}

// viewKey is what the view's pane records about what it runs: its
// settings and the build, as one string that tells any two apart.
func viewKey(settings []string, build string) string {
	return quoteArgs(append(slices.Clone(settings), build))
}

// viewCommand is the command line that runs the view of session on the
// server at socket, started from cwd, with the given settings. It is an
// argument list, so tmux runs it with no shell between and the view is its
// pane's own process.
func viewCommand(exe, socket, session, cwd string, settings []string) []string {
	return append([]string{exe, "view", "--socket-path", socket, "--session", session, "--cwd", cwd}, settings...)
}

// viewStep is what a launch does to the view.
type viewStep int

const (
	keepView    viewStep = iota // it runs what is wanted
	respawnView                 // restart it in its pane
	openView                    // the view has quit: a new one in a new current window
	spawnView                   // no session yet: start it with the view
)

// viewPlan decides what a launch does to session's view, given every pane
// on the server and the key of the view it wants, and names the view's
// pane for keepView and respawnView.
func viewPlan(panes []tmux.Pane, session, want string) (viewStep, string) {
	exists := false
	for _, p := range panes {
		switch {
		case p.Session != session:
		case p.List && p.View == want:
			return keepView, p.ID
		case p.List:
			return respawnView, p.ID
		default:
			exists = true
		}
	}
	if exists {
		return openView, ""
	}
	return spawnView, ""
}

// ensureView carries out viewPlan, and records want on the view's pane.
// The view moves itself beside a session as soon as it runs.
func ensureView(srv tmux.Server, session string, view []string, want string) error {
	panes, err := srv.Panes()
	if err != nil {
		return err
	}
	step, pane := viewPlan(panes, session, want)
	switch step {
	case keepView:
		return nil
	case respawnView:
		err = srv.Respawn(pane, nil, view...)
	case openView:
		var p tmux.Pane
		p, err = srv.Open(session, nil, view...)
		pane = p.ID
	case spawnView:
		var p tmux.Pane
		p, err = srv.Spawn(session, "", nil, view...)
		pane = p.ID
	}
	if err != nil {
		return err
	}
	return srv.SetPaneOption(pane, tmux.ViewOption, want)
}

// quoteArgs writes args as one string that tells any two lists apart.
func quoteArgs(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = strconv.Quote(a)
	}
	return strings.Join(q, " ")
}
