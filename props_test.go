package main

import (
	"testing"

	"pgregory.net/rapid"
)

// A plain launch happens only when argv holds no positional word. Any argv
// with a word that is not a flag, a flag's value or a known command must be
// rejected, wherever the global flags sit.
func TestPropStrayWordNeverLaunches(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		flags := rapid.SliceOf(rapid.SampledFrom([][]string{
			{"--home", "x"}, {"--model=m"}, {"--theme", "t"}, {"--socket", "s"}, {"--every", "2h"},
		})).Draw(t, "flags")
		word := rapid.StringMatching(`[a-z][a-z0-9-]{0,10}`).Filter(func(s string) bool {
			_, known := maxArgs[s]
			return !known && !rawCommands[s]
		}).Draw(t, "word")
		var args []string
		pos := rapid.IntRange(0, len(flags)).Draw(t, "pos")
		for i, f := range flags {
			if i == pos {
				args = append(args, word)
			}
			args = append(args, f...)
		}
		if pos == len(flags) {
			args = append(args, word)
		}
		c, err := parseCLI(args)
		if err == nil {
			t.Fatalf("%q parsed as %q %q, want an error", args, c.cmd, c.rest)
		}
	})
}

// A known command is recognised wherever the global flags sit, and a launch
// has no positional argument.
func TestPropCommandPosition(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cmd := rapid.SampledFrom([]string{"sessions", "digest", "activity"}).Draw(t, "cmd")
		before := rapid.SliceOfN(rapid.SampledFrom([]string{"--home=x", "--model=m", "--theme=t"}), 0, 3).Draw(t, "before")
		after := rapid.SliceOfN(rapid.SampledFrom([]string{"--home=x", "--model=m", "--theme=t"}), 0, 3).Draw(t, "after")
		args := append(append(append([]string{}, before...), cmd), after...)
		c, err := parseCLI(args)
		if err != nil || c.cmd != cmd || len(c.rest) != 0 {
			t.Fatalf("%q gave %q %q, %v", args, c.cmd, c.rest, err)
		}
		c, err = parseCLI(before)
		if err != nil || c.cmd != "" {
			t.Fatalf("%q gave %q, %v; want a launch", before, c.cmd, err)
		}
	})
}
