package vcs

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func TestParseOpLog(t *testing.T) {
	got := ParseOpLog("abc 1700\n\nbad line\nxyz notanumber\n def 20 \n")
	if len(got) != 2 || got[0].ID != "abc" || got[0].End != time.Unix(1700, 0) || got[1].ID != "def" {
		t.Errorf("got %+v", got)
	}
	if len(ParseOpLog("")) != 0 {
		t.Error("empty log gave ops")
	}
}

func TestParseReflog(t *testing.T) {
	got := ParseReflog("aaa HEAD@{1792634408}\nbbb HEAD@{5}\nnope\nccc HEAD@{x}\nddd HEAD@{7\n")
	if len(got) != 2 || got[0].SHA != "aaa" || got[0].Time != time.Unix(1792634408, 0) || got[1].SHA != "bbb" {
		t.Errorf("got %+v", got)
	}
}

func TestParsersNeverPanicProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.String().Draw(t, "s")
		ParseOpLog(s)
		ParseReflog(s)
		r := rapid.SliceOf(rapid.SampledFrom([]string{"a", " ", "\n", "HEAD@{", "}", "12", "-"})).Draw(t, "pieces")
		ParseOpLog(strings.Join(r, ""))
		ParseReflog(strings.Join(r, ""))
	})
}

func TestOpAtProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Newest first: times descend.
		gaps := rapid.SliceOfN(rapid.IntRange(0, 100), 0, 12).Draw(t, "gaps")
		var ops []Op
		ts := int64(10000)
		for i, g := range gaps {
			ts -= int64(g)
			ops = append(ops, Op{fmt.Sprint(i), time.Unix(ts, 0)})
		}
		at := time.Unix(rapid.Int64Range(9000, 10100).Draw(t, "at"), 0)
		i, ok := OpIndexAt(ops, at)
		if ok != (len(ops) > 0) {
			t.Fatalf("ok = %v for %d ops", ok, len(ops))
		}
		if !ok {
			return
		}
		if ops[i].End.After(at) && i != len(ops)-1 {
			t.Fatalf("chose %v after %v and not the oldest", ops[i], at)
		}
		for _, o := range ops[:i] {
			if !o.End.After(at) {
				t.Fatalf("skipped %v, not after %v", o, at)
			}
		}
	})
}

func TestFindProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		segs := rapid.SliceOfN(rapid.SampledFrom([]string{"a", "b", "c"}), 0, 5).Draw(t, "segs")
		dir := string(filepath.Separator) + filepath.Join(segs...)
		marks := map[string]bool{}
		for i := range segs {
			p := string(filepath.Separator) + filepath.Join(segs[:i]...)
			if rapid.Bool().Draw(t, "git") {
				marks[filepath.Join(p, ".git")] = true
			}
			if rapid.Bool().Draw(t, "jj") {
				marks[filepath.Join(p, ".jj")] = true
			}
		}
		repo, ok := Find(dir, func(p string) bool { return marks[p] })
		if !ok {
			if len(marks) != 0 && anyAbove(marks, dir) {
				t.Fatalf("missed a repository above %s", dir)
			}
			return
		}
		if !marks[filepath.Join(repo.Root, ".jj")] && !marks[filepath.Join(repo.Root, ".git")] {
			t.Fatalf("%s is no repository", repo.Root)
		}
		if repo.Kind == Git && marks[filepath.Join(repo.Root, ".jj")] {
			t.Fatal("git chosen over jj at one level")
		}
		for d := dir; d != repo.Root; d = filepath.Dir(d) {
			if marks[filepath.Join(d, ".jj")] || marks[filepath.Join(d, ".git")] {
				t.Fatalf("%s is nearer than %s", d, repo.Root)
			}
		}
	})
}

func anyAbove(marks map[string]bool, dir string) bool {
	for d := dir; ; d = filepath.Dir(d) {
		if marks[filepath.Join(d, ".git")] || marks[filepath.Join(d, ".jj")] {
			return true
		}
		if filepath.Dir(d) == d {
			return false
		}
	}
}
