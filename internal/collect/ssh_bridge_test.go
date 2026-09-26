package collect

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHBridgeApprovesRunnerUnitRead(t *testing.T) {
	bridge := filepath.Join("..", "..", "scripts", "ssh-readonly.py")
	contents, err := os.ReadFile(bridge)
	if err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(RunnerUnitsScript)))
	if !strings.Contains(string(contents), `"`+digest+`"`) {
		t.Fatalf("runner unit script digest %s missing from forced SSH command", digest)
	}
}

func TestRunnerBridgeRejectsOtherCollectorReads(t *testing.T) {
	bridge := filepath.Join("..", "..", "scripts", "ssh-readonly.py")
	command := func(script string) ([]byte, error) {
		cmd := exec.Command("python3", "-I", bridge, "runner-units")
		cmd.Env = append(os.Environ(), "SSH_ORIGINAL_COMMAND=sh -s")
		cmd.Stdin = strings.NewReader(script)
		return cmd.CombinedOutput()
	}
	if output, err := command(MetricsScript); err == nil || !strings.Contains(string(output), "comando SSH não permitido") {
		t.Fatalf("runner bridge accepted metrics: %q, %v", output, err)
	}
	output, err := command(RunnerUnitsScript)
	if err != nil && strings.Contains(string(output), "comando SSH não permitido") {
		t.Fatalf("runner unit read was denied: %q, %v", output, err)
	}
}

func runSSHBridge(t *testing.T, command, input string) ([]byte, error) {
	t.Helper()
	bridge := filepath.Join("..", "..", "scripts", "ssh-readonly.py")
	cmd := exec.Command("python3", "-I", bridge)
	cmd.Env = append(os.Environ(), "SSH_ORIGINAL_COMMAND="+command)
	cmd.Stdin = strings.NewReader(input)
	return cmd.CombinedOutput()
}

func TestSSHBridgeAcceptsCollectorReads(t *testing.T) {
	for _, tc := range []struct{ name, script string }{
		{"metrics", MetricsScript},
		{"discovery", DiscoveryScript},
		{"sessions", SessionsScript},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := runSSHBridge(t, "sh -s", tc.script)
			if err != nil {
				t.Fatalf("bridge rejected %s: %v", tc.name, err)
			}
			if tc.name == "metrics" {
				if _, err := ParseMetricsSnapshot(string(output)); err != nil {
					t.Fatalf("metrics output: %v: %s", err, output)
				}
			}
		})
	}
	command, err := healthCommand("systemd", "vpsdash-nonexistent.service")
	if err != nil {
		t.Fatal(err)
	}
	output, err := runSSHBridge(t, command, "")
	if err != nil || strings.TrimSpace(string(output)) != "inactive" {
		t.Fatalf("health read = %q, %v", output, err)
	}
}

func TestSSHBridgeRejectsArbitraryCommands(t *testing.T) {
	target := filepath.Join(t.TempDir(), "written")
	for _, tc := range []struct{ command, input string }{
		{"sh -s", "touch " + target + "\n"},
		{"sh -s", RunnerUnitsScript},
		{"touch " + target, MetricsScript},
	} {
		if output, err := runSSHBridge(t, tc.command, tc.input); err == nil {
			t.Fatalf("bridge accepted %q: %s", tc.command, output)
		}
	}
	health, err := healthCommand("systemd", "missing; touch "+target)
	if err != nil {
		t.Fatal(err)
	}
	if output, err := runSSHBridge(t, health, ""); err != nil || strings.TrimSpace(string(output)) != "inactive" {
		t.Fatalf("health command = %q, %v", output, err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("forbidden command changed the filesystem: %v", err)
	}
}
