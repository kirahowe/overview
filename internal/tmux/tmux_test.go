package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

func TestParsePanes(t *testing.T) {
	out := "%0\t/dev/ttys004\toverview-work\t@0\t60\t40\t1\n" +
		"%3\t/dev/ttys009\toverview-work-parked\t@2\t80\t24\t\n" +
		"garbage\n" +
		"%4\t/dev/ttys010\ts\t@3\twide\t24\t\n" +
		"\n"
	got := ParsePanes(out)
	want := []Pane{
		{ID: "%0", TTY: "ttys004", Session: "overview-work", Window: "@0", W: 60, H: 40, List: true},
		{ID: "%3", TTY: "ttys009", Session: "overview-work-parked", Window: "@2", W: 80, H: 24},
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
			}
		})).Draw(t, "panes")
		var b strings.Builder
		for _, p := range panes {
			list := ""
			if p.List {
				list = "1"
			}
			fmt.Fprintf(&b, "%s\t/dev/%s\t%s\t%s\t%d\t%d\t%s\n", p.ID, p.TTY, p.Session, p.Window, p.W, p.H, list)
		}
		if got := ParsePanes(b.String()); fmt.Sprint(got) != fmt.Sprint(panes) {
			t.Fatalf("got %+v\nwant %+v", got, panes)
		}
	})
}

// testServer is a real tmux server on a socket of its own, killed when the
// test ends.
func testServer(t *testing.T) Server {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	sock := fmt.Sprintf("overview-test-%d", os.Getpid())
	s := On(sock, "/dev/null")
	t.Cleanup(func() { s.Run("kill-server") }) //nolint:errcheck
	return s
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
	// "ov" must not also pick out "ov-parked".
	a, err := s.Spawn("ov-parked", "/", nil, "sleep 300")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Spawn("ov-parked", "", nil, "sleep 300")
	if err != nil {
		t.Fatal(err)
	}
	// b gets a split of its own, and the list moves in beside both.
	split, err := s.Run("split-window", "-d", "-v", "-P", "-F", "#{pane_id}", "-t", b.ID, "sleep 300")
	if err != nil {
		t.Fatal(err)
	}
	split = strings.TrimSpace(split)
	for _, w := range []string{"ov-parked:0", "ov-parked:1"} {
		if err := s.MoveWindow(w, "ov"); err != nil {
			t.Fatal(err)
		}
	}
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
	if err := s.SetOption("ov", "@overview-holder", a.ID); err != nil {
		t.Fatal(err)
	}
	if got := s.Option("ov", "@overview-holder"); got != a.ID {
		t.Fatalf("option %q, want %q", got, a.ID)
	}
	if got := s.Option("ov-parked", "@overview-holder"); got != "" {
		t.Fatalf("the parked session has the option: %q", got)
	}
	view, err := s.Open("ov", nil, "sleep 300")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Select(view.ID); err != nil {
		t.Fatal(err)
	}
	if out, _ := s.Run("display-message", "-p", "-t", "=ov:", "#{pane_id}"); strings.TrimSpace(out) != view.ID {
		t.Fatalf("the opened window is not current: %q", out)
	}
}

// Move puts the list in a full-height column on the left of the target's
// window, shows that window and focuses the list. The window the list
// leaves closes when it is empty, and otherwise keeps its panes, splits
// included, for when the list comes back.
func TestMoveTakesTheListAcrossWindows(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	s := On(fmt.Sprintf("ovtest-move-%d", os.Getpid()), "/dev/null")
	t.Cleanup(func() { s.Run("kill-server") }) //nolint:errcheck
	run := func(args ...string) string {
		t.Helper()
		out, err := s.Run(args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	list := run("new-session", "-d", "-s", "ov", "-x", "200", "-y", "50", "-P", "-F", "#{pane_id}", "sleep 300")
	first := run("display-message", "-p", "-t", list, "#{window_id}")
	a, err := s.Spawn("ov", "", nil, "sleep 300")
	if err != nil {
		t.Fatal(err)
	}
	split := run("split-window", "-d", "-v", "-t", a.ID, "-P", "-F", "#{pane_id}", "sleep 300")
	deep := run("split-window", "-d", "-h", "-t", split, "-P", "-F", "#{pane_id}", "sleep 300")
	b, err := s.Spawn("ov", "", nil, "sleep 300")
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
