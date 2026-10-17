package tui

import (
	"fmt"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"overview/internal/fleet"
)

// psSample is trimmed from a real `ps -A -o pid=,ppid=,tty=,comm=`: a
// session an Obsidian plugin started, one in Ghostty, and one ps has lost.
const psSample = `    1     0 ??       /sbin/launchd
  700     1 ??       /Applications/Obsidian.app/Contents/MacOS/Obsidian
63372   700 ??       /Applications/Obsidian.app/Contents/Frameworks/Obsidian Helper (Renderer).app/Contents/MacOS/Obsidian Helper (Renderer)
63381 63372 ??       /opt/homebrew/bin/claude
  900     1 ??       /Applications/Ghostty.app/Contents/MacOS/ghostty
  901   900 ttys004  /usr/bin/login
  902   901 ttys004  -zsh
  903   902 ttys004  claude
 5000     1 ??       /usr/local/bin/claude
not a line
`

func TestPlaces(t *testing.T) {
	rows := []fleet.Row{
		row("obs", "claude-bd", "", 63381),
		row("term", "overview-5f", "", 903),
		row("gone", "old", "", 4242),
		row("bare", "cron", "", 5000),
		{Live: fleet.Live{SessionID: "bg", PID: 63381, Kind: "background"}},
	}
	ttys, hosts := places(parseProcs(psSample), rows)
	if len(ttys) != 1 || ttys[903] != "ttys004" {
		t.Fatalf("ttys %v", ttys)
	}
	want := map[int]host{
		63381: {Name: "Obsidian", Path: "/Applications/Obsidian.app"},
		5000:  {},
	}
	if fmt.Sprint(hosts) != fmt.Sprint(want) {
		t.Fatalf("hosts %v, want %v", hosts, want)
	}
}

func TestAppBundle(t *testing.T) {
	for comm, want := range map[string]string{
		"/Applications/Visual Studio Code.app/Contents/Frameworks/Code Helper (Plugin).app/Contents/MacOS/Code Helper (Plugin)": "Visual Studio Code|/Applications/Visual Studio Code.app",
		"/Users/kira/Applications/Zed.app/Contents/MacOS/zed":                                                                   "Zed|/Users/kira/Applications/Zed.app",
		"/usr/bin/login":                "",
		"-zsh":                          "",
		"/.app/Contents/MacOS/x":        "",
		"/opt/my.application/bin/thing": "",
		"relative/Thing.app/Contents/x": "",
	} {
		h, ok := appBundle(comm)
		got := ""
		if ok {
			got = h.Name + "|" + h.Path
		}
		if got != want {
			t.Errorf("%q: got %q want %q", comm, got, want)
		}
	}
}

// genProcs draws a process table over a few pids: a tree, cycles and
// dangling parents included, with some executables inside app bundles.
func genProcs(t *rapid.T) map[int]proc {
	comms := []string{"/usr/bin/login", "-zsh", "/bin/claude", "/Applications/A.app/Contents/MacOS/a",
		"/Applications/B.app/Contents/Frameworks/B Helper.app/Contents/MacOS/B Helper"}
	procs := map[int]proc{}
	for pid := 1; pid <= rapid.IntRange(0, 20).Draw(t, "n"); pid++ {
		procs[pid] = proc{
			ppid: rapid.IntRange(0, 22).Draw(t, "ppid"),
			tty:  rapid.SampledFrom([]string{"", "", "ttys001"}).Draw(t, "tty"),
			comm: rapid.SampledFrom(comms).Draw(t, "comm"),
		}
	}
	return procs
}

func TestPropPlacesPartitionTheSessions(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		procs := genProcs(t)
		var rows []fleet.Row
		for i := rapid.IntRange(0, 8).Draw(t, "rows"); i > 0; i-- {
			r := row(fmt.Sprint(i), "s", "", rapid.IntRange(-1, 22).Draw(t, "pid"))
			if rapid.IntRange(0, 4).Draw(t, "bg") == 0 {
				r.Kind = "background"
			}
			rows = append(rows, r)
		}
		ttys, hosts := places(procs, rows)
		for _, r := range rows {
			_, inTerm := ttys[r.PID]
			h, away := hosts[r.PID]
			p, listed := procs[r.PID]
			if inTerm && away {
				t.Fatalf("pid %d is both in a terminal and away", r.PID)
			}
			if r.Background() {
				continue // another row may share the pid
			}
			if (inTerm || away) != (listed && r.PID > 0) {
				t.Fatalf("pid %d listed=%v but placed term=%v away=%v", r.PID, listed, inTerm, away)
			}
			if inTerm != (listed && p.tty != "") {
				t.Fatalf("pid %d tty %q but inTerm=%v", r.PID, p.tty, inTerm)
			}
			if h.Name != "" && !strings.HasSuffix(h.Path, "/"+h.Name+".app") {
				t.Fatalf("host %+v", h)
			}
		}
	})
}

// The host is the outer app of the nearest ancestor in a bundle, so a
// session under a helper is credited to the helper's app, never the helper.
func TestPropHostIsTheNearestBundledAncestor(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		procs := genProcs(t)
		pid := rapid.IntRange(1, 22).Draw(t, "pid")
		got := hostOf(procs, pid)
		want := host{}
		p, ok := procs[pid]
		for hop := 0; ok && hop < maxHops && p.ppid > 0; hop++ {
			if p, ok = procs[p.ppid]; ok && strings.Contains(p.comm, ".app/") {
				want, _ = appBundle(p.comm)
				break
			}
		}
		if got != want {
			t.Fatalf("hostOf(%d) = %+v, want %+v", pid, got, want)
		}
		if strings.Contains(got.Name, "Helper") {
			t.Fatalf("credited a helper: %+v", got)
		}
	})
}

// awayModel is a test model where claude-bd runs inside Obsidian and red
// runs in a terminal. It records what reveal was asked to open.
func awayModel(t *testing.T) (model, *[]string) {
	t.Helper()
	obs := row("obs", "claude-bd", "", 63381)
	obs.Cwd = "/"
	m := newTestModel(t, &fakeJumper{}, row("s1", "red", "auth", 11), obs)
	var opened []string
	m.reveal = func(path string) error { opened = append(opened, path); return nil }
	m = update(m, rowsMsg{rows: m.rows, ttys: map[int]string{11: "ttys1"},
		hosts: map[int]host{63381: {Name: "Obsidian", Path: "/Applications/Obsidian.app"}}})
	return m, &opened
}

func TestAwaySessionsGoLast(t *testing.T) {
	m, _ := awayModel(t)
	secs := m.sections()
	last := secs[len(secs)-1]
	if !last.Away || last.Title != awayTitle || len(last.Rows) != 1 || last.Rows[0].SessionID != "obs" {
		t.Fatalf("sections %+v", secs)
	}
	m.filter = "obsidian"
	if got := ids(m.sections()); len(got) != 1 || got[0] != "obs" {
		t.Fatalf("searching for the app found %v", got)
	}
	view := m.View()
	if !strings.Contains(view, awayTitle) || !strings.Contains(view, "Obsidian ·") {
		t.Fatal("the row does not name its app")
	}
}

func TestAwaySessionThatNeedsYouIsStillPinned(t *testing.T) {
	m, _ := awayModel(t)
	for i := range m.rows {
		if m.rows[i].SessionID == "obs" {
			m.rows[i].State = fleet.Blocked
		}
	}
	if secs := m.sections(); !secs[0].NeedsYou || secs[0].Rows[0].SessionID != "obs" || secs[len(secs)-1].Away {
		t.Fatalf("sections %+v", secs)
	}
}

func TestEnterOnAwaySessionOpensItsApp(t *testing.T) {
	m, opened := awayModel(t)
	m.sel = "obs"
	m, cmd := press(m, "enter")
	if cmd == nil {
		t.Fatal("enter did nothing")
	}
	msg := cmd().(doneMsg)
	if len(*opened) != 1 || (*opened)[0] != "/Applications/Obsidian.app" || msg.err != nil {
		t.Fatalf("opened %v err %v", *opened, msg.err)
	}
	if _, cmd = press(m, "a"); cmd != nil || len(*opened) != 1 {
		t.Fatal("a should only say where the session is")
	}
}

func TestXOnAwaySessionPointsToItsApp(t *testing.T) {
	m, _ := awayModel(t)
	m.sel = "obs"
	m, _ = press(m, "x")
	if m.confirm != "" || !strings.Contains(m.note, "Close it there") {
		t.Fatalf("confirm %q note %q", m.confirm, m.note)
	}
}

func TestEnterOnSessionWithoutAnAppOnlySaysSo(t *testing.T) {
	m, opened := awayModel(t)
	m = update(m, rowsMsg{rows: m.rows, ttys: m.ttys, hosts: map[int]host{63381: {}}})
	m.sel = "obs"
	m, cmd := press(m, "enter")
	if cmd != nil || len(*opened) != 0 || !strings.Contains(m.note, "without a terminal") {
		t.Fatalf("cmd %v opened %v note %q", cmd != nil, *opened, m.note)
	}
}
