package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"overview/internal/tmux"
	"overview/internal/tmuxtest"
)

// privateServer is a real tmux server on a private socket, which reads
// conf when it starts. It is killed and its socket removed when the test
// ends.
func privateServer(t *testing.T, conf string) (tmux.Server, tmuxtest.Socket) {
	t.Helper()
	sock := tmuxtest.New(t, conf)
	return tmux.At(sock.Path, conf), sock
}

func TestViewCommandAddressesTheServerByPath(t *testing.T) {
	got := viewCommand("/bin/overview", "/tmp/tmux-501/ov", "overview-work", []string{"--theme", "revdiff"})
	want := []string{"/bin/overview", "view", "--socket", "/tmp/tmux-501/ov", "--session", "overview-work", "--theme", "revdiff"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestEnsureView(t *testing.T) {
	srv, sock := privateServer(t, "/dev/null")
	// The view's stand-in marks its own pane, as Run does.
	view := []string{"sh", "-c", `tmux -S "$1" set-option -p -t "$TMUX_PANE" @overview-list 1; sleep 300`, "sh", srv.Socket()}

	count := func() (list, all int) {
		panes, err := srv.Panes()
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range panes {
			if p.Session == "ov" {
				all++
				if p.List {
					list++
				}
			}
		}
		return list, all
	}
	waitForList := func() {
		for range 50 {
			if l, _ := count(); l > 0 {
				return
			}
			exec.Command("sleep", "0.05").Run() //nolint:errcheck
		}
		t.Fatal("the view never marked its pane")
	}

	if err := ensureView(srv, "ov", view, nil); err != nil {
		t.Fatal(err)
	}
	waitForList()
	if l, all := count(); l != 1 || all != 1 {
		t.Fatalf("new session: %d list panes of %d", l, all)
	}
	// A view already there is left alone.
	if err := ensureView(srv, "ov", view, nil); err != nil {
		t.Fatal(err)
	}
	if l, all := count(); l != 1 || all != 1 {
		t.Fatalf("second launch: %d list panes of %d", l, all)
	}
	// Other flags restart the view in its own pane; no flags reattach.
	pid := func() string {
		panes, _ := srv.Panes()
		out, _ := sock.Run("display", "-p", "-t", panes[0].ID, "#{pane_pid}")
		return out
	}
	before := pid()
	if err := ensureView(srv, "ov", []string{"sleep", "300"}, nil); err != nil {
		t.Fatal(err)
	}
	if pid() != before {
		t.Fatal("a launch without flags restarted the view")
	}
	themed := []string{"--theme", "one-dark"}
	if err := ensureView(srv, "ov", view, themed); err != nil {
		t.Fatal(err)
	}
	waitForList()
	if l, all := count(); l != 1 || all != 1 || pid() == before {
		t.Fatalf("new flags: %d list panes of %d, restarted %v", l, all, pid() != before)
	}
	before = pid()
	if err := ensureView(srv, "ov", view, themed); err != nil {
		t.Fatal(err)
	}
	if pid() != before {
		t.Fatal("the same flags restarted the view")
	}
	// The view quits while a session lives on: a new view opens in a
	// window of its own, which is the one shown.
	panes, _ := srv.Panes()
	other, err := srv.Spawn("ov", "", nil, "sleep 300")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sock.Run("kill-pane", "-t", panes[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := ensureView(srv, "ov", view, nil); err != nil {
		t.Fatal(err)
	}
	waitForList()
	if l, all := count(); l != 1 || all != 2 {
		t.Fatalf("relaunch: %d list panes of %d", l, all)
	}
	panes, _ = srv.Panes()
	where := map[string]tmux.Pane{}
	for _, p := range panes {
		where[p.ID] = p
	}
	shown, _ := sock.Run("display-message", "-p", "-t", "=ov:", "#{pane_id}")
	if p := where[strings.TrimSpace(shown)]; !p.List || p.Window == where[other.ID].Window {
		t.Fatalf("shown %q, panes %+v", shown, panes)
	}
}

// The settings are written when there are none, and a file that exists,
// edited or not, is left alone.
func TestWriteConfOnlyWhenMissing(t *testing.T) {
	dir := t.TempDir()
	fresh, mine := filepath.Join(dir, "a", "tmux.conf"), filepath.Join(dir, "mine.conf")
	if err := os.WriteFile(mine, []byte("set -g status on\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{fresh: defaultConf, mine: "set -g status on\n"} {
		if err := writeConf(path); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(path); string(b) != want {
			t.Errorf("%s holds %.40q", filepath.Base(path), b)
		}
	}
}

// ctrl+[ from a pane deep in the splits of a session's window goes to the
// list, and from the list to the right. tmux runs the binding's command as
// it would for the key.
func TestCtrlBracketFindsTheListFromAnySplit(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "tmux.conf")
	if err := os.WriteFile(conf, []byte(defaultConf), 0o644); err != nil {
		t.Fatal(err)
	}
	var binding string
	for _, line := range strings.Split(defaultConf, "\n") {
		if cmd, ok := strings.CutPrefix(line, "bind -n C-[ "); ok {
			binding = cmd
		}
	}
	key := filepath.Join(dir, "key.conf")
	if err := os.WriteFile(key, []byte(binding+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, sock := privateServer(t, conf)
	run := func(args ...string) string {
		t.Helper()
		out, err := sock.Run(args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	list := run("new-session", "-d", "-s", "ov", "-x", "200", "-y", "50", "-P", "-F", "#{pane_id}", "sleep 300")
	if err := srv.MarkList(list); err != nil {
		t.Fatal(err)
	}
	a := run("new-window", "-d", "-t", "=ov:", "-P", "-F", "#{pane_id}", "sleep 300")
	b := run("split-window", "-d", "-h", "-t", a, "-P", "-F", "#{pane_id}", "sleep 300")
	deep := run("split-window", "-d", "-v", "-t", b, "-P", "-F", "#{pane_id}", "sleep 300")
	if err := srv.Move(list, deep, "60"); err != nil {
		t.Fatal(err)
	}
	focused := func() string { return run("display-message", "-p", "-t", "=ov:", "#{pane_id}") }
	for _, from := range []string{deep, a, b} {
		run("select-pane", "-t", from)
		run("source-file", key)
		if got := focused(); got != list {
			t.Fatalf("ctrl+[ from %s went to %s, not the list %s", from, got, list)
		}
		run("source-file", key)
		if got := focused(); got == list {
			t.Fatal("ctrl+[ from the list stayed there")
		}
	}
}
