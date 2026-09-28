package api

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heliowap/vpsdash/internal/store"
)

const sendKeysPath = "/api/hosts/vps/sessions/send-keys"

// fakeTmux makes the fake SSH server run exec requests through sh -c with a
// tmux stub first in PATH. The stub answers list-panes with two panes and
// records every other invocation's argv, NUL-separated, one line per call.
func fakeTmux(t *testing.T, fx *interactiveFixture) func() [][]string {
	t.Helper()
	bin := filepath.Join(fx.ssh.Dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = list-panes ]; then printf '%%7 4242\n%%8 999\n'; exit 0; fi
if [ -e fail ]; then echo "no server" >&2; exit 1; fi
printf '%s\0' "$@" >> argv.log
printf '\n' >> argv.log
`
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	fx.ssh.HandleExec(func(command string, _ io.Reader, stdout io.Writer) int {
		cmd := exec.Command("sh", "-c", command)
		cmd.Dir = fx.ssh.Dir
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		cmd.Stdout = stdout
		if err := cmd.Run(); err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				return exit.ExitCode()
			}
			return 127
		}
		return 0
	})
	ctx := context.Background()
	if err := fx.store.ReplaceSessions(ctx, "vps", []store.Session{
		{HostID: "vps", Name: "agente 1", PanePID: 4242, Agent: "claude", State: "waiting"},
		{HostID: "vps", Name: "ocioso", PanePID: 999, Agent: "codex", State: "idle"},
		{HostID: "vps", Name: "sem-pane"},
		{HostID: "vps", Name: "sumiu", PanePID: 5555, Agent: "claude", State: "waiting"},
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	return func() [][]string {
		data, _ := os.ReadFile(filepath.Join(fx.ssh.Dir, "argv.log"))
		calls := [][]string{}
		for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			if line != "" {
				calls = append(calls, strings.Split(strings.TrimSuffix(line, "\x00"), "\x00"))
			}
		}
		return calls
	}
}

func sendKeysBody(t *testing.T, value map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestSendKeysRequiresPrivateRouteStepUpCSRFAndConfirmation(t *testing.T) {
	fx := newInteractiveFixture(t)
	calls := fakeTmux(t, fx)
	now := time.Now()
	fx.server.now = func() time.Time { return now }
	body := `{"session":"agente 1","key":"y","confirm":true}`

	public := fx.login(fx.public.URL)
	if response, _ := public.do("POST", sendKeysPath, body, true); response.StatusCode != 404 {
		t.Fatalf("public send-keys = %d, want 404", response.StatusCode)
	}

	anonymous := &client{fx: fx, base: fx.private.URL}
	if response, _ := anonymous.do("POST", sendKeysPath, body, false); response.StatusCode != 401 {
		t.Fatalf("anonymous send-keys = %d", response.StatusCode)
	}
	c := fx.login(fx.private.URL)
	response, data := c.do("POST", sendKeysPath, body, true)
	if response.StatusCode != 403 || !strings.Contains(string(data), "step_up_required") {
		t.Fatalf("send-keys without step-up = %d %s", response.StatusCode, data)
	}
	c.stepUp()
	if response, _ := c.do("POST", sendKeysPath, body, false); response.StatusCode != 403 {
		t.Fatalf("send-keys without CSRF = %d", response.StatusCode)
	}
	if response, _ := c.do("POST", sendKeysPath, `{"session":"agente 1","key":"y"}`, true); response.StatusCode != 400 {
		t.Fatalf("send-keys without confirmation = %d", response.StatusCode)
	}
	if got := calls(); len(got) != 0 {
		t.Fatalf("refused requests reached tmux: %q", got)
	}
	if response, data := c.do("POST", sendKeysPath, body, true); response.StatusCode != 200 || !strings.Contains(string(data), `"state":"waiting"`) {
		t.Fatalf("send-keys = %d %s", response.StatusCode, data)
	}
	now = now.Add(stepUpLifetime + time.Second)
	if response, data := c.do("POST", sendKeysPath, body, true); response.StatusCode != 403 || !strings.Contains(string(data), "step_up_required") {
		t.Fatalf("send-keys after step-up expiry = %d %s", response.StatusCode, data)
	}
	if got := calls(); len(got) != 1 {
		t.Fatalf("tmux calls = %q", got)
	}
	entries := fx.audit()
	denied, ok := 0, 0
	for _, entry := range entries {
		if entry.Action != "send_keys" {
			continue
		}
		if entry.HostID != "vps" || entry.Target != "agente 1" || entry.ClientIP == "" {
			t.Fatalf("audit entry = %+v", entry)
		}
		switch entry.Outcome {
		case "denied":
			denied++
		case "ok":
			ok++
		}
	}
	// Step-up missing, confirmation missing, step-up expired. The public
	// 404 and the CSRF refusal never reach the handler.
	if denied != 3 || ok != 1 {
		t.Fatalf("audit denied=%d ok=%d: %+v", denied, ok, entries)
	}
}

func TestSendKeysRefusesUnknownSessionsAndInvalidReplies(t *testing.T) {
	fx := newInteractiveFixture(t)
	calls := fakeTmux(t, fx)
	c := fx.login(fx.private.URL)
	c.stepUp()
	cases := []struct {
		path string
		body map[string]any
		want int
	}{
		{sendKeysPath, map[string]any{"session": "outra", "key": "y", "confirm": true}, 404},
		{sendKeysPath, map[string]any{"session": "agente", "key": "y", "confirm": true}, 404},
		{sendKeysPath, map[string]any{"session": "agente 1; kill-server", "key": "y", "confirm": true}, 404},
		{sendKeysPath, map[string]any{"session": "", "key": "y", "confirm": true}, 404},
		{"/api/hosts/semchave/sessions/send-keys", map[string]any{"session": "agente 1", "key": "y", "confirm": true}, 404},
		{"/api/hosts/outro/sessions/send-keys", map[string]any{"session": "agente 1", "key": "y", "confirm": true}, 404},
		{sendKeysPath, map[string]any{"session": "agente 1", "confirm": true}, 400},
		{sendKeysPath, map[string]any{"session": "agente 1", "key": "C-c", "confirm": true}, 400},
		{sendKeysPath, map[string]any{"session": "agente 1", "key": "Enter ; kill-server", "confirm": true}, 400},
		{sendKeysPath, map[string]any{"session": "agente 1", "key": "y", "text": "y", "confirm": true}, 400},
		{sendKeysPath, map[string]any{"session": "agente 1", "text": "sim\nrm -rf ~", "confirm": true}, 400},
		{sendKeysPath, map[string]any{"session": "agente 1", "text": "\x1b[A", "confirm": true}, 400},
		{sendKeysPath, map[string]any{"session": "agente 1", "text": strings.Repeat("a", 201), "confirm": true}, 400},
		{sendKeysPath, map[string]any{"session": "agente 1", "key": "y", "argv": []string{"id"}, "confirm": true}, 400},
		{sendKeysPath, map[string]any{"session": "sem-pane", "key": "y", "confirm": true}, 409},
		{sendKeysPath, map[string]any{"session": "sumiu", "key": "y", "confirm": true}, 409},
	}
	for _, tc := range cases {
		if response, data := c.do("POST", tc.path, sendKeysBody(t, tc.body), true); response.StatusCode != tc.want {
			t.Fatalf("%s %v = %d %s, want %d", tc.path, tc.body, response.StatusCode, data, tc.want)
		}
	}
	if got := calls(); len(got) != 0 {
		t.Fatalf("refused replies reached send-keys: %q", got)
	}
	for _, entry := range fx.audit() {
		if entry.Action == "send_keys" && entry.Outcome != "denied" {
			t.Fatalf("unexpected outcome: %+v", entry)
		}
		if strings.Contains(entry.Target, "kill-server") || strings.Contains(entry.Target, "rm -rf") {
			t.Fatalf("audit stored request text: %+v", entry)
		}
	}
}

func TestSendKeysTypesLiteralArgvIntoTheObservedPane(t *testing.T) {
	fx := newInteractiveFixture(t)
	calls := fakeTmux(t, fx)
	c := fx.login(fx.private.URL)
	c.stepUp()
	hostile := `$(touch pwned); "q' ` + "`touch pwned`" + ` -y;`
	requests := []map[string]any{
		{"session": "agente 1", "key": "y", "confirm": true},
		{"session": "agente 1", "key": "Escape", "confirm": true},
		{"session": "agente 1", "text": hostile, "enter": true, "confirm": true},
		{"session": "agente 1", "text": "sem enter", "confirm": true},
		// A session the heuristic reads as idle still receives the reply;
		// the panel warns, the server does not block.
		{"session": "ocioso", "key": "2", "confirm": true},
	}
	for _, body := range requests {
		if response, data := c.do("POST", sendKeysPath, sendKeysBody(t, body), true); response.StatusCode != 200 {
			t.Fatalf("%v = %d %s", body, response.StatusCode, data)
		}
	}
	want := [][]string{
		{"send-keys", "-t", "%7", "-l", "--", "y"},
		{"send-keys", "-t", "%7", "Escape"},
		{"send-keys", "-t", "%7", "-l", "--", `$(touch pwned); "q' ` + "`touch pwned`" + ` -y\;`, ";", "send-keys", "-t", "%7", "Enter"},
		{"send-keys", "-t", "%7", "-l", "--", "sem enter"},
		{"send-keys", "-t", "%8", "-l", "--", "2"},
	}
	if got := calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("tmux argv = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(fx.ssh.Dir, "pwned")); !os.IsNotExist(err) {
		t.Fatal("reply text reached a shell")
	}
	commands := fx.ssh.Commands()
	wantCommands := []string{
		`'tmux' 'list-panes' '-s' '-t' '=agente 1:' '-F' '#{pane_id} #{pane_pid}'`,
		`'tmux' 'send-keys' '-t' '%7' '-l' '--' 'y'`,
		`'tmux' 'list-panes' '-s' '-t' '=agente 1:' '-F' '#{pane_id} #{pane_pid}'`,
		`'tmux' 'send-keys' '-t' '%7' 'Escape'`,
		`'tmux' 'list-panes' '-s' '-t' '=agente 1:' '-F' '#{pane_id} #{pane_pid}'`,
		`'tmux' 'send-keys' '-t' '%7' '-l' '--' '$(touch pwned); "q'\'' ` + "`touch pwned`" + ` -y\;' ';' 'send-keys' '-t' '%7' 'Enter'`,
		`'tmux' 'list-panes' '-s' '-t' '=agente 1:' '-F' '#{pane_id} #{pane_pid}'`,
		`'tmux' 'send-keys' '-t' '%7' '-l' '--' 'sem enter'`,
		`'tmux' 'list-panes' '-s' '-t' '=ocioso:' '-F' '#{pane_id} #{pane_pid}'`,
		`'tmux' 'send-keys' '-t' '%8' '-l' '--' '2'`,
	}
	if !reflect.DeepEqual(commands, wantCommands) {
		t.Fatalf("remote commands = %q\nwant %q", commands, wantCommands)
	}
	for _, user := range fx.ssh.Users() {
		if user != "helio" {
			t.Fatalf("ssh user = %q", user)
		}
	}

	if err := os.WriteFile(filepath.Join(fx.ssh.Dir, "fail"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if response, _ := c.do("POST", sendKeysPath, `{"session":"agente 1","text":"segredo-final","confirm":true}`, true); response.StatusCode != 502 {
		t.Fatalf("failed send = %d", response.StatusCode)
	}
	entries := fx.audit()
	if !hasAudit(entries, "send_keys", "agente 1", "ok") || !hasAudit(entries, "send_keys", "ocioso", "ok") || !hasAudit(entries, "send_keys", "agente 1", "failed") {
		t.Fatalf("audit = %+v", entries)
	}
	_, auditBody := c.do("GET", "/api/audit", "", false)
	for _, secret := range []string{"pwned", "sem enter", "segredo-final", "Escape", `"y"`} {
		if strings.Contains(string(auditBody), secret) {
			t.Fatalf("audit exposes reply content %q: %s", secret, auditBody)
		}
	}
}
