//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// pane is one line of the private server's pane listing.
type pane struct {
	id, window, session, command string
	list                         bool
	left, top, w, h, winH, pid   int
}

const paneFormat = "#{pane_id}\t#{window_id}\t#{session_name}\t#{pane_current_command}\t#{@overview-list}\t" +
	"#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}\t#{window_height}\t#{pane_pid}"

// server is a private tmux server, addressed by a name that is never
// overview's own.
type server struct {
	t    *testing.T
	name string
}

func private(t *testing.T, name string) server {
	t.Helper()
	if name == "overview" || !strings.HasPrefix(name, "ovtest-e2e-") {
		t.Fatalf("refusing to use the tmux socket %q", name)
	}
	s := server{t, name}
	t.Cleanup(func() {
		// tmux leaves the socket file behind, so it goes too.
		path, _ := s.run("-f", "/dev/null", "start-server", ";", "display-message", "-p", "#{socket_path}")
		exec.Command("tmux", "-L", name, "kill-server").Run() //nolint:errcheck
		if filepath.Base(path) == name {
			os.Remove(path)
		}
	})
	return s
}

func (s server) run(args ...string) (string, error) {
	out, err := exec.Command("tmux", append([]string{"-L", s.name}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (s server) must(args ...string) string {
	s.t.Helper()
	out, err := s.run(args...)
	if err != nil {
		s.t.Fatalf("tmux %v: %v: %s", args, err, out)
	}
	return out
}

func (s server) panes() []pane {
	out, err := s.run("list-panes", "-a", "-F", paneFormat)
	if err != nil {
		return nil
	}
	var ps []pane
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 11 {
			continue
		}
		n := func(i int) int { v, _ := strconv.Atoi(f[i]); return v }
		ps = append(ps, pane{id: f[0], window: f[1], session: f[2], command: f[3], list: f[4] == "1",
			left: n(5), top: n(6), w: n(7), h: n(8), winH: n(9), pid: n(10)})
	}
	return ps
}

// waitFor polls until cond holds, failing with what it last said.
func waitFor(t *testing.T, what string, cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	last := ""
	for time.Now().Before(deadline) {
		ok, say := cond()
		if ok {
			return
		}
		last = say
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s: %s", what, last)
}

// beside reports whether the list sits in the window of pane id, leftmost,
// full height, w wide (w 0 for any width).
func beside(ps []pane, id string, w int) (bool, string) {
	var list, with pane
	for _, p := range ps {
		if p.list {
			list = p
		}
		if p.id == id {
			with = p
		}
	}
	ok := list.id != "" && with.id != "" && list.window == with.window &&
		list.left == 0 && list.top == 0 && list.h == list.winH && (w == 0 || list.w == w)
	return ok, fmt.Sprintf("list %+v, %s %+v", list, id, with)
}

func listPane(ps []pane) pane {
	for _, p := range ps {
		if p.list {
			return p
		}
	}
	return pane{}
}

// The built binary, launched in a terminal of its own: the view marks its
// pane and stands beside the holder, c opens a session window that the list
// moves into, a split there stays with its window while the cursor is
// elsewhere, and the list keeps the width it is dragged to. A relaunch with
// new flags restarts the view in place, and one with the same flags does not.
func TestOverviewInTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	ov := private(t, fmt.Sprintf("ovtest-e2e-%d", os.Getpid()))
	term := private(t, fmt.Sprintf("ovtest-e2e-term-%d", os.Getpid()))

	dir := t.TempDir()
	bin := filepath.Join(dir, "bin", "overview")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	// The fake claude lists what the test writes to listing, and as a new
	// session it just waits, so its pane stays open.
	listing := filepath.Join(dir, "listing.json")
	fake := filepath.Join(dir, "claude")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = agents ]; then cat %q 2>/dev/null || echo '[]'; exit 0; fi\nexec sleep 600\n", listing)
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "config")
	home := filepath.Join(dir, ".claude-e2e")
	work := filepath.Join(dir, "work")
	for _, d := range []string{config, home, work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(config, "config"), []byte("theme = revdiff\nclaude = "+fake+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The terminal is a pane of the second server. env -u TMUX, since tmux
	// refuses to attach from inside another tmux.
	term.must("-f", "/dev/null", "new-session", "-d", "-x", "200", "-y", "50", "-c", work,
		"env", "-u", "TMUX", bin, "--socket", ov.name, "--config", config, "--home", home)

	var holder string
	waitFor(t, "the view and its holder", func() (bool, string) {
		holder, _ = ov.run("show-options", "-qv", "-t", "overview-e2e", "@overview-holder")
		ok, say := beside(ov.panes(), holder, 0)
		return ok && holder != "", "holder " + holder + ": " + say
	})
	list := listPane(ov.panes())
	if clients := ov.must("list-clients", "-F", "#{client_tty}"); clients == "" {
		t.Fatal("the terminal never attached")
	}
	// The view runs as its pane's own process, with the socket's path.
	args, err := exec.Command("ps", "-o", "args=", "-p", strconv.Itoa(list.pid)).Output()
	path := ov.must("display-message", "-p", "#{socket_path}")
	if err != nil || !strings.Contains(string(args), " view --socket "+path+" --session overview-e2e ") ||
		!strings.Contains(string(args), "--theme revdiff") || !strings.Contains(string(args), "--claude "+fake) {
		t.Fatalf("the list pane runs %q, %v", args, err)
	}

	// c starts a session, and the list moves into its window.
	var sessions []pane
	start := func() pane {
		t.Helper()
		before := map[string]bool{}
		for _, p := range ov.panes() {
			before[p.id] = true
		}
		ov.must("send-keys", "-t", list.id, "c")
		var fresh pane
		waitFor(t, "a session window", func() (bool, string) {
			for _, p := range ov.panes() {
				if !before[p.id] && p.command == "sleep" {
					fresh = p
				}
			}
			if fresh.id == "" {
				return false, fmt.Sprintf("panes %+v", ov.panes())
			}
			return beside(ov.panes(), fresh.id, 0)
		})
		// Claude Code lists the session from now on.
		sessions = append(sessions, fresh)
		var live []map[string]any
		for i, s := range sessions {
			live = append(live, map[string]any{"pid": s.pid, "cwd": work, "kind": "interactive",
				"sessionId": fmt.Sprintf("s-%d", i), "name": fmt.Sprintf("e2e-%d", i), "status": "idle",
				"startedAt": time.Now().UnixMilli()})
		}
		b, _ := json.Marshal(live)
		if err := os.WriteFile(listing, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return fresh
	}
	start()
	ov.must("select-pane", "-t", list.id)
	start()
	ov.must("select-pane", "-t", list.id)

	// The list shows both sessions; the one higher up is first.
	var first, second pane
	waitFor(t, "both sessions in the list", func() (bool, string) {
		screen, _ := ov.run("capture-pane", "-p", "-t", list.id)
		a, b := strings.Index(screen, "e2e-0"), strings.Index(screen, "e2e-1")
		first, second = sessions[0], sessions[1]
		if b < a {
			first, second = second, first
		}
		return a >= 0 && b >= 0, screen
	})
	// g puts the cursor on the first session.
	ov.must("send-keys", "-t", list.id, "g")
	waitFor(t, "the list beside the first session", func() (bool, string) { return beside(ov.panes(), first.id, 0) })

	// A split in the first session's window, and the list dragged wider.
	split := ov.must("split-window", "-d", "-v", "-t", first.id, "-P", "-F", "#{pane_id}", "sleep 600")
	ov.must("resize-pane", "-t", list.id, "-x", "57")
	waitFor(t, "the drag", func() (bool, string) { return beside(ov.panes(), first.id, 57) })
	// The view rereads tmux's panes twice a second; let it see the width.
	time.Sleep(1200 * time.Millisecond)

	inWindow := func(id string) string {
		for _, p := range ov.panes() {
			if p.id == id {
				return p.window
			}
		}
		return ""
	}
	window := inWindow(first.id)
	ov.must("send-keys", "-t", list.id, "j")
	waitFor(t, "the list beside the second session, as wide", func() (bool, string) {
		return beside(ov.panes(), second.id, 57)
	})
	if got := inWindow(split); got != window {
		t.Fatalf("the split moved from %s to %s", window, got)
	}
	ov.must("send-keys", "-t", list.id, "k")
	waitFor(t, "the list back beside the first session and its split", func() (bool, string) {
		ps := ov.panes()
		ok, say := beside(ps, first.id, 57)
		if ok2, _ := beside(ps, split, 57); !ok2 {
			return false, "split elsewhere: " + say
		}
		return ok, say
	})

	// A second launch with other flags restarts the view in its pane and
	// leaves the sessions running; one with the same flags only attaches.
	relaunch := func(flags ...string) {
		t.Helper()
		term.must(append([]string{"new-window", "-c", work, "env", "-u", "TMUX", bin,
			"--socket", ov.name, "--config", config, "--home", home}, flags...)...)
	}
	relaunch("--theme", "one-light")
	waitFor(t, "the view restarted with the new theme", func() (bool, string) {
		l := listPane(ov.panes())
		args, _ := exec.Command("ps", "-o", "args=", "-p", strconv.Itoa(l.pid)).Output()
		return l.id == list.id && l.pid != list.pid && strings.Contains(string(args), "--theme one-light"), string(args)
	})
	list = listPane(ov.panes())
	relaunch("--theme", "one-light")
	waitFor(t, "a second client", func() (bool, string) {
		clients := ov.must("list-clients", "-F", "#{client_tty}")
		return len(strings.Fields(clients)) == 3, clients
	})
	if l := listPane(ov.panes()); l.pid != list.pid {
		t.Fatalf("the same flags restarted the view: pid %d, was %d", l.pid, list.pid)
	}
	for _, s := range append(sessions, pane{id: split}) {
		if inWindow(s.id) == "" {
			t.Fatalf("session pane %s went with the restart", s.id)
		}
	}
}
