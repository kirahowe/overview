// Package vcs finds a directory's repository and gets a diff from git or
// jj. Every program runs through a Runner, so the commands are built by
// pure functions and the effects stay at the edge.
package vcs

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"overview/internal/diff"
)

// Runner runs a program (args[0]) in dir and returns its standard
// output. An error includes the standard error.
type Runner func(dir string, args ...string) ([]byte, error)

// Exec is the real Runner.
func Exec(dir string, args ...string) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("no command to run")
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", strings.Join(args[:min(len(args), 3)], " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// Kind says which program a repository belongs to.
type Kind uint8

const (
	Git Kind = iota
	JJ
)

func (k Kind) String() string {
	if k == JJ {
		return "jj"
	}
	return "git"
}

// Repo is a repository: the directory that holds .jj or .git.
type Repo struct {
	Root string
	Kind Kind
}

// Find walks up from dir to the nearest directory that holds .jj or
// .git. At one level .jj wins, since a colocated repository has both.
func Find(dir string, exists func(path string) bool) (Repo, bool) {
	for d := filepath.Clean(dir); ; {
		if exists(filepath.Join(d, ".jj")) {
			return Repo{d, JJ}, true
		}
		if exists(filepath.Join(d, ".git")) {
			return Repo{d, Git}, true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return Repo{}, false
		}
		d = parent
	}
}

// maxUntracked is the size past which an untracked file is shown as
// binary rather than as lines.
const maxUntracked = 1 << 20

// symlinkMode is the mode git records for a symbolic link.
const symlinkMode = "120000"

// Untracked is what reading one untracked path gave: a regular file's
// content, a symlink's target, or a regular file too big to show.
type Untracked struct {
	Content  []byte // file content, or the link target for a symlink
	Symlink  bool   // Content is a link target, shown with mode 120000
	Oversize bool   // a regular file past the cap; Content is empty
}

// ReadUntracked is the real reader for Diff. It never follows a symlink
// and never opens anything but a regular file: a symlink gives its target,
// a FIFO, device, socket or directory is an error (so skipped), a regular
// file over the cap is Oversize without being read, and any other regular
// file is read through a limit so one that grew is still bounded.
func ReadUntracked(path string) (Untracked, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Untracked{}, err
	}
	switch mode := info.Mode(); {
	case mode&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return Untracked{}, err
		}
		return Untracked{Content: []byte(target), Symlink: true}, nil
	case !mode.IsRegular():
		return Untracked{}, fmt.Errorf("%s is not a regular file or symlink", path)
	case info.Size() > maxUntracked:
		return Untracked{Oversize: true}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return Untracked{}, err
	}
	defer f.Close()
	content, err := io.ReadAll(io.LimitReader(f, maxUntracked+1))
	if err != nil {
		return Untracked{}, err
	}
	if len(content) > maxUntracked {
		return Untracked{Oversize: true}, nil
	}
	return Untracked{Content: content}, nil
}

// untrackedFile turns one read into its diff.
func untrackedFile(name string, u Untracked) diff.File {
	if u.Oversize || len(u.Content) > maxUntracked {
		return diff.File{NewPath: name, NewMode: "100644", Status: diff.Created, Binary: true}
	}
	f := diff.Untracked(name, u.Content)
	if u.Symlink {
		f.NewMode = symlinkMode
	}
	return f
}

// Diff returns what changed in the working copy since base, a commit id.
// For git, untracked files count as added, read through read (use
// ReadUntracked); a read that fails skips the file.
func (r Repo) Diff(run Runner, read func(path string) (Untracked, error), base string) ([]diff.File, error) {
	if err := checkRev(base); err != nil {
		return nil, err
	}
	if r.Kind == JJ {
		out, err := run(r.Root, jjDiffArgs(base)...)
		if err != nil {
			return nil, err
		}
		return diff.Parse(string(out))
	}
	out, err := run(r.Root, gitDiffArgs(base)...)
	if err != nil {
		return nil, err
	}
	files, err := diff.Parse(string(out))
	if err != nil {
		return nil, err
	}
	names, err := run(r.Root, gitUntrackedArgs()...)
	if err != nil {
		return nil, err
	}
	for _, name := range strings.Split(string(names), "\x00") {
		if name == "" {
			continue
		}
		u, err := read(filepath.Join(r.Root, name))
		if err != nil {
			continue // vanished, unreadable, special, or a nested repository
		}
		files = append(files, untrackedFile(name, u))
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].Path() < files[j].Path() })
	return files, nil
}

func jjDiffArgs(base string) []string {
	return []string{"jj", "--no-pager", "--color=never", "diff", "--git", "--from", base}
}

func gitDiffArgs(base string) []string {
	return []string{
		"git", "-c", "core.quotepath=false", "-c", "color.ui=never", "-c", "color.diff=never", "--no-pager",
		"diff", "--no-color", "--no-ext-diff", "--no-textconv", "-M", "--src-prefix=a/", "--dst-prefix=b/", base, "--",
	}
}

func gitUntrackedArgs() []string {
	return []string{"git", "ls-files", "--others", "--exclude-standard", "-z"}
}

// Uncommitted returns the commit the working copy's changes sit on: the
// parent of jj's working-copy commit, or git's HEAD.
func (r Repo) Uncommitted(run Runner) (string, error) {
	if r.Kind == JJ {
		return r.Resolve(run, "@-")
	}
	return r.Resolve(run, "HEAD")
}

// Resolve turns one revision into a full commit id.
func (r Repo) Resolve(run Runner, rev string) (string, error) {
	if err := checkRev(rev); err != nil {
		return "", err
	}
	if r.Kind == JJ {
		out, err := run(r.Root, jjResolveArgs(rev)...)
		if err != nil {
			return "", err
		}
		return oneCommit(string(out), rev)
	}
	out, err := run(r.Root, "git", "rev-parse", "--verify", rev+"^{commit}")
	if err != nil {
		return "", err
	}
	return oneCommit(string(out), rev)
}

func jjResolveArgs(rev string) []string {
	return []string{"jj", "--no-pager", "--ignore-working-copy", "log", "--no-graph", "-r", rev, "-T", `commit_id ++ "\n"`}
}

// checkRev refuses what a program would read as an option.
func checkRev(rev string) error {
	if rev == "" || strings.HasPrefix(rev, "-") {
		return fmt.Errorf("bad revision %q", rev)
	}
	return nil
}

// oneCommit reads the single commit id a command printed.
func oneCommit(out, rev string) (string, error) {
	ids := strings.Fields(out)
	switch len(ids) {
	case 1:
		return ids[0], nil
	case 0:
		return "", fmt.Errorf("%s names no commit", rev)
	}
	return "", fmt.Errorf("%s names %d commits, not one", rev, len(ids))
}

// BaseAt returns the commit id to diff from to see what changed since t:
// the working-copy commit jj had at the last operation ending by t, or
// the commit git's HEAD named then. A git repository whose first commit
// came after t is diffed from the empty tree, so all of it counts.
func (r Repo) BaseAt(run Runner, t time.Time) (string, error) {
	if r.Kind == JJ {
		return r.jjBaseAt(run, t)
	}
	out, err := run(r.Root, gitReflogArgs()...)
	if err != nil {
		return "", err
	}
	entries := ParseReflog(string(out))
	sha, ok := ReflogAt(entries, t)
	if !ok && Born(entries) {
		tree, err := run(r.Root, gitEmptyTreeArgs()...)
		if err != nil {
			return "", err
		}
		return oneCommit(string(tree), "the empty tree")
	}
	if !ok {
		return "", fmt.Errorf("HEAD has no history from before %s", t.Format(time.RFC3339))
	}
	return sha, nil
}

func (r Repo) jjBaseAt(run Runner, t time.Time) (string, error) {
	out, err := run(r.Root, jjOpLogArgs()...)
	if err != nil {
		return "", err
	}
	ops := ParseOpLog(string(out))
	at, ok := OpIndexAt(ops, t)
	if !ok {
		return "", errors.New("jj has no operations")
	}
	// No listed operation is old enough. If the listing was cut short, the
	// base would silently be a later one than the session started from.
	if ops[at].End.After(t) && len(ops) >= opLogLimit {
		return "", fmt.Errorf("jj's operation log does not reach back to %s, the session's start", t.Format(time.RFC3339))
	}
	// The oldest operation, which made the repository, has no working-copy
	// commit. Fall back to a newer one rather than fail.
	for i := at; i >= 0; i-- {
		var id string
		if id, err = r.jjWorkingCopyAt(run, ops[i].ID); err == nil {
			return id, nil
		}
	}
	return "", err
}

// jjWorkingCopyAt is the working-copy commit as of one operation.
func (r Repo) jjWorkingCopyAt(run Runner, op string) (string, error) {
	out, err := run(r.Root, "jj", "--no-pager", "--ignore-working-copy", "--at-op", op,
		"log", "--no-graph", "-r", "@", "-T", `commit_id ++ "\n"`)
	if err != nil {
		return "", err
	}
	return oneCommit(string(out), "@ at "+op)
}

// opLogLimit is how many operations BaseAt looks through.
const opLogLimit = 20000

func jjOpLogArgs() []string {
	return []string{"jj", "--no-pager", "--ignore-working-copy", "op", "log", "--no-graph", "-n", strconv.Itoa(opLogLimit),
		"-T", `id ++ " " ++ time.end().format("%s") ++ "\n"`}
}

func gitReflogArgs() []string {
	return []string{"git", "reflog", "show", "--date=unix", "--format=%H %gd %gs", "HEAD"}
}

// gitEmptyTreeArgs print the empty tree's id, which depends on the
// repository's hash.
func gitEmptyTreeArgs() []string { return []string{"git", "hash-object", "-t", "tree", "/dev/null"} }
