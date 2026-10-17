package ghostty

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// comms pairs each command name with whether it is Ghostty, so the expected
// answer is known by construction rather than recomputed.
var comms = []struct {
	name    string
	ghostty bool
}{
	{"/Applications/Ghostty.app/Contents/MacOS/ghostty", true},
	{"ghostty", true},
	{"/opt/Ghostty Dev.app/Contents/MacOS/GHOSTTY", true},
	{"-/bin/zsh", false},
	{"/usr/bin/login", false},
	{"tmux", false},
	{"/Applications/Ghostty.app/Contents/MacOS/ghostty-helper", false},
	{"/Applications/iTerm.app/Contents/MacOS/iTerm2", false},
	{"/Applications/Ghostty.app", false},
}

// A chain of processes from the session up, ending in a missing parent,
// pid 0, launchd, or a cycle back into the chain, plus unrelated processes
// (some of them Ghostty) that are nobody's ancestor.
func TestPropUnderGhostty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 16).Draw(t, "len")
		procs := map[int]proc{}
		isG := make([]bool, n)
		for i := 0; i < n; i++ {
			c := comms[rapid.IntRange(0, len(comms)-1).Draw(t, "comm")]
			procs[1000+i] = proc{ppid: 1000 + i + 1, comm: c.name}
			isG[i] = c.ghostty
		}
		// What the walk visits after the session, in order, by construction.
		var walk []bool
		for i := 1; i < n; i++ {
			walk = append(walk, isG[i])
		}
		top := 1000 + n - 1
		switch end := rapid.SampledFrom([]string{"missing", "zero", "launchd", "cycle"}).Draw(t, "end"); end {
		case "missing":
			procs[top] = proc{ppid: 5, comm: procs[top].comm}
		case "zero":
			procs[top] = proc{ppid: 0, comm: procs[top].comm}
		case "launchd":
			procs[top] = proc{ppid: 1, comm: procs[top].comm}
			procs[1] = proc{ppid: 0, comm: "/sbin/launchd"}
			walk = append(walk, false)
		case "cycle":
			j := rapid.IntRange(0, n-1).Draw(t, "back")
			procs[top] = proc{ppid: 1000 + j, comm: procs[top].comm}
			for len(walk) < maxHops {
				walk = append(walk, isG[j:]...)
			}
		}
		for k := rapid.IntRange(0, 6).Draw(t, "noise"); k > 0; k-- {
			c := comms[rapid.IntRange(0, len(comms)-1).Draw(t, "ncomm")]
			procs[2000+k] = proc{ppid: rapid.SampledFrom([]int{0, 1, 1000, 2001, 9999}).Draw(t, "nppid"), comm: c.name}
		}

		want := false
		for i, g := range walk {
			if i < maxHops && g {
				want = true
			}
		}
		if got := underGhostty(procs, 1000); got != want {
			t.Fatalf("underGhostty = %v, want %v (walk %v)", got, want, walk)
		}
	})
}

func TestUnderGhosttyEdges(t *testing.T) {
	procs := map[int]proc{
		10: {ppid: 9, comm: "claude"}, 9: {ppid: 8, comm: "-/bin/zsh"},
		8: {ppid: 7, comm: "/usr/bin/login"}, 7: {ppid: 1, comm: "/Applications/Ghostty.app/Contents/MacOS/ghostty"},
		1:  {ppid: 0, comm: "/sbin/launchd"},
		20: {ppid: 21, comm: "claude"}, 21: {ppid: 22, comm: "-zsh"}, 22: {ppid: 1, comm: "tmux"},
		30: {ppid: 0, comm: "ghostty"},
	}
	for pid, want := range map[int]bool{10: true, 9: true, 7: false, 20: false, 30: false, 404: false} {
		if got := underGhostty(procs, pid); got != want {
			t.Errorf("pid %d: got %v want %v", pid, got, want)
		}
	}
}

func TestPropParsePS(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		want := map[int]proc{}
		var b strings.Builder
		for k := rapid.IntRange(0, 8).Draw(t, "n"); k > 0; k-- {
			pid := rapid.IntRange(1, 99999).Draw(t, "pid")
			p := proc{ppid: rapid.IntRange(0, 99999).Draw(t, "ppid"),
				comm: comms[rapid.IntRange(0, len(comms)-1).Draw(t, "comm")].name}
			want[pid] = p
			fmt.Fprintf(&b, "%5d %5d %s\n", pid, p.ppid, p.comm)
			if rapid.Bool().Draw(t, "junk") {
				b.WriteString(rapid.SampledFrom([]string{"", "  ", "x y z", "12", "1 2"}).Draw(t, "line") + "\n")
			}
		}
		if got := parsePS(b.String()); !reflect.DeepEqual(got, want) {
			t.Fatalf("parsePS(%q) = %v, want %v", b.String(), got, want)
		}
	})
}

func TestInGhosttyRunsPSOnce(t *testing.T) {
	calls := 0
	c := Client{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls++
		if name != "ps" {
			t.Fatalf("ran %s", name)
		}
		return []byte("  10     7 claude\n   7     1 /Applications/Ghostty.app/Contents/MacOS/ghostty\n"), nil
	}}
	ok, err := c.InGhostty(context.Background(), 10)
	if !ok || err != nil || calls != 1 {
		t.Fatalf("ok=%v err=%v calls=%d", ok, err, calls)
	}
	c.Run = func(context.Context, string, ...string) ([]byte, error) { return nil, fmt.Errorf("boom") }
	if ok, err := c.InGhostty(context.Background(), 10); ok || err == nil {
		t.Fatalf("ps failure: ok=%v err=%v", ok, err)
	}
}
