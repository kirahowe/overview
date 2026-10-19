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

// envSession tells the view which tmux session it runs in. Launch sets it on
// the view's pane, and its absence is how a plain `overview` knows to launch.
const envSession = "OVERVIEW_TMUX"

// viewArgs is the pane option holding the flags the view was started with,
// so a relaunch with other flags can tell it is running the wrong view.
const viewArgs = "@overview-args"

// listSize is the list's share of the window when Launch puts the view back
// beside a stage that outlived it.
const listSize = "30%"

// InTmux reports whether this process is the view's pane in overview's tmux.
func InTmux() bool { return os.Getenv(envSession) != "" }

// defaultConf is written to the config directory on the first launch, and
// is the user's to change from then on.
const defaultConf = `# overview's tmux. The server reads this when it starts. After a change:
#   tmux -L overview source-file ~/.config/overview/tmux.conf

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

# ctrl+[ moves between the list and the session beside it. A terminal that
# cannot send extended keys sends Esc for it instead, which goes to the pane.
bind -n C-[ if -F "#{@overview-list}" "select-pane -R" "select-pane -L"
`

// Launch puts the view in overview's tmux server and attaches this terminal
// to it. A session already running is attached as it is, unless this launch
// gives the view other flags, which restart it in place; one whose view has
// quit gets a new view beside its stage. args are the view's own flags.
// On success it does not return.
func Launch(home fleet.Home, args []string) error {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return errors.New("overview runs its sessions in tmux, and tmux is not installed")
	}
	if os.Getenv("TMUX") != "" && !strings.Contains(os.Getenv("TMUX"), "/"+tmux.Socket+",") {
		return errors.New("run overview outside tmux: it starts a tmux of its own")
	}
	dir, err := ConfigDir()
	if err != nil {
		return err
	}
	conf := filepath.Join(dir, "tmux.conf")
	if _, err := os.Stat(conf); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(conf, []byte(defaultConf), 0o644); err != nil {
			return err
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	srv := tmux.New(conf)
	session := SessionName(home)
	if err := ensureView(srv, session, home, append([]string{exe}, args...)); err != nil {
		return err
	}
	env := os.Environ()
	for i, e := range env {
		if strings.HasPrefix(e, "TMUX=") {
			env = append(env[:i], env[i+1:]...)
			break
		}
	}
	return syscall.Exec(tmuxPath, []string{"tmux", "-L", tmux.Socket, "attach-session", "-t", "=" + session}, env)
}

// ensureView starts the view's session when it is missing, and a new view
// in it when the old one has quit. A view still running is restarted when
// view carries flags other than its own; with no flags it is kept, so a
// plain relaunch only reattaches.
func ensureView(srv tmux.Server, session string, home fleet.Home, view []string) error {
	panes, err := srv.Panes()
	if err != nil {
		return err
	}
	env := []string{envSession + "=" + session, "CLAUDE_CONFIG_DIR=" + string(home)}
	args := quoteArgs(view[1:])
	var stage string
	for _, p := range panes {
		if p.Session != session {
			continue
		}
		if p.List {
			if len(view) == 1 || srv.PaneOption(p.ID, viewArgs) == args {
				return nil
			}
			if err := srv.Respawn(p.ID, env, view...); err != nil {
				return err
			}
			return srv.SetPaneOption(p.ID, viewArgs, args)
		}
		stage = p.ID
	}
	var p tmux.Pane
	if stage == "" {
		if p, err = srv.Spawn(session, "", env, view...); err != nil {
			return err
		}
	} else {
		if p, err = srv.SplitLeft(stage, listSize, env, view...); err != nil {
			return err
		}
		if err := srv.Select(p.ID); err != nil {
			return err
		}
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
