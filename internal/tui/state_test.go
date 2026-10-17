package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"pgregory.net/rapid"

	"overview/internal/fleet"
)

// keyPool is every key the sessions screen answers to, minus the ones that
// run programs from inside Update (y copies with pbcopy).
var keyPool = []string{
	"j", "k", "g", "G", "n", "N", "/", "esc", "enter", "a", "x", "d", "1", "2", "?",
	"tab", "l", "h", "r", "D", "J", "K", "ctrl+d", "ctrl+u", " ", "backspace", "down", "up",
	"[", "]", "z",
}

var typeable = []string{"a", "e", "o", "r", "b", "f", "w", "x", "A", " ", "u"}

func genStateRows(t *rapid.T) ([]fleet.Row, map[int]string) {
	n := rapid.IntRange(0, 8).Draw(t, "n")
	var rows []fleet.Row
	ttys := map[int]string{}
	for i := 0; i < n; i++ {
		// A small id pool, so refreshes keep, drop and add sessions.
		r := genRow(t, fmt.Sprintf("s%d", rapid.IntRange(0, 9).Draw(t, "id")))
		dup := false
		for _, o := range rows {
			dup = dup || o.SessionID == r.SessionID
		}
		if dup {
			continue
		}
		r.PID = 100 + i
		ttys[r.PID] = fmt.Sprintf("ttys%03d", i)
		rows = append(rows, r)
	}
	return rows, ttys
}

func checkInvariants(t *rapid.T, m model) {
	rows := m.ordered()
	if len(rows) > 0 {
		found := false
		for _, r := range rows {
			found = found || r.SessionID == m.sel
		}
		if !found {
			t.Fatalf("cursor %q is not on a visible row of %d (filter %q)", m.sel, len(rows), m.filter)
		}
		if _, i, ok := m.selected(rows); !ok || rows[i].SessionID != m.sel {
			t.Fatalf("selected() disagrees with sel %q", m.sel)
		}
	}
	if m.filtering && m.screen != sessionsScreen {
		t.Fatal("filtering on the digest screen")
	}
	if m.filter != printable(m.filter) {
		t.Fatalf("filter %q holds control characters", m.filter)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.SessionID] {
			t.Fatalf("%s listed twice", r.SessionID)
		}
		seen[r.SessionID] = true
	}
}

func TestStateMachine(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		j := &fakeJumper{}
		m := newModel(fleet.Home(t.Name()), "")
		m.projects.exists = func(string) bool { return false }
		m.jump = j
		m = update(m, tea.WindowSizeMsg{Width: 120, Height: 30})
		rows, ttys := genStateRows(t)
		m = update(m, rowsMsg{rows: rows, ttys: ttys})

		t.Repeat(map[string]func(*rapid.T){
			"": func(t *rapid.T) { checkInvariants(t, m) },
			"key": func(t *rapid.T) {
				m, _ = press(m, rapid.SampledFrom(keyPool).Draw(t, "key"))
			},
			"type": func(t *rapid.T) {
				m, _ = press(m, rapid.SampledFrom(typeable).Draw(t, "rune"))
			},
			"search": func(t *rapid.T) {
				m, _ = press(m, "/")
				m, _ = press(m, typed(genQuery(t))...)
			},
			"paste": func(t *rapid.T) {
				m, _ = press(m, "/"+rapid.SampledFrom([]string{"fox", "au th", "", "zz"}).Draw(t, "paste"))
			},
			"refresh": func(t *rapid.T) {
				r, tt := genStateRows(t)
				m = update(m, rowsMsg{rows: r, ttys: tt})
			},
			"refresh error": func(t *rapid.T) {
				m = update(m, rowsMsg{err: fmt.Errorf("claude agents --json: boom")})
			},
			"resize": func(t *rapid.T) {
				m = update(m, tea.WindowSizeMsg{
					Width:  rapid.IntRange(40, 200).Draw(t, "w"),
					Height: rapid.IntRange(3, 60).Draw(t, "h"),
				})
			},
			"jump result": func(t *rapid.T) {
				m = update(m, jumpMsg{key: "s1\x00ttys000", id: "T1", note: rapid.SampledFrom([]string{"", "x failed"}).Draw(t, "note")})
			},
			"view": func(t *rapid.T) {
				for _, w := range []int{40, 41, 63, 89, 90, 91, 120, 200} {
					v := update(m, tea.WindowSizeMsg{Width: w, Height: rapid.IntRange(3, 60).Draw(t, "vh")})
					out := v.View()
					for i, line := range strings.Split(out, "\n") {
						if got := ansi.StringWidth(line); got > w {
							t.Fatalf("width %d: line %d is %d cells: %q", w, i, got, line)
						}
					}
				}
			},
		})
	})
}
