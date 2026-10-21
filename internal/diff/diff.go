// Package diff is a diff as data: the files a change touched, their hunks
// and lines, read from git's unified format, which both git and jj write.
// Everything here is pure.
package diff

// Kind says which side of the diff a line belongs to.
type Kind uint8

const (
	Context Kind = iota // on both sides
	Added               // only on the new side
	Removed             // only on the old side
)

// Line is one line of a hunk. Old and New are its line numbers on each
// side, counting from 1, and 0 on the side it is not on. Text has no line
// ending. NoEOL says the file ends on this line without a newline.
type Line struct {
	Kind     Kind
	Old, New int
	Text     string
	NoEOL    bool
}

// Hunk is one @@ block. Section is the text after the second @@, often
// the enclosing function, without the leading space.
type Hunk struct {
	OldStart, OldCount int
	NewStart, NewCount int
	Section            string
	Lines              []Line
}

// Status is what happened to a file.
type Status uint8

const (
	Changed Status = iota
	Created
	Deleted
	Renamed
	Copied
)

// File is one file's part of a diff. OldPath is empty for an added file
// and NewPath for a deleted one. A binary file has no hunks. The modes
// are git's octal strings, empty when the diff did not give them.
type File struct {
	OldPath, NewPath string
	Status           Status
	Binary           bool
	OldMode, NewMode string
	Hunks            []Hunk
}

// Path is the name to show a file by: its new path, or its old one when
// it was deleted.
func (f File) Path() string {
	if f.NewPath == "" {
		return f.OldPath
	}
	return f.NewPath
}
