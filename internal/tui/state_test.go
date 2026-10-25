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
	"j", "k", "g", "G", "n", "N", "/", "esc", "enter", "x", "y", "d", "1", "2", "?",
	"tab", "l", "h", "r", "D", "ctrl+d", "ctrl+u", " ", "backspace", "down", "up",
	"[", "]", "c", "t", "a", "z", "o", "left", "right",
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
	// The panels mode is tmux's, so it is never on without tmux, and the
	// keys are in the issues card only inside the cockpit.
	if m.panels && m.mux == nil {
		t.Fatal("the panels mode is on, with no tmux")
	}
	if m.cockpit.issues && !m.cockpit.open {
		t.Fatal("the keys are in the issues card, with no cockpit")
	}
	// The keys are in the issues card only while it is there.
	if m.inIssues() {
		if _, tracked := m.trackers[m.target().root]; !tracked || !m.issuesShown() {
			t.Fatalf("the keys are in the issues card, which is not shown (tracked %v)", tracked)
		}
	}
	rows := m.ordered()
	if len(rows) > 0 {
		found := false
		for _, r := range rows {
			found = found || r.key() == m.sel
		}
		if !found {
			t.Fatalf("cursor %q is not on a visible row of %d (filter %q)", m.sel, len(rows), m.filter)
		}
		if _, i, ok := m.selected(rows); !ok || rows[i].key() != m.sel {
			t.Fatalf("selected() disagrees with sel %q", m.sel)
		}
	}
	if m.filtering && m.screen != sessionsScreen {
		t.Fatalf("filtering on screen %d", m.screen)
	}
	if m.filter != printable(m.filter) {
		t.Fatalf("filter %q holds control characters", m.filter)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.key()] {
			t.Fatalf("%s listed twice", r.key())
		}
		seen[r.key()] = true
	}
}

// observed is what input can change, short of notes and their times.
type observed struct {
	sel, filter, confirm             string
	screen                           screen
	filtering, help                  bool
	listTop, digestScroll, digestIdx int
}

func observe(m model) observed {
	return observed{m.sel, m.filter, m.confirm, m.screen, m.filtering, m.help,
		m.listTop, m.digestScroll, m.digestIdx}
}

func sameAs(t *rapid.T, what string, got, want model) {
	if g, w := observe(got), observe(want); !reflect.DeepEqual(g, w) {
		t.Fatalf("%s:\n got %+v\nwant %+v", what, g, w)
	}
}

// mouseAt sends a mouse press through Update, stamped at at.
func mouseAt(m model, b tea.MouseButton, x, y int, at time.Time) model {
	m.deps.now = func() time.Time { return at }
	return update(m, tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: b})
}

// shownLine is the screen line of a session's first list line, or -1.
func shownLine(m model, id string) int {
	off := 0
	if m.cockpit.open {
		off = 1 // the cockpit draws the list's rule
	}
	for i, o := range m.shownColumn().owners {
		if o == id {
			return i + off
		}
	}
	return -1
}

func TestStateMachine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		m := newModel(caseHome(t, root), "")
		m.deps = fakeDeps(nil)
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
				m = update(m, rowsMsg{rows: r, ttys: tt, pinned: genPinned(t)})
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
				m, _ = press(m, rapid.SampledFrom(keyPool).Draw(t, "key"))
			},
			"click": func(t *rapid.T) {
				x := rapid.IntRange(0, m.w-1).Draw(t, "x")
				y := rapid.IntRange(0, m.h-1).Draw(t, "y")
				// Often soon after the last click, so double clicks happen.
				at := m.clickAt.Add(time.Duration(rapid.IntRange(0, 800).Draw(t, "ms")) * time.Millisecond)
				before := m
				m = mouseAt(m, tea.MouseButtonLeft, x, y, at)

				var ref model
				switch lh := before.listHeight(); {
				case before.confirm != "" || before.help:
					// A click closes an overlay as esc does.
					ref, _ = press(before, "esc")
				case before.screen == digestScreen, y >= lh, before.pick.open:
					ref = before
				case before.cockpit.open && !before.inCockpitList(x):
					ref = before // a click on the cockpit's cards
				default:
					// A click on a row is the cursor moved there, and a second
					// one on it is enter.
					ref = before
					owners, line := before.shownColumn().owners, y
					if before.cockpit.open {
						line--
					}
					if line < 0 || line >= min(len(owners), lh) || owners[line] == "" {
						break
					}
					id := owners[line]
					rows := before.ordered()
					for j, r := range rows {
						if r.key() == id {
							ref = ref.moveTo(rows, j)
						}
					}
					ref.cockpit.issues = false
					ref = update(ref, rowsMsg{rows: ref.in.Rows, ttys: ref.in.TTYs, pinned: ref.in.Pinned, err: ref.rowsErr}) // settles listTop
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
				b, step := tea.MouseButtonWheelUp, "up"
				if down {
					b, step = tea.MouseButtonWheelDown, "down"
				}
				before := m
				m = mouseAt(m, b, x, y, t0)

				// The wheel over the list is up or down there, over the
				// cockpit's cards it is nothing, and over the digests it
				// is three lines of scrolling.
				ref := before
				switch {
				case before.confirm != "" || before.help, before.pick.open && before.screen == sessionsScreen,
					before.screen == sessionsScreen && y >= before.listHeight(),
					before.screen == sessionsScreen && before.cockpit.open && !before.inCockpitList(x):
				case before.screen == digestScreen:
					ref, _ = press(ref, "j", "j", "j")
					if !down {
						ref, _ = press(before, "k", "k", "k")
					}
				default:
					// The wheel moves the cursor as the arrows do inside the
					// list, and leaves the keys where they were.
					ref.cockpit.issues = false
					ref, _ = press(ref, step)
					ref.cockpit.issues = before.cockpit.issues
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
