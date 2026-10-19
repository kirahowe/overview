package tmux

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// A layout cell is one rectangle of a window: a pane (leaf) or a left-right
// ('{') or top-bottom ('[') split of child cells. Children of a split are one
// cell apart, the width of the border between them.
type cell struct {
	kind       byte // 'p' for a pane, '{' or '['
	w, h, x, y int
	id         string // pane number without the %, leaves only
	kids       []*cell
}

// Without returns layout with pane's cell removed and the space it held
// shared among its siblings in proportion to their sizes, ready for
// select-layout. Pane is a pane id such as "%12". The window's overall size
// is unchanged. layout is the string from #{window_layout}, either the
// classic form with a checksum or the JSON form that tmux 3.8 prints to
// ordinary clients; the result is always the classic form, which every
// tmux accepts.
func Without(layout, pane string) (string, error) {
	id := strings.TrimPrefix(pane, "%")
	if id == "" {
		return "", errors.New("layout: empty pane id")
	}
	root, err := parseLayout(layout)
	if err != nil {
		return "", err
	}
	if root.kind == 'p' {
		if root.id == id {
			return "", errors.New("layout: cannot remove the only pane")
		}
		return "", fmt.Errorf("layout: pane %%%s not found", id)
	}
	if !removeLeaf(root, id) {
		return "", fmt.Errorf("layout: pane %%%s not found", id)
	}
	// Dropping a child can leave a split with one child, which then takes
	// its place, so the root may now be a different cell.
	root = collapse(root)
	place(root, root.w, root.h, root.x, root.y)
	return formatLayout(root), nil
}

// removeLeaf deletes the leaf with the given id from the subtree under c and
// reports whether it found one. The split it leaves is laid out afresh.
func removeLeaf(c *cell, id string) bool {
	for i, k := range c.kids {
		if k.kind == 'p' && k.id == id {
			c.kids = append(c.kids[:i:i], c.kids[i+1:]...)
			c.grow()
			// Lay the split's subtree out now, so a nested split that
			// collapse splices upwards brings children that fit it.
			place(c, c.w, c.h, c.x, c.y)
			return true
		}
		if k.kind != 'p' && removeLeaf(k, id) {
			c.kids[i] = collapse(k)
			return true
		}
	}
	return false
}

// grow hands the room a removed child held to the remaining ones.
func (c *cell) grow() {
	sizes := make([]int, len(c.kids))
	for i, k := range c.kids {
		sizes[i] = k.along(c.kind)
	}
	total := c.along(c.kind) - (len(c.kids) - 1)
	for i, s := range scale(sizes, total) {
		c.kids[i].setAlong(c.kind, s)
	}
}

// collapse replaces a split that has one child by that child, and splices
// the children of a nested split of the same direction into their parent.
// Neither changes any rectangle.
func collapse(c *cell) *cell {
	for c.kind != 'p' && len(c.kids) == 1 {
		k := c.kids[0]
		k.w, k.h, k.x, k.y = c.w, c.h, c.x, c.y
		c = k
	}
	if c.kind == 'p' {
		return c
	}
	var kids []*cell
	for _, k := range c.kids {
		if k.kind == c.kind {
			kids = append(kids, k.kids...)
		} else {
			kids = append(kids, k)
		}
	}
	c.kids = kids
	return c
}

// along is the cell's size in the direction a split of the given kind lays
// its children out.
func (c *cell) along(kind byte) int {
	if kind == '{' {
		return c.w
	}
	return c.h
}

func (c *cell) setAlong(kind byte, n int) {
	if kind == '{' {
		c.w = n
	} else {
		c.h = n
	}
}

// place gives c the rectangle and lays its descendants out inside it:
// children share the cross size, and along the split they are scaled to fit
// with one border cell between neighbours.
func place(c *cell, w, h, x, y int) {
	c.w, c.h, c.x, c.y = w, h, x, y
	if c.kind == 'p' {
		return
	}
	sizes := make([]int, len(c.kids))
	for i, k := range c.kids {
		sizes[i] = k.along(c.kind)
	}
	sizes = scale(sizes, c.along(c.kind)-(len(c.kids)-1))
	off := 0
	for i, k := range c.kids {
		if c.kind == '{' {
			place(k, sizes[i], h, x+off, y)
		} else {
			place(k, w, sizes[i], x, y+off)
		}
		off += sizes[i] + 1
	}
}

// scale returns sizes resized to sum to total, each in proportion to its
// old value and none below 1. Rounding leftovers go to the first cells.
func scale(sizes []int, total int) []int {
	old := 0
	for _, s := range sizes {
		old += s
	}
	out := make([]int, len(sizes))
	sum := 0
	for i, s := range sizes {
		if old > 0 {
			out[i] = total * s / old
		}
		if out[i] < 1 {
			out[i] = 1
		}
		sum += out[i]
	}
	for i := 0; sum < total; i = (i + 1) % len(out) {
		out[i]++
		sum++
	}
	for sum > total {
		big := 0
		for i := range out {
			if out[i] > out[big] {
				big = i
			}
		}
		if out[big] <= 1 {
			break
		}
		out[big]--
		sum--
	}
	return out
}

// checksum is tmux's layout_checksum.
func checksum(s string) uint16 {
	var sum uint16
	for i := 0; i < len(s); i++ {
		sum = sum>>1 + (sum&1)<<15
		sum += uint16(s[i])
	}
	return sum
}

func formatLayout(root *cell) string {
	body := formatCell(root)
	return fmt.Sprintf("%04x,%s", checksum(body), body)
}

func formatCell(c *cell) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%dx%d,%d,%d", c.w, c.h, c.x, c.y)
	if c.kind == 'p' {
		b.WriteString("," + c.id)
		return b.String()
	}
	b.WriteByte(c.kind)
	for i, k := range c.kids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(formatCell(k))
	}
	b.WriteByte(map[byte]byte{'{': '}', '[': ']'}[c.kind])
	return b.String()
}

func parseLayout(s string) (*cell, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "{") {
		return parseJSONLayout(s)
	}
	if len(s) < 5 || s[4] != ',' {
		return nil, fmt.Errorf("layout: malformed %q", s)
	}
	want, err := strconv.ParseUint(s[:4], 16, 16)
	if err != nil {
		return nil, fmt.Errorf("layout: bad checksum in %q", s)
	}
	body := s[5:]
	if uint16(want) != checksum(body) {
		return nil, fmt.Errorf("layout: checksum mismatch in %q", s)
	}
	p := &layoutParser{s: body}
	c, err := p.cell()
	if err != nil {
		return nil, err
	}
	if p.i != len(p.s) {
		return nil, fmt.Errorf("layout: trailing text at %d in %q", p.i, s)
	}
	return c, nil
}

type layoutParser struct {
	s string
	i int
}

func (p *layoutParser) num() (int, error) {
	start := p.i
	for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
		p.i++
	}
	if start == p.i || p.i-start > 9 {
		return 0, fmt.Errorf("layout: expected a number at %d in %q", start, p.s)
	}
	return strconv.Atoi(p.s[start:p.i])
}

func (p *layoutParser) lit(b byte) error {
	if p.i >= len(p.s) || p.s[p.i] != b {
		return fmt.Errorf("layout: expected %q at %d in %q", b, p.i, p.s)
	}
	p.i++
	return nil
}

func (p *layoutParser) cell() (*cell, error) {
	c := &cell{}
	var err error
	for _, step := range []struct {
		sep byte
		dst *int
	}{{0, &c.w}, {'x', &c.h}, {',', &c.x}, {',', &c.y}} {
		if step.sep != 0 {
			if err = p.lit(step.sep); err != nil {
				return nil, err
			}
		}
		if *step.dst, err = p.num(); err != nil {
			return nil, err
		}
	}
	if c.w < 1 || c.h < 1 {
		return nil, fmt.Errorf("layout: empty cell %dx%d", c.w, c.h)
	}
	if p.i >= len(p.s) {
		return nil, fmt.Errorf("layout: cell ends early in %q", p.s)
	}
	switch p.s[p.i] {
	case ',':
		p.i++
		c.kind = 'p'
		start := p.i
		if _, err := p.num(); err != nil {
			return nil, err
		}
		c.id = p.s[start:p.i]
		return c, nil
	case '{', '[':
		c.kind = p.s[p.i]
		closer := byte('}')
		if c.kind == '[' {
			closer = ']'
		}
		p.i++
		for {
			k, err := p.cell()
			if err != nil {
				return nil, err
			}
			c.kids = append(c.kids, k)
			if p.i < len(p.s) && p.s[p.i] == ',' {
				p.i++
				continue
			}
			if err := p.lit(closer); err != nil {
				return nil, err
			}
			return c, nil
		}
	}
	return nil, fmt.Errorf("layout: unexpected %q at %d in %q", p.s[p.i], p.i, p.s)
}

// jsonCell is a cell in the JSON form of #{window_layout} (tmux 3.8).
type jsonCell struct {
	T    string     `json:"t"`
	W    int        `json:"w"`
	H    int        `json:"h"`
	X    int        `json:"x"`
	Y    int        `json:"y"`
	ID   string     `json:"I"`
	Idx  int        `json:"i"` // pane index; named so "I" is matched exactly
	Kids []jsonCell `json:"c"`
}

func parseJSONLayout(s string) (*cell, error) {
	var doc struct {
		L jsonCell `json:"L"`
	}
	if err := json.Unmarshal([]byte(s), &doc); err != nil {
		return nil, fmt.Errorf("layout: bad JSON: %w", err)
	}
	return doc.L.cell()
}

func (j jsonCell) cell() (*cell, error) {
	c := &cell{w: j.W, h: j.H, x: j.X, y: j.Y}
	if c.w < 1 || c.h < 1 {
		return nil, fmt.Errorf("layout: empty cell %dx%d", c.w, c.h)
	}
	switch j.T {
	case "p":
		c.kind = 'p'
		c.id = strings.TrimPrefix(j.ID, "%")
		if _, err := strconv.Atoi(c.id); err != nil {
			return nil, fmt.Errorf("layout: bad pane id %q", j.ID)
		}
	case "h", "v":
		c.kind = map[string]byte{"h": '{', "v": '['}[j.T]
		if len(j.Kids) == 0 {
			return nil, errors.New("layout: split without children")
		}
		for _, k := range j.Kids {
			kc, err := k.cell()
			if err != nil {
				return nil, err
			}
			c.kids = append(c.kids, kc)
		}
	default:
		return nil, fmt.Errorf("layout: unknown cell type %q", j.T)
	}
	return c, nil
}
