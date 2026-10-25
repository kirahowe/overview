package diffview

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// Text from a diff is the file's own bytes, and the terminal must never see
// them raw: a tab's width depends on its column, and a control character or
// an escape sequence in the code would move the cursor or change colours
// under the frame. clean turns a line into text where every grapheme has a
// known width, and every frame line can be exactly as wide as the pane.

// tabWidth is how far apart tab stops are.
const tabWidth = 4

// clean is s as it is drawn: tabs expanded to the next stop, control
// characters as their visible pictures, invalid UTF-8 and bidirectional
// overrides as U+FFFD, and a line-ending carriage return dropped. at maps
// each byte offset of s, and len(s), to the offset in out where it lands,
// so spans found on the raw text can be found again in the clean one.
func clean(s string) (out string, at []int) {
	var b strings.Builder
	at = make([]int, len(s)+1)
	col := 0
	s2 := strings.TrimSuffix(s, "\r")
	for i := 0; i < len(s2); {
		r, size := utf8.DecodeRuneInString(s2[i:])
		for k := 0; k < size; k++ {
			at[i+k] = b.Len()
		}
		switch {
		case r == '\t':
			n := tabWidth - col%tabWidth
			b.WriteString(strings.Repeat(" ", n))
			col += n
		case r == utf8.RuneError && size == 1, bidi(r):
			b.WriteRune('�')
			col++
		case r < 0x20:
			b.WriteRune(0x2400 + r) // ␀ to ␟
			col++
		case r == 0x7f:
			b.WriteRune('␡') // ␡
			col++
		case unicode.Is(unicode.Cc, r): // the C1 controls
			b.WriteRune('�')
			col++
		default:
			// Column tracking only needs to be right for tabs, so a
			// rune's own width is enough here.
			b.WriteRune(r)
			col += uniseg.StringWidth(string(r))
		}
		i += size
	}
	for i := len(s2); i <= len(s); i++ {
		at[i] = b.Len()
	}
	return b.String(), at
}

// bidi reports the characters that reorder text: drawn as they are, they
// can make code read differently from what it does.
func bidi(r rune) bool {
	return (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) || r == 0x200e || r == 0x200f || r == 0x061c
}

// Color is a theme colour: "#rrggbb", "#rgb" or an ANSI index. "" is the
// terminal's own.
type Color string

// Style is how a run of text is drawn.
type Style struct {
	Fg, Bg             Color
	Bold, Italic, Dim  bool
	Underline, Reverse bool
}

// Seg is a run of clean text in one style.
type Seg struct {
	Text string
	St   Style
}

// width is how many cells segs take.
func width(segs []Seg) int {
	n := 0
	for _, s := range segs {
		n += uniseg.StringWidth(s.Text)
	}
	return n
}

// wrap breaks segs into rows at most w cells wide, never inside a
// grapheme. A grapheme wider than the room left starts the next row, and
// one wider than w itself, which only a pane one cell wide can meet, is
// drawn as "?" so it cannot spill. Every row but the last is filled to w
// with spaces in fill. Empty input gives one empty row.
func wrap(segs []Seg, w int, fill Style) [][]Seg {
	rows := [][]Seg{nil}
	col := 0
	add := func(text string, st Style) {
		r := &rows[len(rows)-1]
		if n := len(*r); n > 0 && (*r)[n-1].St == st {
			(*r)[n-1].Text += text
			return
		}
		*r = append(*r, Seg{text, st})
	}
	for _, s := range join(segs) {
		g := uniseg.NewGraphemes(s.Text)
		for g.Next() {
			text, gw := g.Str(), g.Width()
			if gw > w {
				text, gw = "?", 1
			}
			if col+gw > w {
				if col < w {
					add(strings.Repeat(" ", w-col), fill)
				}
				rows = append(rows, nil)
				col = 0
			}
			add(text, s.St)
			col += gw
		}
	}
	return rows
}

// join merges neighbours in one style, as a row draws them, so they are
// measured joined: a zero-width joiner or combining mark at the start of
// one fuses with the grapheme that ends the other. An empty segment draws
// nothing, so it does not keep them apart.
func join(segs []Seg) []Seg {
	var out []Seg
	for _, s := range segs {
		if s.Text == "" {
			continue
		}
		if n := len(out); n > 0 && out[n-1].St == s.St {
			out[n-1].Text += s.Text
			continue
		}
		out = append(out, s)
	}
	return out
}

// pad fills segs out to exactly w cells with spaces in fill, cutting it
// short when it is wider, at a grapheme boundary, padding what a wide
// grapheme leaves.
func pad(segs []Seg, w int, fill Style) []Seg {
	var out []Seg
	col := 0
	for _, s := range segs {
		if col >= w {
			break
		}
		if sw := uniseg.StringWidth(s.Text); col+sw <= w {
			out = append(out, s)
			col += sw
			continue
		}
		var b strings.Builder
		g := uniseg.NewGraphemes(s.Text)
		for g.Next() && col+g.Width() <= w {
			b.WriteString(g.Str())
			col += g.Width()
		}
		out = append(out, Seg{b.String(), s.St})
		break
	}
	if col < w {
		out = append(out, Seg{strings.Repeat(" ", w-col), fill})
	}
	return out
}

// truncate is s cut to w cells, ending in "…" when it was longer.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if uniseg.StringWidth(s) <= w {
		return s
	}
	var b strings.Builder
	col := 0
	g := uniseg.NewGraphemes(s)
	for g.Next() && col+g.Width() <= w-1 {
		b.WriteString(g.Str())
		col += g.Width()
	}
	return b.String() + "…"
}

// plain cleans a line of the viewer's own text, such as a note, into one
// segment.
func plain(s string, st Style) Seg {
	c, _ := clean(s)
	return Seg{c, st}
}
