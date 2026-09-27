package collect

import (
	"testing"
	"time"
)

func TestAgentStateHeuristic(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	later := start.Add(time.Minute)
	previous := PaneSample{PaneID: "%1", Screen: "10", CPUSeconds: 100, At: start}
	agent := func(screen string, cpu int, sleeping bool, last string) SessionCandidate {
		return SessionCandidate{Agent: "codex", PaneID: "%1", Screen: screen, CPUSeconds: cpu, Sleeping: sleeping, LastLine: last}
	}
	cases := []struct {
		name         string
		previous     PaneSample
		havePrevious bool
		current      SessionCandidate
		at           time.Time
		want         string
	}{
		{"first sample stays unknown", PaneSample{}, false, agent("10", 100, true, "›"), later, ""},
		{"samples too close", previous, true, agent("10", 100, true, "›"), start.Add(10 * time.Second), ""},
		{"pane replaced", PaneSample{PaneID: "%9", Screen: "10", At: start}, true, agent("10", 100, true, "›"), later, ""},
		{"shell without agent", previous, true, SessionCandidate{PaneID: "%1", Screen: "10"}, later, ""},
		{"screen changed", previous, true, agent("11", 100, true, "›"), later, AgentWorking},
		{"cpu above five percent", previous, true, agent("10", 104, true, "›"), later, AgentWorking},
		{"cpu at five percent", previous, true, agent("10", 103, true, "Allow? (y/n)"), later, AgentWaiting},
		{"permission prompt", previous, true, agent("10", 100, true, "Run this command? (Y/n)"), later, AgentWaiting},
		{"input prompt", previous, true, agent("10", 100, true, "❯ "), later, AgentWaiting},
		{"prompt but running", previous, true, agent("10", 100, false, "❯"), later, AgentIdle},
		{"quiet output", previous, true, agent("10", 100, true, "done in 3s"), later, AgentIdle},
		{"restarted process", previous, true, agent("10", 3, true, "done"), later, AgentIdle},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AgentState(tc.previous, tc.havePrevious, tc.current, tc.at); got != tc.want {
				t.Fatalf("state = %q, want %q", got, tc.want)
			}
		})
	}
}
