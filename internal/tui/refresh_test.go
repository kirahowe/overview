package tui

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"overview/internal/fleet"
)

// batched is how many Cmds the answer to a message holds.
func batched(t *testing.T, cmd tea.Cmd) int {
	t.Helper()
	if cmd == nil {
		return 0
	}
	b, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("the answer is one Cmd, not several")
	}
	return len(b)
}

// A tick starts a refresh only while none is out, so a machine too slow to
// finish one in two seconds does not gather them. A refresh the user asked
// for while one is out, with r or by something done, starts when that one
// lands, since it may have read the sessions too early.
func TestOneRefreshIsOutAtATime(t *testing.T) {
	m := newModel(fleet.Home{Dir: t.TempDir()}, "")
	m.deps = fakeDeps(newFakeMux())
	now := time.Now()

	// A tick's answer holds the next tick and the read of the digests,
	// and a refresh when it starts one.
	m, cmd := step(m, tickMsg(now))
	if n := batched(t, cmd); !m.refreshing || n != 3 {
		t.Fatalf("the first tick: refreshing %v, %d Cmds, want a refresh among 3", m.refreshing, n)
	}
	for range 3 {
		m, cmd = step(m, tickMsg(now))
		if n := batched(t, cmd); !m.refreshing || m.refreshOwed || n != 2 {
			t.Fatalf("a tick with a refresh out: refreshing %v, owed %v, %d Cmds, want 2 and no refresh", m.refreshing, m.refreshOwed, n)
		}
	}
	m, cmd = step(m, rowsMsg{})
	if m.refreshing || cmd != nil {
		t.Fatalf("the refresh landed: refreshing %v, another Cmd %v", m.refreshing, cmd != nil)
	}
	m, cmd = step(m, tickMsg(now))
	if n := batched(t, cmd); !m.refreshing || n != 3 {
		t.Fatalf("the tick after it landed: refreshing %v, %d Cmds, want a refresh among 3", m.refreshing, n)
	}

	// r, and something done, while that one is out.
	m, cmd = press(m, "r")
	if !m.refreshOwed || cmd != nil {
		t.Fatalf("r with a refresh out: owed %v, a Cmd %v", m.refreshOwed, cmd != nil)
	}
	m, cmd = step(m, doneMsg{})
	if !m.refreshOwed || cmd != nil {
		t.Fatalf("something done with a refresh out: owed %v, a Cmd %v", m.refreshOwed, cmd != nil)
	}
	m, cmd = step(m, rowsMsg{})
	if !m.refreshing || m.refreshOwed || cmd == nil {
		t.Fatalf("the refresh landed with one owed: refreshing %v, owed %v, a Cmd %v", m.refreshing, m.refreshOwed, cmd != nil)
	}
	m, cmd = step(m, rowsMsg{})
	if m.refreshing || cmd != nil {
		t.Fatalf("the owed refresh landed: refreshing %v, another Cmd %v", m.refreshing, cmd != nil)
	}
	// With none out, r starts one at once.
	m, cmd = press(m, "r")
	if !m.refreshing || m.refreshOwed || cmd == nil {
		t.Fatalf("r with no refresh out: refreshing %v, owed %v, a Cmd %v", m.refreshing, m.refreshOwed, cmd != nil)
	}
}

// A tick lists tmux's panes only while no listing it asked for is out. A
// listing that failed, or came too late to be taken, has still landed.
func TestOneListingOfThePanesIsOutAtATime(t *testing.T) {
	f := newFakeMux()
	m := stagedModel(t, f, row("s1", "red", "auth", 11))
	now := time.Now()
	m, cmd := step(m, panesTick(now))
	if n := batched(t, cmd); !m.listing || n != 2 {
		t.Fatalf("the first tick: listing %v, %d Cmds, want a listing and the next tick", m.listing, n)
	}
	// The answer is the next tick alone, which waits and then ticks.
	m, cmd = step(m, panesTick(now))
	if _, ok := cmd().(panesTick); !m.listing || !ok {
		t.Fatalf("a tick with a listing out: listing %v, and its answer is not the next tick alone", m.listing)
	}
	for _, landed := range []panesMsg{
		{panes: f.panes, gen: m.spawns},
		{err: errors.New("no server")},
		{panes: f.panes, gen: m.spawns - 1},
	} {
		m, _ = step(m, landed)
		if m.listing {
			t.Fatalf("the listing %+v landed, and one is still out", landed)
		}
		m, cmd = step(m, panesTick(now))
		if n := batched(t, cmd); !m.listing || n != 2 {
			t.Fatalf("the tick after it landed: listing %v, %d Cmds, want a listing and the next tick", m.listing, n)
		}
	}
}

// A refresh that cannot read the process table fails, and the list keeps
// what the last one found. With an empty table instead, no session would
// have a terminal, and the list would leave them all for the holder.
func TestARefreshWithoutTheProcessTableKeepsTheLastOne(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "claude")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho '[]'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := newModel(fleet.Home{Dir: dir, Claude: fake}, "")
	m.deps = fakeDeps(newFakeMux())
	m = update(m, rowsMsg{rows: []fleet.Row{row("s1", "red", "auth", 11)}, ttys: map[int]string{11: "ttys011"}})
	m.deps.psTable = func() ([]byte, error) { return nil, errors.New("ps: timed out after 30s") }
	msg, ok := m.refresh().(rowsMsg)
	if !ok || msg.err == nil {
		t.Fatalf("the refresh gave %+v, want it to fail", msg)
	}
	m = update(m, msg)
	if len(m.in.Rows) != 1 || m.in.TTYs[11] != "ttys011" || m.in.RefreshErr == nil {
		t.Fatalf("after a refresh that failed: rows %v, ttys %v, err %v", m.in.Rows, m.in.TTYs, m.in.RefreshErr)
	}
}
