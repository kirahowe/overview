package diff

import (
	"bytes"
	"strings"
)

// Untracked is the diff of an untracked file, which git does not show: an
// added file with all its lines. Content with a NUL byte is binary and
// has no hunks. Its mode is the usual 100644, since the content cannot say.
func Untracked(path string, content []byte) File {
	f := File{NewPath: path, NewMode: "100644", Status: Created}
	if bytes.IndexByte(content, 0) >= 0 {
		f.Binary = true
		return f
	}
	if len(content) == 0 {
		return f
	}
	text := string(content)
	noEOL := !strings.HasSuffix(text, "\n")
	parts := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	h := Hunk{NewStart: 1, NewCount: len(parts)}
	for i, p := range parts {
		h.Lines = append(h.Lines, Line{Kind: Added, New: i + 1, Text: p})
	}
	h.Lines[len(parts)-1].NoEOL = noEOL
	f.Hunks = []Hunk{h}
	return f
}
