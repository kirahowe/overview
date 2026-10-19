package config

import (
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

// fields reads each setting of a config.
var fields = map[string]func(Config) string{
	"home":   func(c Config) string { return c.Home },
	"theme":  func(c Config) string { return c.Theme },
	"socket": func(c Config) string { return c.Socket },
	"claude": func(c Config) string { return c.Claude },
}

// resolve is Resolve with a socket never given as a path, which it refuses.
func resolve(t *rapid.T, flags, file Values) Config {
	c, err := Resolve("/d", flags, file, "/u", "/w")
	if err != nil {
		t.Fatalf("refused %+v over %+v: %v", flags, file, err)
	}
	return c
}

func drawConfigValues(t *rapid.T, label string) Values {
	v := drawValues(t, label)
	v.Socket = strings.ReplaceAll(v.Socket, "/", "")
	return v
}

// A flag that is set fixes its setting, whatever the file says.
func TestPropAFlagFixesItsSetting(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		flags := drawConfigValues(t, "flag")
		a, b := resolve(t, flags, drawConfigValues(t, "file a")), resolve(t, flags, drawConfigValues(t, "file b"))
		for name, field := range keys {
			if *field(&flags) != "" && fields[name](a) != fields[name](b) {
				t.Fatalf("%s flag %q: %q with one file, %q with another", name, *field(&flags), fields[name](a), fields[name](b))
			}
		}
	})
}

// Each setting depends only on its own flag and line: changing one
// setting's inputs leaves every other setting as it was.
func TestPropEachSettingStandsAlone(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		flags, file := drawConfigValues(t, "flag"), drawConfigValues(t, "file")
		name := rapid.SampledFrom([]string{"home", "theme", "socket", "claude"}).Draw(t, "changed")
		flags2, file2 := flags, file
		otherFlags, otherFile := drawConfigValues(t, "other flag"), drawConfigValues(t, "other file")
		*keys[name](&flags2), *keys[name](&file2) = *keys[name](&otherFlags), *keys[name](&otherFile)
		a, b := resolve(t, flags, file), resolve(t, flags2, file2)
		for n, get := range fields {
			if n != name && get(a) != get(b) {
				t.Fatalf("changing %s changed %s: %q to %q", name, n, get(a), get(b))
			}
		}
	})
}
