package diff

import (
	"unicode"
	"unicode/utf8"
)

// Span is a byte range of a string, half-open.
type Span struct{ Start, End int }

// maxWordWork caps the table WordDiff fills: the product of the two
// lines' token counts.
const maxWordWork = 250000

// WordDiff finds what changed inside a pair of lines. It splits both
// into words, runs of white space and single other characters, and marks
// the tokens outside their longest common run. ok is false when the
// lines have too little in common for the marks to help, or are too long
// to compare.
func WordDiff(old, new string) (oldSpans, newSpans []Span, ok bool) {
	a, b := tokens(old), tokens(new)
	if len(a)*len(b) > maxWordWork {
		return nil, nil, false
	}
	keepA, keepB := common(old, new, a, b)
	kept := 0
	for i, t := range a {
		if keepA[i] {
			kept += t.End - t.Start
		}
	}
	if longest := max(len(old), len(new)); kept*5 < longest*2 {
		return nil, nil, false
	}
	return spansOf(a, keepA), spansOf(b, keepB), true
}

// tokens splits s into word, white space and single-character tokens.
func tokens(s string) []Span {
	var out []Span
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		class := classOf(r)
		end := i + n
		for class != 0 && end < len(s) {
			r, n := utf8.DecodeRuneInString(s[end:])
			if classOf(r) != class {
				break
			}
			end += n
		}
		out = append(out, Span{i, end})
		i = end
	}
	return out
}

// classOf is 1 for a word character, 2 for white space and 0 for any
// other, which stands alone.
func classOf(r rune) int {
	switch {
	case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
		return 1
	case unicode.IsSpace(r):
		return 2
	}
	return 0
}

// common marks the tokens of a and b that belong to a longest common
// subsequence of their text.
func common(old, new string, a, b []Span) (keepA, keepB []bool) {
	w := len(b) + 1
	lcs := make([]int32, (len(a)+1)*w)
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if old[a[i].Start:a[i].End] == new[b[j].Start:b[j].End] {
				lcs[i*w+j] = lcs[(i+1)*w+j+1] + 1
			} else {
				lcs[i*w+j] = max(lcs[(i+1)*w+j], lcs[i*w+j+1])
			}
		}
	}
	keepA, keepB = make([]bool, len(a)), make([]bool, len(b))
	for i, j := 0, 0; i < len(a) && j < len(b); {
		switch {
		case old[a[i].Start:a[i].End] == new[b[j].Start:b[j].End]:
			keepA[i], keepB[j] = true, true
			i++
			j++
		case lcs[(i+1)*w+j] >= lcs[i*w+j+1]:
			i++
		default:
			j++
		}
	}
	return keepA, keepB
}

// spansOf merges the tokens that are not kept into spans.
func spansOf(toks []Span, keep []bool) []Span {
	var out []Span
	for i, t := range toks {
		if keep[i] {
			continue
		}
		if n := len(out); n > 0 && out[n-1].End == t.Start {
			out[n-1].End = t.End
		} else {
			out = append(out, t)
		}
	}
	return out
}
