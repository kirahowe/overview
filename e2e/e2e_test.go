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
	list, holder                 bool
	left, top, w, h, winH, pid   int
}

// The pid comes last: run trims the output, and with it an empty last field.
const paneFormat = "#{pane_id}\t#{window_id}\t#{session_name}\t#{pane_current_command}\t#{@overview-list}\t#{@overview-holder}\t" +
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
		if len(f) != 12 {
			continue
		}
		n := func(i int) int { v, _ := strconv.Atoi(f[i]); return v }
		ps = append(ps, pane{id: f[0], window: f[1], session: f[2], command: f[3], list: f[4] == "1", holder: f[5] == "1",
			left: n(6), top: n(7), w: n(8), h: n(9), winH: n(10), pid: n(11)})
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

// rig is the built binary launched in a terminal of its own, against a
// private server, with a fake claude that lists what listing holds.
type rig struct {
	ov, term                server
	bin, dir, listing, fake string
	config, home, work      string
	list                    pane
	holder                  string
}

// launch builds overview and starts it on two private servers named after
// tag, then waits for the view and its holder.
func launch(t *testing.T, tag string) *rig {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	r := &rig{
		ov:   private(t, fmt.Sprintf("ovtest-e2e-%s%d", tag, os.Getpid())),
		term: private(t, fmt.Sprintf("ovtest-e2e-term-%s%d", tag, os.Getpid())),
	}
	r.dir = t.TempDir()
	if real, err := filepath.EvalSymlinks(r.dir); err == nil {
		r.dir = real
	}
	r.bin = filepath.Join(r.dir, "bin", "overview")
	build := exec.Command("go", "build", "-o", r.bin, ".")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	// The fake claude lists what the test writes to listing, and as a new
	// session it just waits, so its pane stays open.
	r.listing = filepath.Join(r.dir, "listing.json")
	r.fake = filepath.Join(r.dir, "claude")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = agents ]; then cat %q 2>/dev/null || echo '[]'; exit 0; fi\nexec sleep 600\n", r.listing)
	if err := os.WriteFile(r.fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r.config = filepath.Join(r.dir, "config")
	r.home = filepath.Join(r.dir, ".claude-e2e")
	r.work = filepath.Join(r.dir, "work")
	for _, d := range []string{r.config, r.home, r.work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(r.config, "config"), []byte("theme = revdiff\nclaude = "+r.fake+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The terminal is a pane of the second server. env -u TMUX, since tmux
	// refuses to attach from inside another tmux.
	r.term.must("-f", "/dev/null", "new-session", "-d", "-x", "200", "-y", "50", "-c", r.work,
		"env", "-u", "TMUX", r.bin, "--socket", r.ov.name, "--config", r.config, "--home", r.home)

	waitFor(t, "the view and its holder", func() (bool, string) {
		ps := r.ov.panes()
		r.holder = ""
		for _, p := range ps {
			if p.holder && p.session == "overview-e2e" {
				r.holder = p.id
			}
		}
		ok, say := beside(ps, r.holder, 0)
		return ok && r.holder != "", "holder " + r.holder + ": " + say
	})
	r.list = listPane(r.ov.panes())
	return r
}

// The built binary, launched in a terminal of its own: the view marks its
// pane and stands beside the holder, c opens a session window that the list
// moves into, a split there stays with its window while the cursor is
// elsewhere, and the list keeps the width it is dragged to. A relaunch with
// new flags restarts the view in place, and one with the same flags does not.
func TestOverviewInTmux(t *testing.T) {
	r := launch(t, "")
	ov, term, bin, listing, fake := r.ov, r.term, r.bin, r.listing, r.fake
	config, home, work, list := r.config, r.home, r.work, r.list
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

// screen is what the list pane shows.
func (r *rig) screen() string {
	out, _ := r.ov.run("capture-pane", "-p", "-t", r.list.id)
	return out
}

// press sends keys to the list, one tmux key name each.
func (r *rig) press(keys ...string) {
	r.ov.t.Helper()
	for _, k := range keys {
		r.ov.must("send-keys", "-t", r.list.id, k)
	}
}

// lists makes the fake claude list these session panes, all in dir.
func (r *rig) lists(t *testing.T, dir string, ps ...pane) {
	t.Helper()
	live := []map[string]any{}
	for i, p := range ps {
		live = append(live, map[string]any{"pid": p.pid, "cwd": dir, "kind": "interactive",
			"sessionId": fmt.Sprintf("s-%d", i), "name": fmt.Sprintf("e2e-%d", i), "status": "idle",
			"startedAt": time.Now().UnixMilli()})
	}
	b, _ := json.Marshal(live)
	if err := os.WriteFile(r.listing, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// fresh waits for a pane that before lacks and that satisfies is, and
// returns it.
func (r *rig) fresh(t *testing.T, what string, before []pane, is func(pane) bool) pane {
	t.Helper()
	old := map[string]bool{}
	for _, p := range before {
		old[p.id] = true
	}
	var got pane
	waitFor(t, what, func() (bool, string) {
		for _, p := range r.ov.panes() {
			if !old[p.id] && is(p) {
				got = p
				return true, ""
			}
		}
		return false, fmt.Sprintf("panes %+v", r.ov.panes())
	})
	return got
}

func (r *rig) windowOf(id string) string {
	for _, p := range r.ov.panes() {
		if p.id == id {
			return p.window
		}
	}
	return ""
}

// active is the pane the view's session shows focused.
func (r *rig) active() string {
	out, _ := r.ov.run("display-message", "-p", "-t", "=overview-e2e:", "#{pane_id}")
	return out
}

// Terminals and pinned projects, end to end: t opens a terminal that is
// listed under its project and focused. A Claude that exits beside a split
// leaves the cursor in its window, now a terminal. x and y close a terminal
// and the list survives. A project pinned in the file, or with a, shows a
// placeholder, and c there starts a session in it.
func TestTerminalsAndProjects(t *testing.T) {
	r := launch(t, "t")
	isSleep := func(p pane) bool { return p.command == "sleep" && !p.list }
	isShell := func(p pane) bool { return p.command != "sleep" && !p.list && p.id != r.holder }

	// A session in work, listed.
	before := r.ov.panes()
	r.press("c")
	session := r.fresh(t, "a session window", before, isSleep)
	r.lists(t, r.work, session)
	r.ov.must("select-pane", "-t", r.list.id)
	waitFor(t, "the session in the list", func() (bool, string) {
		s := r.screen()
		return strings.Contains(s, "e2e-0"), s
	})

	// t opens a terminal in work's project, listed after the session, with
	// the list beside it and the keyboard in it.
	before = r.ov.panes()
	r.press("t")
	shell := r.fresh(t, "a terminal window", before, isShell)
	waitFor(t, "the terminal shown and focused", func() (bool, string) {
		ok, say := beside(r.ov.panes(), shell.id, 0)
		return ok && r.active() == shell.id, say + ", active " + r.active()
	})
	if got := r.ov.must("show-options", "-wqv", "-t", shell.id, "@overview-project"); got != r.work {
		t.Fatalf("the terminal's project is %q, want %q", got, r.work)
	}
	waitFor(t, "the terminal under the session", func() (bool, string) {
		s := r.screen()
		i, j := strings.Index(s, "e2e-0"), strings.Index(s, "$ ")
		return i >= 0 && j > i && strings.Contains(s[j:], "terminal"), s
	})

	// The session's window gets a split, and then its claude exits. The
	// cursor stays in that window, now a terminal, so the list stays put.
	r.ov.must("select-pane", "-t", r.list.id)
	r.press("g")
	waitFor(t, "the list beside the session", func() (bool, string) { return beside(r.ov.panes(), session.id, 0) })
	split := r.ov.must("split-window", "-d", "-v", "-t", session.id, "-P", "-F", "#{pane_id}", "sleep 600")
	time.Sleep(700 * time.Millisecond) // the view sees the split
	r.ov.must("kill-pane", "-t", session.id)
	r.lists(t, r.work)
	waitFor(t, "two terminals and no session", func() (bool, string) {
		s := r.screen()
		return strings.Count(s, "$ ") == 2 && !strings.Contains(s, "e2e-0"), s
	})
	for range 6 { // three seconds: a refresh and some listings
		if ok, say := beside(r.ov.panes(), split, 0); !ok {
			t.Fatalf("the list left the split: %s", say)
		}
		time.Sleep(500 * time.Millisecond)
	}

	// x and y on the new terminal close its window. The list was in it,
	// and survives, beside the terminal before it.
	r.press("G")
	waitFor(t, "the list beside the new terminal", func() (bool, string) { return beside(r.ov.panes(), shell.id, 0) })
	r.press("x")
	waitFor(t, "the question", func() (bool, string) {
		s := r.screen()
		return strings.Contains(s, "Close terminal"), s
	})
	r.press("y")
	waitFor(t, "the terminal closed", func() (bool, string) {
		return r.windowOf(shell.id) == "" && r.windowOf(shell.window) == "", fmt.Sprintf("panes %+v", r.ov.panes())
	})
	waitFor(t, "the list beside the split", func() (bool, string) { return beside(r.ov.panes(), split, 0) })
	if l := listPane(r.ov.panes()); l.id != r.list.id || l.pid != r.list.pid {
		t.Fatalf("the list is %+v, was %+v", l, r.list)
	}

	// A project pinned in the file gets a placeholder, and c there starts
	// a session in it.
	proj := filepath.Join(r.dir, "aproj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(r.home, "overview"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.home, "overview", "projects"), []byte(proj+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the placeholder", func() (bool, string) {
		s := r.screen()
		return strings.Contains(s, "no sessions") && strings.Contains(s, "x unpin"), s
	})
	r.press("g") // aproj sorts before work
	waitFor(t, "the holder for the placeholder", func() (bool, string) { return beside(r.ov.panes(), r.holder, 0) })
	before = r.ov.panes()
	r.press("c")
	started := r.fresh(t, "a session in the pinned project", before, isSleep)
	if got := r.ov.must("show-options", "-wqv", "-t", started.id, "@overview-project"); got != proj {
		t.Fatalf("the session's project is %q, want %q", got, proj)
	}
	waitFor(t, "the list beside it", func() (bool, string) { return beside(r.ov.panes(), started.id, 0) })

	// a, then a typed path, pins another project, and the cursor goes to
	// its placeholder.
	other := filepath.Join(r.dir, "zother")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	r.ov.must("select-pane", "-t", r.list.id)
	// Wide enough to show the temporary directory's long path.
	r.ov.must("resize-pane", "-t", r.list.id, "-x", "170")
	r.press("a")
	waitFor(t, "the picker", func() (bool, string) {
		s := r.screen()
		return strings.Contains(s, "add project"), s
	})
	r.ov.must("send-keys", "-t", r.list.id, "-l", filepath.Join(r.dir, "zoth"))
	waitFor(t, "the match", func() (bool, string) {
		s := r.screen()
		return strings.Contains(s, "zother"), s
	})
	r.press("Tab")
	waitFor(t, "the completion", func() (bool, string) {
		s := r.screen()
		return strings.Contains(s, "zother/"), s
	})
	r.press("Enter")
	waitFor(t, "the pinned project", func() (bool, string) {
		b, _ := os.ReadFile(filepath.Join(r.home, "overview", "projects"))
		return strings.Contains(string(b), other+"\n"), string(b)
	})
	waitFor(t, "its placeholder, selected, with the holder", func() (bool, string) {
		s := r.screen()
		ok, say := beside(r.ov.panes(), r.holder, 0)
		return ok && strings.Count(s, "no sessions") == 1 && strings.Contains(s, "zother"), say + "\n" + s
	})
}

// Claude typed into a terminal makes it a session, and while the keyboard
// is in it the list stays beside it, though the terminal's entry went and
// the cursor's rules would move it to the session before.
func TestTheListStaysWhileYouType(t *testing.T) {
	r := launch(t, "k")
	isSleep := func(p pane) bool { return p.command == "sleep" && !p.list }
	isShell := func(p pane) bool { return p.command != "sleep" && !p.list && p.id != r.holder }

	before := r.ov.panes()
	r.press("c")
	session := r.fresh(t, "a session window", before, isSleep)
	r.lists(t, r.work, session)
	r.ov.must("select-pane", "-t", r.list.id)
	waitFor(t, "the session in the list", func() (bool, string) {
		s := r.screen()
		return strings.Contains(s, "e2e-0"), s
	})

	before = r.ov.panes()
	r.press("t")
	shell := r.fresh(t, "a terminal window", before, isShell)
	waitFor(t, "the terminal shown and focused", func() (bool, string) {
		ok, say := beside(r.ov.panes(), shell.id, 0)
		return ok && r.active() == shell.id && strings.Contains(r.screen(), "$ "), say + ", active " + r.active()
	})

	// The shell becomes what claude would be, keeping its pid, and claude
	// lists it.
	r.ov.must("send-keys", "-t", shell.id, "exec sleep 600", "Enter")
	waitFor(t, "the command to run", func() (bool, string) {
		for _, p := range r.ov.panes() {
			if p.id == shell.id {
				return p.command == "sleep", p.command
			}
		}
		return false, "the pane is gone"
	})
	r.lists(t, r.work, session, shell)
	waitFor(t, "the terminal listed as a session", func() (bool, string) {
		s := r.screen()
		return strings.Contains(s, "e2e-1") && !strings.Contains(s, "$ "), s
	})
	for range 6 { // three seconds: a refresh and some listings
		ok, say := beside(r.ov.panes(), shell.id, 0)
		if !ok || r.active() != shell.id {
			t.Fatalf("the list moved while typing: %s, active %s", say, r.active())
		}
		time.Sleep(500 * time.Millisecond)
	}
}
