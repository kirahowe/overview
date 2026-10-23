package fleet

import (
	"encoding/json"
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

// LinkTasks records the issues an event shows the session taking on. It is
// separate from Apply because subagents link tasks to their parent session
// without changing its state. Linking a ref the session already has moves it
// to the end, and the result only differs from s when the list does.
func LinkTasks(s Session, ev HookEvent) (Session, bool) {
	cwd := ev.Cwd
	if cwd == "" {
		cwd = s.Cwd
	}
	var refs []TaskRef
	switch ev.Name {
	case "UserPromptSubmit":
		if r, ok := OpeningTask(ev.Prompt, cwd); ok {
			refs = []TaskRef{r}
		}
	case "PostToolUse":
		if ev.ToolName != "Bash" {
			return s, false
		}
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(ev.ToolInput, &in) != nil {
			return s, false
		}
		refs = IssuesStarts(in.Command, cwd)
	}
	if len(refs) == 0 {
		return s, false
	}
	tasks := append([]TaskRef(nil), s.Tasks...)
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
	if sameTasks(tasks, s.Tasks) {
		return s, false
	}
	s.Tasks = tasks
	return s, true
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
// reader for the common shapes, not a shell: no expansion, no subshells. A
// `cd <dir>` earlier in the string moves the directory later commands use.
func IssuesStarts(command, cwd string) []TaskRef {
	var out []TaskRef
	for _, argv := range splitCommands(command) {
		for len(argv) > 0 && isAssignment(argv[0]) {
			argv = argv[1:]
		}
		if len(argv) == 0 {
			continue
		}
		if argv[0] == "cd" {
			if len(argv) == 2 {
				cwd = resolveDir(cwd, argv[1], cwd)
			}
			continue
		}
		if ref, ok := parseStart(argv, cwd); ok {
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

// resolveDir makes dir absolute against cwd. A home-relative or empty dir
// gives fallback, since the hook cannot expand ~ and must not guess.
func resolveDir(cwd, dir, fallback string) string {
	switch {
	case dir == "" || dir == "~" || strings.HasPrefix(dir, "~/"):
		return fallback
	case filepath.IsAbs(dir):
		return filepath.Clean(dir)
	case cwd == "" || !filepath.IsAbs(cwd):
		return fallback
	}
	return filepath.Join(cwd, dir)
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

func parseStart(argv []string, cwd string) (TaskRef, bool) {
	if argv[0] != "issues" && !strings.HasSuffix(argv[0], "/issues") {
		return TaskRef{}, false
	}
	var project, dir string
	// apply consumes a flag at args[i] and returns the next index, or -1
	// when the word is not a known flag.
	apply := func(args []string, i int) int {
		w := args[i]
		switch w {
		case "--json", "--edn", "--all":
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
			dir = val
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
		if id != 0 || strings.HasPrefix(args[i], "-") {
			// Anything unknown after the id is the command's own business.
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
	if id == 0 {
		return TaskRef{}, false
	}
	return TaskRef{Project: project, Dir: resolveDir(cwd, dir, cwd), ID: id}, true
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
// newlines outside quotes, and each into words with basic quoting.
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
				if r[i] == '\\' && i+1 < len(r) && (r[i+1] == '"' || r[i+1] == '\\') {
					i++
				}
				word.WriteRune(r[i])
			}
		case c == '\\':
			if i+1 < len(r) {
				i++
				if r[i] != '\n' {
					inWord = true
					word.WriteRune(r[i])
				}
			}
		case c == '&' || c == '|' || c == ';' || c == '\n':
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
