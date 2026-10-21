package review

import "overview/internal/diff"

// Place is where a note sits in the current diff. File indexes the files
// slice, or is -1 when the note's file is not in the diff. Start and End
// are line numbers on the anchor's side. Outdated says the anchored text
// is no longer there, and then Start and End are the anchor's own.
type Place struct {
	File       int
	Start, End int
	Outdated   bool
}

// sideLines is the text of the hunk lines present on a side, by number.
func sideLines(f diff.File, side Side) map[int]string {
	m := map[int]string{}
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			switch {
			case side == New && l.Kind != diff.Removed && l.New > 0:
				m[l.New] = l.Text
			case side == Old && l.Kind != diff.Added && l.Old > 0:
				m[l.Old] = l.Text
			}
		}
	}
	return m
}

func runAt(m map[int]string, start int, lines []string) bool {
	for i, t := range lines {
		if got, ok := m[start+i]; !ok || got != t {
			return false
		}
	}
	return true
}

// Locate finds the anchor's lines in a file's diff. It prefers the
// original line numbers; failing that it takes the run of lines with the
// same text that starts nearest, the earlier one on a tie.
func Locate(a Anchor, f diff.File) (start int, ok bool) {
	if len(a.Lines) == 0 {
		return 0, false
	}
	m := sideLines(f, a.Side)
	if runAt(m, a.Start, a.Lines) {
		return a.Start, true
	}
	best, found := 0, false
	for s := range m {
		if !runAt(m, s, a.Lines) {
			continue
		}
		d, bd := abs(s-a.Start), abs(best-a.Start)
		if !found || d < bd || (d == bd && s < best) {
			best, found = s, true
		}
	}
	return best, found
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// fileIndex finds the anchor's file in the diff, or -1.
func fileIndex(a Anchor, files []diff.File) int {
	for i, f := range files {
		p := f.NewPath
		if a.Side == Old {
			p = f.OldPath
		}
		if p != "" && p == a.Path {
			return i
		}
	}
	return -1
}

// Places locates every note of the review in the diff, by note id.
func Places(r Review, files []diff.File) map[string]Place {
	out := make(map[string]Place, len(r.Notes))
	for _, n := range r.Notes {
		a := n.Anchor
		p := Place{File: fileIndex(a, files), Start: a.Start, End: a.End, Outdated: true}
		if p.File >= 0 {
			if s, ok := Locate(a, files[p.File]); ok {
				p.Start, p.End, p.Outdated = s, s+len(a.Lines)-1, false
			}
		}
		out[n.ID] = p
	}
	return out
}
