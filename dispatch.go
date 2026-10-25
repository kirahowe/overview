package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"overview/internal/config"
)

// maxArgs is how many positional arguments each subcommand that reads the
// global flags takes. The empty name is the plain launch, which takes none.
var maxArgs = map[string]int{
	"":             0,
	"view":         0,
	"sessions":     0,
	"digest":       0,
	"activity":     0,
	"publish":      1,
	"orchestrator": 1,
	"themes":       1,
}

// rawCommands read their own arguments, so they are recognised only as the
// first word and the global flags are not parsed for them.
var rawCommands = map[string]bool{"hook": true, "stage": true, "review": true}

// cli is a parsed command line: the subcommand, its positional arguments
// and the global flags.
type cli struct {
	cmd  string
	rest []string

	flags      config.Values
	socketPath string
	session    string
	cwd        string
	configDir  string
	orchDir    string
	every      time.Duration
	model      string
}

// parseCLI turns argv (without the program name) into a cli. The subcommand
// may come before or after the global flags. A positional word that is not a
// known subcommand, or an argument a subcommand does not take, is an error,
// so a typo can never fall through to launching the view. The plain launch
// happens only when no positional argument is given. flag.ErrHelp means
// help was asked for.
func parseCLI(args []string) (cli, error) {
	var c cli
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' && rawCommands[args[0]] {
		c.cmd, c.rest = args[0], args[1:]
		return c, nil
	}
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		c.cmd, args = args[0], args[1:]
		if _, ok := maxArgs[c.cmd]; !ok || c.cmd == "" {
			return c, fmt.Errorf("unknown command %q", c.cmd)
		}
	}
	fs := flag.NewFlagSet("overview", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&c.flags.Home, "home", "", "")
	fs.StringVar(&c.flags.Theme, "theme", "", "")
	fs.StringVar(&c.flags.Claude, "claude", "", "")
	fs.StringVar(&c.flags.Issues, "issues", "", "")
	fs.StringVar(&c.flags.Socket, "socket", "", "")
	fs.StringVar(&c.socketPath, "socket-path", "", "")
	fs.StringVar(&c.session, "session", "", "")
	fs.StringVar(&c.cwd, "cwd", "", "")
	fs.StringVar(&c.configDir, "config", "", "")
	fs.StringVar(&c.orchDir, "orchestrator-dir", "", "")
	fs.DurationVar(&c.every, "every", time.Hour, "")
	fs.StringVar(&c.model, "model", "sonnet", "")
	// Flags may come before or after a positional argument such as "start".
	for {
		if err := fs.Parse(args); err != nil {
			return c, err
		}
		if fs.NArg() == 0 {
			break
		}
		consumed := len(args) - fs.NArg()
		if consumed > 0 && args[consumed-1] == "--" {
			c.rest = append(c.rest, fs.Args()...)
			break
		}
		c.rest, args = append(c.rest, fs.Arg(0)), fs.Args()[1:]
	}
	if c.cmd == "" && len(c.rest) > 0 {
		word := c.rest[0]
		if rawCommands[word] {
			return c, fmt.Errorf("%s takes no overview flags before it: put it first", word)
		}
		if _, ok := maxArgs[word]; !ok {
			return c, fmt.Errorf("unknown command %q", word)
		}
		c.cmd, c.rest = word, c.rest[1:]
	}
	if len(c.rest) > maxArgs[c.cmd] {
		if c.cmd == "" {
			return c, fmt.Errorf("unexpected argument %q", c.rest[0])
		}
		return c, fmt.Errorf("unexpected argument %q for %s", c.rest[maxArgs[c.cmd]], c.cmd)
	}
	return c, nil
}

// isHelp reports whether err asks for the usage text.
func isHelp(err error) bool { return errors.Is(err, flag.ErrHelp) }
