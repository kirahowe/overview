package fleet

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// maxTasks is how many linked issues a session reads as.
const maxTasks = 8

// TaskRef names an issue a session took on, as a transcript shows it. Project is
// the issues project id when the reference gave one; Dir is the directory the
// command named (made absolute against the event's cwd) or else the event's cwd.
// A directory under the user's home that the command wrote with ~ keeps the ~
// until HomeDirs puts it under that home. Dir is empty when the reader could
// not tell where the command ran, which leaves the world to place the ref.
// Linked is when the session first took it on, zero when that is unknown.
type TaskRef struct {
	Project string    `json:"project,omitempty"`
	Dir     string    `json:"dir,omitempty"`
	ID      int       `json:"id"`
	Linked  time.Time `json:"linked,omitzero"`
}

// Same says whether two refs name the same issue the same way, whenever
// each was linked.
func (r TaskRef) Same(o TaskRef) bool {
	return r.Project == o.Project && r.Dir == o.Dir && r.ID == o.ID
}

// FoldTasks is the task list that lines, read in order, add up to: each
// ref once, as late as it was last linked, with the first time it was
// linked, and the newest maxTasks of them.
func FoldTasks(lines []TaskRef) []TaskRef {
	var out []TaskRef
	for _, r := range lines {
		if i := slices.IndexFunc(out, r.Same); i >= 0 {
			if !out[i].Linked.IsZero() {
				r.Linked = out[i].Linked
			}
			out = slices.Delete(out, i, i+1)
		}
		out = append(out, r)
	}
	if len(out) > maxTasks {
		out = out[len(out)-maxTasks:]
	}
	return out
}

// OpeningTask reports the issue named by the opening line overview writes
// when it starts a workstream, "Work on <project>#<n>". The project id is
// any run of characters other than spaces and #, so any id overview can
// write without them reads back. Any other mention of an issue links
// nothing, so a prompt that merely talks about one is safe.
func OpeningTask(prompt, cwd string) (TaskRef, bool) {
	rest, ok := strings.CutPrefix(strings.TrimLeftFunc(prompt, unicode.IsSpace), "Work on ")
	if !ok {
		return TaskRef{}, false
	}
	end := strings.IndexFunc(rest, func(c rune) bool { return c == '#' || unicode.IsSpace(c) })
	if end < 0 {
		return TaskRef{}, false
	}
	project := rest[:end]
	rest, ok = strings.CutPrefix(rest[end:], "#")
	if project == "" || !ok {
		return TaskRef{}, false
	}
	n := 0
	for n < len(rest) && rest[n] >= '0' && rest[n] <= '9' {
		n++
	}
	if n == 0 {
		return TaskRef{}, false
	}
	if tail := rest[n:]; tail != "" {
		c := tail[0]
		if c != ':' && c != '.' && !unicode.IsSpace(rune(c)) {
			// A multi-byte space still counts, so decode the whole rune.
			r := []rune(tail)[0]
			if !unicode.IsSpace(r) {
				return TaskRef{}, false
			}
		}
	}
	id, err := strconv.Atoi(rest[:n])
	if err != nil || id <= 0 {
		return TaskRef{}, false
	}
	return TaskRef{Project: project, Dir: cwd, ID: id}, true
}

// IssuesStarts finds every `issues start <n>` in a shell command. It is a
// reader for the common shapes, not a shell: no expansion, and command
// substitutions and heredoc bodies are opaque. A `cd <dir>` or `pushd
// <dir>` earlier in the string moves the directory later commands use,
// and a bare `cd` moves it to ~. A `cd` the reader cannot follow (`-`, a
// variable), a `popd`, and a group or subshell, whose `cd` may or may not
// last past it, leave the directory unknown. So does a `cd` behind a
// keyword or wrapper (`if`, `then`, `do`, `command`, `builtin`), a loop,
// `eval`, `source`, `exec` and any command with `cd` among its words.
// `then`, `do`, `else` and `command` before `issues` do not hide the
// invocation.
//
// A start still reads when the directory is unknown or the one it names
// cannot be read (a variable, a substitution, a relative path with nothing
// to stand on). Its ref then has no directory: it says which issue was
// started and not where, and the world decides whether that is enough to
// link. What the command does is another matter. A start with a flag the
// reader does not know, or one asking for help, gives no ref at all.
//
// A function defined in the command stands for its body wherever a later
// command calls it, with the call's arguments in place of "$@" and of $1
// to $9, so `i() { issues --dir . "$@"; }; i start 3` reads as the start
// it runs. Defining a function runs nothing. Reading stops at a
// definition whose end the reader cannot find, since what follows may be
// a body that never runs, and at a function that calls itself.
func IssuesStarts(command, cwd string) []TaskRef {
	s := shell{cwd: cwd, known: true, funcs: map[string][][]string{}}
	s.run(splitCommands(command), 0)
	return s.out
}

// maxCalls is how deep the reader follows functions that call functions.
const maxCalls = 4

// shell is what the reader knows as it goes through a command's simple
// commands: the directory they run in, whether that is still known, the
// functions defined so far and the starts found. stopped says the reader
// met something it cannot read past.
type shell struct {
	cwd     string
	known   bool
	funcs   map[string][][]string
	out     []TaskRef
	stopped bool
}

func (s *shell) run(cmds [][]string, depth int) {
	for i := 0; i < len(cmds) && !s.stopped; i++ {
		argv := cmds[i]
		if name, n, ok := funcHead(argv); ok {
			body, next, ok := funcBody(cmds, i, n)
			if !ok {
				s.stopped = true
				return
			}
			s.funcs[name] = body
			i = next - 1
			continue
		}
		if opensGroup(argv[0]) {
			s.known = false
			continue
		}
		for len(argv) > 0 && isAssignment(argv[0]) {
			argv = argv[1:]
		}
		if len(argv) == 0 {
			continue
		}
		argv, wrapped, program, ok := unwrapCommand(argv)
		if !ok {
			s.known = false
			continue
		}
		if len(argv) == 0 {
			continue
		}
		// A function stands in for a builtin or a program of its name.
		if body, ok := s.funcs[argv[0]]; ok && !program {
			if depth == maxCalls {
				s.stopped = true
				return
			}
			s.run(bind(body, argv[1:]), depth+1)
			continue
		}
		switch argv[0] {
		case "cd", "pushd":
			switch {
			case wrapped || !s.known:
				s.known = false
			case len(argv) == 1 && argv[0] == "cd":
				s.cwd = "~"
			case len(argv) == 2 && !strings.HasPrefix(argv[1], "-") && !strings.HasPrefix(argv[1], "+"):
				s.cwd, s.known = resolveDir(s.cwd, argv[1])
			default:
				s.known = false
			}
			continue
		case "popd":
			s.known = false
			continue
		}
		if !isIssues(argv[0]) && slices.ContainsFunc(argv, func(w string) bool {
			return w == "cd" || w == "pushd" || w == "popd"
		}) {
			s.known = false
			continue
		}
		if ref, ok := parseStart(argv, s.cwd, s.known); ok {
			s.out = append(s.out, ref)
		}
	}
}

// funcHead reads the head of a function definition off the front of a
// command: `name() {`, `name () {`, `name(){` or `function name {`. It
// gives the function's name and how many words the head takes, the
// opening brace included. The name is the head's first word, and the
// words after it are the parentheses and the brace and nothing else, so
// an argument such as 'main(){' makes no definition of the command
// before it.
func funcHead(argv []string) (name string, n int, ok bool) {
	keyword := len(argv) > 0 && argv[0] == "function"
	if keyword {
		argv = argv[1:]
	}
	var head strings.Builder
	for i, w := range argv {
		if i > 2 || i > 0 && w != "()" && w != "{" && w != "(){" {
			break
		}
		head.WriteString(w)
		h, brace := strings.CutSuffix(head.String(), "{")
		if !brace {
			continue
		}
		h, parens := strings.CutSuffix(h, "()")
		if !isFuncName(h) || !parens && !keyword {
			break
		}
		if keyword {
			i++
		}
		return h, i + 1, true
	}
	return "", 0, false
}

func isFuncName(w string) bool {
	for i := 0; i < len(w); i++ {
		c := w[i]
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && (c == '-' || c >= '0' && c <= '9')) {
			return false
		}
	}
	return w != ""
}

// funcBody is the body of the function whose head takes the first n words
// of cmds[i], and the index of the command after its closing brace. The
// brace closes the body as a command of its own, or as the last word of
// one, which zsh allows. It reports false for a body the reader cannot
// find the end of: one with a brace of its own inside, or with no closing
// brace.
func funcBody(cmds [][]string, i, n int) (body [][]string, next int, ok bool) {
	for j := i; j < len(cmds); j++ {
		cmd := cmds[j]
		if j == i {
			cmd = cmd[n:]
		}
		if len(cmd) == 0 {
			continue
		}
		if j > i && cmd[0] == "}" {
			return body, j + 1, true
		}
		closes := cmd[len(cmd)-1] == "}"
		if closes {
			cmd = cmd[:len(cmd)-1]
		}
		for _, w := range cmd {
			if strings.HasPrefix(w, "{") || w == "}" {
				return nil, 0, false
			}
		}
		if len(cmd) > 0 {
			body = append(body, cmd)
		}
		if closes {
			return body, j + 1, true
		}
	}
	return nil, 0, false
}

// bind is a function's body as one call runs it: "$@" gives way to the
// call's arguments, and $1 to $9 each to the argument it names, or to
// nothing when the call has too few.
func bind(body [][]string, args []string) [][]string {
	var out [][]string
	for _, cmd := range body {
		var argv []string
		for _, w := range cmd {
			switch p := param(w); {
			case p == "@":
				argv = append(argv, args...)
			case len(p) == 1 && p[0] >= '1' && p[0] <= '9':
				if n := int(p[0] - '0'); n <= len(args) {
					argv = append(argv, args[n-1])
				}
			default:
				argv = append(argv, w)
			}
		}
		if len(argv) > 0 {
			out = append(out, argv)
		}
	}
	return out
}

// param is the parameter a word expands and nothing else, as $@ or ${1}
// do: "@" or "1" there, and "" for any other word.
func param(w string) string {
	name, ok := strings.CutPrefix(w, "$")
	if !ok {
		return ""
	}
	if inner, braced := strings.CutPrefix(name, "{"); braced {
		if name, ok = strings.CutSuffix(inner, "}"); !ok {
			return ""
		}
	}
	return name
}

// unwrapCommand strips the control keywords and wrappers off the front of
// a simple command. wrapped says one was there, so a `cd` that follows may
// not run, or not last, and program that it was a wrapper, which runs the
// program of that name and never a function. It reports false when the
// command may change the directory in a way the reader cannot follow: a
// loop, case or function head, eval, source, or a wrapper in front of
// anything but `issues`.
func unwrapCommand(argv []string) (rest []string, wrapped, program, ok bool) {
	for len(argv) > 0 {
		switch argv[0] {
		case "if", "then", "else", "elif", "do", "!":
			argv, wrapped = argv[1:], true
			continue
		case "command", "builtin", "exec":
			if len(argv) > 1 && isIssues(argv[1]) {
				return argv[1:], true, true, true
			}
			return nil, true, true, false
		case "for", "while", "until", "case", "select", "function", "eval", "source", ".":
			return nil, true, false, false
		}
		break
	}
	return argv, wrapped, false, true
}

func isIssues(w string) bool { return w == "issues" || strings.HasSuffix(w, "/issues") }

// opensGroup says whether a command's first word starts or ends a
// subshell or a brace group, as in `(cd x; ...)` or `{ cd x; ...; }`.
func opensGroup(w string) bool {
	return strings.HasPrefix(w, "(") || strings.HasPrefix(w, "{") || w == "}" || w == ")"
}

func isAssignment(w string) bool {
	i := strings.IndexByte(w, '=')
	if i <= 0 {
		return false
	}
	for j := 0; j < i; j++ {
		c := w[j]
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || j > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// resolveDir makes dir a rooted path against cwd: an absolute one, or one
// under the user's home, written with a leading ~. It reports false for a
// dir the reader cannot expand (~user, $VAR, substitutions, empty), for a
// relative dir when cwd is not rooted to resolve it against, and for one
// that climbs out of ~.
func resolveDir(cwd, dir string) (string, bool) {
	switch {
	case dir == "" || strings.ContainsAny(dir, "$`"):
		return "", false
	case underHome(dir):
		return cleanHome(dir)
	case strings.HasPrefix(dir, "~"):
		return "", false
	case filepath.IsAbs(dir):
		return filepath.Clean(dir), true
	case filepath.IsAbs(cwd):
		return filepath.Join(cwd, dir), true
	case underHome(cwd):
		return cleanHome(cwd + "/" + dir)
	}
	return "", false
}

// underHome says whether a directory is written from the user's home.
func underHome(dir string) bool { return dir == "~" || strings.HasPrefix(dir, "~/") }

// rooted says whether a directory is absolute or written from ~.
func rooted(dir string) bool { return filepath.IsAbs(dir) || underHome(dir) }

// cleanHome cleans a path written from ~. It reports false for one that
// climbs out of the home, which only whoever knows the home could follow.
func cleanHome(dir string) (string, bool) {
	if d := filepath.Clean(dir); underHome(d) {
		return d, true
	}
	return "", false
}

// HomeDirs is refs with each directory written from ~ put under user, the
// user's home. With no home to put it under, such a directory is unknown
// and its ref keeps none.
func HomeDirs(refs []TaskRef, user string) []TaskRef {
	out := slices.Clone(refs)
	for i, r := range out {
		switch {
		case !underHome(r.Dir):
		case filepath.IsAbs(user):
			out[i].Dir = filepath.Join(user, r.Dir[1:])
		default:
			out[i].Dir = ""
		}
	}
	return out
}

// flagValue reports whether a flag takes a value, and if it is written
// --name=value, that value.
func flagValue(w string) (takes bool, inline string, hasInline bool) {
	name := w
	if i := strings.IndexByte(w, '='); i > 0 && strings.HasPrefix(w, "--") {
		name, inline, hasInline = w[:i], w[i+1:], true
	}
	switch name {
	case "-C", "--dir", "--project", "--config":
		if hasInline && name == "-C" {
			return false, "", false
		}
		return true, inline, hasInline
	}
	return false, "", false
}

func parseStart(argv []string, cwd string, cwdKnown bool) (TaskRef, bool) {
	if !isIssues(argv[0]) {
		return TaskRef{}, false
	}
	for _, a := range argv[1:] {
		if a == "-h" || a == "--help" {
			return TaskRef{}, false
		}
	}
	var project, dir string
	var dirSet bool
	// apply consumes a flag at args[i] and returns the next index, or -1
	// when the word is not a known flag.
	apply := func(args []string, i int) int {
		w := args[i]
		switch w {
		case "--json", "--edn", "--all", "--force":
			return i + 1
		}
		takes, val, inline := flagValue(w)
		if !takes {
			return -1
		}
		next := i + 1
		if !inline {
			if next >= len(args) {
				return -1
			}
			val = args[next]
			next++
		}
		switch strings.SplitN(w, "=", 2)[0] {
		case "-C", "--dir":
			dir, dirSet = val, true
		case "--project":
			project = val
		}
		return next
	}
	args := argv[1:]
	i := 0
	for i < len(args) && args[i] != "start" {
		if i = apply(args, i); i < 0 {
			return TaskRef{}, false
		}
	}
	if i >= len(args) {
		return TaskRef{}, false
	}
	i++
	id := 0
	for i < len(args) {
		if next := apply(args, i); next >= 0 {
			i = next
			continue
		}
		if strings.HasPrefix(args[i], "-") {
			// An unknown flag may change what the command does.
			return TaskRef{}, false
		}
		if id != 0 {
			// Anything extra after the id is the command's own business.
			i++
			continue
		}
		n, ok := positiveInt(args[i])
		if !ok {
			return TaskRef{}, false
		}
		id = n
		i++
	}
	if id == 0 || strings.ContainsAny(project, "$`") {
		return TaskRef{}, false
	}
	ref := TaskRef{Project: project, ID: id}
	if !cwdKnown {
		cwd = ""
	}
	switch {
	case dirSet:
		ref.Dir, _ = resolveDir(cwd, dir)
	case rooted(cwd):
		ref.Dir = cwd
	}
	return ref, true
}

func positiveInt(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && n > 0
}

// splitCommands cuts a shell string into simple commands at && || ; | & and
// newlines outside quotes, and each into words with basic quoting. Command
// substitutions become an opaque `$()` or backtick word, heredoc bodies
// are skipped, and so is a comment: an unquoted # that starts a word, to
// the end of its line.
func splitCommands(s string) [][]string {
	var cmds [][]string
	var words []string
	var word strings.Builder
	inWord := false
	endWord := func() {
		if inWord {
			words = append(words, word.String())
			word.Reset()
			inWord = false
		}
	}
	endCmd := func() {
		endWord()
		if len(words) > 0 {
			cmds = append(cmds, words)
			words = nil
		}
	}
	type heredoc struct {
		delim string
		strip bool
	}
	var pending []heredoc
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch {
		case c == '\'':
			inWord = true
			for i++; i < len(r) && r[i] != '\''; i++ {
				word.WriteRune(r[i])
			}
		case c == '"':
			inWord = true
			for i++; i < len(r) && r[i] != '"'; i++ {
				switch {
				case r[i] == '\\' && i+1 < len(r) && (r[i+1] == '"' || r[i+1] == '\\'):
					i++
					word.WriteRune(r[i])
				case r[i] == '$' && i+1 < len(r) && r[i+1] == '(':
					i = skipSubst(r, i)
					word.WriteString("$()")
				case r[i] == '`':
					i = skipBackticks(r, i)
					word.WriteString("`")
				default:
					word.WriteRune(r[i])
				}
			}
		case c == '$' && i+1 < len(r) && r[i+1] == '(':
			inWord = true
			i = skipSubst(r, i)
			word.WriteString("$()")
		case c == '`':
			inWord = true
			i = skipBackticks(r, i)
			word.WriteString("`")
		case c == '\\':
			if i+1 < len(r) {
				i++
				if r[i] != '\n' {
					inWord = true
					word.WriteRune(r[i])
				}
			}
		case c == '<' && i+2 < len(r) && r[i+1] == '<' && r[i+2] == '<':
			// A here-string: its word is an ordinary argument.
			inWord = true
			word.WriteString("<<<")
			i += 2
		case c == '<' && i+1 < len(r) && r[i+1] == '<':
			j := i + 2
			strip := j < len(r) && r[j] == '-'
			if strip {
				j++
			}
			for j < len(r) && (r[j] == ' ' || r[j] == '\t') {
				j++
			}
			delim, end := heredocDelim(r, j)
			if delim == "" {
				endWord()
				i = j - 1
				break
			}
			endWord()
			pending = append(pending, heredoc{delim, strip})
			i = end - 1
		case c == '\n':
			endCmd()
			pos := i + 1
			for _, h := range pending {
				pos = skipHeredocBody(r, pos, h.delim, h.strip)
			}
			if len(pending) > 0 {
				i = pos - 1
				pending = nil
			}
		case c == '#' && !inWord:
			for i+1 < len(r) && r[i+1] != '\n' {
				i++
			}
		case c == '&' || c == '|' || c == ';':
			endCmd()
		case unicode.IsSpace(c):
			endWord()
		default:
			inWord = true
			word.WriteRune(c)
		}
	}
	endCmd()
	return cmds
}

// skipSubst returns the index of the ) closing the $( at r[i].
func skipSubst(r []rune, i int) int {
	depth := 0
	for ; i < len(r); i++ {
		switch r[i] {
		case '\\':
			i++
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return len(r) - 1
}

// skipBackticks returns the index of the backtick closing the one at r[i].
func skipBackticks(r []rune, i int) int {
	for i++; i < len(r); i++ {
		if r[i] == '\\' {
			i++
		} else if r[i] == '`' {
			return i
		}
	}
	return len(r) - 1
}

// heredocDelim reads a heredoc delimiter word starting at r[j], unquoted, and
// returns it with the index after it.
func heredocDelim(r []rune, j int) (string, int) {
	var d strings.Builder
	for j < len(r) {
		c := r[j]
		switch {
		case c == '\'' || c == '"':
			k := j + 1
			for k < len(r) && r[k] != c {
				d.WriteRune(r[k])
				k++
			}
			j = k + 1
		case c == '\\' && j+1 < len(r):
			d.WriteRune(r[j+1])
			j += 2
		case unicode.IsSpace(c) || strings.ContainsRune("&|;<>()", c):
			return d.String(), j
		default:
			d.WriteRune(c)
			j++
		}
	}
	return d.String(), len(r)
}

// skipHeredocBody returns the index after the line that ends a heredoc body
// starting at pos, or len(r) when no line does.
func skipHeredocBody(r []rune, pos int, delim string, strip bool) int {
	for pos < len(r) {
		end := pos
		for end < len(r) && r[end] != '\n' {
			end++
		}
		line := string(r[pos:end])
		if strip {
			line = strings.TrimLeft(line, "\t")
		}
		pos = end
		if pos < len(r) {
			pos++
		}
		if line == delim {
			return pos
		}
	}
	return len(r)
}
