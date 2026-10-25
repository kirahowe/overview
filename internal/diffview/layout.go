package diffview

import (
	"overview/internal/diff"
	"overview/internal/review"
)

// The layout is the review as a list of items, top to bottom: each file's
// header, its hunks' headers and rows, and the notes under the lines they
// are on. It is a pure function of the diff, the notes and the options,
// rebuilt only when one of those changes. How tall an item is depends on
// the width, so that is left to drawing, which only measures the items on
// screen.

type itemKind uint8

const (
	fileItem  itemKind = iota // a file's header
	infoItem                  // a file with no lines to show: binary, or renamed only
	hunkItem                  // a hunk's @@ line
	rowItem                   // a line, or in split view a pair of lines
	noteItem                  // a note and its thread
	strayItem                 // the heading over notes on files not in the diff
	emptyItem                 // the whole diff is empty
)

// item is one entry of the layout. A rowItem's old and new index the
// hunk's lines, -1 for an empty side; in unified view one of them is -1,
// or both are the same context line, and mate is the line a changed line
// replaced or was replaced by, -1 for none, for its changed words.
type item struct {
	kind     itemKind
	file     int
	hunk     int
	old, new int
	mate     int
	note     string
	outdated bool // a note that no longer matches the diff
}

// layoutOpts is what the layout depends on besides the diff and notes.
type layoutOpts struct {
	split        bool
	showResolved bool
}

// selectable says whether the cursor can rest on an item.
func (it item) selectable() bool { return it.kind == rowItem || it.kind == noteItem }

// layout builds the items for files and the notes of r, placed by places.
func layout(files []diff.File, r review.Review, places map[string]review.Place, o layoutOpts) []item {
	// Notes go after the row their last line is on, or at the top of their
	// file when they no longer match, or at the end when their file is not
	// in the diff at all.
	type key struct {
		file int
		side review.Side
		line int
	}
	after := map[key][]string{}
	top := map[int][]string{}
	var stray []string
	for _, n := range r.Notes {
		if n.Resolved && !o.showResolved {
			continue
		}
		p, ok := places[n.ID]
		switch {
		case !ok || p.File < 0:
			stray = append(stray, n.ID)
		case p.Outdated:
			top[p.File] = append(top[p.File], n.ID)
		default:
			k := key{p.File, n.Anchor.Side, p.End}
			after[k] = append(after[k], n.ID)
		}
	}
	notes := func(ids []string, file int, outdated bool) []item {
		var out []item
		for _, id := range ids {
			out = append(out, item{kind: noteItem, file: file, hunk: -1, old: -1, new: -1, mate: -1, note: id, outdated: outdated})
		}
		return out
	}

	var out []item
	for fi, f := range files {
		out = append(out, item{kind: fileItem, file: fi, hunk: -1, old: -1, new: -1, mate: -1})
		out = append(out, notes(top[fi], fi, true)...)
		if len(f.Hunks) == 0 {
			out = append(out, item{kind: infoItem, file: fi, hunk: -1, old: -1, new: -1, mate: -1})
			continue
		}
		for hi, h := range f.Hunks {
			out = append(out, item{kind: hunkItem, file: fi, hunk: hi, old: -1, new: -1, mate: -1})
			for _, rw := range rows(h, o.split) {
				out = append(out, item{kind: rowItem, file: fi, hunk: hi, old: rw.old, new: rw.new, mate: rw.mate})
				// A context row is on both sides, so a note on either
				// side of it goes here, new side first.
				if rw.new >= 0 {
					out = append(out, notes(after[key{fi, review.New, h.Lines[rw.new].New}], fi, false)...)
				}
				if rw.old >= 0 {
					out = append(out, notes(after[key{fi, review.Old, h.Lines[rw.old].Old}], fi, false)...)
				}
			}
		}
	}
	if len(stray) > 0 {
		out = append(out, item{kind: strayItem, file: -1, hunk: -1, old: -1, new: -1, mate: -1})
		out = append(out, notes(stray, -1, true)...)
	}
	if len(files) == 0 && len(stray) == 0 {
		out = append(out, item{kind: emptyItem, file: -1, hunk: -1, old: -1, new: -1, mate: -1})
	}
	return out
}

// row is one row of a hunk: the line on each side, -1 for none, and in
// unified view the changed line's mate.
type row struct{ old, new, mate int }

// rows is a hunk's rows: its lines paired for split view, or one by one.
// In unified view a context line is on both sides, a removed line on the
// old and an added one on the new.
func rows(h diff.Hunk, split bool) []row {
	pairs := diff.Pairs(h)
	if split {
		out := make([]row, len(pairs))
		for i, p := range pairs {
			out[i] = row{p.Old, p.New, -1}
		}
		return out
	}
	mate := make([]int, len(h.Lines))
	for i := range mate {
		mate[i] = -1
	}
	for _, p := range pairs {
		if p.Old >= 0 && p.New >= 0 && p.Old != p.New {
			mate[p.Old], mate[p.New] = p.New, p.Old
		}
	}
	out := make([]row, len(h.Lines))
	for i, l := range h.Lines {
		switch l.Kind {
		case diff.Context:
			out[i] = row{i, i, -1}
		case diff.Removed:
			out[i] = row{i, -1, mate[i]}
		default:
			out[i] = row{-1, i, mate[i]}
		}
	}
	return out
}

// spot is a place in the diff that survives a reload: a line of a file on
// one side, or a note. The cursor is kept as a spot, so it stays on the
// same line when the diff under it changes.
type spot struct {
	path string
	side review.Side
	line int
	text string // the line's text, so a line that moved is found again
	note string
	// head says the spot is a header, of the file or, when hunk is not
	// -1, of its hunk-th hunk. A header has no line of its own, so it is
	// found again as itself and never as the nearest line.
	head bool
	hunk int
}

// spotOf is the spot an item is at.
func spotOf(files []diff.File, it item) spot {
	if it.kind == noteItem {
		return spot{note: it.note}
	}
	if it.file < 0 || it.file >= len(files) {
		return spot{}
	}
	f := files[it.file]
	s := spot{path: f.Path()}
	if it.kind != rowItem {
		s.head, s.hunk = true, it.hunk
		return s
	}
	lines := f.Hunks[it.hunk].Lines
	if it.new >= 0 {
		s.side, s.line, s.text = review.New, lines[it.new].New, lines[it.new].Text
	} else if it.old >= 0 {
		s.side, s.line, s.text = review.Old, lines[it.old].Old, lines[it.old].Text
	}
	return s
}

// find is the index of the item best matching s: its note, else the row
// of its file nearest its line number with the same text, as when lines
// were added above it, else the row nearest the number, else the file's
// header. A header is found as the same header, else its file's. ok is
// false when s's file is gone too.
func find(items []item, files []diff.File, s spot) (int, bool) {
	const far = 1 << 30
	best, bestDist := -1, far
	for i, it := range items {
		if s.head {
			if it.file < 0 || files[it.file].Path() != s.path || it.kind == rowItem || it.kind == noteItem {
				continue
			}
			if s.hunk >= 0 && it.kind == hunkItem && it.hunk == s.hunk {
				return i, true
			}
			if best < 0 {
				best = i
			}
			continue
		}
		if s.note != "" {
			if it.kind == noteItem && it.note == s.note {
				return i, true
			}
			continue
		}
		if it.file < 0 || files[it.file].Path() != s.path {
			continue
		}
		if it.kind != rowItem {
			if best < 0 {
				best = i
			}
			continue
		}
		lines := files[it.file].Hunks[it.hunk].Lines
		var l diff.Line
		var n int
		switch {
		case s.side == review.New && it.new >= 0:
			l = lines[it.new]
			n = l.New
		case s.side == review.Old && it.old >= 0:
			l = lines[it.old]
			n = l.Old
		default:
			continue
		}
		d := n - s.line
		if d < 0 {
			d = -d
		}
		if l.Text != s.text {
			d += far / 2
		} else if d == 0 {
			return i, true
		}
		if d < bestDist {
			best, bestDist = i, d
		}
	}
	return best, best >= 0
}

// nextSelectable is the first selectable item from i going in step dir,
// or i itself when there is none.
func nextSelectable(items []item, i, dir int) int {
	for j := i; j >= 0 && j < len(items); j += dir {
		if items[j].selectable() {
			return j
		}
	}
	return i
}

// settle puts i on a selectable item, looking forward first.
func settle(items []item, i int) int {
	i = max(0, min(i, len(items)-1))
	if len(items) == 0 || items[i].selectable() {
		return i
	}
	if j := nextSelectable(items, i, 1); items[j].selectable() {
		return j
	}
	return nextSelectable(items, i, -1)
}
