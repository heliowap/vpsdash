package collect

import "testing"

func TestParseSessionsFromVPSFixture(t *testing.T) {
	sessions, err := ParseSessions(fixture(t, "tmux.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Name != "fixture" || sessions[0].Agent != "" {
		t.Fatalf("sessions = %+v", sessions)
	}
	got := sessions[0]
	if got.PaneID != "%0" || got.Screen != "26582678" || got.LastLine != "Allow this command? (y/n)" || !got.Sleeping {
		t.Fatalf("pane screen = %+v", got)
	}
}

func TestParseSessionsSumsPaneTreeCPUAndReadsAgentState(t *testing.T) {
	raw := "work\t100\tbash\t/srv\t%3\n--PROCESSES--\n100 1 Ss 2 bash bash\n101 100 Rl 40 node node /usr/local/bin/claude\n102 101 S 5 rg rg TODO\n--SCREENS--\n%3\t991\t\u276f \n"
	sessions, err := ParseSessions(raw)
	if err != nil {
		t.Fatal(err)
	}
	got := sessions[0]
	if got.Agent != "claude" || got.CPUSeconds != 47 || got.Sleeping || got.Screen != "991" || got.LastLine != "\u276f " {
		t.Fatalf("session = %+v", got)
	}
}

func TestParseSessionsDetectsAgentInChildProcess(t *testing.T) {
	raw := "work\t100\tbash\t/home/helio/project\n--PROCESSES--\n100 1 Ss 0 bash bash\n101 100 Sl 0 node node /opt/claude-code/cli.js\n"
	sessions, err := ParseSessions(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Agent != "claude" {
		t.Fatalf("agent = %+v", sessions)
	}
}

func TestParseSessionsDetectsClaudeNodeEntrypoint(t *testing.T) {
	raw := "work\t100\tbash\t/home/helio/project\n--PROCESSES--\n100 1 Ss 0 bash bash\n101 100 Sl 0 node node /usr/local/bin/claude\n"
	sessions, err := ParseSessions(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Agent != "claude" {
		t.Fatalf("agent = %+v", sessions)
	}
}

func TestParseSessionsKeepsAgentWhenSessionHasAnotherShellPane(t *testing.T) {
	raw := "work\t100\tbash\t/home/helio/project\nwork\t200\tbash\t/home/helio\n--PROCESSES--\n100 1 Ss 0 bash bash\n101 100 Sl 0 codex codex exec\n200 1 Ss 0 bash bash\n"
	sessions, err := ParseSessions(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Agent != "codex" || sessions[0].PanePID != 100 {
		t.Fatalf("multi-pane session = %+v", sessions)
	}
}
