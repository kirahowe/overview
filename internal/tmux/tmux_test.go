package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"overview/internal/tmuxtest"
)

func TestParsePanes(t *testing.T) {
	out := "%0\t/dev/ttys004\toverview-work\t@0\t60\t40\t1\toverview\t\t\t\t\"--theme\" \"x\"\t\t/w\n" +
		"%3\t/dev/ttys009\toverview-other\t@2\t80\t24\t\tnode\t/w/app\tbg1\t1\t\tsid-1\t/w/app/odd\tdir\n" +
		"garbage\n" +
		"%4\t/dev/ttys010\ts\t@3\twide\t24\t\tzsh\t\t\t\t\t\t/\n" +
		"%5\t/dev/ttys011\ts\t@3\t1\t24\t\n" +
		"\n"
	got := ParsePanes(out)
	want := []Pane{
		{ID: "%0", TTY: "ttys004", Session: "overview-work", Window: "@0", W: 60, H: 40, List: true, Command: "overview",
			View: `"--theme" "x"`, Path: "/w"},
		{ID: "%3", TTY: "ttys009", Session: "overview-other", Window: "@2", W: 80, H: 24,
			Command: "node", Project: "/w/app", Attach: "bg1", Holder: true, Review: "sid-1", Path: "/w/app/odd\tdir"},
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
				List:    rapid.Bool().Draw(t, "list"),
				Command: rapid.SampledFrom([]string{"", "zsh", "node", "claude", "-zsh"}).Draw(t, "cmd"),
				Project: rapid.SampledFrom([]string{"", "/w/app", "/w/a b"}).Draw(t, "project"),
				Attach:  rapid.SampledFrom([]string{"", "bg1"}).Draw(t, "attach"),
				Holder:  rapid.Bool().Draw(t, "holder"),
				View:    rapid.SampledFrom([]string{"", `"--theme" "a b"`}).Draw(t, "view"),
				Review:  rapid.SampledFrom([]string{"", "5f2c-1d"}).Draw(t, "review"),
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
			fmt.Fprintf(&b, "%s\t/dev/%s\t%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				p.ID, p.TTY, p.Session, p.Window, p.W, p.H, flag(p.List), p.Command, p.Project, p.Attach, flag(p.Holder), p.View, p.Review, p.Path)
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
	comm, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil || filepath.Base(strings.TrimSpace(string(comm))) != "sleep" {
		t.Fatalf("pane %s runs %q, %v; want sleep itself", p.ID, comm, err)
	}
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
	out, _ = s.run("display-message", "-p", "-t", p.ID, "#{pane_pid} #{pane_current_command}")
	if f := strings.Fields(out); len(f) != 2 || f[1] != "sleep" || f[0] == strconv.Itoa(pid) {
		t.Fatalf("after respawn: %q", out)
	}
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
		var out string
		for range 40 { // the script takes a moment to exec sleep
			out, _ = s.run("display-message", "-p", "-t", id, "#{pane_dead} #{pane_current_command}")
			if strings.TrimSpace(out) == "0 sleep" {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("pane %s: %q, want it running sleep", id, out)
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
	if a.Session != "ov" || !strings.HasPrefix(a.Window, "@") || a.Path != dir {
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
	if err := s.SetPaneOption(b.ID, AttachOption, "bg1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPaneOption(split, HolderOption, "1"); err != nil {
		t.Fatal(err)
	}
	panes := byID(t, s)
	for _, id := range []string{a.ID, split} {
		p := panes[id]
		if p.Project != "/w/a b" || p.Attach != "" || p.Holder != (id == split) || p.Command != "sleep" || p.Path != dir || p.Window != a.Window {
			t.Fatalf("%s: %+v", id, p)
		}
	}
	shell := panes[b.ID]
	if shell.Project != "" || shell.Attach != "bg1" || shell.Holder || shell.Command == "" || shell.Command == "sleep" || shell.Path != "/" {
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
	for range 100 {
		if out, _ := sock.Run("list-clients"); out != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	want(list, true)
	want(other, false)
	sock.Must("select-pane", "-t", other)
	want(list, false)
	want(other, true)
	sock.Must("new-window", "-t", "=ov:", "sleep", "300")
	want(other, false) // its window is no longer the current one
}

// A review opens in a split under its session's pane, and the paste of a
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
	command, _, err := s.Live(target.ID)
	if err != nil || command != "cat" {
		t.Fatalf("command %q err %v", command, err)
	}
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
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, out, err := s.Live(target.ID)
		if err != nil {
			t.Fatal(err)
		}
		// cat echoes the typed line, then writes it back after Enter.
		if strings.Count(out, "second line xxxxxxxxxx") == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the paste never arrived whole:\n%s", out)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
