package fleet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// maxTasks is how many linked issues a session's record keeps.
const maxTasks = 8

// TaskRef names an issue a session took on, as the hook saw it. Project is
// the issues project id when the reference gave one; Dir is the directory the
// command named (made absolute against the event's cwd) or else the event's cwd.
type TaskRef struct {
	Project string `json:"project,omitempty"`
	Dir     string `json:"dir,omitempty"`
	ID      int    `json:"id"`
}

// EventTasks returns the issues an event shows the session taking on. The
// event's own cwd wins over fallbackCwd, the one the session record holds.
func EventTasks(ev HookEvent, fallbackCwd string) []TaskRef {
	cwd := ev.Cwd
	if cwd == "" {
		cwd = fallbackCwd
	}
	switch ev.Name {
	case "UserPromptSubmit":
		if r, ok := OpeningTask(ev.Prompt, cwd); ok {
			return []TaskRef{r}
		}
	case "PostToolUse":
		if ev.ToolName != "Bash" {
			return nil
		}
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(ev.ToolInput, &in) != nil {
			return nil
		}
		return IssuesStarts(in.Command, cwd)
	}
	return nil
}

// MergeTasks adds refs to a session's task list. Linking a ref the list
// already has moves it to the end, the list keeps the newest maxTasks, and
// changed is true only when the result differs from cur.
func MergeTasks(cur, refs []TaskRef) (merged []TaskRef, changed bool) {
	if len(refs) == 0 {
		return cur, false
	}
	tasks := append([]TaskRef(nil), cur...)
	for _, r := range refs {
		for i, t := range tasks {
			if t == r {
				tasks = append(tasks[:i], tasks[i+1:]...)
				break
			}
		}
		tasks = append(tasks, r)
	}
	if len(tasks) > maxTasks {
		tasks = tasks[len(tasks)-maxTasks:]
	}
	if sameTasks(tasks, cur) {
		return cur, false
	}
	return tasks, true
}

func (h Home) tasksFile(id string) string {
	return filepath.Join(h.sessionsDir(), filepath.Base(id)+".tasks")
}

func (h Home) loadTasks(id string) []TaskRef {
	var tasks []TaskRef
	if b, err := os.ReadFile(h.tasksFile(id)); err == nil {
		_ = json.Unmarshal(b, &tasks)
	}
	return tasks
}

// linkTasks links what ev shows to its session. It reads and writes only the
// session's tasks file, and writes only when the list changed.
func (h Home) linkTasks(ev HookEvent, fallbackCwd string) {
	refs := EventTasks(ev, fallbackCwd)
	if len(refs) == 0 {
		return
	}
	merged, changed := MergeTasks(h.loadTasks(ev.SessionID), refs)
	if !changed {
		return
	}
	if b, err := json.Marshal(merged); err == nil {
		_ = h.writeSessionsFile(h.tasksFile(ev.SessionID), b)
	}
}

func sameTasks(a, b []TaskRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// OpeningTask reports the issue named by the opening line overview writes
// when it starts a workstream, "Work on <project>#<n>". Any other mention of
// an issue links nothing, so a prompt that merely talks about one is safe.
func OpeningTask(prompt, cwd string) (TaskRef, bool) {
	rest, ok := strings.CutPrefix(strings.TrimLeftFunc(prompt, unicode.IsSpace), "Work on ")
	if !ok {
		return TaskRef{}, false
	}
	end := 0
	for i, c := range rest {
		alnum := c < 128 && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9')
		if alnum || i > 0 && (c == '.' || c == '_' || c == '-') {
			end = i + 1
			continue
		}
		break
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
// substitutions and heredoc bodies are opaque. A `cd <dir>` earlier in the
// string moves the directory later commands use. A `cd` the hook cannot
// follow (home, `-`, a variable) leaves the directory unknown, and a start
// after it links only when it names an absolute directory itself. A false
// link is worse than a missed one, so anything unclear links nothing.
func IssuesStarts(command, cwd string) []TaskRef {
	var out []TaskRef
	known := true
	for _, argv := range splitCommands(command) {
		for len(argv) > 0 && isAssignment(argv[0]) {
			argv = argv[1:]
		}
		if len(argv) == 0 {
			continue
		}
		if argv[0] == "cd" {
			if len(argv) == 2 && known && !strings.HasPrefix(argv[1], "-") {
				cwd, known = resolveDir(cwd, argv[1])
			} else {
				known = false
			}
			continue
		}
		if ref, ok := parseStart(argv, cwd, known); ok {
			out = append(out, ref)
		}
	}
	return out
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

// opaqueWord reports whether the hook cannot know what a word expands to.
func opaqueWord(w string) bool {
	return strings.HasPrefix(w, "~") || strings.ContainsAny(w, "$`")
}

// resolveDir makes dir absolute against cwd. It reports false for a dir the
// hook cannot expand (~, $VAR, substitutions, empty) and for a relative dir
// when cwd is not an absolute path to resolve it against.
func resolveDir(cwd, dir string) (string, bool) {
	switch {
	case dir == "" || opaqueWord(dir):
		return "", false
	case filepath.IsAbs(dir):
		return filepath.Clean(dir), true
	case cwd == "" || !filepath.IsAbs(cwd):
		return "", false
	}
	return filepath.Join(cwd, dir), true
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
	if argv[0] != "issues" && !strings.HasSuffix(argv[0], "/issues") {
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
	switch {
	case dirSet:
		d, ok := resolveDir(cwd, dir)
		if !ok || !cwdKnown && !filepath.IsAbs(dir) {
			return TaskRef{}, false
		}
		ref.Dir = d
	case cwdKnown:
		ref.Dir = cwd
	default:
		return TaskRef{}, false
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
// substitutions become an opaque `$()` or backtick word, and heredoc bodies
// are skipped.
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
