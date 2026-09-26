package collect

import (
	"fmt"
	"strconv"
	"strings"
)

type SessionCandidate struct {
	Name    string `json:"name"`
	PanePID int    `json:"pane_pid"`
	CWD     string `json:"cwd"`
	Agent   string `json:"agent,omitempty"`
}

type process struct {
	pid, parent   int
	command, args string
}

func ParseSessions(raw string) ([]SessionCandidate, error) {
	sections := strings.SplitN(raw, "--PROCESSES--", 2)
	if len(sections) != 2 {
		return nil, fmt.Errorf("tmux output has no process section")
	}
	processes := map[int]process{}
	children := map[int][]int{}
	for _, line := range strings.Split(strings.TrimSpace(sections[1]), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		parent, parentErr := strconv.Atoi(fields[1])
		if pidErr != nil || parentErr != nil {
			continue
		}
		processes[pid] = process{pid: pid, parent: parent, command: fields[2], args: strings.Join(fields[3:], " ")}
		children[parent] = append(children[parent], pid)
	}
	result := []SessionCandidate{}
	byName := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(sections[0]), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 4 {
			return nil, fmt.Errorf("invalid tmux pane: %q", line)
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, err
		}
		session := SessionCandidate{Name: fields[0], PanePID: pid, CWD: fields[3]}
		seen := map[int]bool{}
		queue := []int{pid}
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			if seen[current] {
				continue
			}
			seen[current] = true
			if p, ok := processes[current]; ok {
				if agent := detectAgent(p.command, p.args); agent != "" {
					session.Agent = agent
					break
				}
			}
			queue = append(queue, children[current]...)
		}
		if session.Agent == "" {
			session.Agent = detectAgent(fields[2], fields[2])
		}
		if index, exists := byName[session.Name]; exists {
			if result[index].Agent == "" && session.Agent != "" {
				result[index] = session
			}
			continue
		}
		byName[session.Name] = len(result)
		result = append(result, session)
	}
	return result, nil
}

func detectAgent(command, args string) string {
	command = strings.ToLower(command)
	args = strings.ToLower(args)
	switch {
	case command == "opencode" || strings.Contains(args, "opencode serve") || strings.Contains(args, "opencode run") || strings.Contains(args, "opencode github run"):
		return "opencode"
	case command == "codex" || strings.Contains(args, "codex exec") || strings.Contains(args, "codex resume") || strings.Contains(args, "codex app-server"):
		return "codex"
	case command == "claude" || (command == "node" && strings.Contains(args, "claude")) || strings.Contains(args, "@anthropic-ai/claude-code") || strings.Contains(args, "claude-code/cli"):
		return "claude"
	default:
		return ""
	}
}
