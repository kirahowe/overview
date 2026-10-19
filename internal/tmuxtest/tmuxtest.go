// Package tmuxtest gives a test a tmux server of its own. The server
// listens on a socket in a fresh directory under /tmp, never on the
// overview socket, and the test's cleanup kills it and removes the
// directory, since tmux leaves its socket file behind.
package tmuxtest

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Socket is a private server's socket. The server starts with the first
// command sent to it.
type Socket struct {
	// Path is where the socket lives: tmux.At(Path, conf) reaches it.
	Path string
	conf string
	t    testing.TB
}

// New makes a directory for a socket and arranges for the server on it to
// be killed and the directory removed when the test ends. conf is the
// file the server reads when Run starts it, "" for none. A test is
// skipped when tmux is not installed.
func New(t testing.TB, conf string) Socket {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	// A unix socket path is limited to about a hundred bytes, which
	// t.TempDir's paths can pass, so the directory is short and in /tmp.
	dir, err := os.MkdirTemp("/tmp", "ovt")
	if err != nil {
		t.Fatal(err)
	}
	if conf == "" {
		conf = "/dev/null"
	}
	s := Socket{Path: filepath.Join(dir, "s"), conf: conf, t: t}
	t.Cleanup(func() {
		s.Run("kill-server") //nolint:errcheck
		os.RemoveAll(dir)
	})
	return s
}

// Run runs one tmux command against the server and returns its output,
// trimmed.
func (s Socket) Run(args ...string) (string, error) {
	cmd := exec.Command("tmux", append([]string{"-S", s.Path, "-f", s.conf}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("tmux %v: %w: %s", args, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// Must is Run, failing the test on an error.
func (s Socket) Must(args ...string) string {
	s.t.Helper()
	out, err := s.Run(args...)
	if err != nil {
		s.t.Fatal(err)
	}
	return out
}
