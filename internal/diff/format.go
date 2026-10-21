package diff

import (
	"strconv"
	"strings"
)

// Format writes files in git's unified format, quoting the paths that
// git would. Parse reads the result back to the same files.
func Format(files []File) string {
	var b strings.Builder
	for _, f := range files {
		formatFile(&b, f)
	}
	return b.String()
}

func formatFile(b *strings.Builder, f File) {
	oldPath, newPath := f.OldPath, f.NewPath
	if oldPath == "" {
		oldPath = newPath
	}
	if newPath == "" {
		newPath = oldPath
	}
	line(b, "diff --git ", quote("a/"+oldPath), " ", quote("b/"+newPath))
	formatModes(b, f)
	switch f.Status {
	case Renamed:
		line(b, "similarity index 100%")
		line(b, "rename from ", quote(f.OldPath))
		line(b, "rename to ", quote(f.NewPath))
	case Copied:
		line(b, "similarity index 100%")
		line(b, "copy from ", quote(f.OldPath))
		line(b, "copy to ", quote(f.NewPath))
	}
	if f.Binary {
		line(b, "Binary files ", sidePathText(f.OldPath, "a/"), " and ", sidePathText(f.NewPath, "b/"), " differ")
		return
	}
	if len(f.Hunks) == 0 {
		return
	}
	line(b, "--- ", sidePathText(f.OldPath, "a/"))
	line(b, "+++ ", sidePathText(f.NewPath, "b/"))
	for _, h := range f.Hunks {
		formatHunk(b, h)
	}
}

func formatModes(b *strings.Builder, f File) {
	switch f.Status {
	case Created:
		if f.NewMode != "" {
			line(b, "new file mode ", f.NewMode)
		}
	case Deleted:
		if f.OldMode != "" {
			line(b, "deleted file mode ", f.OldMode)
		}
	default:
		if f.OldMode != "" {
			line(b, "old mode ", f.OldMode)
		}
		if f.NewMode != "" {
			line(b, "new mode ", f.NewMode)
		}
	}
}

func sidePathText(path, prefix string) string {
	if path == "" {
		return devNull
	}
	return quote(prefix + path)
}

func formatHunk(b *strings.Builder, h Hunk) {
	head := "@@ -" + formatRange(h.OldStart, h.OldCount) + " +" + formatRange(h.NewStart, h.NewCount) + " @@"
	if h.Section != "" {
		head += " " + h.Section
	}
	line(b, head)
	for _, l := range h.Lines {
		mark := " "
		switch l.Kind {
		case Added:
			mark = "+"
		case Removed:
			mark = "-"
		}
		line(b, mark, l.Text)
		if l.NoEOL {
			line(b, `\ No newline at end of file`)
		}
	}
}

func formatRange(start, count int) string {
	if count == 1 {
		return strconv.Itoa(start)
	}
	return strconv.Itoa(start) + "," + strconv.Itoa(count)
}

func line(b *strings.Builder, parts ...string) {
	for _, p := range parts {
		b.WriteString(p)
	}
	b.WriteByte('\n')
}
