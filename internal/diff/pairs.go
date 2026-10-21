package diff

// Pair is one row of a side-by-side view: indexes into a hunk's Lines,
// -1 for an empty side.
type Pair struct{ Old, New int }

// Pairs lines a hunk up for a side-by-side view. A context line is on
// both sides. A run of removed lines sits beside the run of added lines
// that follows it, row by row, the shorter side padded with empty cells.
func Pairs(h Hunk) []Pair {
	var rows []Pair
	for i := 0; i < len(h.Lines); {
		if h.Lines[i].Kind == Context {
			rows = append(rows, Pair{i, i})
			i++
			continue
		}
		removed := runEnd(h.Lines, i, Removed)
		added := runEnd(h.Lines, removed, Added)
		for k := 0; i+k < removed || removed+k < added; k++ {
			p := Pair{-1, -1}
			if i+k < removed {
				p.Old = i + k
			}
			if removed+k < added {
				p.New = removed + k
			}
			rows = append(rows, p)
		}
		i = added
	}
	return rows
}

// runEnd returns the index after the run of lines of one kind that
// starts at from.
func runEnd(lines []Line, from int, kind Kind) int {
	for from < len(lines) && lines[from].Kind == kind {
		from++
	}
	return from
}
