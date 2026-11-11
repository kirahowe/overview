package tmux

import (
	"bytes"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/charmbracelet/x/term"
)

// TestMain lets the test binary stand in for the program in a pane: run
// as "probe <file>" it is probe, and no test runs.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "probe" {
		probe(os.Args[2])
		return
	}
	os.Exit(m.Run())
}

// probe is a pane's program that writes down what tmux tells it, a line
// each in the file at path: "size 40x12" when it starts and at every
// resize, and "focus in" or "focus out" when the keyboard comes or goes.
// It asks for the focus reports, as the view does.
func probe(path string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		os.Exit(1)
	}
	var mu sync.Mutex
	say := func(line string) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintln(f, line)
	}
	size := func() {
		w, h, _ := term.GetSize(os.Stdout.Fd())
		say(fmt.Sprintf("size %dx%d", w, h))
	}
	// Listening comes before the first size is read, so no resize falls
	// between the two.
	resized := make(chan os.Signal, 16)
	signal.Notify(resized, syscall.SIGWINCH)
	if _, err := term.MakeRaw(os.Stdin.Fd()); err != nil {
		os.Exit(1)
	}
	os.Stdout.WriteString("\x1b[?1004h") //nolint:errcheck
	size()
	go func() {
		for range resized {
			size()
		}
	}()
	var in []byte
	buf := make([]byte, 256)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return
		}
		in = append(in, buf[:n]...)
		for len(in) >= 3 {
			switch {
			case bytes.HasPrefix(in, []byte("\x1b[I")):
				say("focus in")
				in = in[3:]
			case bytes.HasPrefix(in, []byte("\x1b[O")):
				say("focus out")
				in = in[3:]
			default:
				in = in[1:]
			}
		}
	}
}

// told is what the probe writing to path has been told so far.
func told(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Fields(strings.ReplaceAll(strings.TrimSpace(string(b)), " ", "-"))
}
