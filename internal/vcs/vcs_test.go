package vcs

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"overview/internal/diff"
)

// call is one command a fake Runner saw.
type call struct {
	Dir  string
	Args []string
}

// fake answers each command by looking its argv up in replies, joined by
// a space, and records every call.
type fake struct {
	calls   []call
	replies map[string]string
	err     error
}

func (f *fake) run(dir string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, call{dir, args})
	if f.err != nil {
		return nil, f.err
	}
	for prefix, reply := range f.replies {
		if strings.HasPrefix(strings.Join(args, " "), prefix) {
			return []byte(reply), nil
		}
	}
	return nil, nil
}

func (f *fake) argv() [][]string {
	var out [][]string
	for _, c := range f.calls {
		out = append(out, c.Args)
	}
	return out
}

func TestFind(t *testing.T) {
	has := func(paths ...string) func(string) bool {
		return func(p string) bool {
			for _, q := range paths {
				if p == q {
					return true
				}
			}
			return false
		}
	}
	for _, tc := range []struct {
		name   string
		dir    string
		exists func(string) bool
		want   Repo
		ok     bool
	}{
		{"in the root", "/r", has("/r/.git"), Repo{"/r", Git}, true},
		{"below the root", "/r/a/b", has("/r/.jj"), Repo{"/r", JJ}, true},
		{"jj wins at one level", "/r/a", has("/r/.git", "/r/.jj"), Repo{"/r", JJ}, true},
		{"nearest wins", "/r/a", has("/r/.jj", "/r/a/.git"), Repo{"/r/a", Git}, true},
		{"trailing slash", "/r/a/", has("/r/.git"), Repo{"/r", Git}, true},
		{"none", "/r/a", has(), Repo{}, false},
		{"filesystem root", "/", has(), Repo{}, false},
		{"empty", "", has(), Repo{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Find(tc.dir, tc.exists)
			if ok != tc.ok || got != tc.want {
				t.Errorf("got %v %v, want %v %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestCommands(t *testing.T) {
	jj := Repo{"/r", JJ}
	git := Repo{"/g", Git}
	noRead := func(string) (Untracked, error) { return Untracked{}, errors.New("unexpected read") }

	f := &fake{}
	if _, err := jj.Diff(f.run, noRead, "abc"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"jj", "--no-pager", "--color=never", "diff", "--git", "--from", "abc"}}
	if !reflect.DeepEqual(f.argv(), want) || f.calls[0].Dir != "/r" {
		t.Errorf("jj diff ran %v in %q", f.argv(), f.calls[0].Dir)
	}

	f = &fake{}
	if _, err := git.Diff(f.run, noRead, "abc"); err != nil {
		t.Fatal(err)
	}
	want = [][]string{
		{"git", "-c", "core.quotepath=false", "-c", "color.ui=never", "-c", "color.diff=never", "--no-pager", "diff", "--no-color", "--no-ext-diff", "--no-textconv", "-M", "--src-prefix=a/", "--dst-prefix=b/", "abc", "--"},
		{"git", "ls-files", "--others", "--exclude-standard", "-z"},
	}
	if !reflect.DeepEqual(f.argv(), want) || f.calls[1].Dir != "/g" {
		t.Errorf("git diff ran %v", f.argv())
	}

	f = &fake{replies: map[string]string{"jj": "abc\n"}}
	if id, err := jj.Uncommitted(f.run); err != nil || id != "abc" {
		t.Errorf("jj Uncommitted = %q %v", id, err)
	}
	want = [][]string{{"jj", "--no-pager", "--ignore-working-copy", "log", "--no-graph", "-r", "@-", "-T", `commit_id ++ "\n"`}}
	if !reflect.DeepEqual(f.argv(), want) {
		t.Errorf("jj Uncommitted ran %v", f.argv())
	}

	f = &fake{replies: map[string]string{"git": "def\n"}}
	if id, err := git.Uncommitted(f.run); err != nil || id != "def" {
		t.Errorf("git Uncommitted = %q %v", id, err)
	}
	if id, err := git.Resolve(f.run, "main"); err != nil || id != "def" {
		t.Errorf("git Resolve = %q %v", id, err)
	}
	want = [][]string{{"git", "rev-parse", "--verify", "HEAD^{commit}"}, {"git", "rev-parse", "--verify", "main^{commit}"}}
	if !reflect.DeepEqual(f.argv(), want) {
		t.Errorf("git resolve ran %v", f.argv())
	}
}

func TestResolveRefuses(t *testing.T) {
	f := &fake{replies: map[string]string{"jj": "a\nb\n"}}
	if _, err := (Repo{"/r", JJ}).Uncommitted(f.run); err == nil {
		t.Error("a merge should be an error")
	}
	f = &fake{replies: map[string]string{"jj": "\n"}}
	if _, err := (Repo{"/r", JJ}).Resolve(f.run, "x"); err == nil {
		t.Error("no commit should be an error")
	}
	for _, rev := range []string{"", "--output=x", "-r"} {
		for _, k := range []Kind{Git, JJ} {
			f = &fake{}
			if _, err := (Repo{"/r", k}).Resolve(f.run, rev); err == nil || len(f.calls) != 0 {
				t.Errorf("%v accepted revision %q", k, rev)
			}
			if _, err := (Repo{"/r", k}).Diff(f.run, nil, rev); err == nil || len(f.calls) != 0 {
				t.Errorf("%v diffed from revision %q", k, rev)
			}
		}
	}
	f = &fake{err: errors.New("boom")}
	if _, err := (Repo{"/r", Git}).Resolve(f.run, "x"); err == nil {
		t.Error("runner errors must come through")
	}
}

func TestDiffUntracked(t *testing.T) {
	big := make([]byte, maxUntracked+1)
	files := map[string]Untracked{
		"/g/z.txt":     {Content: []byte("z\n")},
		"/g/dir/a.txt": {Content: []byte("a")},
		"/g/big":       {Content: big},
		"/g/bin":       {Content: []byte{0, 1}},
		"/g/link":      {Content: []byte("dir/a.txt"), Symlink: true},
		"/g/huge":      {Oversize: true},
	}
	read := func(p string) (Untracked, error) {
		if c, ok := files[p]; ok {
			return c, nil
		}
		return Untracked{}, os.ErrNotExist
	}
	tracked := "diff --git a/m b/m\n--- a/m\n+++ b/m\n@@ -1 +1 @@\n-a\n+b\n"
	f := &fake{replies: map[string]string{
		"git -c":       tracked,
		"git ls-files": "z.txt\x00dir/a.txt\x00big\x00bin\x00link\x00huge\x00gone\x00\x00",
	}}
	got, err := (Repo{"/g", Git}).Diff(f.run, read, "abc")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, file := range got {
		paths = append(paths, file.Path())
	}
	if want := []string{"big", "bin", "dir/a.txt", "huge", "link", "m", "z.txt"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for _, file := range got {
		switch file.Path() {
		case "link":
			if file.NewMode != "120000" || file.Status != diff.Created || file.Hunks[0].Lines[0].Text != "dir/a.txt" || !file.Hunks[0].Lines[0].NoEOL {
				t.Errorf("link = %+v", file)
			}
		case "big", "bin", "huge":
			if !file.Binary || file.Status != diff.Created || len(file.Hunks) != 0 {
				t.Errorf("%s = %+v", file.Path(), file)
			}
		case "dir/a.txt":
			if !file.Hunks[0].Lines[0].NoEOL {
				t.Errorf("dir/a.txt = %+v", file)
			}
		}
	}
}

func TestBaseAtCommands(t *testing.T) {
	at := time.Unix(1000, 0)
	f := &fake{replies: map[string]string{
		"jj --no-pager --ignore-working-copy op":      "op3 1500\nop2 900\nop1 100\n",
		"jj --no-pager --ignore-working-copy --at-op": "commit\n",
	}}
	id, err := (Repo{"/r", JJ}).BaseAt(f.run, at)
	if err != nil || id != "commit" {
		t.Fatalf("BaseAt = %q %v", id, err)
	}
	want := [][]string{
		{"jj", "--no-pager", "--ignore-working-copy", "op", "log", "--no-graph", "-n", "20000", "-T", `id ++ " " ++ time.end().format("%s") ++ "\n"`},
		{"jj", "--no-pager", "--ignore-working-copy", "--at-op", "op2", "log", "--no-graph", "-r", "@", "-T", `commit_id ++ "\n"`},
	}
	if !reflect.DeepEqual(f.argv(), want) {
		t.Errorf("ran %v", f.argv())
	}

	f = &fake{replies: map[string]string{"git": "aaa HEAD@{1500}\nbbb HEAD@{900}\nccc HEAD@{100}\n"}}
	id, err = (Repo{"/g", Git}).BaseAt(f.run, at)
	if err != nil || id != "bbb" {
		t.Fatalf("git BaseAt = %q %v", id, err)
	}
	want = [][]string{{"git", "reflog", "show", "--date=unix", "--format=%H %gd %gs", "HEAD"}}
	if !reflect.DeepEqual(f.argv(), want) {
		t.Errorf("ran %v", f.argv())
	}
	if _, err := (Repo{"/g", Git}).BaseAt(f.run, time.Unix(50, 0)); err == nil {
		t.Error("a time before the reflog should be an error")
	}
	f = &fake{replies: map[string]string{
		"git reflog":      "aaa HEAD@{1500} commit: more\nbbb HEAD@{900} commit (initial): first\n",
		"git hash-object": "empty\n",
	}}
	if id, err := (Repo{"/g", Git}).BaseAt(f.run, time.Unix(50, 0)); err != nil || id != "empty" {
		t.Errorf("a repository born after t should diff from the empty tree, got %q %v", id, err)
	}
	f = &fake{}
	if _, err := (Repo{"/r", JJ}).BaseAt(f.run, at); err == nil {
		t.Error("an empty op log should be an error")
	}
}

func TestExec(t *testing.T) {
	dir := t.TempDir()
	out, err := Exec(dir, "pwd")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	want, _ := filepath.EvalSymlinks(dir)
	if got != want {
		t.Errorf("ran in %q, want %q", got, want)
	}
	if _, err := Exec(dir, "sh", "-c", "echo oops >&2; exit 3"); err == nil || !strings.Contains(err.Error(), "oops") {
		t.Errorf("error should carry stderr: %v", err)
	}
	if _, err := Exec(dir); err == nil {
		t.Error("no command should be an error")
	}
}

// realRepo makes a repository in a temp dir, runs setup commands in it
// and returns a Runner that adds the test identity to every command.
func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRun(t *testing.T, run Runner, dir string, args ...string) string {
	t.Helper()
	out, err := run(dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func needs(t *testing.T, prog string) {
	t.Helper()
	if _, err := exec.LookPath(prog); err != nil {
		t.Skipf("%s is not installed", prog)
	}
}

// checkDiff verifies a diff of a change with a rename, an untracked file
// and a deletion.
func checkDiff(t *testing.T, files []diff.File) {
	t.Helper()
	got := map[string]diff.File{}
	for _, f := range files {
		got[f.Path()] = f
	}
	if len(got) != 3 {
		t.Fatalf("want 3 files, got %+v", files)
	}
	if f := got["renamed.txt"]; f.Status != diff.Renamed || f.OldPath != "keep.txt" || len(f.Hunks) != 1 {
		t.Errorf("renamed.txt = %+v", f)
	}
	if f := got["gone.txt"]; f.Status != diff.Deleted || len(f.Hunks) != 1 {
		t.Errorf("gone.txt = %+v", f)
	}
	if f := got["new.txt"]; f.Status != diff.Created || len(f.Hunks) != 1 || f.Hunks[0].Lines[0].Text != "fresh" {
		t.Errorf("new.txt = %+v", f)
	}
	for i := 1; i < len(files); i++ {
		if files[i-1].Path() > files[i].Path() {
			t.Errorf("not sorted: %s before %s", files[i-1].Path(), files[i].Path())
		}
	}
}

const tenLines = "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"

func TestRealGit(t *testing.T) {
	needs(t, "git")
	dir := t.TempDir()
	run := func(dir string, args ...string) ([]byte, error) {
		if args[0] == "git" {
			args = append([]string{"git", "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args[1:]...)
		}
		return Exec(dir, args...)
	}
	mustRun(t, run, dir, "git", "init", "-q")
	write(t, dir, "keep.txt", tenLines)
	write(t, dir, "gone.txt", "bye\n")
	mustRun(t, run, dir, "git", "add", "-A")
	mustRun(t, run, dir, "git", "commit", "-qm", "base")
	repo, ok := Find(dir, func(p string) bool { _, err := os.Stat(p); return err == nil })
	if !ok || repo.Kind != Git {
		t.Fatalf("Find = %v %v", repo, ok)
	}
	head, err := repo.Uncommitted(run)
	if err != nil {
		t.Fatal(err)
	}
	if base, err := repo.BaseAt(run, time.Now()); err != nil || base != head {
		t.Errorf("BaseAt(now) = %q %v, want %q", base, err, head)
	}

	mustRun(t, run, dir, "git", "mv", "keep.txt", "renamed.txt")
	write(t, dir, "renamed.txt", strings.Replace(tenLines, "5\n", "five\n", 1))
	mustRun(t, run, dir, "git", "rm", "-q", "gone.txt")
	write(t, dir, "new.txt", "fresh\n")
	files, err := repo.Diff(run, ReadUntracked, head)
	if err != nil {
		t.Fatal(err)
	}
	checkDiff(t, files)
	if id, err := repo.Resolve(run, "HEAD"); err != nil || id != head {
		t.Errorf("Resolve = %q %v", id, err)
	}
}

func TestRealJJ(t *testing.T) {
	needs(t, "jj")
	dir := t.TempDir()
	run := func(dir string, args ...string) ([]byte, error) {
		if args[0] == "jj" {
			args = append([]string{"jj", "--config", "user.name=t", "--config", "user.email=t@t"}, args[1:]...)
		}
		return Exec(dir, args...)
	}
	mustRun(t, run, dir, "jj", "git", "init")
	write(t, dir, "keep.txt", tenLines)
	write(t, dir, "gone.txt", "bye\n")
	mustRun(t, run, dir, "jj", "--no-pager", "new")
	repo, ok := Find(dir, func(p string) bool { _, err := os.Stat(p); return err == nil })
	if !ok || repo.Kind != JJ {
		t.Fatalf("Find = %v %v", repo, ok)
	}
	base, err := repo.Uncommitted(run)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimSpace(mustRun(t, run, dir, "jj", "--no-pager", "log", "--no-graph", "-r", "@-", "-T", `commit_id ++ "\n"`)); base != want {
		t.Errorf("Uncommitted = %q, want %q", base, want)
	}
	cur := strings.TrimSpace(mustRun(t, run, dir, "jj", "--no-pager", "log", "--no-graph", "-r", "@", "-T", `commit_id ++ "\n"`))
	if got, err := repo.BaseAt(run, time.Now()); err != nil || got != cur {
		t.Errorf("BaseAt(now) = %q %v, want %q", got, err, cur)
	}

	if err := os.Rename(filepath.Join(dir, "keep.txt"), filepath.Join(dir, "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "renamed.txt", strings.Replace(tenLines, "5\n", "five\n", 1))
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "new.txt", "fresh\n")
	files, err := repo.Diff(run, ReadUntracked, base)
	if err != nil {
		t.Fatal(err)
	}
	checkDiff(t, files)

	// A time before every operation falls back to the oldest one.
	if _, err := repo.BaseAt(run, time.Unix(1, 0)); err != nil {
		t.Errorf("BaseAt(1970) = %v", err)
	}
}

func TestBaseAtDrift(t *testing.T) {
	at := time.Unix(1000, 0)
	reply := map[string]string{"jj --no-pager --ignore-working-copy --at-op": "commit\n"}

	// A short log whose oldest op is newer than t: the repo is younger
	// than t, so the oldest op is right.
	reply["jj --no-pager --ignore-working-copy op"] = "op2 1500\nop1 1200\n"
	f := &fake{replies: reply}
	if id, err := (Repo{"/r", JJ}).BaseAt(f.run, at); err != nil || id != "commit" {
		t.Errorf("short log: BaseAt = %q %v", id, err)
	}

	// A log cut at the limit with nothing old enough: refuse.
	var b strings.Builder
	for i := 0; i < opLogLimit; i++ {
		fmt.Fprintf(&b, "op%d %d\n", i, 5000-i/10)
	}
	reply["jj --no-pager --ignore-working-copy op"] = b.String()
	f = &fake{replies: reply}
	_, err := (Repo{"/r", JJ}).BaseAt(f.run, at)
	if err == nil || !strings.Contains(err.Error(), "does not reach back") {
		t.Errorf("full log: err = %v", err)
	}
	for _, c := range f.argv() {
		if strings.Contains(strings.Join(c, " "), "--at-op") {
			t.Errorf("resolved a base despite the error: %v", c)
		}
	}
}

func TestReadUntracked(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "plain.txt", "hi\n")
	if err := os.Symlink("plain.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/zero", filepath.Join(dir, "zero")); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("fifo", filepath.Join(dir, "fifolink")); err != nil {
		t.Fatal(err)
	}
	huge := filepath.Join(dir, "huge")
	h, err := os.Create(huge)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Truncate(5 << 30); err != nil {
		t.Skipf("no sparse files: %v", err)
	}
	h.Close()
	write(t, dir, "edge.txt", strings.Repeat("x", maxUntracked))

	cases := []struct {
		name     string
		want     Untracked
		wantFail bool
	}{
		{"plain.txt", Untracked{Content: []byte("hi\n")}, false},
		{"link", Untracked{Content: []byte("plain.txt"), Symlink: true}, false},
		{"zero", Untracked{Content: []byte("/dev/zero"), Symlink: true}, false},
		{"fifolink", Untracked{Content: []byte("fifo"), Symlink: true}, false},
		{"fifo", Untracked{}, true},
		{"huge", Untracked{Oversize: true}, false},
		{"missing", Untracked{}, true},
		{"edge.txt", Untracked{Content: []byte(strings.Repeat("x", maxUntracked))}, false},
	}
	for _, c := range cases {
		type result struct {
			u   Untracked
			err error
		}
		ch := make(chan result, 1)
		go func() {
			u, err := ReadUntracked(filepath.Join(dir, c.name))
			ch <- result{u, err}
		}()
		select {
		case r := <-ch:
			if (r.err != nil) != c.wantFail || !reflect.DeepEqual(r.u, c.want) {
				t.Errorf("%s = %+v, %v; want %+v, fail=%v", c.name, r.u, r.err, c.want, c.wantFail)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: ReadUntracked blocked", c.name)
		}
	}
}

func TestRealGitColorAlways(t *testing.T) {
	needs(t, "git")
	dir := t.TempDir()
	mustRun(t, Exec, dir, "git", "init", "-q")
	mustRun(t, Exec, dir, "git", "config", "color.diff", "always")
	mustRun(t, Exec, dir, "git", "config", "color.ui", "always")
	write(t, dir, "a.txt", "one\n")
	mustRun(t, Exec, dir, "git", "add", "-A")
	mustRun(t, Exec, dir, "git", "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	head, err := (Repo{dir, Git}).Uncommitted(Exec)
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "a.txt", "two\n")
	files, err := (Repo{dir, Git}).Diff(Exec, ReadUntracked, head)
	if err != nil || len(files) != 1 || files[0].Path() != "a.txt" || len(files[0].Hunks) != 1 {
		t.Errorf("Diff = %+v, %v", files, err)
	}
}

// A failed read says why in a few words, never with git's whole message.
func TestShort(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{errors.New("git rev-parse --verify: exit status 128: fatal: not a git repository (or any of the parent directories): .git"), "not a git repository"},
		{errors.New("jj --no-pager --ignore-working-copy: exit status 1: Error: There is no jj repo in \".\""), "not a jj repository"},
		{errors.New("git rev-parse --verify: exit status 128: fatal: Needed a single revision"), "no commits yet"},
		{fmt.Errorf("git: %w", exec.ErrNotFound), "git or jj is not installed"},
		{errors.New("first line\nsecond"), "first line"},
		{nil, ""},
	} {
		if got := Short(c.err); got != c.want {
			t.Errorf("Short(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}
