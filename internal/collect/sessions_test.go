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
}

func TestParseSessionsDetectsAgentInChildProcess(t *testing.T) {
	raw := "work\t100\tbash\t/home/helio/project\n--PROCESSES--\n100 1 bash bash\n101 100 node node /opt/claude-code/cli.js\n"
	sessions, err := ParseSessions(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Agent != "claude" {
		t.Fatalf("agent = %+v", sessions)
	}
}

func TestParseSessionsDetectsClaudeNodeEntrypoint(t *testing.T) {
	raw := "work\t100\tbash\t/home/helio/project\n--PROCESSES--\n100 1 bash bash\n101 100 node node /usr/local/bin/claude\n"
	sessions, err := ParseSessions(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Agent != "claude" {
		t.Fatalf("agent = %+v", sessions)
	}
}

func TestParseSessionsKeepsAgentWhenSessionHasAnotherShellPane(t *testing.T) {
	raw := "work\t100\tbash\t/home/helio/project\nwork\t200\tbash\t/home/helio\n--PROCESSES--\n100 1 bash bash\n101 100 codex codex exec\n200 1 bash bash\n"
	sessions, err := ParseSessions(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Agent != "codex" || sessions[0].PanePID != 100 {
		t.Fatalf("multi-pane session = %+v", sessions)
	}
}
