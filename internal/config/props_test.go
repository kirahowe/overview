package config

import (
	"fmt"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// A value never has surrounding space and never is empty, as Settings
// trims and refuses those.
var value = rapid.StringMatching(`[a-z~/.#][a-z0-9~/.#= -]{0,10}[a-z0-9]|[a-z]`)

func drawValues(t *rapid.T, label string) Values {
	maybe := func(name string) string {
		if rapid.Bool().Draw(t, label+name+"set") {
			return value.Draw(t, label+name)
		}
		return ""
	}
	return Values{Home: maybe("home"), Theme: maybe("theme"), Socket: maybe("socket"), Claude: maybe("claude")}
}

// Whatever order, spacing and comments a file uses, Parse reads back the
// settings written into it.
func TestPropParseReadsWhatIsWritten(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		want := drawValues(t, "")
		var lines []string
		for k, field := range keys {
			if v := *field(&want); v != "" {
				pad := strings.Repeat(" ", rapid.IntRange(0, 2).Draw(t, k+"pad"))
				lines = append(lines, pad+k+pad+"="+pad+v+pad)
			}
			if rapid.Bool().Draw(t, k+"comment") {
				lines = append(lines, "# "+k+" = nope", "")
			}
		}
		perm := rapid.Permutation(lines).Draw(t, "order")
		got, err := Parse(strings.Join(perm, "\n"))
		if err != nil || got != want {
			t.Fatalf("got %+v, %v; want %+v from\n%s", got, err, want, strings.Join(perm, "\n"))
		}
	})
}

// Each setting comes from its flag when there is one, the file when that
// says, and the default otherwise, independently of the others.
func TestPropResolveTakesFlagThenFileThenDefault(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		flags, file := drawValues(t, "flag"), drawValues(t, "file")
		c, err := Resolve("/d", flags, file)
		if err != nil {
			// Only a socket given as a path is refused.
			sock := flags.Socket
			if sock == "" {
				sock = file.Socket
			}
			if !strings.ContainsRune(sock, '/') {
				t.Fatalf("refused %+v over %+v: %v", flags, file, err)
			}
			return
		}
		for _, f := range []struct {
			name            string
			got, flag, file string
			def             string
		}{
			{"home", c.Home, flags.Home, file.Home, ""},
			{"theme", c.Theme, flags.Theme, file.Theme, DefaultTheme},
			{"socket", c.Socket, flags.Socket, file.Socket, DefaultSocket},
			{"claude", c.Claude, flags.Claude, file.Claude, DefaultClaude},
		} {
			want := f.def
			if f.file != "" {
				want = f.file
			}
			if f.flag != "" {
				want = f.flag
			}
			if f.got != want {
				t.Fatalf("%s: got %q, want %q (%s)", f.name, f.got, want, fmt.Sprint(flags, file))
			}
		}
	})
}
