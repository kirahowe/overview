package tui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"overview/internal/fleet"
	"overview/internal/tmux"
)

// viewArgs is the pane option holding the flags the view was started with,
// so a relaunch with other flags can tell it is running the wrong view.
const viewArgs = "@overview-args"

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
// socket, and attaches this terminal to it. A session already running is
// attached as it is, unless given carries flags other than the view's own,
// which restart it in place; one whose view has quit gets a new view in a
// window of its own. dir is overview's config directory. given is the flags
// as typed, and view the settings they resolved to, which the view is
// started with. On success it does not return.
//
// Run inside another tmux, the attach fails with tmux's own message about
// nesting, which says what to do.
func Launch(home fleet.Home, socket, dir string, given, view []string) error {
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
	// From here on the server is addressed by its path, which every
	// overview process started below is given.
	srv, err := tmux.Named(socket, conf)
	if err != nil {
		return err
	}
	session := SessionName(home)
	if err := ensureView(srv, session, viewCommand(exe, srv.Socket(), session, view), given); err != nil {
		return err
	}
	// The client passes the terminal's environment, TERM and the rest, on
	// to tmux unread.
	return syscall.Exec(tmuxPath, []string{"tmux", "-S", srv.Socket(), "attach-session", "-t", "=" + session}, os.Environ())
}

// viewCommand is the command line that runs the view of session on the
// server at socket, with the given flags. It is an argument list, so tmux
// runs it with no shell between and the view is its pane's own process.
func viewCommand(exe, socket, session string, flags []string) []string {
	return append([]string{exe, "view", "--socket", socket, "--session", session}, flags...)
}

// ensureView starts the view's session when it is missing, and a new view
// in a new current window of it when the old one has quit. The view moves
// itself beside a session as soon as it runs. A view still running is
// restarted when given carries flags other than the ones it was started
// with; with no flags it is kept, so a plain relaunch only reattaches.
func ensureView(srv tmux.Server, session string, view, given []string) error {
	panes, err := srv.Panes()
	if err != nil {
		return err
	}
	args := quoteArgs(given)
	exists := false
	for _, p := range panes {
		if p.Session != session {
			continue
		}
		if p.List {
			if len(given) == 0 || srv.PaneOption(p.ID, viewArgs) == args {
				return nil
			}
			if err := srv.Respawn(p.ID, nil, view...); err != nil {
				return err
			}
			return srv.SetPaneOption(p.ID, viewArgs, args)
		}
		exists = true
	}
	open := srv.Spawn
	if exists {
		open = func(session, _ string, env []string, command ...string) (tmux.Pane, error) {
			return srv.Open(session, env, command...)
		}
	}
	p, err := open(session, "", nil, view...)
	if err != nil {
		return err
	}
	return srv.SetPaneOption(p.ID, viewArgs, args)
}

// quoteArgs writes args as one string that tells any two lists apart.
func quoteArgs(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = strconv.Quote(a)
	}
	return strings.Join(q, " ")
}
