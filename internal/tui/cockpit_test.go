package tui

import (
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestOOpensTheCockpitZoomedAndEscPutsItBack(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11), row("s2", "blue", "billing", 12))
	first := paneWithTTY(f, m.ttys[m.ordered()[0].row.PID])
	if !f.staged(first) {
		t.Fatalf("the list is in %s, want beside %s", f.stage(), first)
	}
	m = pressSettled(t, m, "o")
	if !m.cockpit.open || !slices.Equal(f.zooms, []string{"%0"}) || !f.zoomed("%0") {
		t.Fatalf("after o: open %v zooms %v", m.cockpit.open, f.zooms)
	}
	// The cursor moves, and the list stays in its window.
	stage, moves := f.stage(), len(f.moves)
	m = pressSettled(t, m, "j")
	m, cmd := step(m, listPanes(f, m.deps.roots, m.landed)())
	m = settle(t, m, cmd)
	if m.sel != m.ordered()[1].key() || f.stage() != stage || !m.cockpit.open || len(f.moves) != moves {
		t.Fatalf("after j: sel %s, list in %s (was %s), open %v, moves %v", m.sel, f.stage(), stage, m.cockpit.open, f.moves)
	}
	m = pressSettled(t, m, "esc")
	second := paneWithTTY(f, m.ttys[m.ordered()[1].row.PID])
	if m.cockpit.open || f.zoomed("%0") || len(f.zooms) != 2 {
		t.Fatalf("after esc: open %v zoomed %v zooms %v", m.cockpit.open, f.zoomed("%0"), f.zooms)
	}
	if !f.staged(second) {
		t.Fatalf("after esc the list is in %s, want beside s2's %s", f.stage(), second)
	}
	// o closes it too.
	m = pressSettled(t, m, "o", "o")
	if m.cockpit.open || f.zoomed("%0") || len(f.zooms) != 4 {
		t.Fatalf("after o o: open %v zooms %v", m.cockpit.open, f.zooms)
	}
}

func TestTheCockpitClosesWhenTmuxEndsTheZoom(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11))
	m = pressSettled(t, m, "o")
	// The user goes to the pane beside the list, and tmux ends the zoom.
	f.setZoom(f.stage(), false)
	m, cmd := step(m, listPanes(f, m.deps.roots, m.landed)())
	m = settle(t, m, cmd)
	if m.cockpit.open || len(f.zooms) != 1 || f.zoomed("%0") {
		t.Fatalf("open %v zooms %v zoomed %v", m.cockpit.open, f.zooms, f.zoomed("%0"))
	}
}

func TestAListingFromBeforeTheZoomDoesNotCloseTheCockpit(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11))
	stale := listPanes(f, m.deps.roots, m.landed)() // taken before the zoom
	m, cmd := press(m, "o")
	m = settle(t, m, cmd)
	m, cmd = step(m, stale)
	m = settle(t, m, cmd)
	if !m.cockpit.open {
		t.Fatal("a listing from before the zoom closed the cockpit")
	}
}

func TestActionsThatShowAPaneCloseTheCockpit(t *testing.T) {
	for _, k := range []string{"enter", "c", "t", "d"} {
		f := newFakeMux()
		m := stagedModel(t, f, row("s1", "red", "auth", 11))
		m = pressSettled(t, m, "o")
		m = pressSettled(t, m, k)
		if m.cockpit.open || f.zoomed("%0") {
			t.Errorf("%s: open %v zoomed %v", k, m.cockpit.open, f.zoomed("%0"))
		}
	}
}

func TestTheCockpitOutsideTmuxOnlyDraws(t *testing.T) {
	m := newTestModel(t, row("s1", "red", "auth", 11))
	m = update(m, tea.WindowSizeMsg{Width: 140, Height: 20})
	m, cmd := press(m, "o")
	if !m.cockpit.open || cmd != nil {
		t.Fatalf("open %v cmd %v", m.cockpit.open, cmd != nil)
	}
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 20 {
		t.Fatalf("%d lines", len(lines))
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w != 140 {
			t.Fatalf("line %d is %d wide: %q", i, w, ansi.Strip(l))
		}
	}
	if body := ansi.Strip(lines[2]); []rune(body)[cockpitList] != '│' {
		t.Fatalf("no border after the list: %q", body)
	}
	m, _ = press(m, "esc")
	if m.cockpit.open {
		t.Fatal("esc left it open")
	}
}

func TestCockpitTarget(t *testing.T) {
	m := taskModel(t, newFakeMux())
	for _, c := range []struct{ key, root, task string }{
		{"task:/code/app#6", "/code/app", "/code/app#6"},
		{"s1", "/code/app", "/code/app#6"},
		{"s2", "/code/app", ""},
		{"agents/s1", "/code/app", ""},
	} {
		got := selectKey(t, m, c.key).target()
		if got.root != c.root || got.task != c.task {
			t.Errorf("%s: target %+v, want root %s task %s", c.key, got, c.root, c.task)
		}
	}
}

func TestCockpitListWidth(t *testing.T) {
	for _, c := range []struct{ want, w, got int }{
		{0, 200, cockpitList},
		{30, 200, 30},
		{30, 81, 30},
		{30, 80, 0},
		{0, 90, 0},
	} {
		if got := cockpitListWidth(c.want, c.w); got != c.got {
			t.Errorf("cockpitListWidth(%d, %d) = %d, want %d", c.want, c.w, got, c.got)
		}
	}
}
