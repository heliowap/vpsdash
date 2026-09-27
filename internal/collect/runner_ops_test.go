package collect

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heliowap/vpsdash/internal/config"
	"github.com/heliowap/vpsdash/internal/store"
	"golang.org/x/crypto/ssh"
)

const opsUnit = "actions.runner.owner--repo-1.service"

// fakeUnits answers bridge commands from a script of results and records
// every request it receives.
type fakeUnits struct {
	mu       sync.Mutex
	calls    []string
	results  []error
	release  chan struct{}
	snapshot string
}

func (f *fakeUnits) RunnerUnitCommand(ctx context.Context, host config.Host, action, unit string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, host.SSHUser+"@"+host.ID+" "+action+" "+unit)
	var result error
	if len(f.results) > 0 {
		result, f.results = f.results[0], f.results[1:]
	}
	release := f.release
	f.mu.Unlock()
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return "", result
}

func (f *fakeUnits) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeUnits) setSnapshot(raw string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshot = raw
}

// Run serves the runner unit snapshot requested after each operation.
func (f *fakeUnits) Run(_ context.Context, _ config.Host, script string) (string, error) {
	if script != RunnerUnitsScript {
		return "", errors.New("unexpected script")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot, nil
}
func (f *fakeUnits) Check(context.Context, config.Host, string, string) (string, error) {
	return "", errors.New("unexpected check")
}
func (f *fakeUnits) Close() {}

func newOpsCollector(t *testing.T) (*Collector, *store.Store, *fakeUnits) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "vpsdash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.UpsertHost(ctx, store.Host{ID: "vps", TailnetName: "vps.example.invalid", Kind: "vps"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{opsUnit, "gh-agents-cleanup.timer"} {
		if _, err := s.UpsertNativeRunnerUnit(ctx, "vps", name); err != nil {
			t.Fatal(err)
		}
	}
	h := config.Host{ID: "vps", TailnetName: "vps.example.invalid", Kind: "vps", SSHUser: "helio", SSHKeyFile: "/keys/helio"}
	c := New(config.Config{Hosts: []config.Host{h}, RunnerUnitHosts: []config.RunnerUnitHost{{HostID: "vps", SSHUser: "gh-agents", SSHKeyFile: "/keys/gh-agents"}}}, s, nil)
	fake := &fakeUnits{snapshot: opsUnit + "\tloaded\tactive\ngh-agents-cleanup.timer\tloaded\tactive\n"}
	c.Executor = fake
	c.Units = fake
	c.ops.poll = 10 * time.Millisecond
	return c, s, fake
}

func waitUnitOp(t *testing.T, c *Collector, s *store.Store, id int64) store.UnitOp {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ops, err := s.LatestUnitOps(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		c.ops.mu.Lock()
		idle := len(c.ops.active) == 0
		c.ops.mu.Unlock()
		for _, op := range ops {
			if op.ID == id && op.Status != "running" && idle {
				return op
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("operation %d did not finish", id)
	return store.UnitOp{}
}

func TestRunnerUnitOpsRejectUnobservedAndReadOnlyUnits(t *testing.T) {
	c, _, fake := newOpsCollector(t)
	ctx := context.Background()
	for _, tc := range []struct{ host, unit, action string }{
		{"vps", "actions.runner.owner--repo-2.service", "restart"},
		{"vps", "gh-agents-cleanup.timer", "restart"},
		{"vps", "actions.runner.x;reboot.service", "drain"},
		{"other", opsUnit, "restart"},
	} {
		if _, err := c.StartRunnerUnitOp(ctx, tc.host, tc.unit, tc.action); !errors.Is(err, ErrUnknownRunnerUnit) {
			t.Fatalf("%+v accepted: %v", tc, err)
		}
	}
	if _, err := c.StartRunnerUnitOp(ctx, "vps", opsUnit, "stop"); err == nil {
		t.Fatal("unknown action accepted")
	}
	if calls := fake.Calls(); len(calls) != 0 {
		t.Fatalf("rejected operations reached the bridge: %v", calls)
	}
}

func TestRunnerUnitRestartUsesRunnerKeyAndAllowsOneOperation(t *testing.T) {
	c, s, fake := newOpsCollector(t)
	ctx := context.Background()
	fake.release = make(chan struct{})
	op, err := c.StartRunnerUnitOp(ctx, "vps", opsUnit, "restart")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartRunnerUnitOp(ctx, "vps", opsUnit, "drain"); !errors.Is(err, ErrUnitOpRunning) {
		t.Fatalf("second operation = %v", err)
	}
	if err := c.CancelDrain("vps", opsUnit); !errors.Is(err, ErrNoDrainRunning) {
		t.Fatalf("restart was cancellable as a drain: %v", err)
	}
	close(fake.release)
	done := waitUnitOp(t, c, s, op.ID)
	if done.Status != "done" || done.Action != "restart" || done.FinishedAt == 0 {
		t.Fatalf("restart audit = %+v", done)
	}
	if calls := fake.Calls(); len(calls) != 1 || calls[0] != "gh-agents@vps runner-restart "+opsUnit {
		t.Fatalf("bridge calls = %v", calls)
	}
}

func TestRunnerUnitRestartFailureIsAudited(t *testing.T) {
	c, s, fake := newOpsCollector(t)
	fake.results = []error{errors.New("ssh vps: exit status 1")}
	op, err := c.StartRunnerUnitOp(context.Background(), "vps", opsUnit, "restart")
	if err != nil {
		t.Fatal(err)
	}
	if done := waitUnitOp(t, c, s, op.ID); done.Status != "failed" || !strings.Contains(done.Detail, "exit status 1") {
		t.Fatalf("failed restart audit = %+v", done)
	}
}

func TestRunnerUnitDrainWaitsForIdleRunnerThenStopsQuietly(t *testing.T) {
	c, s, fake := newOpsCollector(t)
	ctx := context.Background()
	runner := store.Runner{Repo: "owner/repo", ID: 1, Name: RunnerNameForUnit(opsUnit), Status: "online", Busy: true}
	if err := s.UpsertRunner(ctx, runner, time.Now()); err != nil {
		t.Fatal(err)
	}
	fake.results = []error{ErrRunnerJobRunning, nil}
	fake.setSnapshot(opsUnit + "\tloaded\tinactive\ngh-agents-cleanup.timer\tloaded\tactive\n")
	op, err := c.StartRunnerUnitOp(ctx, "vps", opsUnit, "drain")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if calls := fake.Calls(); len(calls) != 0 {
		t.Fatalf("drain contacted the bridge while GitHub reported a job: %v", calls)
	}
	runner.Busy = false
	if err := s.UpsertRunner(ctx, runner, time.Now()); err != nil {
		t.Fatal(err)
	}
	done := waitUnitOp(t, c, s, op.ID)
	if !done.Drained() {
		t.Fatalf("drain audit = %+v", done)
	}
	if calls := fake.Calls(); len(calls) != 2 || calls[1] != "gh-agents@vps runner-drain "+opsUnit {
		t.Fatalf("bridge calls = %v", calls)
	}
	// Planned stops keep their readings but never become project_down alerts.
	for i := 0; i < 3; i++ {
		if err := c.pollRunnerUnits(ctx, c.Config.RunnerUnitHosts[0]); err != nil {
			t.Fatal(err)
		}
	}
	alerts, err := s.PendingAlerts(ctx)
	if err != nil || len(alerts) != 0 {
		t.Fatalf("drained unit alerted: %+v, %v", alerts, err)
	}
	projects, err := s.Projects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range projects {
		if p.Name == opsUnit && (p.CheckOK == nil || *p.CheckOK) {
			t.Fatalf("drained unit reported healthy: %+v", p)
		}
	}
}

func TestRunnerUnitDrainCanBeCancelledOrExpire(t *testing.T) {
	c, s, fake := newOpsCollector(t)
	ctx := context.Background()
	fake.results = []error{ErrRunnerJobRunning, ErrRunnerJobRunning, ErrRunnerJobRunning, ErrRunnerJobRunning, ErrRunnerJobRunning}
	c.ops.poll = time.Hour
	op, err := c.StartRunnerUnitOp(ctx, "vps", opsUnit, "drain")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for len(fake.Calls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if err := c.CancelDrain("vps", opsUnit); err != nil {
		t.Fatal(err)
	}
	if done := waitUnitOp(t, c, s, op.ID); done.Status != "cancelled" || done.Drained() {
		t.Fatalf("cancelled drain = %+v", done)
	}
	c.ops.poll = 5 * time.Millisecond
	c.ops.timeout = 30 * time.Millisecond
	fake.mu.Lock()
	fake.results = nil
	fake.mu.Unlock()
	if err := s.UpsertRunner(ctx, store.Runner{Repo: "owner/repo", ID: 1, Name: RunnerNameForUnit(opsUnit), Status: "online", Busy: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	op, err = c.StartRunnerUnitOp(ctx, "vps", opsUnit, "drain")
	if err != nil {
		t.Fatal(err)
	}
	if done := waitUnitOp(t, c, s, op.ID); done.Status != "expired" || !strings.Contains(done.Detail, "não foi parada") {
		t.Fatalf("expired drain = %+v", done)
	}
	for _, call := range fake.Calls()[1:] {
		if strings.Contains(call, "runner-drain") {
			t.Fatalf("busy runner was stopped: %v", fake.Calls())
		}
	}
}

func TestExecutorMapsBridgeBusyExit(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{NoClientAuth: true}
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	commands := make(chan string, 4)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		server, channels, requests, err := ssh.NewServerConn(connection, serverConfig)
		if err != nil {
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(requests)
		for pending := range channels {
			channel, requests, err := pending.Accept()
			if err != nil {
				return
			}
			go func() {
				defer channel.Close()
				go func() { _, _ = io.Copy(io.Discard, channel) }()
				for request := range requests {
					var exec struct{ Command string }
					if request.Type != "exec" || ssh.Unmarshal(request.Payload, &exec) != nil {
						request.Reply(false, nil)
						continue
					}
					request.Reply(true, nil)
					commands <- exec.Command
					status := uint32(0)
					if strings.HasPrefix(exec.Command, "runner-drain ") {
						_, _ = io.WriteString(channel, "busy\n")
						status = runnerBusyExit
					}
					_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
					return
				}
			}()
		}
	}()
	client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{User: "gh-agents", HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	executor := NewExecutor()
	executor.clients["runner-ops:vps"] = client
	defer executor.Close()
	host := config.Host{ID: "vps"}
	ctx := context.Background()
	if _, err := executor.RunnerUnitCommand(ctx, host, "runner-drain", opsUnit); !errors.Is(err, ErrRunnerJobRunning) {
		t.Fatalf("busy drain = %v", err)
	}
	if _, err := executor.RunnerUnitCommand(ctx, host, "runner-restart", opsUnit); err != nil {
		t.Fatalf("restart = %v", err)
	}
	for _, want := range []string{"runner-drain " + opsUnit, "runner-restart " + opsUnit} {
		if got := <-commands; got != want {
			t.Fatalf("SSH command = %q, want %q", got, want)
		}
	}
	for _, tc := range []struct{ action, unit string }{
		{"runner-stop", opsUnit},
		{"runner-restart", "gh-agents-cleanup.timer"},
		{"runner-restart", opsUnit + " ; reboot"},
	} {
		if _, err := executor.RunnerUnitCommand(ctx, host, tc.action, tc.unit); err == nil {
			t.Fatalf("executor sent %q %q", tc.action, tc.unit)
		}
	}
}
