package tmux

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// genCell draws a cell of the given size with consistent geometry: children
// of a split are one border apart and fill it exactly. next hands out pane
// numbers.
func genCell(t *rapid.T, next *int, w, h, x, y, depth int) *cell {
	canH := w >= 3 // two panes and a border
	canV := h >= 3
	if depth == 0 || !(canV || canH) || rapid.IntRange(0, 3).Draw(t, "leaf") == 0 {
		*next++
		return &cell{kind: 'p', w: w, h: h, x: x, y: y, id: fmt.Sprint(*next - 1)}
	}
	var kinds []byte
	if canH {
		kinds = append(kinds, '{')
	}
	if canV {
		kinds = append(kinds, '[')
	}
	c := &cell{kind: rapid.SampledFrom(kinds).Draw(t, "kind"), w: w, h: h, x: x, y: y}
	axis := c.along(c.kind)
	n := rapid.IntRange(2, min(5, (axis+1)/2)).Draw(t, "children")
	left := axis - (n - 1)
	off := 0
	for i := 0; i < n; i++ {
		size := left
		if i < n-1 {
			size = rapid.IntRange(1, left-(n-1-i)).Draw(t, "size")
		}
		left -= size
		if c.kind == '{' {
			c.kids = append(c.kids, genCell(t, next, size, h, x+off, y, depth-1))
		} else {
			c.kids = append(c.kids, genCell(t, next, w, size, x, y+off, depth-1))
		}
		off += size + 1
	}
	return c
}

func genLayout(t *rapid.T) *cell {
	next := 0
	return genCell(t, &next, rapid.IntRange(1, 150).Draw(t, "w"), rapid.IntRange(1, 60).Draw(t, "h"), 0, 0, 4)
}

// leaves lists the pane numbers in layout order.
func leaves(c *cell) []string {
	if c.kind == 'p' {
		return []string{c.id}
	}
	var out []string
	for _, k := range c.kids {
		out = append(out, leaves(k)...)
	}
	return out
}

// consistent reports the first way c breaks the rules of a tmux layout.
func consistent(c *cell) error {
	if c.w < 1 || c.h < 1 {
		return fmt.Errorf("cell %dx%d at %d,%d is empty", c.w, c.h, c.x, c.y)
	}
	if c.kind == 'p' {
		if len(c.kids) != 0 {
			return fmt.Errorf("pane %s has children", c.id)
		}
		return nil
	}
	if len(c.kids) < 1 {
		return fmt.Errorf("split at %d,%d has no children", c.x, c.y)
	}
	off, sum := 0, 0
	for i, k := range c.kids {
		wantX, wantY := c.x, c.y
		if c.kind == '{' {
			wantX += off
			if k.h != c.h {
				return fmt.Errorf("child %d height %d, parent %d", i, k.h, c.h)
			}
		} else {
			wantY += off
			if k.w != c.w {
				return fmt.Errorf("child %d width %d, parent %d", i, k.w, c.w)
			}
		}
		if k.x != wantX || k.y != wantY {
			return fmt.Errorf("child %d at %d,%d, want %d,%d", i, k.x, k.y, wantX, wantY)
		}
		off += k.along(c.kind) + 1
		sum += k.along(c.kind)
		if err := consistent(k); err != nil {
			return err
		}
	}
	if sum+len(c.kids)-1 != c.along(c.kind) {
		return fmt.Errorf("children of %c split at %d,%d span %d+%d borders, parent %d",
			c.kind, c.x, c.y, sum, len(c.kids)-1, c.along(c.kind))
	}
	return nil
}

func TestPropFormatParseRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		root := genLayout(t)
		if err := consistent(root); err != nil {
			t.Fatalf("generator: %v", err)
		}
		s := formatLayout(root)
		back, err := parseLayout(s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		if again := formatLayout(back); again != s {
			t.Fatalf("round trip changed %q to %q", s, again)
		}
	})
}

func TestPropWithout(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		root := genLayout(t)
		in := formatLayout(root)
		ids := leaves(root)
		victim := rapid.SampledFrom(ids).Draw(t, "victim")

		out, err := Without(in, "%"+victim)
		if len(ids) == 1 {
			if err == nil {
				t.Fatalf("removing the only pane of %q gave %q", in, out)
			}
			return
		}
		if err != nil {
			t.Fatalf("Without(%q, %%%s): %v", in, victim, err)
		}
		// parseLayout checks the checksum.
		got, err := parseLayout(out)
		if err != nil {
			t.Fatalf("result %q does not parse: %v", out, err)
		}
		if got.w != root.w || got.h != root.h || got.x != root.x || got.y != root.y {
			t.Fatalf("root %dx%d at %d,%d became %dx%d at %d,%d",
				root.w, root.h, root.x, root.y, got.w, got.h, got.x, got.y)
		}
		if err := consistent(got); err != nil {
			t.Fatalf("%q from %q minus %%%s: %v", out, in, victim, err)
		}
		want := slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return id == victim })
		if have := leaves(got); !slices.Equal(have, want) {
			t.Fatalf("panes %v, want %v", have, want)
		}
		// Only the split the pane was in changes: a pane outside it keeps
		// its exact place and size, and one inside it, a sibling of the
		// removed pane or within one, never shrinks.
		parent := parentOf(root, victim)
		after := rects(got)
		for id, r := range rects(root) {
			a, kept := after[id]
			switch {
			case id == victim:
			case !kept:
				t.Fatalf("%%%s is gone", id)
			case !parent.holds(r) && a != r:
				t.Fatalf("%%%s outside the split moved from %+v to %+v", id, r, a)
			case parent.holds(r) && (a.w < r.w || a.h < r.h):
				t.Fatalf("%%%s in the split shrank from %+v to %+v", id, r, a)
			}
		}
	})
}

// rect is a cell's place and size.
type rect struct{ w, h, x, y int }

func (r rect) holds(o rect) bool {
	return o.x >= r.x && o.y >= r.y && o.x+o.w <= r.x+r.w && o.y+o.h <= r.y+r.h
}

// rects is every pane's rectangle, by pane number.
func rects(c *cell) map[string]rect {
	out := map[string]rect{}
	if c.kind == 'p' {
		out[c.id] = rect{c.w, c.h, c.x, c.y}
	}
	for _, k := range c.kids {
		for id, r := range rects(k) {
			out[id] = r
		}
	}
	return out
}

// parentOf is the rectangle of the split that holds pane id directly.
func parentOf(c *cell, id string) rect {
	for _, k := range c.kids {
		if k.kind == 'p' && k.id == id {
			return rect{c.w, c.h, c.x, c.y}
		}
		if r := parentOf(k, id); r != (rect{}) {
			return r
		}
	}
	return rect{}
}

// Asking to remove a pane that is not in the layout is an error.
func TestPropWithoutAbsentPane(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := formatLayout(genLayout(t))
		if out, err := Without(in, "%9999"); err == nil {
			t.Fatalf("Without(%q, %%9999) = %q, want an error", in, out)
		}
	})
}

// PaneOf finds the pane listed with the pid, and no pane when none is.
func TestPropPaneOfFindsTheListedPid(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		pids := rapid.SliceOfDistinct(rapid.IntRange(1, 99999), rapid.ID[int]).Draw(t, "pids")
		var b strings.Builder
		ids := map[int]string{}
		for i, pid := range pids {
			ids[pid] = fmt.Sprintf("%%%d", i)
			fmt.Fprintf(&b, "%s\t%d\n", ids[pid], pid)
		}
		pid := rapid.IntRange(1, 99999).Draw(t, "pid")
		got, ok := PaneOf(b.String(), pid)
		if want, listed := ids[pid]; got != want || ok != listed {
			t.Fatalf("PaneOf(%d) = %q, %v; want %q, %v in\n%s", pid, got, ok, want, listed, b.String())
		}
	})
}

// latestClient picks a client no other was used after, the last listed of
// those used last, whatever junk lines are among them.
func TestPropLatestClientIsTheLastUsed(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		type client struct {
			at  int64
			pid int
		}
		clients := rapid.SliceOf(rapid.Custom(func(t *rapid.T) client {
			return client{rapid.Int64Range(0, 5).Draw(t, "at"), rapid.IntRange(1, 99999).Draw(t, "pid")}
		})).Draw(t, "clients")
		var lines []string
		for _, c := range clients {
			lines = append(lines, fmt.Sprintf("%d\t%d", c.at, c.pid))
			if rapid.Bool().Draw(t, "junk") {
				lines = append(lines, rapid.SampledFrom([]string{"", "x\t1", "1\t-3", "1\t2\t3"}).Draw(t, "line"))
			}
		}
		want := 0
		var at int64 = -1
		for _, c := range clients {
			if c.at >= at {
				at, want = c.at, c.pid
			}
		}
		if got := latestClient(strings.Join(lines, "\n")); got != want {
			t.Fatalf("got %d, want %d", got, want)
		}
	})
}

// CopyAll copies with every way it is given, and fails exactly when all
// of them fail.
func TestPropCopyAllFailsOnlyWhenAllDo(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		works := rapid.SliceOfN(rapid.Bool(), 1, -1).Draw(t, "works")
		tried := 0
		var copies []func(string) error
		for _, w := range works {
			copies = append(copies, func(string) error {
				tried++
				if w {
					return nil
				}
				return fmt.Errorf("no clipboard")
			})
		}
		err := CopyAll(copies...)("abc")
		if tried != len(works) || (err == nil) != slices.Contains(works, true) {
			t.Fatalf("works %v: tried %d, err %v", works, tried, err)
		}
	})
}
