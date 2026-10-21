package diff

import (
	"reflect"
	"strings"
	"testing"
)

func TestWordDiff(t *testing.T) {
	for _, tc := range []struct {
		name, old, new string
		oldSpans       []Span
		newSpans       []Span
		ok             bool
	}{
		{"identical", "foo bar", "foo bar", nil, nil, true},
		{"one word", "let x = 1;", "let y = 1;", []Span{{4, 5}}, []Span{{4, 5}}, true},
		{"insertion", "f(a)", "f(a, b)", nil, []Span{{3, 6}}, true},
		{"words around a kept space", "aa bb cc dd ee", "aa XX YY dd ee", []Span{{3, 5}, {6, 8}}, []Span{{3, 5}, {6, 8}}, true},
		{"unrelated", "abcdefgh", "ijklmnop", nil, nil, false},
		{"unicode", "héllo wörld", "héllo wörld!", nil, []Span{{13, 14}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, n, ok := WordDiff(tc.old, tc.new)
			if ok != tc.ok || !reflect.DeepEqual(o, tc.oldSpans) || !reflect.DeepEqual(n, tc.newSpans) {
				t.Errorf("got %v %v %v, want %v %v %v", o, n, ok, tc.oldSpans, tc.newSpans, tc.ok)
			}
		})
	}
}

func TestWordDiffTooLong(t *testing.T) {
	long := strings.Repeat("a ", 400)
	if _, _, ok := WordDiff(long, long+"b"); ok {
		t.Error("lines this long should not be compared")
	}
}

func TestPairs(t *testing.T) {
	h := Hunk{Lines: []Line{
		{Kind: Context},
		{Kind: Removed}, {Kind: Removed}, {Kind: Removed},
		{Kind: Added}, {Kind: Added},
		{Kind: Context},
		{Kind: Removed},
		{Kind: Context},
		{Kind: Added},
	}}
	want := []Pair{{0, 0}, {1, 4}, {2, 5}, {3, -1}, {6, 6}, {7, -1}, {8, 8}, {-1, 9}}
	if got := Pairs(h); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
	if got := Pairs(Hunk{}); len(got) != 0 {
		t.Errorf("empty hunk gave %v", got)
	}
}

func TestUntracked(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		want          File
	}{
		{"empty", "", File{NewPath: "n", NewMode: "100644", Status: Created}},
		{"binary", "a\x00b", File{NewPath: "n", NewMode: "100644", Status: Created, Binary: true}},
		{"with newline", "a\nb\n", File{NewPath: "n", NewMode: "100644", Status: Created, Hunks: []Hunk{{
			NewStart: 1, NewCount: 2,
			Lines: []Line{{Kind: Added, New: 1, Text: "a"}, {Kind: Added, New: 2, Text: "b"}},
		}}}},
		{"no final newline", "a\r\nb", File{NewPath: "n", NewMode: "100644", Status: Created, Hunks: []Hunk{{
			NewStart: 1, NewCount: 2,
			Lines: []Line{{Kind: Added, New: 1, Text: "a\r"}, {Kind: Added, New: 2, Text: "b", NoEOL: true}},
		}}}},
		{"lone newline", "\n", File{NewPath: "n", NewMode: "100644", Status: Created, Hunks: []Hunk{{
			NewStart: 1, NewCount: 1, Lines: []Line{{Kind: Added, New: 1}},
		}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Untracked("n", []byte(tc.content)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestPath(t *testing.T) {
	if (File{OldPath: "a"}).Path() != "a" || (File{OldPath: "a", NewPath: "b"}).Path() != "b" {
		t.Error("Path picks the wrong name")
	}
}
