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
	"syscall"
	"testing"
	"time"

	"overview/internal/tmuxtest"
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

// waitFor polls until cond holds, failing with what it last said. It is
// tmuxtest.Wait, so every wait in the repo has the same generous deadline.
func waitFor(t *testing.T, what string, cond func() (bool, string)) {
	t.Helper()
	tmuxtest.Wait(t, what, cond)
}

// beside reports whether the list sits in the window of pane id, leftmost,
// full height under the rule along its top, w wide (w 0 for any width).
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
		list.left == 0 && list.top <= 1 && list.top+list.h == list.winH && (w == 0 || list.w == w)
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
	termPane                string // the terminal overview was launched in
}

// launch builds overview and starts it on two private servers named after
// tag, then waits for the view and its holder.
func launch(t *testing.T, tag string) *rig { t.Helper(); return launchWith(t, tag, nil) }

// launchWith is launch with setup run before overview starts, to add to
// the rig's directories and config file.
func launchWith(t *testing.T, tag string, setup func(r *rig) string) *rig {
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
	extra := ""
	if setup != nil {
		extra = setup(r)
	}
	if err := os.WriteFile(filepath.Join(r.config, "config"), []byte("theme = revdiff\nclaude = "+r.fake+"\n"+extra), 0o644); err != nil {
		t.Fatal(err)
	}

	// The terminal is a pane of the second server. env -u TMUX, since tmux
	// refuses to attach from inside another tmux.
	r.termPane = r.term.must("-f", "/dev/null", "new-session", "-d", "-x", "200", "-y", "50", "-c", r.work, "-P", "-F", "#{pane_id}",
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
	if err != nil || !strings.Contains(string(args), " view --socket-path "+path+" --session overview-e2e --cwd "+work+" ") ||
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
	// Each pane has a rule along its top, with its title in it: the
	// list's names the home, a session's its name and state.
	waitFor(t, "the panes' rules", func() (bool, string) {
		status := ov.must("display-message", "-p", "-t", list.id, "#{pane-border-status}")
		mine := ov.must("show-options", "-pqv", "-t", list.id, "@overview-title")
		theirs := ov.must("show-options", "-pqv", "-t", sessions[0].id, "@overview-title")
		return strings.TrimSpace(status) == "top" && strings.Contains(mine, "◆ ") && strings.Contains(theirs, "e2e-") && strings.Contains(theirs, "idle"),
			fmt.Sprintf("status %q, list %q, session %q", status, mine, theirs)
	})
	// g puts the cursor on the first session.
	ov.must("send-keys", "-t", list.id, "g")
	waitFor(t, "the list beside the first session", func() (bool, string) { return beside(ov.panes(), first.id, 0) })

	// A split in the first session's window, and the list dragged wider.
	split := ov.must("split-window", "-d", "-v", "-t", first.id, "-P", "-F", "#{pane_id}", "sleep 600")
	ov.must("resize-pane", "-t", list.id, "-x", "57")
	waitFor(t, "the drag", func() (bool, string) { return beside(ov.panes(), first.id, 57) })
	// The view rereads tmux's panes twice a second, and nothing on screen
	// shows that it has seen the width, so wait for several refreshes.
	time.Sleep(2500 * time.Millisecond)

	inWindow := func(id string) string {
		for _, p := range ov.panes() {
			if p.id == id {
				return p.window
			}
		}
		return ""
	}
	window := inWindow(first.id)
	// The keys are in the list, so j moves its cursor.
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

	// A theme given as a relative path reaches the view absolute, and a
	// change to the config file restarts the view on a plain relaunch.
	restarted := func(what, want string) {
		t.Helper()
		was := list.pid
		waitFor(t, what, func() (bool, string) {
			l := listPane(ov.panes())
			args, _ := exec.Command("ps", "-o", "args=", "-p", strconv.Itoa(l.pid)).Output()
			return l.id == list.id && l.pid != was && strings.Contains(string(args), want), string(args)
		})
		list = listPane(ov.panes())
	}
	theme := filepath.Join(work, "mine.theme")
	if err := os.WriteFile(theme, []byte("accent = #c678dd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	relaunch("--theme", "./mine.theme")
	restarted("the view with the theme's path", "--theme "+theme+" ")
	if err := os.WriteFile(filepath.Join(config, "config"), []byte("theme = one-dark\nclaude = "+fake+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	relaunch()
	restarted("the view with the file's new theme", "--theme one-dark ")
}

// screen is what the list pane shows.
func (r *rig) screen() string {
	out, _ := r.ov.run("capture-pane", "-p", "-t", r.list.id)
	return out
}

// inPanels says whether the list's footer shows the panels mode. The
// list's own hints include "esc panels", which says nothing of the mode,
// so it is taken out first.
func (r *rig) inPanels() bool {
	lines := strings.Split(strings.TrimRight(r.screen(), "\n"), "\n")
	return strings.Contains(strings.ReplaceAll(lines[len(lines)-1], "esc panels", ""), "panels")
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
	waitFor(t, "the terminal under the session, with its directory", func() (bool, string) {
		s := r.screen()
		i, j := strings.Index(s, "e2e-0"), strings.Index(s, "$ ")
		if i < 0 || j <= i {
			return false, s
		}
		line, _, _ := strings.Cut(s[j:], "\n")
		return strings.Contains(line, filepath.Base(r.work)), s
	})

	// The session's window gets a split, and then its claude exits. The
	// cursor stays in that window, now a terminal, so the list stays put.
	r.ov.must("select-pane", "-t", r.list.id)
	r.press("g")
	waitFor(t, "the list beside the session", func() (bool, string) { return beside(r.ov.panes(), session.id, 0) })
	split := r.ov.must("split-window", "-d", "-v", "-t", session.id, "-P", "-F", "#{pane_id}", "sleep 600")
	// Nothing on screen shows that the view has seen the split, so this
	// waits for several of its half-second refreshes.
	time.Sleep(2 * time.Second)
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
		return strings.Contains(s, "+ c claude · t terminal"), s
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
		return ok && strings.Count(s, "+ c claude · t terminal") == 1 && strings.Contains(s, "zother") && strings.Contains(s, "▌+ c claude · t terminal"), say + "\n" + s
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

// q in the list detaches the terminal it was typed in, and a second
// terminal attached to the view stays attached, though it was used more
// recently than the first until the q.
func TestQDetachesOnlyItsTerminal(t *testing.T) {
	r := launch(t, "q")
	second := r.term.must("new-window", "-P", "-F", "#{pane_id}", "-c", r.work, "env", "-u", "TMUX", r.bin,
		"--socket", r.ov.name, "--config", r.config, "--home", r.home)
	clients := func() []string { return strings.Fields(r.ov.must("list-clients", "-F", "#{client_tty}")) }
	waitFor(t, "two clients", func() (bool, string) {
		c := clients()
		return len(c) == 2, strings.Join(c, " ")
	})
	r.ov.must("select-pane", "-t", r.list.id)
	// The second terminal is used, then q is typed in the first. tmux
	// counts a client's activity in seconds.
	time.Sleep(1100 * time.Millisecond)
	r.term.must("send-keys", "-t", second, "k")
	time.Sleep(1100 * time.Millisecond)
	secondTTY := r.term.must("display-message", "-p", "-t", second, "#{pane_tty}")
	r.term.must("send-keys", "-t", r.termPane, "q")
	waitFor(t, "only the second client", func() (bool, string) {
		c := clients()
		return len(c) == 1 && c[0] == secondTTY, strings.Join(c, " ")
	})
	if l := listPane(r.ov.panes()); l.pid != r.list.pid {
		t.Fatalf("the view went with the client: %+v", l)
	}
}

// Workstreams, end to end: a project with .issues lists its issue in
// progress before its session, with no session under it. c on it starts
// claude with the opening prompt that names the issue. Once that prompt
// is in the session's transcript, the session is listed under the
// workstream. t opens a terminal that is listed under it, and an issues
// start in the other session's transcript moves it there too. Last, s on a tracked project
// pinned with nothing running opens the task picker, and enter on the
// issue typed for starts claude on it.
func TestWorkstreams(t *testing.T) {
	r := launchWith(t, "w", func(r *rig) string {
		for _, d := range []string{".jj", ".issues"} {
			if err := os.MkdirAll(filepath.Join(r.work, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for name, text := range map[string]string{"project.edn": `{:id "work"}`, "6-keep-width.edn": `{:id 6}`} {
			if err := os.WriteFile(filepath.Join(r.work, ".issues", name), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		// zbacklog is a second tracked project, with nothing running,
		// that the test pins at the end.
		backlog := filepath.Join(r.dir, "zbacklog")
		for _, d := range []string{".jj", ".issues"} {
			if err := os.MkdirAll(filepath.Join(backlog, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for name, text := range map[string]string{"project.edn": `{:id "zbacklog"}`, "3-add-threads.edn": `{:id 3}`, "4-fix-scroll.edn": `{:id 4}`} {
			if err := os.WriteFile(filepath.Join(backlog, ".issues", name), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		fake := filepath.Join(r.dir, "issues")
		list := `{"issues": [{"id": 6, "ref": "work#6", "status": "in-progress", "title": "keep width", "priority": "p1"}]}`
		blist := `{"issues": [{"id": 3, "ref": "zbacklog#3", "status": "ready", "title": "add threads", "priority": "p2"},` +
			`{"id": 4, "ref": "zbacklog#4", "status": "inbox", "title": "fix scroll", "priority": "p1"}]}`
		show := `{"id": 3, "details-text": "# Add threads\n\nReplies nest under the\nmessage they answer.\n"}`
		script := fmt.Sprintf("#!/bin/sh\nif [ \"$3\" = show ]; then printf '%%s\\n' '%s'\nelif [ \"$2\" = %q ]; then echo '%s'; else echo '%s'; fi\n", show, backlog, blist, list)
		if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		return "issues = " + fake + "\n"
	})
	isSleep := func(p pane) bool { return p.command == "sleep" && !p.list }
	isShell := func(p pane) bool { return p.command != "sleep" && !p.list && p.id != r.holder }

	before := r.ov.panes()
	r.press("c")
	session := r.fresh(t, "a session window", before, isSleep)
	r.lists(t, r.work, session)
	r.ov.must("select-pane", "-t", r.list.id)
	waitFor(t, "the workstream above the session", func() (bool, string) {
		s := r.screen()
		i, j, k := strings.Index(s, "#6 keep width"), strings.Index(s, "no sessions"), strings.Index(s, "e2e-0")
		// The heading's count gives way to the long temporary name in
		// the narrow list, so the workstream's row is what shows it.
		return i >= 0 && j > i && k > j, s
	})

	r.press("g")
	waitFor(t, "the holder for the workstream", func() (bool, string) { return beside(r.ov.panes(), r.holder, 0) })
	before = r.ov.panes()
	r.press("c")
	started := r.fresh(t, "a session on the task", before, isSleep)
	if got := r.ov.must("display-message", "-p", "-t", started.id, "#{pane_start_command}"); !strings.Contains(got, "Work on work#6: keep width") {
		t.Fatalf("the session started as %q", got)
	}

	// Nothing has been written to the new session's transcript yet, so it
	// is listed unlinked.
	r.lists(t, r.work, session, started)
	r.ov.must("select-pane", "-t", r.list.id)
	waitFor(t, "the new session in the list", func() (bool, string) {
		s := r.screen()
		return strings.Contains(s, "e2e-1"), s
	})

	// The session's transcript opens with the prompt that names the issue,
	// as Claude Code writes it, and the list moves the session under the
	// workstream.
	r.transcript(t, "s-1", map[string]any{"type": "user", "isSidechain": false, "timestamp": "2026-10-23T17:44:47.677Z",
		"cwd": r.work, "message": map[string]any{"role": "user", "content": "Work on work#6: keep width"}})
	waitFor(t, "the linked session under the workstream", func() (bool, string) {
		s := r.screen()
		i, j, k := strings.Index(s, "#6 keep width"), strings.Index(s, "e2e-1"), strings.Index(s, "e2e-0")
		return i >= 0 && j > i && k > j && !strings.Contains(s, "no sessions"), s
	})

	// On the workstream, the list is beside its session now.
	r.press("g")
	waitFor(t, "the list beside the linked session", func() (bool, string) { return beside(r.ov.panes(), started.id, 0) })
	before = r.ov.panes()
	r.press("t")
	shell := r.fresh(t, "a terminal for the task", before, isShell)
	if got := r.ov.must("show-options", "-wqv", "-t", shell.id, "@overview-task"); got != r.work+"#6" {
		t.Fatalf("the terminal's task is %q", got)
	}
	waitFor(t, "the terminal under the workstream", func() (bool, string) {
		s := r.screen()
		i, j, k := strings.Index(s, "#6 keep width"), strings.Index(s, "   $ "), strings.Index(s, "e2e-0")
		return i >= 0 && j > i && k > j && !strings.Contains(s, "no sessions"), s
	})

	// An issues start that ran and succeeded in the other session's
	// transcript links it too, and it goes under the workstream before the
	// terminal.
	r.transcript(t, "s-0",
		map[string]any{"type": "assistant", "timestamp": "2026-10-23T17:50:00.000Z", "cwd": r.work, "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Bash", "input": map[string]any{"command": "issues start 6"}}}}},
		map[string]any{"type": "user", "timestamp": "2026-10-23T17:50:02.000Z", "message": map[string]any{"content": []any{
			map[string]any{"tool_use_id": "toolu_1", "type": "tool_result", "content": "started", "is_error": false}}}})
	waitFor(t, "both sessions under the workstream", func() (bool, string) {
		s := r.screen()
		i, term := strings.Index(s, "#6 keep width"), strings.Index(s, "   $ ")
		a, b := strings.Index(s, "e2e-0"), strings.Index(s, "e2e-1")
		return i >= 0 && a > i && b > i && term > a && term > b, s
	})

	// A tracked project pinned with nothing running offers s, and the
	// task picker starts a session on the issue typed for.
	backlog := filepath.Join(r.dir, "zbacklog")
	if err := os.MkdirAll(filepath.Join(r.home, "overview"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.home, "overview", "projects"), []byte(backlog+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the tracked placeholder", func() (bool, string) {
		s := r.screen()
		return strings.Contains(s, "+ s task · c claude · t terminal"), s
	})
	r.press("/")
	r.ov.must("send-keys", "-t", r.list.id, "-l", "zbacklog")
	r.press("Enter")
	waitFor(t, "the placeholder selected", func() (bool, string) {
		s := r.screen()
		return strings.Contains(s, "▌+ s task · c claude · t terminal"), s
	})
	// The picker zooms the list's pane and shows the selected issue's
	// details beside its rows.
	zoomed := func() string {
		return r.ov.must("display-message", "-p", "-t", r.list.id, "#{window_zoomed_flag}")
	}
	r.press("s")
	waitFor(t, "the task picker, zoomed, with the details", func() (bool, string) {
		s := r.screen()
		return zoomed() == "1" && strings.Contains(s, "start a task in") && strings.Contains(s, "#3 add threads") &&
			strings.Contains(s, "#4 fix scroll") && strings.Contains(s, "Replies nest under the message they answer."), zoomed() + "\n" + s
	})
	r.ov.must("send-keys", "-t", r.list.id, "-l", "thr")
	waitFor(t, "the picker narrowed", func() (bool, string) {
		s := r.screen()
		return strings.Contains(s, "#3 add threads") && !strings.Contains(s, "#4 fix scroll"), s
	})
	before = r.ov.panes()
	r.press("Enter")
	picked := r.fresh(t, "a session on the picked task", before, isSleep)
	if got := r.ov.must("display-message", "-p", "-t", picked.id, "#{pane_start_command}"); !strings.Contains(got, "Work on zbacklog#3: add threads") {
		t.Fatalf("the picked session started as %q", got)
	}
	if got := r.ov.must("display-message", "-p", "-t", picked.id, "#{pane_current_path}"); got != backlog {
		t.Fatalf("the picked session started in %q, want %q", got, backlog)
	}
	waitFor(t, "the zoom ended beside the picked session", func() (bool, string) {
		ok, say := beside(r.ov.panes(), picked.id, 0)
		return ok && zoomed() == "0", zoomed() + " " + say
	})
}

// transcript appends lines to a session's transcript, as Claude Code
// writes them, under the rig's config directory.
func (r *rig) transcript(t *testing.T, session string, lines ...map[string]any) {
	t.Helper()
	path := filepath.Join(r.home, "projects", "work", session+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		b, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}
}

// The cockpit, end to end: o zooms the list's pane to fill its window and
// draws the cards beside the list, and esc ends the zoom, leaving the list
// beside the session as it was.
func TestCockpit(t *testing.T) {
	r := launch(t, "o")
	isSleep := func(p pane) bool { return p.command == "sleep" && !p.list }
	before := r.ov.panes()
	r.press("c")
	session := r.fresh(t, "a session window", before, isSleep)
	r.lists(t, r.work, session)
	r.ov.must("select-pane", "-t", r.list.id)
	waitFor(t, "the list beside the session", func() (bool, string) {
		ok, say := beside(r.ov.panes(), session.id, 0)
		return ok && strings.Contains(r.screen(), "e2e-0"), say
	})
	width := listPane(r.ov.panes()).w
	zoomed := func() string {
		return r.ov.must("display-message", "-p", "-t", r.list.id, "#{window_zoomed_flag} #{pane_width} #{window_width}")
	}

	r.press("o")
	waitFor(t, "the cockpit, zoomed", func() (bool, string) {
		z, s := zoomed(), r.screen()
		f := strings.Fields(z)
		return len(f) == 3 && f[0] == "1" && f[1] == f[2] && strings.Contains(s, "Uncommitted") && strings.Contains(s, "no issues"), z + "\n" + s
	})
	if w := r.windowOf(session.id); w != r.windowOf(r.list.id) {
		t.Fatalf("the session left the list's window: %s, list in %s", w, r.windowOf(r.list.id))
	}
	r.press("Escape")
	waitFor(t, "the list beside the session again", func() (bool, string) {
		ok, say := beside(r.ov.panes(), session.id, width)
		f := strings.Fields(zoomed())
		return ok && len(f) == 3 && f[0] == "0" && !strings.Contains(r.screen(), "Uncommitted"), zoomed() + " " + say
	})
}

// typed sends keys to the terminal the view is attached in, as a user's
// are: through the attached client and its key tables, which send-keys at
// a pane would skip.
func (r *rig) typed(keys ...string) {
	r.term.t.Helper()
	for _, k := range keys {
		r.term.must("send-keys", "-t", r.termPane, k)
	}
}

// table is the key table of the view's client.
func (r *rig) table() string {
	return strings.TrimSpace(r.ov.must("list-clients", "-F", "#{client_key_table}"))
}

// cursor is the list's row under the cursor, whichever panel it is in.
func (r *rig) cursor() string {
	for _, l := range strings.Split(r.screen(), "\n") {
		if strings.Contains(l, "▌") {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

// The panels mode, end to end, typed at the terminal the view is attached
// in. The tmux package tests the key table itself; this tests the view's
// side. esc in the list puts the client in the mode, and the footer says
// panels. However the keys come back to the list, by ctrl+[ from a
// session or by enter in the mode, j and k move its cursor at once.
func TestPanelsMode(t *testing.T) {
	r := launch(t, "p")
	isSleep := func(p pane) bool { return p.command == "sleep" && !p.list }
	before := r.ov.panes()
	r.press("c")
	one := r.fresh(t, "a session window", before, isSleep)
	r.lists(t, r.work, one)
	r.ov.must("select-pane", "-t", r.list.id)
	waitFor(t, "the session in the list", func() (bool, string) {
		return strings.Contains(r.screen(), "e2e-0"), r.screen()
	})
	session := func() string { // the pane beside the list
		for _, p := range r.ov.panes() {
			if !p.list && !p.holder && p.window == r.windowOf(r.list.id) {
				return p.id
			}
		}
		return ""
	}
	waitFor(t, "a session beside the list", func() (bool, string) {
		ok, say := beside(r.ov.panes(), session(), 0)
		return ok && session() != "", say
	})
	focused := func() bool { return r.active() == r.list.id }
	waitFor(t, "the keys in the list", func() (bool, string) { return focused(), r.active() })

	// moves checks that j and k move the list's cursor now, on the screen.
	moves := func(what string) {
		t.Helper()
		was := r.cursor()
		r.typed("j")
		waitFor(t, what+": j to move the cursor", func() (bool, string) { return r.cursor() != was, was + " -> " + r.cursor() })
		now := r.cursor()
		r.typed("k")
		waitFor(t, what+": k to move it back", func() (bool, string) { return r.cursor() != now, now + " -> " + r.cursor() })
	}

	// esc in the list: the mode, which the footer shows. l moves the
	// focus to the session, and enter leaves the mode there.
	r.typed("Escape")
	waitFor(t, "the panels mode", func() (bool, string) {
		return r.table() == "overview-panels" && r.inPanels(), r.table() + "\n" + r.screen()
	})
	r.typed("l")
	waitFor(t, "the focus in the session", func() (bool, string) { return r.active() == session(), r.active() })
	r.typed("Enter")
	waitFor(t, "the mode left", func() (bool, string) { return r.table() == "root", r.table() })

	// Back to the list by ctrl+[, which the terminal sends the view's tmux
	// as extended keys: -K sends it as the client's own.
	client := r.ov.must("list-clients", "-F", "#{client_name}")
	r.ov.must("send-keys", "-K", "-c", client, "C-[")
	waitFor(t, "ctrl+[ to bring the keys to the list", func() (bool, string) { return focused(), r.active() })
	moves("after ctrl+[")

	// enter on the list leaves the mode with the keys there.
	r.typed("Escape")
	waitFor(t, "the mode again", func() (bool, string) { return r.table() == "overview-panels", r.table() })
	r.typed("Enter")
	waitFor(t, "the mode left", func() (bool, string) { return r.table() == "root" && !r.inPanels(), r.table() + "\n" + r.screen() })
	moves("after enter on the list")
}

// atoi reads a number tmux printed, 0 when it is none.
func atoi(s string) int {
	v, _ := strconv.Atoi(strings.TrimSpace(s))
	return v
}

// The cockpit's issues card takes the keyboard: l moves it in from the
// list, with the card's cursor and the footer saying so, j moves the
// cursor, m marks the issue under it done, h gives the keyboard back to
// the list, and esc closes the cockpit from there.
func TestCockpitIssuesCard(t *testing.T) {
	r := launchWith(t, "i", func(r *rig) string {
		for _, d := range []string{".jj", ".issues"} {
			if err := os.MkdirAll(filepath.Join(r.work, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for name, text := range map[string]string{"project.edn": `{:id "work"}`, "6-first.edn": `{:id 6}`, "7-second.edn": `{:id 7}`} {
			if err := os.WriteFile(filepath.Join(r.work, ".issues", name), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		fake := filepath.Join(r.dir, "issues")
		list := `{"issues": [{"id": 6, "ref": "work#6", "status": "ready", "title": "first thing", "priority": "p1"},` +
			`{"id": 7, "ref": "work#7", "status": "ready", "title": "second thing", "priority": "p2"}]}`
		// It records what it was asked, so a verdict given with m shows.
		script := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %s\necho '%s'\n", filepath.Join(r.dir, "issues-calls"), list)
		if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		return "issues = " + fake + "\n"
	})
	isSleep := func(p pane) bool { return p.command == "sleep" && !p.list }
	before := r.ov.panes()
	r.press("c")
	session := r.fresh(t, "a session window", before, isSleep)
	r.lists(t, r.work, session)
	r.ov.must("select-pane", "-t", r.list.id)
	waitFor(t, "the list beside the session", func() (bool, string) {
		ok, say := beside(r.ov.panes(), session.id, 0)
		return ok && strings.Contains(r.screen(), "e2e-0"), say
	})
	width := listPane(r.ov.panes()).w
	// footer is the screen's last line, where the key hints are.
	footer := func() string {
		lines := strings.Split(strings.TrimRight(r.screen(), "\n"), "\n")
		return lines[len(lines)-1]
	}
	// marked is the issue the card's cursor is on, "" when it is not
	// in the card.
	marked := func() string {
		for _, l := range strings.Split(r.screen(), "\n") {
			if i := strings.Index(l, "▸ "); i >= 0 && strings.Contains(l, "#") {
				return l[i:]
			}
		}
		return ""
	}

	r.press("o")
	waitFor(t, "the cockpit with both issues and l offered", func() (bool, string) {
		s := r.screen()
		return strings.Contains(s, "#6 first thing") && strings.Contains(s, "#7 second thing") &&
			strings.Contains(footer(), "l issues") && marked() == "", s
	})

	r.press("l")
	waitFor(t, "the keyboard in the issues card, on the first issue", func() (bool, string) {
		m := marked()
		return strings.Contains(m, "#6 first thing") && strings.Contains(footer(), "h list") && !strings.Contains(footer(), "l issues"), m + "\n" + r.screen()
	})
	r.press("j")
	waitFor(t, "the cursor on the second issue", func() (bool, string) {
		m := marked()
		return strings.Contains(m, "#7 second thing"), m + "\n" + r.screen()
	})
	// m gives that issue a verdict: a ready one can be done or dropped.
	r.press("m")
	waitFor(t, "the verdict box on #7", func() (bool, string) {
		s := r.screen()
		return strings.Contains(s, "work#7 second thing") && strings.Contains(s, "a done   r drop"), s
	})
	r.press("a")
	waitFor(t, "the box asking for a note", func() (bool, string) {
		s := r.screen()
		return strings.Contains(s, "note, optional:"), s
	})
	r.press("Enter")
	waitFor(t, "issues approve run on #7", func() (bool, string) {
		calls, _ := os.ReadFile(filepath.Join(r.dir, "issues-calls"))
		return strings.Contains(string(calls), "--dir "+r.work+" approve 7"), string(calls) + "\n" + r.screen()
	})
	r.press("h")
	waitFor(t, "the keyboard back in the list", func() (bool, string) {
		return marked() == "" && strings.Contains(footer(), "l issues") && strings.Contains(r.screen(), "#7 second thing"), r.screen()
	})
	// With the keyboard back in the list, the cockpit is still open, and
	// esc closes it.
	r.press("Escape")
	waitFor(t, "the cockpit closed, the list beside the session again", func() (bool, string) {
		ok, say := beside(r.ov.panes(), session.id, width)
		z := r.ov.must("display-message", "-p", "-t", r.list.id, "#{window_zoomed_flag}")
		return ok && z == "0" && !strings.Contains(r.screen(), "#7 second thing"), z + " " + say
	})
}

// A session's claude exits while the keyboard is in it, and the list was
// alone beside it. tmux keeps the pane, dead, until the list has moved on
// beside the holder, so the list never fills the window, and keeps its
// width throughout.
func TestAnEndedSessionLeavesTheListItsWidth(t *testing.T) {
	r := launch(t, "x")
	before := r.ov.panes()
	r.press("c")
	session := r.fresh(t, "a session window", before, func(p pane) bool { return p.command == "sleep" && !p.list })
	waitFor(t, "the list beside the session, the keyboard in it", func() (bool, string) {
		ok, say := beside(r.ov.panes(), session.id, 0)
		return ok && r.active() == session.id, say + ", active " + r.active()
	})
	width := listPane(r.ov.panes()).w
	// The fake claude is sleep, the pane's own process.
	if err := syscall.Kill(session.pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		ps := r.ov.panes()
		list, shared := listPane(ps), 0
		for _, p := range ps {
			if p.window == list.window && !p.list {
				shared++
			}
		}
		if shared == 0 || list.w != width {
			t.Fatalf("the list is %+v, %d panes beside it, %d wide before", list, shared, width)
		}
		gone := true
		for _, p := range ps {
			gone = gone && p.id != session.id
		}
		if ok, _ := beside(ps, r.holder, width); ok && gone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the list never moved on beside the holder: %+v", ps)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
