package tmux

import (
	"fmt"
	"os"
	"os/exec"
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
	if err := s.Join(a.ID, list.ID, "70%"); err != nil {
		t.Fatal(err)
	}
	if err := s.Swap(b.ID, a.ID); err != nil {
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
	if where[b.ID].Window != where[list.ID].Window || where[a.ID].Session != "ov-parked" {
		t.Fatalf("b should sit beside the list and a be parked: %+v", panes)
	}
	if where[a.ID].TTY != a.TTY || !strings.HasPrefix(a.TTY, "ttys") {
		t.Fatalf("tty %q, listed as %q", a.TTY, where[a.ID].TTY)
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
	left, err := s.SplitLeft(b.ID, "30%", nil, "sleep 300")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Select(left.ID); err != nil {
		t.Fatal(err)
	}
}
