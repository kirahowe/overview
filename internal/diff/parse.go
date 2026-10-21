package diff

import (
	"fmt"
	"strconv"
	"strings"
)

const devNull = "/dev/null"

// Parse reads git's unified diff format, as both `git diff` and
// `jj diff --git` write it. It returns an error only for a structural
// problem, such as a hunk shorter than its counts.
func Parse(src string) ([]File, error) {
	lines := strings.Split(src, "\n")
	if n := len(lines); lines[n-1] == "" {
		lines = lines[:n-1]
	}
	var files []File
	for i := 0; i < len(lines); {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "diff --git "):
			files = append(files, fileFromHeader(line[len("diff --git "):]))
			i++
		case strings.HasPrefix(line, "@@ "):
			if len(files) == 0 {
				return nil, fmt.Errorf("line %d: hunk outside a file", i+1)
			}
			h, next, err := parseHunk(lines, i)
			if err != nil {
				return nil, err
			}
			f := &files[len(files)-1]
			f.Hunks = append(f.Hunks, h)
			i = next
		case line == "GIT binary patch" && len(files) > 0:
			files[len(files)-1].Binary = true
			for i++; i < len(lines) && !strings.HasPrefix(lines[i], "diff --git "); i++ {
			}
		default:
			if len(files) == 0 && strings.HasPrefix(line, "--- ") {
				files = append(files, File{})
			}
			if len(files) > 0 {
				applyHeader(&files[len(files)-1], line)
			}
			i++
		}
	}
	return files, nil
}

// applyHeader updates f from one extended header line. A line it does
// not know is ignored.
func applyHeader(f *File, line string) {
	switch {
	case strings.HasPrefix(line, "old mode "):
		f.OldMode = strings.TrimSpace(line[len("old mode "):])
	case strings.HasPrefix(line, "new mode "):
		f.NewMode = strings.TrimSpace(line[len("new mode "):])
	case strings.HasPrefix(line, "deleted file mode "):
		f.Status, f.NewPath = Deleted, ""
		f.OldMode = strings.TrimSpace(line[len("deleted file mode "):])
	case strings.HasPrefix(line, "new file mode "):
		f.Status, f.OldPath = Created, ""
		f.NewMode = strings.TrimSpace(line[len("new file mode "):])
	case strings.HasPrefix(line, "rename from "):
		f.Status, f.OldPath = Renamed, headerPath(line[len("rename from "):])
	case strings.HasPrefix(line, "rename to "):
		f.Status, f.NewPath = Renamed, headerPath(line[len("rename to "):])
	case strings.HasPrefix(line, "copy from "):
		f.Status, f.OldPath = Copied, headerPath(line[len("copy from "):])
	case strings.HasPrefix(line, "copy to "):
		f.Status, f.NewPath = Copied, headerPath(line[len("copy to "):])
	case strings.HasPrefix(line, "Binary files "):
		f.Binary = true
		if strings.HasPrefix(line, "Binary files "+devNull+" and ") {
			f.Status, f.OldPath = Created, ""
		} else if strings.HasSuffix(line, " and "+devNull+" differ") {
			f.Status, f.NewPath = Deleted, ""
		}
	case strings.HasPrefix(line, "--- "):
		if p, absent := sidePath(line[4:], "a/"); absent {
			if f.Status == Changed {
				f.Status = Created
			}
			f.OldPath = ""
		} else {
			f.OldPath = p
		}
	case strings.HasPrefix(line, "+++ "):
		if p, absent := sidePath(line[4:], "b/"); absent {
			if f.Status == Changed {
				f.Status = Deleted
			}
			f.NewPath = ""
		} else {
			f.NewPath = p
		}
	}
}

// headerPath reads the path on a rename or copy line, which git quotes
// when it needs to.
func headerPath(s string) string {
	if v, _, ok := unquote(s); ok {
		return v
	}
	return s
}

// sidePath reads the path on a --- or +++ line: quoted or bare, with the
// a/ or b/ prefix removed. A bare path may be followed by a tab and a
// timestamp. absent is true for /dev/null.
func sidePath(s, prefix string) (path string, absent bool) {
	if v, _, ok := unquote(s); ok {
		s = v
	} else if i := strings.IndexByte(s, '\t'); i >= 0 {
		s = s[:i]
	}
	if s == devNull {
		return "", true
	}
	return strings.TrimPrefix(s, prefix), false
}

// fileFromHeader starts a File from the paths on a `diff --git` line,
// which the later headers refine.
func fileFromHeader(rest string) File {
	a, b := splitHeader(rest)
	return File{OldPath: a, NewPath: b}
}

// splitHeader splits "a/x b/y" into x and y. Quoted paths are read as
// such. A bare path may hold spaces, as jj writes it: when both halves
// are one path, split in the middle, otherwise at " b/".
func splitHeader(rest string) (string, string) {
	if a, tail, ok := unquote(rest); ok {
		tail = strings.TrimPrefix(tail, " ")
		if b, _, ok := unquote(tail); ok {
			tail = b
		}
		return strings.TrimPrefix(a, "a/"), strings.TrimPrefix(tail, "b/")
	}
	if i := strings.Index(rest, ` "`); i >= 0 && strings.HasSuffix(rest, `"`) {
		if b, _, ok := unquote(rest[i+1:]); ok {
			return strings.TrimPrefix(rest[:i], "a/"), strings.TrimPrefix(b, "b/")
		}
	}
	if n := len(rest); n%2 == 1 && rest[n/2] == ' ' {
		a, b := strings.TrimPrefix(rest[:n/2], "a/"), strings.TrimPrefix(rest[n/2+1:], "b/")
		if a == b {
			return a, b
		}
	}
	if i := strings.Index(rest, " b/"); i >= 0 {
		return strings.TrimPrefix(rest[:i], "a/"), rest[i+3:]
	}
	if i := strings.IndexByte(rest, ' '); i >= 0 {
		return strings.TrimPrefix(rest[:i], "a/"), strings.TrimPrefix(rest[i+1:], "b/")
	}
	return rest, rest
}

// parseHunk reads the hunk whose header is lines[at]. It returns the
// hunk and the index of the line after it. The counts in the header say
// where it ends.
func parseHunk(lines []string, at int) (Hunk, int, error) {
	h, err := parseHunkHeader(lines[at])
	if err != nil {
		return Hunk{}, 0, fmt.Errorf("line %d: %w", at+1, err)
	}
	oldNo, newNo := h.OldStart, h.NewStart
	oldLeft, newLeft := h.OldCount, h.NewCount
	i := at + 1
	for oldLeft > 0 || newLeft > 0 {
		if i >= len(lines) {
			return Hunk{}, 0, fmt.Errorf("line %d: hunk is shorter than its header says", at+1)
		}
		line := lines[i]
		if strings.HasPrefix(line, `\`) {
			markNoEOL(&h)
			i++
			continue
		}
		kind, text := Context, line
		if line != "" {
			switch line[0] {
			case ' ':
				text = line[1:]
			case '+':
				kind, text = Added, line[1:]
			case '-':
				kind, text = Removed, line[1:]
			default:
				return Hunk{}, 0, fmt.Errorf("line %d: unexpected %q in a hunk", i+1, clip(line))
			}
		}
		l := Line{Kind: kind, Text: text}
		if kind != Added {
			l.Old = oldNo
			oldNo++
			oldLeft--
		}
		if kind != Removed {
			l.New = newNo
			newNo++
			newLeft--
		}
		if oldLeft < 0 || newLeft < 0 {
			return Hunk{}, 0, fmt.Errorf("line %d: hunk is longer than its header says", i+1)
		}
		h.Lines = append(h.Lines, l)
		i++
	}
	for i < len(lines) && strings.HasPrefix(lines[i], `\`) {
		markNoEOL(&h)
		i++
	}
	return h, i, nil
}

func markNoEOL(h *Hunk) {
	if n := len(h.Lines); n > 0 {
		h.Lines[n-1].NoEOL = true
	}
}

func clip(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

// parseHunkHeader reads "@@ -a[,b] +c[,d] @@[ section]".
func parseHunkHeader(line string) (Hunk, error) {
	rest := line[len("@@ "):]
	end := strings.Index(rest, " @@")
	if end < 0 {
		return Hunk{}, fmt.Errorf("bad hunk header %q", clip(line))
	}
	parts := strings.Split(rest[:end], " ")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "-") || !strings.HasPrefix(parts[1], "+") {
		return Hunk{}, fmt.Errorf("bad hunk header %q", clip(line))
	}
	var h Hunk
	var ok1, ok2 bool
	h.OldStart, h.OldCount, ok1 = parseRange(parts[0][1:])
	h.NewStart, h.NewCount, ok2 = parseRange(parts[1][1:])
	if !ok1 || !ok2 {
		return Hunk{}, fmt.Errorf("bad hunk header %q", clip(line))
	}
	h.Section = strings.TrimPrefix(rest[end+3:], " ")
	return h, nil
}

// parseRange reads "start" or "start,count"; a missing count is 1.
func parseRange(s string) (start, count int, ok bool) {
	a, b, hasCount := strings.Cut(s, ",")
	start, err := strconv.Atoi(a)
	if err != nil || start < 0 {
		return 0, 0, false
	}
	count = 1
	if hasCount {
		count, err = strconv.Atoi(b)
		if err != nil || count < 0 {
			return 0, 0, false
		}
	}
	return start, count, true
}
