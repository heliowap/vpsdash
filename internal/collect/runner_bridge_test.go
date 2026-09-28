package collect

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runnerBridgeHarness runs the real bridge main() with its systemctl, unit
// directory, and cgroup/proc roots redirected to a fixture. Production code
// has no environment override for these paths.
type runnerBridgeHarness struct {
	root, units, log string
}

func newRunnerBridgeHarness(t *testing.T, controlGroup string) runnerBridgeHarness {
	t.Helper()
	root := t.TempDir()
	h := runnerBridgeHarness{root: root, units: filepath.Join(root, "home", ".config", "systemd", "user"), log: filepath.Join(root, "systemctl.log")}
	if err := os.MkdirAll(h.units, 0700); err != nil {
		t.Fatal(err)
	}
	fake := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + h.log + "'\nif [ \"$2\" = show ]; then printf '%s\\n' '" + controlGroup + "'; fi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(root, "systemctl"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h runnerBridgeHarness) addUnit(t *testing.T, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(h.units, name), nil, 0600); err != nil {
		t.Fatal(err)
	}
}

// addCgroup lists processes in the unit cgroup under a fake /proc.
func (h runnerBridgeHarness) addCgroup(t *testing.T, group string, commands map[string]string) {
	t.Helper()
	dir := filepath.Join(h.root, "cgroup", strings.TrimPrefix(group, "/"))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	pids := ""
	for pid, comm := range commands {
		pids += pid + "\n"
		if err := os.MkdirAll(filepath.Join(h.root, "proc", pid), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(h.root, "proc", pid, "comm"), []byte(comm+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(pids), 0600); err != nil {
		t.Fatal(err)
	}
}

const runnerBridgeHarnessScript = `import importlib.util, sys
spec = importlib.util.spec_from_file_location("bridge", sys.argv[1])
bridge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bridge)
root = sys.argv[2]
bridge.SYSTEMCTL = root + "/systemctl"
bridge.CGROUP_ROOT = root + "/cgroup"
bridge.PROC_ROOT = root + "/proc"
bridge.runner_unit_dir = lambda: root + "/home/.config/systemd/user"
sys.argv = ["vpsdash-ssh-readonly.py"] + sys.argv[3:]
sys.exit(bridge.main())
`

func (h runnerBridgeHarness) run(t *testing.T, mode, command string) (string, int) {
	t.Helper()
	script := filepath.Join(h.root, "harness.py")
	if err := os.WriteFile(script, []byte(runnerBridgeHarnessScript), 0600); err != nil {
		t.Fatal(err)
	}
	bridge, err := filepath.Abs(filepath.Join("..", "..", "scripts", "ssh-readonly.py"))
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-I", "-B", script, bridge, h.root}
	if mode != "" {
		args = append(args, mode)
	}
	cmd := exec.Command("python3", args...)
	cmd.Env = append(os.Environ(), "SSH_ORIGINAL_COMMAND="+command)
	output, err := cmd.CombinedOutput()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(output), code
}

func (h runnerBridgeHarness) calls(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(h.log)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

const bridgeUnit = "actions.runner.owner--repo-1.service"

func TestRunnerBridgeRestartsKnownUnit(t *testing.T) {
	h := newRunnerBridgeHarness(t, "")
	h.addUnit(t, bridgeUnit)
	output, code := h.run(t, "runner-units", "runner-restart "+bridgeUnit)
	if code != 0 || strings.TrimSpace(output) != "restarted" {
		t.Fatalf("restart = %q, exit %d", output, code)
	}
	if got := h.calls(t); got != "--user restart -- "+bridgeUnit+"\n" {
		t.Fatalf("systemctl calls = %q", got)
	}
}

func TestRunnerBridgeDrainWaitsForRunnerWorker(t *testing.T) {
	group := "/user.slice/user-1001.slice/user@1001.service/app.slice/" + bridgeUnit
	h := newRunnerBridgeHarness(t, group)
	h.addUnit(t, bridgeUnit)
	h.addCgroup(t, group, map[string]string{"41": "Runner.Listener", "42": "Runner.Worker"})
	output, code := h.run(t, "runner-units", "runner-drain "+bridgeUnit)
	if code != 75 || strings.TrimSpace(output) != "busy" {
		t.Fatalf("drain with a job = %q, exit %d", output, code)
	}
	if strings.Contains(h.calls(t), "stop") {
		t.Fatalf("busy drain stopped the unit: %q", h.calls(t))
	}
	h.addCgroup(t, group, map[string]string{"41": "Runner.Listener"})
	output, code = h.run(t, "runner-units", "runner-drain "+bridgeUnit)
	if code != 0 || strings.TrimSpace(output) != "stopped" {
		t.Fatalf("idle drain = %q, exit %d", output, code)
	}
	if !strings.HasSuffix(h.calls(t), "--user stop -- "+bridgeUnit+"\n") {
		t.Fatalf("systemctl calls = %q", h.calls(t))
	}
}

func TestRunnerBridgeDrainStopsInactiveUnit(t *testing.T) {
	h := newRunnerBridgeHarness(t, "")
	h.addUnit(t, bridgeUnit)
	output, code := h.run(t, "runner-units", "runner-drain "+bridgeUnit)
	if code != 0 || strings.TrimSpace(output) != "stopped" {
		t.Fatalf("drain of an inactive unit = %q, exit %d", output, code)
	}
}

func TestRunnerBridgeDrainFailsClosedOnUnreadableCgroup(t *testing.T) {
	h := newRunnerBridgeHarness(t, "/user.slice/missing.service")
	h.addUnit(t, bridgeUnit)
	output, code := h.run(t, "runner-units", "runner-drain "+bridgeUnit)
	if code == 0 || strings.Contains(h.calls(t), "stop") {
		t.Fatalf("drain without a job reading stopped the unit: %q, exit %d, calls %q", output, code, h.calls(t))
	}
}

func TestRunnerBridgeRejectsUnknownUnitsAndInjection(t *testing.T) {
	h := newRunnerBridgeHarness(t, "")
	h.addUnit(t, bridgeUnit)
	h.addUnit(t, "gh-agents-cleanup.timer")
	h.addUnit(t, "other.service")
	target := filepath.Join(h.root, "written")
	for _, command := range []string{
		"runner-restart actions.runner.owner--repo-2.service",
		"runner-restart gh-agents-cleanup.timer",
		"runner-restart other.service",
		"runner-restart " + bridgeUnit + " extra",
		"runner-restart  " + bridgeUnit,
		"runner-restart " + bridgeUnit + ";touch " + target,
		"runner-restart actions.runner.$(touch " + target + ").service",
		"runner-restart actions.runner.../../x.service",
		"runner-restart -H actions.runner.x.service",
		"runner-restart " + bridgeUnit + "\n",
		"runner-stop " + bridgeUnit,
		"runner-restart",
		"sh -c 'touch " + target + "'",
	} {
		output, code := h.run(t, "runner-units", command)
		if code == 0 || !strings.Contains(output, "comando SSH não permitido") {
			t.Fatalf("bridge accepted %q: %q, exit %d", command, output, code)
		}
	}
	if calls := h.calls(t); calls != "" {
		t.Fatalf("rejected commands reached systemctl: %q", calls)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("rejected command changed the filesystem: %v", err)
	}
}

func TestCollectorBridgeKeyCannotOperateRunnerUnits(t *testing.T) {
	h := newRunnerBridgeHarness(t, "")
	h.addUnit(t, bridgeUnit)
	for _, command := range []string{"runner-restart " + bridgeUnit, "runner-drain " + bridgeUnit} {
		output, code := h.run(t, "", command)
		if code == 0 || !strings.Contains(output, "comando SSH não permitido") {
			t.Fatalf("helio collector key accepted %q: %q, exit %d", command, output, code)
		}
	}
	if calls := h.calls(t); calls != "" {
		t.Fatalf("collector key reached systemctl: %q", calls)
	}
}
