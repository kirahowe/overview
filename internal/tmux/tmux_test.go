package tmux

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"overview/internal/tmuxtest"
)

// A session is in the panels mode when the client last used on it is:
// another left in the mode in a terminal elsewhere does not count.
func TestPanelsSessionsFollowsTheLatestClient(t *testing.T) {
	out := "%0\tttys1\tov\n" +
		"client\tov\troot\t200\n" +
		"client\tov\toverview-panels\t100\n" +
		"client\tother\toverview-panels\t50\n" +
		"client\tthird\troot\t70\n" +
		"client\tbroken\n"
	got := panelsSessions(out)
	want := map[string]bool{"ov": false, "other": true, "third": false}
	if !maps.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParsePanes(t *testing.T) {
	out := "%0\t/dev/ttys004\toverview-work\t@0\t60\t40\t200\t1\toverview\t\t\t\t\"--theme\" \"x\"\t\t\t1\t0\t1\t1\ttop\t/w\n" +
		"%3\t/dev/ttys009\toverview-other\t@2\t80\t24\t80\t\tnode\t/w/app\tbg1\t1\t\tsid-1\t/w/app#6\t\t61\t0\t0\toff\t/w/app/odd\tdir\n" +
		"garbage\n" +
		"%4\t/dev/ttys010\ts\t@3\twide\t24\t80\t\tzsh\t\t\t\t\t\t\t\t0\t0\t0\ttop\t/\n" +
		"%6\t/dev/ttys010\ts\t@3\t1\t24\t80\t\tzsh\t\t\t\t\t\t\t\tleft\t0\t0\ttop\t/\n" +
		"%5\t/dev/ttys011\ts\t@3\t1\t24\t\n" +
		"\n"
	got := ParsePanes(out)
	want := []Pane{
		{ID: "%0", TTY: "ttys004", Session: "overview-work", Window: "@0", W: 60, H: 40, WinW: 200, List: true, Command: "overview",
			View: `"--theme" "x"`, Zoomed: true, Y: 1, Active: true, Framed: true, Path: "/w"},
		{ID: "%3", TTY: "ttys009", Session: "overview-other", Window: "@2", W: 80, H: 24, WinW: 80,
			Command: "node", Project: "/w/app", Attach: "bg1", Holder: true, Review: "sid-1", Task: "/w/app#6", X: 61, Path: "/w/app/odd\tdir"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestPropParsePanesReadsWhatTmuxWrites(t *testing.T) {
	name := rapid.StringMatching(`[a-z][a-z0-9_-]{0,12}`)
	rapid.Check(t, func(t *rapid.T) {
		panes := rapid.SliceOf(rapid.Custom(func(t *rapid.T) Pane {
			return Pane{
				ID:      fmt.Sprintf("%%%d", rapid.IntRange(0, 999).Draw(t, "id")),
				TTY:     fmt.Sprintf("ttys%03d", rapid.IntRange(0, 999).Draw(t, "tty")),
				Session: name.Draw(t, "session"),
				Window:  fmt.Sprintf("@%d", rapid.IntRange(0, 99).Draw(t, "win")),
				W:       rapid.IntRange(1, 500).Draw(t, "w"),
				H:       rapid.IntRange(1, 200).Draw(t, "h"),
				WinW:    rapid.IntRange(1, 500).Draw(t, "winw"),
				List:    rapid.Bool().Draw(t, "list"),
				Command: rapid.SampledFrom([]string{"", "zsh", "node", "claude", "-zsh"}).Draw(t, "cmd"),
				Project: rapid.SampledFrom([]string{"", "/w/app", "/w/a b"}).Draw(t, "project"),
				Attach:  rapid.SampledFrom([]string{"", "bg1"}).Draw(t, "attach"),
				Holder:  rapid.Bool().Draw(t, "holder"),
				View:    rapid.SampledFrom([]string{"", `"--theme" "a b"`}).Draw(t, "view"),
				Review:  rapid.SampledFrom([]string{"", "5f2c-1d"}).Draw(t, "review"),
				Task:    rapid.SampledFrom([]string{"", "/w/app#6", "/w/a b#12"}).Draw(t, "task"),
				Zoomed:  rapid.Bool().Draw(t, "zoomed"),
				X:       rapid.IntRange(0, 500).Draw(t, "x"),
				Y:       rapid.IntRange(0, 200).Draw(t, "y"),
				Active:  rapid.Bool().Draw(t, "active"),
				Framed:  rapid.Bool().Draw(t, "framed"),
				Path:    rapid.SampledFrom([]string{"", "/", "/w/app", "/w/a b", "/w/tab\there"}).Draw(t, "path"),
			}
		})).Draw(t, "panes")
		var b strings.Builder
		for _, p := range panes {
			flag := func(on bool) string {
				if on {
					return "1"
				}
				return ""
			}
			status := "off"
			if p.Framed {
				status = "top"
			}
			fmt.Fprintf(&b, "%s\t/dev/%s\t%s\t%s\t%d\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t%s\t%s\t%s\n",
				p.ID, p.TTY, p.Session, p.Window, p.W, p.H, p.WinW, flag(p.List), p.Command, p.Project, p.Attach, flag(p.Holder), p.View, p.Review, p.Task, flag(p.Zoomed),
				p.X, p.Y, flag(p.Active), status, p.Path)
		}
		if got := ParsePanes(b.String()); fmt.Sprint(got) != fmt.Sprint(panes) {
			t.Fatalf("got %+v\nwant %+v", got, panes)
		}
	})
}

// testServer is a real tmux server on a private socket, killed and its
// socket removed when the test ends.
func testServer(t *testing.T) Server {
	t.Helper()
	return At(tmuxtest.New(t, "").Path, "/dev/null")
}

func TestAgainstRealTmux(t *testing.T) {
	s := testServer(t)
	if panes, err := s.Panes(); err != nil || len(panes) != 0 {
		t.Fatalf("no server yet: panes %v err %v", panes, err)
	}
	list, err := s.Spawn("ov", "", []string{"X=1"}, "sleep", "300")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkList(list.ID); err != nil {
		t.Fatal(err)
	}
	a, err := s.Spawn("ov", "/", nil, "sleep", "300")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Spawn("ov", "", nil, "sleep", "300")
	if err != nil {
		t.Fatal(err)
	}
	// b gets a split of its own, and the list moves in beside both.
	split, err := s.run("split-window", "-d", "-v", "-P", "-F", "#{pane_id}", "-t", b.ID, "sleep 300")
	if err != nil {
		t.Fatal(err)
	}
	split = strings.TrimSpace(split)
	if err := s.Move(list.ID, b.ID, "30"); err != nil {
		t.Fatal(err)
	}
	panes, err := s.Panes()
	if err != nil {
		t.Fatal(err)
	}
	where := map[string]Pane{}
	for _, p := range panes {
		where[p.ID] = p
	}
	if !where[list.ID].List || where[a.ID].List {
		t.Fatalf("list mark: %+v", panes)
	}
	if where[b.ID].Window != where[list.ID].Window || where[split].Window != where[b.ID].Window || where[list.ID].W != 30 {
		t.Fatalf("the list should sit 30 wide beside b and its split: %+v", panes)
	}
	for _, p := range panes {
		if p.Session != "ov" {
			t.Fatalf("%s is still in %s", p.ID, p.Session)
		}
	}
	if where[a.ID].TTY != a.TTY || !strings.HasPrefix(a.TTY, "ttys") {
		t.Fatalf("tty %q, listed as %q", a.TTY, where[a.ID].TTY)
	}
	layout, err := s.Layout(where[b.ID].Window)
	if err != nil || !strings.Contains(layout, "[") {
		t.Fatalf("layout %q, err %v", layout, err)
	}
	if err := s.SetLayout(where[b.ID].Window, layout); err != nil {
		t.Fatal(err)
	}
	// Moving on to a closes nothing that is left in b's window.
	if err := s.Move(list.ID, a.ID, "30"); err != nil {
		t.Fatal(err)
	}
	if panes, _ = s.Panes(); len(panes) != 4 {
		t.Fatalf("panes after the second move: %+v", panes)
	}
	view, err := s.Open("ov", nil, "sleep", "300")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Select(view.ID); err != nil {
		t.Fatal(err)
	}
	if out, _ := s.run("display-message", "-p", "-t", "=ov:", "#{pane_id}"); strings.TrimSpace(out) != view.ID {
		t.Fatalf("the opened window is not current: %q", out)
	}
}

// Move puts the list in a full-height column on the left of the target's
// window, shows that window and focuses the list. The window the list
// leaves closes when it is empty, and otherwise keeps its panes, splits
// included, for when the list comes back.
func TestMoveTakesTheListAcrossWindows(t *testing.T) {
	s := testServer(t)
	run := func(args ...string) string {
		t.Helper()
		out, err := s.run(args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	list := run("new-session", "-d", "-s", "ov", "-x", "200", "-y", "50", "-P", "-F", "#{pane_id}", "sleep 300")
	first := run("display-message", "-p", "-t", list, "#{window_id}")
	a, err := s.Spawn("ov", "", nil, "sleep", "300")
	if err != nil {
		t.Fatal(err)
	}
	split := run("split-window", "-d", "-v", "-t", a.ID, "-P", "-F", "#{pane_id}", "sleep 300")
	deep := run("split-window", "-d", "-h", "-t", split, "-P", "-F", "#{pane_id}", "sleep 300")
	b, err := s.Spawn("ov", "", nil, "sleep", "300")
	if err != nil {
		t.Fatal(err)
	}
	where := func() map[string]string {
		out := run("list-panes", "-s", "-t", "=ov", "-F", "#{pane_id} #{window_id} #{pane_left} #{pane_top} #{pane_width} #{pane_height} #{window_height} #{pane_active} #{window_active}")
		m := map[string]string{}
		for _, line := range strings.Split(out, "\n") {
			id, rest, _ := strings.Cut(line, " ")
			m[id] = rest
		}
		return m
	}
	// at checks the list sits left, full height and 40 wide, focused, in
	// the window shown, beside pane with.
	at := func(with string) {
		t.Helper()
		w := where()
		lf, wf := strings.Fields(w[list]), strings.Fields(w[with])
		if lf[0] != wf[0] || lf[1] != "0" || lf[2] != "0" || lf[3] != "40" || lf[4] != lf[5] || lf[6] != "1" || lf[7] != "1" {
			t.Fatalf("list %v beside %s %v", lf, with, wf)
		}
	}
	if err := s.Move(list, deep, "40"); err != nil {
		t.Fatal(err)
	}
	at(a.ID)
	if wins := run("list-windows", "-a", "-F", "#{window_id}"); slices.Contains(strings.Fields(wins), first) {
		t.Fatalf("the empty window %s is still there: %s", first, wins)
	}
	win := strings.Fields(where()[a.ID])[0]
	if err := s.Move(list, b.ID, "40"); err != nil {
		t.Fatal(err)
	}
	at(b.ID)
	for _, id := range []string{a.ID, split, deep} {
		if got := strings.Fields(where()[id])[0]; got != win {
			t.Fatalf("%s left its window for %s", id, got)
		}
	}
	if err := s.Move(list, split, "40"); err != nil {
		t.Fatal(err)
	}
	at(deep)
	if n := len(where()); n != 5 {
		t.Fatalf("%d panes, want 5", n)
	}
}

// SocketPath finds the path -L would use without leaving a server behind,
// and the server At that path is the one -L name reaches.
func TestSocketPathIsTheNamedSocket(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	name := fmt.Sprintf("ovtest-path-%d", os.Getpid())
	path, err := SocketPath(name)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != name || !filepath.IsAbs(path) {
		t.Fatalf("path %q for %s", path, name)
	}
	s := At(path, "/dev/null")
	t.Cleanup(func() {
		s.run("kill-server") //nolint:errcheck
		// tmux leaves the socket file behind. The name is this test's own.
		if filepath.Base(path) == name {
			os.Remove(path)
		}
	})
	if panes, err := s.Panes(); err != nil || panes != nil {
		t.Fatalf("asking for the path left a server: %v, %v", panes, err)
	}
	p, err := s.Spawn("ov", "", nil, "sleep", "300")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := SocketPath(name); err != nil || again != path {
		t.Fatalf("with a server running: %q, %v; want %q", again, err, path)
	}
	out, err := exec.Command("tmux", "-L", name, "list-panes", "-a", "-F", "#{pane_id}").Output()
	if err != nil || strings.TrimSpace(string(out)) != p.ID {
		t.Fatalf("tmux -L %s sees %q, %v; want %s", name, out, err, p.ID)
	}
}

func TestPaneOf(t *testing.T) {
	out := "%0\t4100\n%3\t41\n\n%7\t410\ngarbage\t41\n"
	for pid, want := range map[int]string{4100: "%0", 41: "%3", 410: "%7", 4: ""} {
		if got, ok := PaneOf(out, pid); got != want || ok != (want != "") {
			t.Errorf("PaneOf(%d) = %q, %v; want %q", pid, got, ok, want)
		}
	}
}

// A command given as an argument list is the pane's own process, with no
// shell between, which is what lets the view find its pane by its pid.
func TestSelfIsThePaneTmuxStartedTheCommandIn(t *testing.T) {
	s := testServer(t)
	p, err := s.Spawn("ov", "", nil, "sleep", "300")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Spawn("ov", "", nil, "sleep", "301"); err != nil {
		t.Fatal(err)
	}
	out, err := s.run("display-message", "-p", "-t", p.ID, "#{pane_pid}")
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		t.Fatal(err)
	}
	// The pane's process is forked before it execs sleep.
	tmuxtest.Wait(t, "pane "+p.ID+"'s own process to be sleep", func() (bool, string) {
		comm, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
		return err == nil && filepath.Base(strings.TrimSpace(string(comm))) == "sleep", fmt.Sprintf("%q, %v", comm, err)
	})
	if id, ok, err := s.Self(pid); err != nil || !ok || id != p.ID {
		t.Fatalf("Self(%d) = %q, %v, %v; want %s", pid, id, ok, err, p.ID)
	}
	if _, ok, err := s.Self(os.Getpid()); err != nil || ok {
		t.Fatalf("the test process is in pane: %v, %v", ok, err)
	}
	// respawn-pane runs an argument list the same way.
	if err := s.Respawn(p.ID, nil, "sleep", "302"); err != nil {
		t.Fatal(err)
	}
	// tmux reports its own name as the command until the new process has
	// exec'd, so wait for it.
	tmuxtest.Wait(t, "the respawned pane to run sleep under a new pid", func() (bool, string) {
		out, _ := s.run("display-message", "-p", "-t", p.ID, "#{pane_pid} #{pane_current_command}")
		f := strings.Fields(out)
		return len(f) == 2 && f[1] == "sleep" && f[0] != strconv.Itoa(pid), out
	})
}

// A command of one word, such as a claude with no arguments, is run as it
// is too, though tmux would give a lone argument to the shell: a path with
// a space and an = in it, which a shell would split and env would take for
// an assignment, runs.
func TestALoneCommandHasNoShellBetween(t *testing.T) {
	s := testServer(t)
	dir := filepath.Join(t.TempDir(), "a=b dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := filepath.Join(dir, "x=my claude")
	if err := os.WriteFile(cmd, []byte("#!/bin/sh\nexec sleep 300\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := s.Spawn("ov", "", nil, cmd)
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.Spawn("ov", "", nil, "sleep", "300")
	if err != nil {
		t.Fatal(err)
	}
	check := func(id string) {
		t.Helper()
		// the script takes a moment to exec sleep
		tmuxtest.Wait(t, "pane "+id+" to run sleep", func() (bool, string) {
			out, _ := s.run("display-message", "-p", "-t", id, "#{pane_dead} #{pane_current_command}")
			return strings.TrimSpace(out) == "0 sleep", out
		})
	}
	check(p.ID)
	if err := s.Respawn(q.ID, nil, cmd); err != nil {
		t.Fatal(err)
	}
	check(q.ID)
}

// byID lists the server's panes by ID.
func byID(t *testing.T, s Server) map[string]Pane {
	t.Helper()
	panes, err := s.Panes()
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]Pane{}
	for _, p := range panes {
		m[p.ID] = p
	}
	return m
}

// The fields the view derives terminals and attaches from parse from what
// a real tmux writes: a window option shows on every pane of its window and
// on no other, a pane option only on its pane, and the command and path are
// the foreground process's.
func TestPaneFieldsFromRealTmux(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	a, err := s.Spawn("ov", dir, nil, "sleep", "300")
	if err != nil {
		t.Fatal(err)
	}
	if a.Session != "ov" || !strings.HasPrefix(a.Window, "@") {
		t.Fatalf("spawned %+v", a)
	}
	split := strings.TrimSpace(must(t)(s.run("split-window", "-d", "-c", dir, "-P", "-F", "#{pane_id}", "-t", a.ID, "sleep 301")))
	b, err := s.Spawn("ov", "/", nil) // tmux's default shell
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetWindowOption(a.ID, ProjectOption, "/w/a b"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWindowOption(a.ID, TaskOption, "/w/a b#6"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPaneOption(b.ID, AttachOption, "bg1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPaneOption(split, HolderOption, "1"); err != nil {
		t.Fatal(err)
	}
	// The command and path are the foreground process's, which are not
	// the sleeps' and the shell's until they have exec'd.
	var panes map[string]Pane
	tmuxtest.Wait(t, "the panes to report their commands and paths", func() (bool, string) {
		panes = byID(t, s)
		for _, id := range []string{a.ID, split} {
			if p := panes[id]; p.Command != "sleep" || p.Path != dir {
				return false, fmt.Sprintf("%s: %+v", id, p)
			}
		}
		shell := panes[b.ID]
		return shell.Command != "" && shell.Command != "sleep" && shell.Path == "/", fmt.Sprintf("the shell: %+v", shell)
	})
	for _, id := range []string{a.ID, split} {
		p := panes[id]
		if p.Project != "/w/a b" || p.Task != "/w/a b#6" || p.Attach != "" || p.Holder != (id == split) || p.Window != a.Window {
			t.Fatalf("%s: %+v", id, p)
		}
	}
	shell := panes[b.ID]
	if shell.Project != "" || shell.Task != "" || shell.Attach != "bg1" || shell.Holder {
		t.Fatalf("the shell: %+v", shell)
	}
	if err := s.KillWindow(a.Window); err != nil {
		t.Fatal(err)
	}
	panes = byID(t, s)
	if _, ok := panes[a.ID]; ok || len(panes) != 1 {
		t.Fatalf("after kill-window: %+v", panes)
	}
	if err := s.KillWindow(a.Window); err == nil {
		t.Fatal("killing a window that is gone did not fail")
	}
}

// SelectBeside from the list on the left picks, of the panes to its right,
// the one used last, so enter on a terminal lands where the user was.
func TestSelectBesidePicksTheLastUsedPane(t *testing.T) {
	s := testServer(t)
	run := func(args ...string) string {
		t.Helper()
		return strings.TrimSpace(must(t)(s.run(args...)))
	}
	list := run("new-session", "-d", "-s", "ov", "-x", "200", "-y", "50", "-P", "-F", "#{pane_id}", "sleep 300")
	top := run("split-window", "-h", "-t", list, "-P", "-F", "#{pane_id}", "sleep 300")
	bottom := run("split-window", "-v", "-t", top, "-P", "-F", "#{pane_id}", "sleep 300")
	active := func() string { return run("display-message", "-p", "-t", "=ov:", "#{pane_id}") }
	for _, last := range []string{top, bottom, top} {
		if err := s.Select(last); err != nil {
			t.Fatal(err)
		}
		if err := s.Select(list); err != nil {
			t.Fatal(err)
		}
		if err := s.SelectBeside(list); err != nil {
			t.Fatal(err)
		}
		if got := active(); got != last {
			t.Fatalf("after using %s, select-pane -R chose %s", last, got)
		}
	}
}

func must(t *testing.T) func(string, error) string {
	return func(out string, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
}

// A size in percent makes the list that share of its new window's width.
func TestMoveTakesAPercentOfTheWindow(t *testing.T) {
	s := testServer(t)
	list := strings.TrimSpace(must(t)(s.run("new-session", "-d", "-s", "ov", "-x", "200", "-y", "50", "-P", "-F", "#{pane_id}", "sleep", "300")))
	target, err := s.Spawn("ov", "", nil, "sleep", "300")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Move(list, target.ID, "30%"); err != nil {
		t.Fatal(err)
	}
	p := byID(t, s)[list]
	if p.Window != target.Window || p.W < 59 || p.W > 60 {
		t.Fatalf("the list is %d wide in %s, want 30%% of 200 beside %s", p.W, p.Window, target.ID)
	}
}

// A window that narrows takes its lost columns from the leftmost pane, the
// list, so a window that was wider when last shown squeezes the list moved
// into it. SetWidth gives the list its width back.
func TestANarrowingWindowSqueezesTheList(t *testing.T) {
	s := testServer(t)
	list := strings.TrimSpace(must(t)(s.run("new-session", "-d", "-s", "ov", "-x", "200", "-y", "50", "-P", "-F", "#{pane_id}", "sleep", "300")))
	target, err := s.Spawn("ov", "", nil, "sleep", "300")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Move(list, target.ID, "60"); err != nil {
		t.Fatal(err)
	}
	must(t)(s.run("resize-window", "-t", target.Window, "-x", "150"))
	if p := byID(t, s)[list]; p.WinW != 150 || p.W >= 60 {
		t.Fatalf("the list is %d wide in a window %d wide, want it squeezed in 150", p.W, p.WinW)
	}
	if err := s.SetWidth(list, 60); err != nil {
		t.Fatal(err)
	}
	if p := byID(t, s)[list]; p.W != 60 {
		t.Fatalf("the list is %d wide after SetWidth 60", p.W)
	}
}

// respawn-pane keeps a pane's options, which is how the view's pane keeps
// its marks across a restart.
func TestRespawnKeepsPaneOptions(t *testing.T) {
	s := testServer(t)
	p, err := s.Spawn("ov", "", nil, "sleep", "300")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkList(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPaneOption(p.ID, ViewOption, `"--theme" "x"`); err != nil {
		t.Fatal(err)
	}
	if err := s.Respawn(p.ID, nil, "sleep", "301"); err != nil {
		t.Fatal(err)
	}
	if got := byID(t, s)[p.ID]; !got.List || got.View != `"--theme" "x"` {
		t.Fatalf("after respawn: %+v", got)
	}
}

// A pane is focused when it is the active pane of the current window and
// a client is attached to its session.
func TestFocused(t *testing.T) {
	sock := tmuxtest.New(t, "")
	s := At(sock.Path, "/dev/null")
	list := sock.Must("new-session", "-d", "-s", "ov", "-x", "80", "-y", "24", "-P", "-F", "#{pane_id}", "sleep", "300")
	other := sock.Must("split-window", "-h", "-t", list, "-P", "-F", "#{pane_id}", "sleep", "300")
	want := func(pane string, focused bool) {
		t.Helper()
		if got, err := s.Focused(pane); err != nil || got != focused {
			t.Fatalf("Focused(%s) = %v, %v; want %v", pane, got, err, focused)
		}
	}
	sock.Must("select-pane", "-t", list)
	want(list, false) // no client
	term := tmuxtest.New(t, "")
	term.Must("new-session", "-d", "-x", "80", "-y", "24", "env", "-u", "TMUX", "tmux", "-S", sock.Path, "attach-session", "-t", "=ov")
	tmuxtest.Wait(t, "the client to attach", func() (bool, string) {
		out, _ := sock.Run("list-clients")
		return out != "", out
	})
	want(list, true)
	want(other, false)
	sock.Must("select-pane", "-t", other)
	want(list, false)
	want(other, true)
	sock.Must("new-window", "-t", "=ov:", "sleep", "300")
	want(other, false) // its window is no longer the current one
}

// A review opens in a split above its session's pane, and the paste of a
// message reaches the pane whole, however long, with Enter after it.
func TestSplitCaptureAndPaste(t *testing.T) {
	s := testServer(t)
	target, err := s.Spawn("ov", "/", nil, "cat")
	if err != nil {
		t.Fatal(err)
	}
	split, err := s.Split(target.ID, "/", "40%", nil, "sleep", "300")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPaneOption(split.ID, ReviewOption, "sid-1"); err != nil {
		t.Fatal(err)
	}
	panes, err := s.Panes()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, p := range panes {
		if p.ID == split.ID {
			found = p.Review == "sid-1" && p.Window == target.Window
		}
	}
	if !found {
		t.Fatalf("the split should be marked and beside its target: %+v", panes)
	}
	top := func(id string) string {
		out, _ := s.run("display-message", "-p", "-t", id, "#{pane_top}")
		return strings.TrimSpace(out)
	}
	if top(split.ID) != "0" || top(target.ID) == "0" {
		t.Fatalf("the split should sit above its target: tops %s and %s", top(split.ID), top(target.ID))
	}
	if err := s.Resize(split.ID, "5"); err != nil {
		t.Fatal(err)
	}
	if err := s.Zoom(split.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Zoom(split.ID); err != nil {
		t.Fatal(err)
	}
	// Live leaves copy mode, where a paste would go in unbracketed.
	if _, err := s.run("copy-mode", "-t", target.ID); err != nil {
		t.Fatal(err)
	}
	tmuxtest.Wait(t, "the pane to report cat", func() (bool, string) {
		command, _, err := s.Live(target.ID)
		return err == nil && command == "cat", fmt.Sprintf("command %q err %v", command, err)
	})
	if out, _ := s.run("display-message", "-p", "-t", target.ID, "#{pane_in_mode}"); strings.TrimSpace(out) != "0" {
		t.Fatalf("still in copy mode: %q", out)
	}
	// More than tmux takes as a command's arguments.
	long := strings.Repeat("x", 40000)
	text := "first line\nsecond line " + long[:10] + "\n" + long
	if err := s.Paste(target.ID, "ovt-msg", text); err != nil {
		t.Fatal(err)
	}
	if out, _ := s.run("list-buffers"); strings.Contains(out, "ovt-msg") {
		t.Fatalf("the buffer should go with the paste: %q", out)
	}
	tmuxtest.Wait(t, "the paste to arrive whole", func() (bool, string) {
		_, out, err := s.Live(target.ID)
		if err != nil {
			t.Fatal(err)
		}
		// cat echoes the typed line, then writes it back after Enter.
		return strings.Count(out, "second line xxxxxxxxxx") == 2, out
	})
}

// Zoom makes a pane fill its window, and the listing says so for every
// pane of that window until a second Zoom puts the window back.
func TestZoomShowsInTheListing(t *testing.T) {
	s := testServer(t)
	list, err := s.Spawn("ov", "/", nil, "sleep", "300")
	if err != nil {
		t.Fatal(err)
	}
	beside, err := s.Split(list.ID, "/", "50%", nil, "sleep", "300")
	if err != nil {
		t.Fatal(err)
	}
	zoomed := func() map[string]bool {
		panes, err := s.Panes()
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, p := range panes {
			out[p.ID] = p.Zoomed
		}
		return out
	}
	if z := zoomed(); z[list.ID] || z[beside.ID] {
		t.Fatalf("zoomed before Zoom: %v", z)
	}
	if err := s.Zoom(list.ID); err != nil {
		t.Fatal(err)
	}
	if z := zoomed(); !z[list.ID] || !z[beside.ID] {
		t.Fatalf("after Zoom: %v", z)
	}
	if err := s.Zoom(list.ID); err != nil {
		t.Fatal(err)
	}
	if z := zoomed(); z[list.ID] || z[beside.ID] {
		t.Fatalf("after a second Zoom: %v", z)
	}
}

// SetZoom says what the window should be, not which way to toggle it:
// asking twice for either leaves it so, and a pane alone in its window
// stays unzoomed.
func TestSetZoomIsIdempotent(t *testing.T) {
	s := testServer(t)
	list, err := s.Spawn("ov", "/", nil, "sleep", "300")
	if err != nil {
		t.Fatal(err)
	}
	zoomed := func(id string) bool {
		panes, err := s.Panes()
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range panes {
			if p.ID == id {
				return p.Zoomed
			}
		}
		t.Fatalf("no pane %s in %+v", id, panes)
		return false
	}
	if err := s.SetZoom(list.ID, true); err != nil {
		t.Fatal(err)
	}
	if zoomed(list.ID) {
		t.Fatal("a pane alone in its window was zoomed")
	}
	if _, err := s.Split(list.ID, "/", "50%", nil, "sleep", "300"); err != nil {
		t.Fatal(err)
	}
	for i, on := range []bool{true, true, false, false, true, false} {
		if err := s.SetZoom(list.ID, on); err != nil {
			t.Fatal(err)
		}
		if got := zoomed(list.ID); got != on {
			t.Fatalf("step %d: SetZoom(%v) left the window zoomed %v", i, on, got)
		}
	}
}

// framed starts a server with a session "ov" of two panes side by side,
// the list on the left and one beside it, both running cat so that what is
// typed in the focused one shows on its screen. A client of a second
// server is attached at 60 by 12 cells, and its screen is what the test
// reads: the colours and text tmux draws.
type framed struct {
	t            *testing.T
	s            Server
	client       tmuxtest.Socket
	list, beside string
	col          int // the column of the border between the two
}

func newFramed(t *testing.T) framed {
	t.Helper()
	sock := tmuxtest.New(t, "")
	s := At(sock.Path, "/dev/null")
	list := strings.TrimSpace(must(t)(s.run("new-session", "-d", "-s", "ov", "-x", "60", "-y", "12", "-P", "-F", "#{pane_id}", "cat")))
	beside := strings.TrimSpace(must(t)(s.run("split-window", "-h", "-d", "-t", list, "-P", "-F", "#{pane_id}", "cat")))
	if err := s.MarkList(list); err != nil {
		t.Fatal(err)
	}
	if err := s.Frame("ov", Colours{Accent: "#ff0000", Border: "#111111", Text: "#eeeeee", Muted: "#777777"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPaneOptions([]PaneSet{{list, TitleOption, "one"}, {beside, TitleOption, "two"}}); err != nil {
		t.Fatal(err)
	}
	col, err := strconv.Atoi(strings.TrimSpace(must(t)(s.run("display-message", "-p", "-t", list, "#{pane_width}"))))
	if err != nil {
		t.Fatal(err)
	}
	must(t)(s.run("set-option", "-g", "status", "off"))
	client := tmuxtest.New(t, "")
	client.Must("new-session", "-d", "-x", "60", "-y", "12", "tmux", "-S", sock.Path, "-f", "/dev/null", "attach", "-t", "ov")
	client.Must("set-option", "-g", "status", "off")
	return framed{t, s, client, list, beside, col}
}

// active is the id of the active pane.
func (f framed) active() string {
	f.t.Helper()
	return strings.TrimSpace(must(f.t)(f.s.run("display-message", "-p", "-t", "ov:", "#{pane_id}")))
}

// table is the key table of the attached client.
func (f framed) table() string {
	f.t.Helper()
	return strings.TrimSpace(must(f.t)(f.s.run("list-clients", "-F", "#{client_key_table}")))
}

// until polls for cond, which the client takes a moment to meet.
func (f framed) until(what string, cond func() bool) {
	f.t.Helper()
	tmuxtest.Wait(f.t, what, func() (bool, string) { return cond(), "" })
}

// rows is the client's screen, once it shows want in the border's top
// cell.
func (f framed) rows(step, want string) [][]drawn {
	f.t.Helper()
	var rows [][]drawn
	tmuxtest.Wait(f.t, step+": the border's top to be "+want, func() (bool, string) {
		rows = cells(f.client.Must("capture-pane", "-e", "-p"))
		return len(rows) > 1 && len(rows[0]) > f.col && rows[0][f.col].fg == want, fmt.Sprint(rows)
	})
	return rows
}

// top is the text of the client's top row.
func (f framed) top() string {
	f.t.Helper()
	return strings.SplitN(f.client.Must("capture-pane", "-p"), "\n", 2)[0]
}

// send types keys at the client, as a user at its terminal would.
func (f framed) send(keys ...string) {
	f.t.Helper()
	f.client.Must(append([]string{"send-keys"}, keys...)...)
}

// The active pane's whole border is drawn in the accent colour, and the
// rule along the top of the other pane stays in the border colour. The
// pane that is active is tmux's to say, so moving the focus either way
// moves the highlight. A second server's pane is the client, so the test
// reads the colours tmux draws.
func TestFrameLightsTheActivePane(t *testing.T) {
	f := newFramed(t)
	const accent, border = "255;0;0", "17;17;17"
	check := func(step, listRule, besideRule string) {
		t.Helper()
		rows := f.rows(step, accent)
		for y := 1; y < len(rows) && len(rows[y]) > f.col; y++ {
			if c := rows[y][f.col]; c.r != "│" || c.fg != accent {
				t.Fatalf("%s: row %d of the border is %q in %s, want %s", step, y, c.r, c.fg, accent)
			}
		}
		if c := rows[0][f.col-1]; c.r != "─" || c.fg != listRule {
			t.Fatalf("%s: the end of the list's rule is %q in %s, want %s", step, c.r, c.fg, listRule)
		}
		if last := rows[0][len(rows[0])-1]; last.r != "─" || last.fg != besideRule {
			t.Fatalf("%s: the end of the rule beside is %q in %s, want %s", step, last.r, last.fg, besideRule)
		}
	}
	must(t)(f.s.run("select-pane", "-t", f.list))
	check("the list active", accent, border)
	must(t)(f.s.run("select-pane", "-t", f.beside))
	check("the pane beside active", border, accent)
}

// The panels mode is a key table: in it h and l move the focus and stay in
// the mode, enter and esc leave it, and ctrl+[ goes to the list. Each key
// is typed at the client, so it takes the path a user's does. While a
// client is in the mode, the active pane's rule says that enter acts
// there, and no other rule does, and the listing of the panes says so.
func TestPanelsModeMovesFocus(t *testing.T) {
	f := newFramed(t)
	if err := f.s.BindPanels(); err != nil {
		t.Fatal(err)
	}
	must(t)(f.s.run("select-pane", "-t", f.list))
	if f.table() != "root" || strings.Contains(f.top(), "enter to act") {
		t.Fatalf("before the mode: table %q, top %q", f.table(), f.top())
	}
	enter := func() {
		t.Helper()
		if err := f.s.EnterPanels(); err != nil {
			t.Fatal(err)
		}
		f.until("the client did not enter the mode", func() bool { return f.table() == PanelsTable })
	}
	// at says which pane the marker is in, by its column, -1 for none.
	at := func() int { return strings.Index(f.top(), "enter to act") }
	// moved waits for the focus to reach pane id with the mode still on.
	moved := func(step, id string) {
		t.Helper()
		f.until(step+": the focus did not reach "+id, func() bool { return f.active() == id })
		if f.table() != PanelsTable {
			t.Fatalf("%s: the key table is %q, not the mode's", step, f.table())
		}
	}

	enter()
	f.until("the marker does not show on the list's rule", func() bool { return at() >= 0 && at() < f.col })
	panes, err := f.s.Panes()
	if err != nil || len(panes) != 2 || !panes[0].Panels || !panes[1].Panels {
		t.Fatalf("the listing should say a client is in the mode: %+v, %v", panes, err)
	}
	f.send("l")
	moved("l", f.beside)
	f.until("the marker does not move to the rule beside", func() bool { return at() > f.col })
	f.send("h")
	moved("h", f.list)

	// enter leaves the mode, and the next keys reach the pane.
	f.send("Enter")
	f.until("enter did not leave the mode", func() bool { return f.table() == "root" })
	f.until("the marker stayed after the mode", func() bool { return at() < 0 })
	f.send("a")
	f.until("a key did not reach the pane after enter", func() bool {
		return strings.Contains(must(t)(f.s.run("capture-pane", "-p", "-t", f.list)), "a")
	})
	if panes, err := f.s.Panes(); err != nil || panes[0].Panels {
		t.Fatalf("the listing should say no client is in the mode: %+v, %v", panes, err)
	}

	// esc leaves it too.
	enter()
	f.send("Escape")
	f.until("esc did not leave the mode", func() bool { return f.table() == "root" })

	// ctrl+[ is bound to the list. A terminal without extended keys sends
	// it as esc, so the key is not typed at the client here.
	keys := must(t)(f.s.run("list-keys", "-T", PanelsTable))
	if !strings.Contains(keys, "C-[") || !strings.Contains(keys, "{top-left}") {
		t.Fatalf("ctrl+[ is not bound to the list:\n%s", keys)
	}
	must(t)(f.s.run("select-pane", "-t", f.beside))
	enter()
	must(t)(f.s.run("send-keys", "-K", "-c", strings.TrimSpace(must(t)(f.s.run("list-clients", "-F", "#{client_name}"))), "C-["))
	f.until("ctrl+[ did not go to the list and leave the mode", func() bool { return f.active() == f.list && f.table() == "root" })
}

// drawn is one cell of a captured screen: its character and its
// foreground, as the r;g;b of an SGR sequence, "" for the default.
type drawn struct{ r, fg string }

// cells splits what capture-pane -e prints into rows of cells, following
// the foreground colours tmux sets.
func cells(out string) [][]drawn {
	var rows [][]drawn
	for _, line := range strings.Split(out, "\n") {
		var row []drawn
		fg := ""
		for rs := []rune(line); len(rs) > 0; {
			if rs[0] != '\x1b' || len(rs) < 2 || rs[1] != '[' {
				row = append(row, drawn{string(rs[0]), fg})
				rs = rs[1:]
				continue
			}
			end := 2
			for end < len(rs) && (rs[end] < 0x40 || rs[end] > 0x7e) {
				end++
			}
			if end < len(rs) && rs[end] == 'm' {
				params := strings.Split(string(rs[2:end]), ";")
				for i := 0; i < len(params); i++ {
					switch {
					case params[i] == "" || params[i] == "0" || params[i] == "39":
						fg = ""
					case params[i] == "38" && i+4 < len(params) && params[i+1] == "2":
						fg = strings.Join(params[i+2:i+5], ";")
						i += 4
					case params[i] == "38" && i+2 < len(params):
						fg = "5;" + params[i+2]
						i += 2
					}
				}
			}
			rs = rs[min(end+1, len(rs)):]
		}
		rows = append(rows, row)
	}
	return rows
}

// A zoomed list draws its own frame, so the window's rules go with the
// zoom and come back when it ends. The listing says which it is.
func TestSetZoomTakesTheRulesAway(t *testing.T) {
	s := testServer(t)
	list, err := s.Spawn("ov", "/", nil, "sleep", "300")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Split(list.ID, "/", "50%", nil, "sleep", "300"); err != nil {
		t.Fatal(err)
	}
	if err := s.Frame("ov", Colours{Accent: "#ff0000", Border: "#111111", Text: "#eeeeee"}); err != nil {
		t.Fatal(err)
	}
	framed := func() bool {
		panes, err := s.Panes()
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range panes {
			if p.ID == list.ID {
				return p.Framed
			}
		}
		t.Fatal("no list")
		return false
	}
	if !framed() {
		t.Fatal("Frame left the window without rules")
	}
	if err := s.SetZoom(list.ID, true); err != nil || framed() {
		t.Fatalf("zoomed: err %v, framed %v", err, framed())
	}
	if err := s.SetZoom(list.ID, false); err != nil || !framed() {
		t.Fatalf("unzoomed: err %v, framed %v", err, framed())
	}
	if err := s.SetFrame(list.ID, false); err != nil || framed() {
		t.Fatalf("SetFrame off: err %v, framed %v", err, framed())
	}
}
