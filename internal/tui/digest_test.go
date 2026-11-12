package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"overview/internal/fleet"
)

const sampleDigest = `# Thu 12 Nov 09:22

## Needs your attention

- **relay-20** (relay): The live relay can send your facts to any server
  - Do: Deploy the fix before 10:00 with ` + "`cd ~/code/projects/relay && jj new lwnyosru`" + `
  - Why: ` + "`github-file`" + ` lets the model add its own flag, and the session was blocked from deploying it.
- **cleanroom-90** (cleanroom): PLAN.md and DESIGN.md are still ignored
  - Do: Should PLAN.md and DESIGN.md stay ignored?
- **relay-3a** (relay): ntfy notifications are not set up
  - Do: Subscribe to the topic ` + "`relay`" + ` on your ntfy server
  - Since: Wed 20:05

## Finished

- **slk-12** (slk): Released 0.29.0 to the Homebrew tap

## In progress

- **relay-20** (relay): QA and audit agents are rerunning the scenario suite
`

// pageText is a digest's page w cells wide, without its styles and the
// spaces that fill its lines.
func pageText(md string, w int) []string {
	lines := digestPage(fleet.ParseDigest(md), w)
	for i, l := range lines {
		lines[i] = strings.TrimRight(ansi.Strip(l), " ")
	}
	return lines
}

func TestTheDigestPageLooksLikeTheMockup(t *testing.T) {
	want := []string{
		"── Needs your attention · 3 ────────────────────────────────",
		"",
		"! relay-20  relay",
		"  The live relay can send your facts to any server",
		"  → Deploy the fix before 10:00 with cd",
		"    ~/code/projects/relay && jj new lwnyosru",
		"    github-file lets the model add its own flag, and the",
		"    session was blocked from deploying it.",
		"",
		"! cleanroom-90  cleanroom",
		"  PLAN.md and DESIGN.md are still ignored",
		"  → Should PLAN.md and DESIGN.md stay ignored?",
		"",
		"! relay-3a  relay                            since Wed 20:05",
		"  ntfy notifications are not set up",
		"  → Subscribe to the topic relay on your ntfy server",
		"",
		"── Finished · 1 ────────────────────────────────────────────",
		"",
		"✓ slk-12  slk",
		"  Released 0.29.0 to the Homebrew tap",
		"",
		"── In progress · 1 ─────────────────────────────────────────",
		"",
		"● relay-20  relay",
		"  QA and audit agents are rerunning the scenario suite",
	}
	if got := pageText(sampleDigest, 60); !slices.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A digest from before items had a line for what to do still shows each
// session apart, its first sentence on a line of its own.
func TestAnOldDigestShowsEachSessionApart(t *testing.T) {
	md := "# Fri 16 Oct 02:22\n\n## Needs your attention\n" +
		"- **website-admin-f5** (website-admin): Close the app fully and reopen it on your phone. Test publishing in airplane mode.\n" +
		"- **slk-12** (slk): Upgrade with `brew upgrade --cask kirahowe/tap/slk`.\n"
	want := []string{
		"── Needs your attention · 2 ────────────────────────────────",
		"",
		"! website-admin-f5  website-admin",
		"  Close the app fully and reopen it on your phone.",
		"  Test publishing in airplane mode.",
		"",
		"! slk-12  slk",
		"  Upgrade with brew upgrade --cask kirahowe/tap/slk.",
	}
	if got := pageText(md, 60); !slices.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// In a column too narrow for an item's names and its wait on one line,
// the wait goes under them, and names too long for the line wrap. None
// of it is cut.
func TestANarrowDigestPageCutsNothing(t *testing.T) {
	md := "## Needs your attention\n- **zero-trust-agents-c6** (zero-trust-agents): Temp dirs wait\n  - Do: Say go\n  - Since: last Fri 22:57\n"
	want := []string{
		"── Needs your attention · 1 ──",
		"",
		"! zero-trust-agents-c6  zero-",
		"  trust-agents",
		"  since last Fri 22:57",
		"  Temp dirs wait",
		"  → Say go",
	}
	if got := pageText(md, 30); !slices.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// In a column of a few cells the arrow and its indent give way to the
// text, which would otherwise have no room: a wide character needs two
// cells, and the arrow's indent would leave it one.
func TestAColumnOfAFewCellsKeepsTheText(t *testing.T) {
	got := pageText("- **a**: x\n  - Do: 日本語\n", 5)
	if want := []string{"• a", "  x", "日本", "語"}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// What follows a code span or a bold span keeps the colour of its line:
// each run is styled by itself.
func TestInlineSpansDoNotEndTheLinesColour(t *testing.T) {
	got := styleInline("run `make` and **wait** here", dim)
	want := dim.Render("run ") + codeFg.Render("make") + dim.Render(" and ") + dim.Bold(true).Render("wait") + dim.Render(" here")
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

// digestModel is a staged model that has read sampleDigest, the newest of
// n digests.
func digestModel(t *testing.T, f *fakeMux, n int) model {
	t.Helper()
	m := stagedModel(t, f, row("s1", "red", "auth", 11))
	dir := filepath.Join(m.home.Dir, "overview", "digests")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range n {
		name := filepath.Join(dir, time.Date(2026, 11, 12, 9, i, 0, 0, time.UTC).Format("20060102-150405")+".md")
		if err := os.WriteFile(name, []byte(sampleDigest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return settle(t, m, m.listDigests(true))
}

// d gives the digests the whole window: the list's pane is zoomed, as it
// is for the cockpit, and every key that goes back to the list ends the
// zoom.
func TestTheDigestsZoomTheList(t *testing.T) {
	for _, back := range []string{"d", "esc", "tab", "1"} {
		f := newFakeMux()
		m := digestModel(t, f, 1)
		m = pressSettled(t, m, "d")
		if m.screen != digestScreen || !f.zoomed("%0") {
			t.Fatalf("after d: screen %v zoomed %v", m.screen, f.zoomed("%0"))
		}
		// 2 is where the digests already are.
		m = pressSettled(t, m, "2")
		m = pressSettled(t, m, back)
		if m.screen != sessionsScreen || f.zoomed("%0") || !slices.Equal(f.zooms, []string{"%0 true", "%0 false"}) {
			t.Errorf("after %s: screen %v zoomed %v zooms %v", back, m.screen, f.zoomed("%0"), f.zooms)
		}
	}
}

// From the cockpit, d closes it and keeps the window for the digests:
// tmux is never asked to end the zoom in between, which would flash the
// panes under it.
func TestDInTheCockpitKeepsTheZoom(t *testing.T) {
	f := newFakeMux()
	m := digestModel(t, f, 1)
	m = pressSettled(t, m, "o")
	m = pressSettled(t, m, "d")
	if m.cockpit.open || m.screen != digestScreen || !f.zoomed("%0") || slices.Contains(f.zooms, "%0 false") {
		t.Fatalf("cockpit %v screen %v zoomed %v zooms %v", m.cockpit.open, m.screen, f.zoomed("%0"), f.zooms)
	}
	// And 1 closes the cockpit as it did, back to the list unzoomed.
	m = pressSettled(t, m, "esc", "o", "1")
	if m.cockpit.open || m.screen != sessionsScreen || f.zoomed("%0") {
		t.Fatalf("after 1: cockpit %v screen %v zoomed %v", m.cockpit.open, m.screen, f.zoomed("%0"))
	}
}

// The user going to another pane ends tmux's zoom, and the digests close
// with it: the list is back in its column as it was.
func TestTheDigestsCloseWhenTmuxEndsTheZoom(t *testing.T) {
	f := newFakeMux()
	m := digestModel(t, f, 1)
	m = pressSettled(t, m, "d")
	f.setZoom(f.stage(), false)
	m, cmd := step(m, listPanes(f, m.deps.roots, m.landed)())
	m = settle(t, m, cmd)
	if m.screen != sessionsScreen || len(f.zooms) != 1 || f.zoomed("%0") {
		t.Fatalf("screen %v zooms %v zoomed %v", m.screen, f.zooms, f.zoomed("%0"))
	}
}

// A pane closing beside the list ends tmux's zoom without the user going
// anywhere: the digests stay, zoomed again while the list has company
// and as they are once it is alone, when it fills its window anyway.
func TestAPaneClosingKeepsTheDigests(t *testing.T) {
	f := newFakeMux()
	m := digestModel(t, f, 1)
	split := f.split(f.panes[f.find(paneWithTTY(f, m.in.TTYs[11]))].ID)
	m, cmd := step(m, listPanes(f, m.deps.roots, m.landed)())
	m = settle(t, m, cmd)
	m = pressSettled(t, m, "d")
	f.exit(split.ID)
	m, cmd = step(m, listPanes(f, m.deps.roots, m.landed)())
	m = settle(t, m, cmd)
	if m.screen != digestScreen || !f.zoomed("%0") {
		t.Fatalf("after a split closed: screen %v zoomed %v", m.screen, f.zoomed("%0"))
	}
	f.exit(paneWithTTY(f, m.in.TTYs[11]))
	m, cmd = step(m, listPanes(f, m.deps.roots, m.landed)())
	m = settle(t, m, cmd)
	if m.screen != digestScreen || f.inWindow(f.stage()) != 1 {
		t.Fatalf("after the session closed: screen %v, %d panes", m.screen, f.inWindow(f.stage()))
	}
}

// d while the list is moving to another window shows the digests at
// once and waits for the move to zoom, so the zoom lands on the window
// the list went to and not the one it left.
func TestDWhileTheListMovesZoomsWhereItLands(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11), in("/w/red", row("s2", "blue", "billing", 12)))
	m, move := press(m, "j")
	if !m.staging {
		t.Fatal("j started no move")
	}
	m, frame := press(m, "d")
	if m.screen != digestScreen || len(f.zooms) != 0 {
		t.Fatalf("d during the move: screen %v zooms %v", m.screen, f.zooms)
	}
	m = feed(t, m, append(cmdMsgs(frame), cmdMsgs(move)...))
	second := paneWithTTY(f, m.in.TTYs[rows(m)[1].sess.PID])
	if m.screen != digestScreen || !f.staged(second) || !f.zoomed("%0") {
		t.Fatalf("screen %v, list in %s beside %s %v, zoomed %v", m.screen, f.stage(), second, f.staged(second), f.zoomed("%0"))
	}
}

// The page has the pane to itself: a rule of its own along the top, as
// tmux draws none over a zoomed pane, that says which digest shows, and
// under it the digest's column in the middle of the window.
func TestTheDigestsPageFillsThePane(t *testing.T) {
	f := newFakeMux()
	m := digestModel(t, f, 3)
	m.digestAt = m.now.Add(-3 * time.Hour)
	m = pressSettled(t, m, "d")
	m = update(m, tea.WindowSizeMsg{Width: 140, Height: 40})
	got := screenText(m)
	if len(got) != 40 {
		t.Fatalf("%d lines in a pane of 40", len(got))
	}
	for i, l := range got {
		if w := ansi.StringWidth(l); w != 140 {
			t.Fatalf("line %d is %d cells wide in a pane of 140: %q", i, w, l)
		}
	}
	if top := got[0]; !strings.HasPrefix(top, "── digest Thu 12 Nov 09:22 · 3h ago ─") || !strings.HasSuffix(top, "─ 3 of 3 ──") {
		t.Errorf("the rule is %q", top)
	}
	// The column is digestWidth wide, with the same margin each side.
	margin := (140 - 2 - digestWidth) / 2
	rule := "── Needs your attention · 3 " + strings.Repeat("─", digestWidth-28)
	if want := " " + strings.Repeat(" ", margin) + rule; strings.TrimRight(got[2], " ") != want {
		t.Errorf("the first section starts\n%q, want\n%q", got[2], want)
	}
	if foot := got[39]; !strings.Contains(foot, "esc list") || strings.Contains(foot, "more") {
		t.Errorf("the footer of a page that shows whole is %q", foot)
	}
}

// A pane of a few lines gives the page its lines before the rule: the
// blank line under the rule goes first, and then the rule.
func TestAShortPaneKeepsTheDigestsText(t *testing.T) {
	m := digestModel(t, newFakeMux(), 1)
	m = pressSettled(t, m, "d")
	for h, want := range map[int][]string{
		2: {"── Needs your attention"},
		3: {"── digest", "── Needs your attention"},
		4: {"── digest", "", "── Needs your attention"},
	} {
		m = update(m, tea.WindowSizeMsg{Width: 60, Height: h})
		got := screenText(m)
		if len(got) != h {
			t.Fatalf("%d lines in a pane of %d", len(got), h)
		}
		for i, w := range want {
			if l := strings.TrimSpace(got[i]); !strings.HasPrefix(l, w) {
				t.Errorf("height %d: line %d is %q, want it to start %q", h, i, l, w)
			}
		}
	}
}

// The footer says how much of a long page is below, and that the end is
// the end, and the page stops scrolling there.
func TestTheDigestsFooterSaysWhatIsBelow(t *testing.T) {
	m := digestModel(t, newFakeMux(), 1)
	m = pressSettled(t, m, "d")
	m = update(m, tea.WindowSizeMsg{Width: 60, Height: 13})
	n, rows := len(m.digestLines()), m.digestRows()
	if rows != 10 || n <= rows {
		t.Fatalf("%d lines of the page in %d rows", n, rows)
	}
	foot := func(m model) string { g := screenText(m); return g[len(g)-1] }
	if want := "↓ " + strconv.Itoa(n-rows) + " more"; !strings.Contains(foot(m), want) {
		t.Errorf("at the top the footer is %q, want %q in it", foot(m), want)
	}
	m, _ = press(m, "j")
	if want := "↓ " + strconv.Itoa(n-rows-1) + " more"; !strings.Contains(foot(m), want) {
		t.Errorf("a line down the footer is %q, want %q in it", foot(m), want)
	}
	m, _ = press(m, "G")
	last := screenText(m)
	if !strings.Contains(foot(m), "end") || !strings.Contains(last[len(last)-2], "rerunning the scenario suite") {
		t.Errorf("at the end the footer is %q under %q", foot(m), last[len(last)-2])
	}
	if m, _ = press(m, "j"); m.digestScroll != n-rows {
		t.Errorf("scrolled to %d past the end at %d", m.digestScroll, n-rows)
	}
}
