package interactive

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxReplyText bounds free text sent to an agent prompt, in characters.
const MaxReplyText = 200

// namedKeys and literalKeys are the fixed allowlist of quick replies.
// Named keys are tmux key names; literal keys are single printable
// characters sent with send-keys -l so tmux never reads them as key names.
// C-c is left out on purpose: it interrupts the agent instead of answering
// it, and Escape already dismisses a prompt.
var (
	namedKeys   = map[string]bool{"Enter": true, "Escape": true, "Up": true, "Down": true, "Tab": true}
	literalKeys = map[string]bool{"y": true, "n": true, "1": true, "2": true, "3": true, "4": true, "5": true, "6": true, "7": true, "8": true, "9": true}
)

// Reply is one answer to an agent prompt: either an allowlisted key or a
// single line of literal text, optionally followed by Enter.
type Reply struct {
	Key   string
	Text  string
	Enter bool
}

var (
	ErrReplyEmpty = errors.New("reply needs a key or text")
	ErrReplyBoth  = errors.New("reply takes a key or text, not both")
	ErrReplyKey   = errors.New("key is not in the reply allowlist")
	ErrReplyText  = errors.New("reply text must be one printable line of at most 200 characters")
)

// Validate refuses anything outside the allowlist or the free-text rules.
func (r Reply) Validate() error {
	switch {
	case r.Key == "" && r.Text == "":
		return ErrReplyEmpty
	case r.Key != "" && r.Text != "":
		return ErrReplyBoth
	case r.Key != "":
		if !(namedKeys[r.Key] || literalKeys[r.Key]) || r.Enter {
			return ErrReplyKey
		}
		return nil
	}
	if !utf8.ValidString(r.Text) || utf8.RuneCountInString(r.Text) > MaxReplyText {
		return ErrReplyText
	}
	for _, c := range r.Text {
		// Control characters (C0, DEL, C1) and line or paragraph separators
		// could submit or reshape the prompt; the reply stays one line.
		if unicode.IsControl(c) || c == ' ' || c == ' ' {
			return ErrReplyText
		}
	}
	return nil
}

// ListPanesArgv lists the panes of exactly one tmux session. list-panes -s
// resolves -t like a window target, so the exact-match form needs both the
// "=" prefix and the trailing ":"; without the colon tmux falls back to a
// session-name prefix match.
func ListPanesArgv(session string) []string {
	return []string{"tmux", "list-panes", "-s", "-t", "=" + session + ":", "-F", "#{pane_id} #{pane_pid}"}
}

var paneID = regexp.MustCompile(`^%[0-9]+$`)

// PaneFor returns the pane ID whose process is pid in list-panes output, so
// the reply reaches the pane the collector observed, not whichever pane is
// active. tmux never reuses a pane ID while its server runs.
func PaneFor(output string, pid int) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	want := strconv.Itoa(pid)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == want && paneID.MatchString(fields[0]) {
			return fields[0], true
		}
	}
	return "", false
}

// tmuxLiteral protects a literal argument from tmux's own argument parser:
// tmux ends a command at an argument whose last character is ";" (and
// drops it), unless it ends in "\;", which becomes ";".
func tmuxLiteral(text string) string {
	if strings.HasSuffix(text, ";") {
		return text[:len(text)-1] + `\;`
	}
	return text
}

// SendKeysArgv builds the tmux command for a validated reply to pane. Text
// goes after "--" with -l, so it is typed literally and never parsed as an
// option or key name; Enter is a second send-keys only when requested.
func SendKeysArgv(pane string, reply Reply) ([]string, error) {
	if !paneID.MatchString(pane) {
		return nil, errors.New("invalid pane id")
	}
	if err := reply.Validate(); err != nil {
		return nil, err
	}
	argv := []string{"tmux", "send-keys", "-t", pane}
	if reply.Key != "" {
		if literalKeys[reply.Key] {
			return append(argv, "-l", "--", reply.Key), nil
		}
		return append(argv, reply.Key), nil
	}
	argv = append(argv, "-l", "--", tmuxLiteral(reply.Text))
	if reply.Enter {
		argv = append(argv, ";", "send-keys", "-t", pane, "Enter")
	}
	return argv, nil
}
