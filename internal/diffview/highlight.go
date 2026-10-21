package diffview

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"

	"overview/internal/diff"
)

// Syntax colours come from chroma, run in a Cmd per file once its rows are
// on screen, so the first frames are plain and colour never holds up a
// key.

// tok is one token of a line: a byte range of its text and its type.
type tok struct {
	start, end int
	tt         chroma.TokenType
}

// lit is a file's tokens, by hunk and then by line of the hunk. A line
// with none is drawn plain.
type lit [][][]tok

// maxLexed is the most text one side of a hunk may have to be highlighted.
const maxLexed = 256 << 10

// highlight tokenises each hunk of f twice, as the old side reads and as
// the new side does, so a construct that spans lines, such as a comment,
// is coloured as the file has it. Context lines take the new side's
// tokens. A file chroma has no lexer for gets nil.
func highlight(f diff.File) lit {
	lexer := lexers.Match(f.Path())
	if lexer == nil || f.Binary {
		return nil
	}
	lexer = chroma.Coalesce(lexer)
	out := make(lit, len(f.Hunks))
	for hi, h := range f.Hunks {
		out[hi] = make([][]tok, len(h.Lines))
		side(lexer, h, out[hi], func(k diff.Kind) bool { return k != diff.Added }, func(k diff.Kind) bool { return k == diff.Removed })
		side(lexer, h, out[hi], func(k diff.Kind) bool { return k != diff.Removed }, func(k diff.Kind) bool { return k != diff.Removed })
	}
	return out
}

// side tokenises the lines of h that on admits, as one text, and keeps the
// tokens of the lines that keep admits.
func side(lexer chroma.Lexer, h diff.Hunk, into [][]tok, on, keep func(diff.Kind) bool) {
	var idx []int
	var b strings.Builder
	for i, l := range h.Lines {
		if on(l.Kind) {
			idx = append(idx, i)
			b.WriteString(l.Text)
			b.WriteByte('\n')
		}
	}
	if len(idx) == 0 || b.Len() > maxLexed {
		return
	}
	it, err := lexer.Tokenise(nil, b.String())
	if err != nil {
		return
	}
	line, off := 0, 0
	for t := it(); t != chroma.EOF; t = it() {
		for text := t.Value; text != "" && line < len(idx); {
			n, nl := len(text), false
			if j := strings.IndexByte(text, '\n'); j >= 0 {
				n, nl = j, true
			}
			li := idx[line]
			if n > 0 && keep(h.Lines[li].Kind) && off+n <= len(h.Lines[li].Text) {
				into[li] = append(into[li], tok{off, off + n, t.Type})
			}
			off += n
			text = text[n:]
			if nl {
				text = text[1:]
				line, off = line+1, 0
			}
		}
	}
}
