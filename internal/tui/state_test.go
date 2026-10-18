package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"pgregory.net/rapid"

	"overview/internal/fleet"
)

// keyPool is every key the view answers to, and one it does not.
var keyPool = []string{
	"j", "k", "g", "G", "n", "N", "/", "esc", "enter", "a", "x", "y", "d", "1", "2", "?",
	"tab", "l", "h", "r", "D", "J", "K", "ctrl+d", "ctrl+u", " ", "backspace", "down", "up",
	"[", "]", "c", "t", "z",
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
	if m.filtering && (m.screen != sessionsScreen || m.focus != listPane) {
		t.Fatalf("filtering on screen %d, pane %d", m.screen, m.focus)
	}
	if m.jumping < 0 || m.jumping > m.jumpSeq {
		t.Fatalf("jump %d in flight, only %d started", m.jumping, m.jumpSeq)
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

// observed is what input can change, short of notes and their times.
type observed struct {
	sel, filter, confirm          string
	focus                         pane
	screen                        screen
	filtering, help               bool
	scroll, listTop, digestScroll int
	digestIdx, jumping, jumpSeq   int
}

func observe(m model) observed {
	return observed{m.sel, m.filter, m.confirm, m.focus, m.screen, m.filtering, m.help,
		m.scroll, m.listTop, m.digestScroll, m.digestIdx, m.jumping, m.jumpSeq}
}

func sameAs(t *rapid.T, what string, got, want model) {
	if g, w := observe(got), observe(want); !reflect.DeepEqual(g, w) {
		t.Fatalf("%s:\n got %+v\nwant %+v", what, g, w)
	}
}

// mouseAt sends a mouse press through Update, stamped at at.
func mouseAt(m model, b tea.MouseButton, x, y int, at time.Time) model {
	defer func(old func() time.Time) { now = old }(now)
	now = func() time.Time { return at }
	return update(m, tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: b})
}

// shownLine is the screen line of a session's first list line, or -1.
func shownLine(m model, id string) int {
	cur, _, _ := m.selected(m.ordered())
	_, owners := m.listView(m.sections(), cur.SessionID)
	for i, o := range owners {
		if o == id {
			return i + 1
		}
	}
	return -1
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
			"key": func(t *rapid.T) {
				before := m
				k := rapid.SampledFrom(keyPool).Draw(t, "key")
				m, _ = press(m, k)
				if before.jumping != 0 && m.jumpSeq != before.jumpSeq {
					t.Fatalf("%q started a second jump while %d was in flight", k, before.jumping)
				}
			},
			"jump result": func(t *rapid.T) {
				// Answers to the jump in flight, to none, or to an older or
				// unknown one, for a row that is shown or one that has gone.
				keys := []string{"gone\x00ttys009"}
				for _, r := range m.rows {
					keys = append(keys, jumpKey(r.SessionID, m.ttys[r.PID]))
				}
				msg := jumpMsg{
					seq:  rapid.SampledFrom([]int{0, m.jumping, m.jumping - 1, m.jumpSeq + 1}).Draw(t, "seq"),
					key:  rapid.SampledFrom(keys).Draw(t, "jkey"),
					id:   rapid.SampledFrom([]string{"", "T1"}).Draw(t, "jid"),
					drop: rapid.Bool().Draw(t, "drop"),
					note: rapid.SampledFrom([]string{"", "x failed"}).Draw(t, "note"),
				}
				before, cached := m, m.terms[msg.key]
				m = update(m, msg)
				live := msg.seq != 0 && msg.seq == before.jumping
				switch {
				case !live && (m.jumping != before.jumping || m.note != before.note):
					t.Fatalf("stale result %d (in flight %d) changed the view", msg.seq, before.jumping)
				case live && m.jumping != 0:
					t.Fatal("the answered jump is still in flight")
				}
				want := cached
				if msg.drop {
					want = ""
				}
				if live && msg.id != "" && before.hasJumpKey(msg.key) {
					want = msg.id
				}
				if got := m.terms[msg.key]; got != want {
					t.Fatalf("cache for %q is %q, want %q (live %v, shown %v)", msg.key, got, want, live, before.hasJumpKey(msg.key))
				}
			},
			"click": func(t *rapid.T) {
				x := rapid.IntRange(0, m.w-1).Draw(t, "x")
				y := rapid.IntRange(0, m.h-1).Draw(t, "y")
				// Often soon after the last click, so double clicks happen.
				at := m.clickAt.Add(time.Duration(rapid.IntRange(0, 800).Draw(t, "ms")) * time.Millisecond)
				before := m
				m = mouseAt(m, tea.MouseButtonLeft, x, y, at)

				var ref model
				switch lw, lh, _, _ := before.split(); {
				case before.confirm != "" || before.help:
					// A click closes an overlay as esc does.
					ref, _ = press(before, "esc")
				case before.screen == digestScreen, y >= before.boxHeight():
					ref = before
				case x >= lw || y >= lh:
					ref, _ = press(before, "tab")
					if before.focus == detailPane || before.filtering {
						ref = before
					}
				default:
					// A click on a row is the cursor moved there, and a second
					// one on it is enter.
					ref = before
					ref.focus = listPane
					cur, _, _ := before.selected(before.ordered())
					_, owners := before.listView(before.sections(), cur.SessionID)
					line := y - 1
					if line < 0 || line >= min(len(owners), lh-2) || owners[line] == "" {
						break
					}
					id := owners[line]
					rows := before.ordered()
					for j, r := range rows {
						if r.SessionID == id {
							ref = ref.moveTo(rows, j)
						}
					}
					ref = update(ref, rowsMsg{rows: ref.rows, ttys: ref.ttys}) // settles listTop
					if m.sel != id {
						t.Fatalf("click on %s left the cursor on %s", id, m.sel)
					}
					if id == before.clickID && at.Sub(before.clickAt) < doubleClick {
						ref, _ = press(ref, "enter")
					} else if got := shownLine(m, id); got != y && got != y-1 {
						t.Fatalf("clicked %s at line %d, now at line %d", id, y, got)
					}
				}
				sameAs(t, fmt.Sprintf("click at %d,%d", x, y), m, ref)
			},
			"wheel": func(t *rapid.T) {
				x := rapid.IntRange(0, m.w-1).Draw(t, "x")
				y := rapid.IntRange(0, m.h-1).Draw(t, "y")
				down := rapid.Bool().Draw(t, "down")
				b, step, line := tea.MouseButtonWheelUp, "up", "K"
				if down {
					b, step, line = tea.MouseButtonWheelDown, "down", "J"
				}
				before := m
				m = mouseAt(m, b, x, y, t0)

				// The wheel over the list is up or down there, and over the
				// detail pane or digests it is three lines of scrolling, even
				// while searching, when J and K would type.
				ref := before
				lw, lh, _, _ := before.split()
				switch {
				case before.confirm != "" || before.help, before.screen == sessionsScreen && y >= before.boxHeight():
				case before.screen == digestScreen:
					ref, _ = press(ref, "j", "j", "j")
					if !down {
						ref, _ = press(before, "k", "k", "k")
					}
				case x < lw && y < lh:
					ref.focus = listPane
					ref, _ = press(ref, step)
					ref.focus = before.focus
				default:
					ref.filtering = false
					ref, _ = press(ref, line, line, line)
					ref.filtering = before.filtering
				}
				sameAs(t, fmt.Sprintf("wheel at %d,%d", x, y), m, ref)
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
