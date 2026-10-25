package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// ctrl+c in an input cancels that input as esc does. It never quits the
// view: that is Q alone, and ctrl+c in the list detaches like q.
func TestCtrlCCancelsEachInput(t *testing.T) {
	base := newTestModel(t, row("s1", "red", "auth", 11))
	surfaces := map[string]func(model) model{
		"search":      func(m model) model { m.filtering = true; return m },
		"picker":      func(m model) model { m.pick.open = true; return m },
		"task picker": func(m model) model { m.taskPick.open = true; return m },
	}
	for name, open := range surfaces {
		m := open(base)
		got, want := m.keyActions(keyMsg("ctrl+c")), m.keyActions(keyMsg("esc"))
		if len(want) == 0 || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: ctrl+c gives %v, esc gives %v", name, got, want)
		}
		for _, a := range got {
			if a.do == quit {
				t.Errorf("%s: ctrl+c quits", name)
			}
		}
	}
	if as := base.keyActions(keyMsg("ctrl+c")); len(as) != 1 || as[0].do != detach {
		t.Errorf("ctrl+c in the list: %v, want detach", as)
	}
	if as := base.keyActions(keyMsg("Q")); len(as) != 1 || as[0].do != quit {
		t.Errorf("Q in the list: %v, want quit", as)
	}
	m, _ := press(base, "/", "ctrl+c")
	if m.filtering {
		t.Error("ctrl+c left the search open")
	}
	m, _ = press(base, "x")
	if m.confirm != "" {
		if m, _ = press(m, "ctrl+c"); m.confirm != "" {
			t.Error("ctrl+c did not cancel the confirm prompt")
		}
	}
}

// Every key the help lists does something where the help says it does,
// and every key the list answers to is in the help, so neither drifts.
func TestHelpMatchesTheKeyHandler(t *testing.T) {
	m := newTestModel(t, row("s1", "red", "auth", 11))
	setup := map[helpMode]func(model) model{
		listMode:    func(m model) model { return m },
		cockpitMode: func(m model) model { m.cockpit.open, m.cockpit.issues = true, true; return m },
		digestMode:  func(m model) model { m.screen = digestScreen; return m },
	}
	listed := map[string]bool{}
	ls, rs := helpSections()
	for _, s := range append(ls, rs...) {
		for _, e := range s.entries {
			for _, k := range strings.Fields(e.keys) {
				if e.mode == listMode {
					listed[k] = true
				}
				if e.mode == tmuxMode || e.mode == mouseMode {
					continue
				}
				if len(setup[e.mode](m).keyActions(keyMsg(k))) == 0 {
					t.Errorf("help lists %q (%s) but the handler ignores it", k, e.keys)
				}
			}
		}
	}
	// Keys that do the same as one the help lists.
	alias := map[string]string{"up": "k", "down": "j", "home": "g", "end": "G", "right": "l", "tab": "l", "ctrl+c": "q"}
	var probes []string
	for r := ' ' + 1; r < 127; r++ {
		probes = append(probes, string(r))
	}
	probes = append(probes, "esc", "enter", "tab", "up", "down", "left", "right", "home", "end", "pgup", "pgdown", "ctrl+c", "ctrl+d", "ctrl+u")
	skip := map[string]bool{"pgup": true, "pgdown": true, "ctrl+d": true, "ctrl+u": true, "left": true}
	for _, k := range probes {
		if skip[k] || len(m.keyActions(keyMsg(k))) == 0 {
			continue
		}
		name := k
		if a, ok := alias[k]; ok {
			name = a
		}
		if !listed[name] {
			t.Errorf("the list answers to %q but the help does not list it", k)
		}
	}
}

func TestPanelsFooterNamesTheWayBack(t *testing.T) {
	m := newTestModel(t, row("s1", "red", "auth", 11))
	m.panels = true
	var got []string
	for _, h := range m.panelHints() {
		got = append(got, h.key+" "+h.label)
	}
	if want := []string{"hjkl move", "enter act", "ctrl+[ list"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("panels hints %v, want %v", got, want)
	}
}

// On a short, narrow pane the help scrolls to every line, shows that
// there is more, and never draws past the pane.
func TestHelpScrollsOnAShortPane(t *testing.T) {
	m := newTestModel(t, row("s1", "red", "auth", 11))
	m = update(m, tea.WindowSizeMsg{Width: 36, Height: 12})
	m, _ = press(m, "?")
	if m.helpMax() == 0 {
		t.Fatal("the help fits in a 12-row pane")
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "↓ more") || strings.Contains(view, "↑ more") {
		t.Fatalf("at the top, want only a down marker:\n%s", view)
	}
	if strings.Contains(view, "Q ") {
		t.Fatal("the last section is visible before scrolling")
	}
	m, _ = press(m, "G")
	view = ansi.Strip(m.View())
	if !strings.Contains(view, "↑ more") || strings.Contains(view, "↓ more") || !strings.Contains(view, "quit the view") {
		t.Fatalf("at the bottom, want only an up marker and the last section:\n%s", view)
	}
	m, _ = press(m, "g")
	if m.helpTop != 0 {
		t.Fatal("g did not go to the top")
	}
	m, _ = press(m, "j", "j", "ctrl+d", "k", "ctrl+u")
	if !m.help || m.helpTop <= 0 {
		t.Fatalf("scroll keys closed or lost the help: help %v top %d", m.help, m.helpTop)
	}
	if m, _ = press(m, "esc"); m.help {
		t.Fatal("esc did not close the help")
	}
	if m, _ = press(m, "?"); m.helpTop != 0 {
		t.Fatal("the help reopened scrolled")
	}
	// Growing the pane clamps the scroll.
	m, _ = press(m, "G")
	m = update(m, tea.WindowSizeMsg{Width: 36, Height: 80})
	if m.helpTop != 0 {
		t.Fatalf("scroll %d on a pane the help fits", m.helpTop)
	}
}
