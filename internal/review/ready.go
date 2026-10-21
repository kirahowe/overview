package review

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// ErrDraft says the session's prompt holds text a paste would mix with.
var ErrDraft = errors.New("the session has a draft in its prompt")

// ErrNoPrompt says the session is not showing an empty chat prompt.
var ErrNoPrompt = errors.New("the session is not at its prompt")

const promptMarker = "❯"

// rule says a line is a box edge: at least 20 of '─' and nothing else.
func rule(line string) bool {
	line = strings.TrimSpace(line)
	return utf8.RuneCountInString(line) >= 20 && strings.Trim(line, "─") == ""
}

// Ready says whether a Claude Code screen, as tmux capture-pane prints it,
// has an empty prompt that a paste can go into. It returns ErrNoPrompt
// when the prompt box is missing or is in another mode, and ErrDraft when
// the box holds text.
func Ready(screen string) error {
	lines := strings.Split(screen, "\n")
	var rules []int
	for i, l := range lines {
		if rule(l) {
			rules = append(rules, i)
		}
	}
	if len(rules) < 2 {
		return ErrNoPrompt
	}
	box := lines[rules[len(rules)-2]+1 : rules[len(rules)-1]]
	if len(box) == 0 || !strings.HasPrefix(box[0], promptMarker) {
		return ErrNoPrompt
	}
	if strings.TrimSpace(strings.TrimPrefix(box[0], promptMarker)) != "" {
		return ErrDraft
	}
	for _, l := range box[1:] {
		if strings.TrimSpace(l) != "" {
			return ErrDraft
		}
	}
	return nil
}
