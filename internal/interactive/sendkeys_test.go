package interactive

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReplyValidation(t *testing.T) {
	valid := []Reply{
		{Key: "Enter"}, {Key: "Escape"}, {Key: "Up"}, {Key: "Down"}, {Key: "Tab"}, {Key: "y"}, {Key: "n"}, {Key: "1"}, {Key: "9"},
		{Text: "sim"}, {Text: "sim", Enter: true}, {Text: "$(touch pwned); `id` 'q' \"q\" \\ ~ *"}, {Text: strings.Repeat("á", MaxReplyText)},
	}
	for _, reply := range valid {
		if err := reply.Validate(); err != nil {
			t.Fatalf("%+v refused: %v", reply, err)
		}
	}
	invalid := []Reply{
		{}, {Key: "y", Text: "y"}, {Key: "C-c"}, {Key: "0"}, {Key: "Y"}, {Key: "enter"}, {Key: "Enter ; kill-server"}, {Key: "y", Enter: true},
		{Key: "F1"}, {Key: "M-x"}, {Key: "yy"},
		{Text: "linha\nsegunda"}, {Text: "a\rb"}, {Text: "\x1b[A"}, {Text: "tab\taqui"}, {Text: "\x00"}, {Text: "\x7f"}, {Text: "\u0085"},
		{Text: "a b"}, {Text: "\xff"}, {Text: strings.Repeat("a", MaxReplyText+1)},
	}
	for _, reply := range invalid {
		if err := reply.Validate(); err == nil {
			t.Fatalf("%+v accepted", reply)
		}
		if _, err := SendKeysArgv("%1", reply); err == nil {
			t.Fatalf("%+v built a command", reply)
		}
	}
}

func TestSendKeysArgv(t *testing.T) {
	cases := []struct {
		reply Reply
		want  []string
	}{
		{Reply{Key: "Enter"}, []string{"tmux", "send-keys", "-t", "%7", "Enter"}},
		{Reply{Key: "Escape"}, []string{"tmux", "send-keys", "-t", "%7", "Escape"}},
		{Reply{Key: "y"}, []string{"tmux", "send-keys", "-t", "%7", "-l", "--", "y"}},
		{Reply{Key: "2"}, []string{"tmux", "send-keys", "-t", "%7", "-l", "--", "2"}},
		{Reply{Text: "-y"}, []string{"tmux", "send-keys", "-t", "%7", "-l", "--", "-y"}},
		{Reply{Text: "Enter"}, []string{"tmux", "send-keys", "-t", "%7", "-l", "--", "Enter"}},
		{Reply{Text: "sim", Enter: true}, []string{"tmux", "send-keys", "-t", "%7", "-l", "--", "sim", ";", "send-keys", "-t", "%7", "Enter"}},
		// tmux would end the command at a trailing ";" and drop it.
		{Reply{Text: "a; kill-server;", Enter: true}, []string{"tmux", "send-keys", "-t", "%7", "-l", "--", `a; kill-server\;`, ";", "send-keys", "-t", "%7", "Enter"}},
		{Reply{Text: `fim\;`}, []string{"tmux", "send-keys", "-t", "%7", "-l", "--", `fim\\;`}},
	}
	for _, c := range cases {
		got, err := SendKeysArgv("%7", c.reply)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%+v = %q, %v; want %q", c.reply, got, err, c.want)
		}
	}
	for _, pane := range []string{"", "=dev:", "%", "%1 ", "%1;", "-t"} {
		if _, err := SendKeysArgv(pane, Reply{Key: "y"}); err == nil {
			t.Fatalf("pane %q accepted", pane)
		}
	}
	if got := ListPanesArgv("agente 1"); !reflect.DeepEqual(got, []string{"tmux", "list-panes", "-s", "-t", "=agente 1:", "-F", "#{pane_id} #{pane_pid}"}) {
		t.Fatalf("list-panes = %q", got)
	}
}

func TestPaneForMatchesTheObservedProcess(t *testing.T) {
	output := "%3 100\n%12 4242\nlixo\n%x 555\n"
	if pane, ok := PaneFor(output, 4242); !ok || pane != "%12" {
		t.Fatalf("pane = %q %v", pane, ok)
	}
	for _, pid := range []int{0, -1, 555, 42, 424} {
		if pane, ok := PaneFor(output, pid); ok {
			t.Fatalf("pid %d matched %q", pid, pane)
		}
	}
}

// TestSendKeysThroughShellAndRealTmux runs the commands through the fake
// SSH server, whose exec goes through sh -c like a login shell, into a real
// tmux server on a private socket. The pane runs cat, which writes exactly
// the bytes tmux typed.
func TestSendKeysThroughShellAndRealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	host, dialer, server := fakeHost(t)
	dir := t.TempDir()
	socket := filepath.Join(dir, "tmux.sock")
	out := filepath.Join(dir, "typed.txt")
	env := []string{}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "TMUX=") && !strings.HasPrefix(value, "TMUX_PANE=") {
			env = append(env, value)
		}
	}
	start := exec.Command("tmux", "-S", socket, "new-session", "-d", "-s", "agente 1", "-x", "200", "-y", "20", "cat > "+Quote(out))
	start.Env = env
	if output, err := start.CombinedOutput(); err != nil {
		t.Fatalf("tmux: %v %s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", socket, "kill-server").Run() })
	withSocket := func(argv []string) []string {
		return append([]string{argv[0], "-S", socket}, argv[1:]...)
	}
	ctx := context.Background()
	run := func(argv []string) Result {
		t.Helper()
		result, err := dialer.Run(ctx, host, withSocket(argv), 5*time.Second, 4096)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("%q = %+v, %v", argv, result, err)
		}
		return result
	}
	panes := run(ListPanesArgv("agente 1"))
	fields := strings.Fields(panes.Output)
	if len(fields) != 2 {
		t.Fatalf("list-panes = %q", panes.Output)
	}
	pane, ok := PaneFor(panes.Output, atoi(t, fields[1]))
	if !ok {
		t.Fatalf("pane not found in %q", panes.Output)
	}
	if result, err := dialer.Run(ctx, host, withSocket(ListPanesArgv("agente")), 5*time.Second, 4096); err != nil || result.ExitCode == 0 {
		t.Fatalf("session prefix matched: %+v %v", result, err)
	}
	hostile := []string{"$(touch pwned)", "`touch pwned`", "a; touch pwned", `it's "q"`, "-y", "fim;", `fim\;`, ";", "~ * $HOME", "Olá ✓", "#{pane_id}"}
	for _, text := range hostile {
		argv, err := SendKeysArgv(pane, Reply{Text: text, Enter: true})
		if err != nil {
			t.Fatal(err)
		}
		run(argv)
	}
	for _, key := range []string{"y", "Enter", "7", "Enter"} {
		argv, _ := SendKeysArgv(pane, Reply{Key: key})
		run(argv)
	}
	want := strings.Join(append(hostile, "y", "7"), "\n") + "\n"
	deadline := time.Now().Add(5 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		got, _ = os.ReadFile(out)
		if string(got) == want {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if string(got) != want {
		t.Fatalf("typed = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(server.Dir, "pwned")); !os.IsNotExist(err) {
		t.Fatal("reply text reached a shell")
	}
}

func atoi(t *testing.T, value string) int {
	t.Helper()
	n := 0
	for _, c := range value {
		if c < '0' || c > '9' {
			t.Fatalf("not a number: %q", value)
		}
		n = n*10 + int(c-'0')
	}
	return n
}
