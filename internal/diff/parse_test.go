package diff

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// Real output of `jj diff --git`, from jj 0.46.
const jjFixture = `diff --git a/b.bin b/b.bin
index 88768efdf7..3e3315e1b0 100644
Binary files a/b.bin and b/b.bin differ
diff --git a/f.txt b/f.txt
index de980441c3..f8f7a32fcd 100644
--- a/f.txt
+++ b/f.txt
@@ -1,3 +1,4 @@
 a
-b
+B
 c
+d
\ No newline at end of file
diff --git a/old.txt b/new.txt
rename from old.txt
rename to new.txt
diff --git a/sp ace.txt b/sp ace.txt
new file mode 100644
index 0000000000..8cc35a3d55
--- /dev/null
+++ b/sp ace.txt
@@ -0,0 +1,1 @@
+tab
`

func TestParseJJ(t *testing.T) {
	got, err := Parse(jjFixture)
	if err != nil {
		t.Fatal(err)
	}
	want := []File{
		{OldPath: "b.bin", NewPath: "b.bin", Binary: true},
		{OldPath: "f.txt", NewPath: "f.txt", Hunks: []Hunk{{
			OldStart: 1, OldCount: 3, NewStart: 1, NewCount: 4,
			Lines: []Line{
				{Kind: Context, Old: 1, New: 1, Text: "a"},
				{Kind: Removed, Old: 2, Text: "b"},
				{Kind: Added, New: 2, Text: "B"},
				{Kind: Context, Old: 3, New: 3, Text: "c"},
				{Kind: Added, New: 4, Text: "d", NoEOL: true},
			},
		}}},
		{OldPath: "old.txt", NewPath: "new.txt", Status: Renamed},
		{NewPath: "sp ace.txt", Status: Created, NewMode: "100644", Hunks: []Hunk{{
			OldStart: 0, OldCount: 0, NewStart: 1, NewCount: 1,
			Lines: []Line{{Kind: Added, New: 1, Text: "tab"}},
		}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestParseGitMixed(t *testing.T) {
	src, err := os.ReadFile("testdata/git_mixed.diff")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(string(src))
	if err != nil {
		t.Fatal(err)
	}
	type summary struct {
		Old, New string
		Status   Status
		OldMode  string
		NewMode  string
		Hunks    int
	}
	var sums []summary
	for _, f := range got {
		sums = append(sums, summary{f.OldPath, f.NewPath, f.Status, f.OldMode, f.NewMode, len(f.Hunks)})
	}
	want := []summary{
		{"gone.txt", "", Deleted, "100644", "", 1},
		{"mode.sh", "mode.sh", Changed, "100644", "100755", 0},
		{"noeol.txt", "noeol.txt", Changed, "", "", 1},
		{"rename_me.txt", "renamed.txt", Renamed, "", "", 1},
		{"ä x.txt", "ä x.txt", Changed, "", "", 1},
	}
	if !reflect.DeepEqual(sums, want) {
		t.Errorf("got %+v\nwant %+v", sums, want)
	}
	noeol := got[2].Hunks[0].Lines
	if !noeol[1].NoEOL || !noeol[2].NoEOL || noeol[0].NoEOL {
		t.Errorf("NoEOL flags wrong: %+v", noeol)
	}
	if h := got[3].Hunks[0]; h.OldStart != 12 || h.Lines[3].Old != 15 || h.Lines[4].New != 15 {
		t.Errorf("renamed hunk wrong: %+v", h)
	}
}

func TestParseGitCRLFAndSection(t *testing.T) {
	src, err := os.ReadFile("testdata/git_crlf_section.diff")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(string(src))
	if err != nil {
		t.Fatal(err)
	}
	h := got[0].Hunks[0]
	if h.Section != "int main() {" {
		t.Errorf("section = %q", h.Section)
	}
	if h.Lines[0].Text != "int other() {\r" {
		t.Errorf("CR lost: %q", h.Lines[0].Text)
	}
	if last := h.Lines[len(h.Lines)-1]; last.Kind != Added || last.Text != "-- x\r" {
		t.Errorf("last line = %+v", last)
	}
}

func TestParseCases(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		want      []File
	}{
		{"empty", "", nil},
		{"preamble only", "hello\nworld\n", nil},
		{
			"quoted unicode path",
			"diff --git \"a/\\303\\244 x\\t\\\"q\\\".txt\" \"b/\\303\\244 x\\t\\\"q\\\".txt\"\nold mode 100644\nnew mode 100755\n",
			[]File{{OldPath: "ä x\t\"q\".txt", NewPath: "ä x\t\"q\".txt", OldMode: "100644", NewMode: "100755"}},
		},
		{
			"unquoted unicode as jj writes it",
			"diff --git a/ä b.txt b/ä b.txt\nGIT binary patch\nliteral 3\nabc\n\n",
			[]File{{OldPath: "ä b.txt", NewPath: "ä b.txt", Binary: true}},
		},
		{
			"rename with spaces uses the rename lines",
			"diff --git a/o ld b/n ew\nsimilarity index 100%\nrename from o ld\nrename to n ew\n",
			[]File{{OldPath: "o ld", NewPath: "n ew", Status: Renamed}},
		},
		{
			"different halves with spaces split at b/",
			"diff --git a/x y b/z w\nBinary files a/x y and b/z w differ\n",
			[]File{{OldPath: "x y", NewPath: "z w", Binary: true}},
		},
		{
			"copy",
			"diff --git a/a b/b\nsimilarity index 100%\ncopy from a\ncopy to b\n",
			[]File{{OldPath: "a", NewPath: "b", Status: Copied}},
		},
		{
			"binary deleted",
			"diff --git a/x b/x\ndeleted file mode 100644\nindex 1..0\nBinary files a/x and /dev/null differ\n",
			[]File{{OldPath: "x", Status: Deleted, Binary: true, OldMode: "100644"}},
		},
		{
			"binary created without a mode",
			"diff --git a/x b/x\nBinary files /dev/null and b/x differ\n",
			[]File{{NewPath: "x", Status: Created, Binary: true}},
		},
		{
			"removed line that looks like a header",
			"diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1,2 +1 @@\n-- x\n--- y\n+z\n",
			[]File{{OldPath: "f", NewPath: "f", Hunks: []Hunk{{
				OldStart: 1, OldCount: 2, NewStart: 1, NewCount: 1,
				Lines: []Line{
					{Kind: Removed, Old: 1, Text: "- x"},
					{Kind: Removed, Old: 2, Text: "-- y"},
					{Kind: Added, New: 1, Text: "z"},
				},
			}}}},
		},
		{
			"bare empty line is empty context",
			"diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1,3 +1,3 @@ sec\n a\n\n-c\n+d\n",
			[]File{{OldPath: "f", NewPath: "f", Hunks: []Hunk{{
				OldStart: 1, OldCount: 3, NewStart: 1, NewCount: 3, Section: "sec",
				Lines: []Line{
					{Kind: Context, Old: 1, New: 1, Text: "a"},
					{Kind: Context, Old: 2, New: 2, Text: ""},
					{Kind: Removed, Old: 3, Text: "c"},
					{Kind: Added, New: 3, Text: "d"},
				},
			}}}},
		},
		{
			"two hunks",
			"diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1 +1 @@\n-a\n+b\n@@ -9 +9 @@\n-c\n+d\n",
			nil,
		},
		{
			"plain unified diff without a git header",
			"--- a/f\t2020-01-01\n+++ b/f\t2020-01-02\n@@ -1 +1 @@\n-a\n+b\n",
			[]File{{OldPath: "f", NewPath: "f", Hunks: []Hunk{{
				OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 1,
				Lines: []Line{{Kind: Removed, Old: 1, Text: "a"}, {Kind: Added, New: 1, Text: "b"}},
			}}}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.src)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == nil && tc.name == "two hunks" {
				if len(got) != 1 || len(got[0].Hunks) != 2 || got[0].Hunks[1].Lines[0].Old != 9 {
					t.Errorf("got %+v", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	head := "diff --git a/f b/f\n--- a/f\n+++ b/f\n"
	for name, src := range map[string]string{
		"short at EOF":       head + "@@ -1,3 +1,3 @@\n a\n",
		"short before file":  head + "@@ -1,3 +1,3 @@\n a\ndiff --git a/g b/g\n",
		"too many removals":  head + "@@ -1 +1 @@\n-a\n-b\n+c\n",
		"bad header":         head + "@@ -x +1 @@\n",
		"no closing marks":   head + "@@ -1 +1\n",
		"hunk without file":  "@@ -1 +1 @@\n-a\n+b\n",
		"junk inside a hunk": head + "@@ -1 +1 @@\nxa\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(src); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestFormatQuotes(t *testing.T) {
	got := Format([]File{{OldPath: "ä\"x", NewPath: "ä\"x", Status: Changed, Binary: true}})
	if !strings.Contains(got, `diff --git "a/\303\244\"x" "b/\303\244\"x"`) {
		t.Errorf("header not quoted: %s", got)
	}
}
