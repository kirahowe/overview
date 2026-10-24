//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A review from start to finish, in the built binary: v opens it under a
// session in a git repository, a note written there is sent into the
// session's prompt, the agent's reply shows up under the note and in the
// list, and only the user's x resolves it.
func TestReview(t *testing.T) {
	r := launch(t, "rv")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		cmd.Dir = r.work
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	file := filepath.Join(r.work, "app.go")
	if err := os.WriteFile(file, []byte("package app\n\nfunc a() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	// The session starts once the base is committed, so its review is of
	// what follows.
	time.Sleep(1100 * time.Millisecond)

	// As a new session, the fake claude draws an empty prompt, as Claude
	// Code does, and keeps what is typed into it.
	received := filepath.Join(r.dir, "received")
	rule := strings.Repeat("─", 40)
	script := "#!/bin/sh\nif [ \"$1\" = agents ]; then cat " + r.listing + " 2>/dev/null || echo '[]'; exit 0; fi\n" +
		"printf '" + rule + "\\n❯ \\n" + rule + "\\n'\nexec cat > " + received + "\n"
	if err := os.WriteFile(r.fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	before := r.ov.panes()
	r.press("c")
	session := r.fresh(t, "the session", before, func(p pane) bool { return p.command == "cat" })
	r.lists(t, r.work, session)
	waitFor(t, "the session in the list", func() (bool, string) { s := r.screen(); return strings.Contains(s, "e2e-0"), s })

	// The session changes the file.
	if err := os.WriteFile(file, []byte("package app\n\nfunc a() {}\n\nfunc renameMe() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.ov.must("select-pane", "-t", r.list.id)
	r.press("v")
	var rev string
	waitFor(t, "the review above the session", func() (bool, string) {
		out := r.ov.must("list-panes", "-a", "-F", "#{pane_id} #{@overview-review} #{window_id} #{pane_top}")
		sessionTop := r.ov.must("display-message", "-p", "-t", session.id, "#{pane_top}")
		for _, line := range strings.Split(out, "\n") {
			f := strings.Fields(line)
			if len(f) == 4 && f[1] == "s-0" && f[2] == r.windowOf(session.id) && atoi(f[3]) < atoi(sessionTop) {
				rev = f[0]
				return true, ""
			}
		}
		return false, out
	})
	review := func() string { out, _ := r.ov.run("capture-pane", "-p", "-t", rev); return out }
	waitFor(t, "the diff", func() (bool, string) { s := review(); return strings.Contains(s, "func renameMe() {}"), s })

	// A note on the last line, sent to the session.
	keys := func(ks ...string) {
		t.Helper()
		for _, k := range ks {
			r.ov.must("send-keys", "-t", rev, k)
			time.Sleep(100 * time.Millisecond)
		}
	}
	keys("G", "c")
	r.ov.must("send-keys", "-t", rev, "-l", "please rename this")
	keys("Enter")
	store := filepath.Join(r.home, "overview", "reviews", "s-0.jsonl")
	waitFor(t, "the note in the review's log", func() (bool, string) {
		b, _ := os.ReadFile(store)
		return strings.Contains(string(b), `"body":"please rename this"`) && strings.Contains(string(b), `"lines":["func renameMe() {}"]`), string(b)
	})
	keys("s")
	waitFor(t, "the note in the session's prompt", func() (bool, string) {
		b, _ := os.ReadFile(received)
		return strings.Contains(string(b), "please rename this") && strings.Contains(string(b), "review reply --store"), string(b)
	})
	waitFor(t, "the note marked sent", func() (bool, string) {
		b, _ := os.ReadFile(store)
		return strings.Contains(string(b), `"op":"sent"`), string(b)
	})

	// The agent answers in the thread, as the message tells it to.
	b, _ := os.ReadFile(store)
	id := between(string(b), `"op":"note","id":"`, `"`)
	if out, err := exec.Command(r.bin, "review", "reply", "--store", store, id, "renamed it").CombinedOutput(); err != nil {
		t.Fatalf("reply: %v\n%s", err, out)
	}
	waitFor(t, "the reply under the note", func() (bool, string) { s := review(); return strings.Contains(s, "renamed it"), s })
	waitFor(t, "the list saying a reply waits", func() (bool, string) { s := r.screen(); return strings.Contains(s, "✎1 reply"), s })

	// There is no command for the agent to resolve with; the user's x does.
	if out, err := exec.Command(r.bin, "review", "resolve", "--store", store, id).CombinedOutput(); err == nil {
		t.Fatalf("the agent resolved a note: %s", out)
	}
	keys("n", "x")
	waitFor(t, "the note resolved", func() (bool, string) {
		b, _ := os.ReadFile(store)
		return strings.Contains(string(b), `"op":"resolve"`), string(b)
	})
	waitFor(t, "the badge gone", func() (bool, string) { s := r.screen(); return !strings.Contains(s, "✎"), s })
}

// between is the text of s after the first from, up to the next to.
func between(s, from, to string) string {
	_, rest, _ := strings.Cut(s, from)
	out, _, _ := strings.Cut(rest, to)
	return out
}
