package interactive

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heliowap/vpsdash/internal/config"
	"github.com/heliowap/vpsdash/internal/interactive/sshtest"
)

func fakeHost(t *testing.T) (config.Host, Dialer, *sshtest.Server) {
	t.Helper()
	dir := t.TempDir()
	key := filepath.Join(dir, "id_ed25519_interactive")
	public, err := sshtest.GenerateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(dir, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	server, err := sshtest.Start(public, work)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	known, err := server.WriteKnownHosts(dir)
	if err != nil {
		t.Fatal(err)
	}
	host := config.Host{ID: "vps", TailnetName: "127.0.0.1", Kind: "vps", SSHUser: "helio", SSHPort: server.Port(), InteractiveKeyFile: key}
	return host, Dialer{KnownHostsFile: known, Timeout: 5 * time.Second}, server
}

func TestRunKeepsArgvLiteral(t *testing.T) {
	host, dialer, server := fakeHost(t)
	hostile := []string{"$(touch pwned)", "`touch pwned`", "a; touch pwned", "it's", "*", "~", "a b", "FOO=bar", "\\$HOME"}
	argv := append([]string{"printf", "%s\\n"}, hostile...)
	result, err := dialer.Run(context.Background(), host, argv, 5*time.Second, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.TimedOut {
		t.Fatalf("result = %+v", result)
	}
	if got := strings.Split(strings.TrimSuffix(result.Output, "\n"), "\n"); strings.Join(got, "|") != strings.Join(hostile, "|") {
		t.Fatalf("remote argv = %q, want %q", got, hostile)
	}
	if _, err := os.Stat(filepath.Join(server.Dir, "pwned")); !os.IsNotExist(err) {
		t.Fatalf("snippet argument reached a shell: %v", err)
	}
	if users := server.Users(); len(users) == 0 || users[0] != "helio" {
		t.Fatalf("ssh user = %v", users)
	}
}

func TestRunCapsOutputAndReportsExitAndTimeout(t *testing.T) {
	host, dialer, _ := fakeHost(t)
	result, err := dialer.Run(context.Background(), host, []string{"sh", "-c", "yes x | head -c 5000; exit 3"}, 5*time.Second, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Output) != 100 || !result.Truncated || result.ExitCode != 3 {
		t.Fatalf("result = len %d truncated %t exit %d", len(result.Output), result.Truncated, result.ExitCode)
	}
	started := time.Now()
	result, err = dialer.Run(context.Background(), host, []string{"sleep", "5"}, 300*time.Millisecond, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !result.TimedOut || time.Since(started) > 3*time.Second {
		t.Fatalf("timeout result = %+v after %s", result, time.Since(started))
	}
}

func TestDialRejectsUnknownHostKey(t *testing.T) {
	host, dialer, _ := fakeHost(t)
	other := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(other, []byte("[127.0.0.1]:1 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dialer.KnownHostsFile = other
	if _, err := dialer.Run(context.Background(), host, []string{"true"}, time.Second, 10); err == nil {
		t.Fatal("unknown host key accepted")
	}
	host.InteractiveKeyFile = ""
	if _, err := dialer.Dial(context.Background(), host); err != ErrDisabled {
		t.Fatalf("host without interactive key = %v", err)
	}
}

func TestPTYEchoResizeAndAttachCommand(t *testing.T) {
	host, dialer, server := fakeHost(t)
	pty, err := dialer.OpenPTY(context.Background(), host, nil, 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer pty.Close()
	if err := pty.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	if _, err := pty.Write([]byte("olá\rexit\r")); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(pty)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "eco: olá") || !strings.Contains(string(output), "logout") {
		t.Fatalf("pty output = %q", output)
	}
	select {
	case <-pty.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("pty did not finish")
	}
	if got := server.PTYs(); len(got) != 1 || got[0] != "xterm-256color 100x30" {
		t.Fatalf("pty requests = %v", got)
	}
	if got := server.Resizes(); len(got) != 1 || got[0] != (sshtest.Resize{Cols: 120, Rows: 40}) {
		t.Fatalf("resizes = %v", got)
	}

	attach, err := dialer.OpenPTY(context.Background(), host, AttachArgv("agente 'x'", true), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	_ = attach.Close()
	commands := server.Commands()
	if len(commands) != 1 || commands[0] != `'tmux' 'attach-session' '-r' '-t' '=agente '\''x'\'''` {
		t.Fatalf("attach command = %q", commands)
	}
}

func TestStalledChannelOpenRespectsDeadline(t *testing.T) {
	host, dialer, server := fakeHost(t)
	server.StallChannels(true)

	started := time.Now()
	if _, err := dialer.Run(context.Background(), host, []string{"true"}, time.Second, 10); err == nil {
		t.Fatal("snippet with a stalled channel succeeded")
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("snippet waited %s for a stalled channel", elapsed)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started = time.Now()
	if pty, err := dialer.OpenPTY(ctx, host, nil, 80, 24); err == nil {
		_ = pty.Close()
		t.Fatal("terminal with a stalled channel opened")
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("terminal waited %s for a stalled channel", elapsed)
	}
	if server.Stalled() != 2 {
		t.Fatalf("stalled channel opens = %d, want 2", server.Stalled())
	}

	// A caller without a deadline is still bounded by the dialer timeout.
	dialer.Timeout = time.Second
	started = time.Now()
	if pty, err := dialer.OpenPTY(context.Background(), host, nil, 80, 24); err == nil {
		_ = pty.Close()
		t.Fatal("terminal with a stalled channel opened")
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("terminal without deadline waited %s", elapsed)
	}
}

func TestValidSessionName(t *testing.T) {
	for name, valid := range map[string]bool{"dev": true, "agente 1": true, "": false, "a\nb": false, "a\x1b[31m": false, strings.Repeat("a", 257): false} {
		if ValidSessionName(name) != valid {
			t.Fatalf("ValidSessionName(%q) = %t", name, !valid)
		}
	}
}
