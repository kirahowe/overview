package diffview

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// editor is the text of a note being written and where the cursor is in
// it, a byte offset always on a rune boundary. Every method returns a new
// editor.
type editor struct {
	text string
	pos  int
}

func (e editor) insert(s string) editor {
	s = strings.ToValidUTF8(strings.ReplaceAll(s, "\r\n", "\n"), "�")
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\r':
			return '\n'
		case '\t':
			return ' '
		}
		return r
	}, s)
	e.text = e.text[:e.pos] + s + e.text[e.pos:]
	e.pos += len(s)
	return e
}

func (e editor) backspace() editor {
	if e.pos == 0 {
		return e
	}
	_, n := utf8.DecodeLastRuneInString(e.text[:e.pos])
	e.text = e.text[:e.pos-n] + e.text[e.pos:]
	e.pos -= n
	return e
}

func (e editor) del() editor {
	if e.pos == len(e.text) {
		return e
	}
	_, n := utf8.DecodeRuneInString(e.text[e.pos:])
	e.text = e.text[:e.pos] + e.text[e.pos+n:]
	return e
}

func (e editor) left() editor {
	_, n := utf8.DecodeLastRuneInString(e.text[:e.pos])
	e.pos -= n
	return e
}

func (e editor) right() editor {
	_, n := utf8.DecodeRuneInString(e.text[e.pos:])
	e.pos += n
	return e
}

// lineStart and lineEnd are the offsets of the ends of the cursor's line.
func (e editor) lineStart() int { return strings.LastIndexByte(e.text[:e.pos], '\n') + 1 }

func (e editor) lineEnd() int {
	if i := strings.IndexByte(e.text[e.pos:], '\n'); i >= 0 {
		return e.pos + i
	}
	return len(e.text)
}

func (e editor) home() editor { e.pos = e.lineStart(); return e }
func (e editor) end() editor  { e.pos = e.lineEnd(); return e }

// up and down move to the line above or below, keeping the column in
// runes as far as the line allows.
func (e editor) up() editor {
	start := e.lineStart()
	if start == 0 {
		e.pos = 0
		return e
	}
	col := utf8.RuneCountInString(e.text[start:e.pos])
	prev := strings.LastIndexByte(e.text[:start-1], '\n') + 1
	e.pos = atColumn(e.text, prev, start-1, col)
	return e
}

func (e editor) down() editor {
	end := e.lineEnd()
	if end == len(e.text) {
		e.pos = end
		return e
	}
	col := utf8.RuneCountInString(e.text[e.lineStart():e.pos])
	next := end + 1
	nextEnd := len(e.text)
	if i := strings.IndexByte(e.text[next:], '\n'); i >= 0 {
		nextEnd = next + i
	}
	e.pos = atColumn(e.text, next, nextEnd, col)
	return e
}

// atColumn is the offset col runes into text[from:to], or to.
func atColumn(text string, from, to, col int) int {
	i := from
	for ; col > 0 && i < to; col-- {
		_, n := utf8.DecodeRuneInString(text[i:])
		i += n
	}
	return i
}

// deleteWord deletes back to the start of the word before the cursor.
func (e editor) deleteWord() editor {
	i := e.pos
	for i > 0 {
		r, n := utf8.DecodeLastRuneInString(e.text[:i])
		if !unicode.IsSpace(r) {
			break
		}
		i -= n
	}
	for i > 0 {
		r, n := utf8.DecodeLastRuneInString(e.text[:i])
		if unicode.IsSpace(r) {
			break
		}
		i -= n
	}
	e.text = e.text[:i] + e.text[e.pos:]
	e.pos = i
	return e
}

// killLine deletes back to the start of the cursor's line.
func (e editor) killLine() editor {
	start := e.lineStart()
	e.text = e.text[:start] + e.text[e.pos:]
	e.pos = start
	return e
}

// rows draws the text wrapped to w cells, the cursor as a reversed cell,
// and says which row the cursor is on.
func (e editor) rows(w int, st Style) (rows [][]Seg, cursorRow int) {
	cur := st
	cur.Reverse = true
	lines := strings.Split(e.text, "\n")
	offset := 0
	for _, line := range lines {
		var segs []Seg
		if e.pos >= offset && e.pos <= offset+len(line) {
			at := e.pos - offset
			// The cursor is drawn on the whole grapheme it is in, so no
			// cluster is split between two segments and measured as two.
			from, under, rest := at, " ", line[at:]
			g := uniseg.NewGraphemes(line)
			for g.Next() {
				if gs, ge := g.Positions(); gs <= at && at < ge {
					from, under, rest = gs, line[gs:ge], line[ge:]
					break
				}
			}
			segs = []Seg{plain(line[:from], st), plain(under, cur), plain(rest, st)}
			wrapped := wrap(segs, w, st)
			// The cursor's row is the one where the text before it ends.
			before := wrap([]Seg{plain(line[:from], st), plain(under, cur)}, w, st)
			cursorRow = len(rows) + len(before) - 1
			rows = append(rows, wrapped...)
		} else {
			rows = append(rows, wrap([]Seg{plain(line, st)}, w, st)...)
		}
		offset += len(line) + 1
	}
	return rows, cursorRow
}
