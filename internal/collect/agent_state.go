package collect

import (
	"strings"
	"time"
)

// Agent session states. The classification is a heuristic, not a contract:
// a wrong state never blocks an action.
const (
	AgentWorking = "working"
	AgentWaiting = "waiting"
	AgentIdle    = "idle"
)

// PaneSample is one observation of an agent pane kept between session polls.
type PaneSample struct {
	PaneID     string
	Screen     string
	CPUSeconds int
	At         time.Time
}

func SampleOf(session SessionCandidate, at time.Time) PaneSample {
	return PaneSample{PaneID: session.PaneID, Screen: session.Screen, CPUSeconds: session.CPUSeconds, At: at}
}

// AgentState compares two observations of the same pane. It returns "" until
// two samples at least 30 s apart exist, so the panel never guesses a state.
func AgentState(previous PaneSample, havePrevious bool, current SessionCandidate, at time.Time) string {
	if current.Agent == "" || current.PaneID == "" || current.Screen == "" {
		return ""
	}
	if !havePrevious || previous.PaneID != current.PaneID || previous.Screen == "" {
		return ""
	}
	elapsed := at.Sub(previous.At)
	if elapsed < 30*time.Second {
		return ""
	}
	cpu := current.CPUSeconds - previous.CPUSeconds
	if cpu < 0 {
		cpu = 0
	}
	if current.Screen != previous.Screen || float64(cpu) > 0.05*elapsed.Seconds() {
		return AgentWorking
	}
	if current.Sleeping && promptLine(current.LastLine) {
		return AgentWaiting
	}
	return AgentIdle
}

func promptLine(line string) bool {
	line = strings.ToLower(strings.TrimRight(line, " \t"))
	for _, marker := range []string{"❯", "›"} {
		if strings.HasSuffix(line, marker) || strings.Contains(line, marker+" ") {
			return true
		}
	}
	return strings.Contains(line, "(y/n)") || strings.Contains(line, "allow?")
}
