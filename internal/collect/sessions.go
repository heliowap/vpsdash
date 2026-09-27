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
	// Inputs for the agent state heuristic; never persisted or served.
	PaneID     string `json:"-"`
	CPUSeconds int    `json:"-"`
	Sleeping   bool   `json:"-"`
	Screen     string `json:"-"`
	LastLine   string `json:"-"`
}

type process struct {
	pid, parent, cpu    int
	stat, command, args string
}

type paneScreen struct{ checksum, lastLine string }

func ParseSessions(raw string) ([]SessionCandidate, error) {
	sections := strings.SplitN(raw, "--PROCESSES--", 2)
	if len(sections) != 2 {
		return nil, fmt.Errorf("tmux output has no process section")
	}
	screens := map[string]paneScreen{}
	if parts := strings.SplitN(sections[1], "--SCREENS--", 2); len(parts) == 2 {
		sections[1] = parts[0]
		for _, line := range strings.Split(parts[1], "\n") {
			fields := strings.SplitN(line, "\t", 3)
			if len(fields) < 2 || fields[0] == "" {
				continue
			}
			screen := paneScreen{checksum: fields[1]}
			if len(fields) == 3 {
				screen.lastLine = fields[2]
			}
			screens[fields[0]] = screen
		}
	}
	processes := map[int]process{}
	children := map[int][]int{}
	for _, line := range strings.Split(strings.TrimSpace(sections[1]), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		parent, parentErr := strconv.Atoi(fields[1])
		cpu, cpuErr := strconv.Atoi(fields[3])
		if pidErr != nil || parentErr != nil || cpuErr != nil {
			continue
		}
		processes[pid] = process{pid: pid, parent: parent, cpu: cpu, stat: fields[2], command: fields[4], args: strings.Join(fields[5:], " ")}
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
		if len(fields) > 4 {
			session.PaneID = fields[4]
			screen := screens[session.PaneID]
			session.Screen, session.LastLine = screen.checksum, screen.lastLine
		}
		agentPID := pid
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
				session.CPUSeconds += p.cpu
				if session.Agent == "" {
					if agent := detectAgent(p.command, p.args); agent != "" {
						session.Agent, agentPID = agent, current
					}
				}
			}
			queue = append(queue, children[current]...)
		}
		if session.Agent == "" {
			session.Agent = detectAgent(fields[2], fields[2])
		}
		session.Sleeping = strings.HasPrefix(processes[agentPID].stat, "S")
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
