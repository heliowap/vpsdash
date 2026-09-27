package collect

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/heliowap/vpsdash/internal/config"
	"github.com/heliowap/vpsdash/internal/githubapp"
	"github.com/heliowap/vpsdash/internal/store"
)

type fleetProbe struct {
	fail    bool
	runners []githubapp.Runner
}

func (f *fleetProbe) Runners(context.Context, string) ([]githubapp.Runner, error) {
	if f.fail {
		return nil, errors.New("GitHub unavailable")
	}
	return f.runners, nil
}

func TestFleetStoreErrorRemainsVisibleAfterQueueRead(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "vpsdash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	probe := &fleetProbe{runners: []githubapp.Runner{{ID: 1, Name: "runner-1", Status: "online"}}}
	c := New(config.Config{Repositories: []config.Repository{{Name: "heliowap/vpsdash"}}}, s, probe)
	defer c.Close()
	c.pollFleet(context.Background())
	_, errs, _ := c.Snapshot()
	if errs["github:heliowap/vpsdash"] == "" {
		t.Fatal("runner storage failure was cleared after successful queue read")
	}
}
func (f *fleetProbe) Runs(context.Context, string, string) ([]githubapp.WorkflowRun, error) {
	return []githubapp.WorkflowRun{}, nil
}
func (f *fleetProbe) Jobs(context.Context, string, int64) ([]githubapp.WorkflowJob, error) {
	return []githubapp.WorkflowJob{}, nil
}

func TestFleetTimestampRequiresSuccessfulRead(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "vpsdash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	probe := &fleetProbe{fail: true}
	c := New(config.Config{Repositories: []config.Repository{{Name: "heliowap/vpsdash"}}}, s, probe)
	defer c.Close()
	c.pollFleet(context.Background())
	_, _, at := c.Snapshot()
	if !at.IsZero() {
		t.Fatalf("failed fleet poll recorded at %v", at)
	}
	probe.fail = false
	c.pollFleet(context.Background())
	_, _, at = c.Snapshot()
	if at.IsZero() {
		t.Fatal("successful empty fleet poll did not record observation")
	}
}

func TestHostFailuresSurviveIdleTicks(t *testing.T) {
	now := time.Unix(1000, 0)
	var state hostCircuit
	state.record(true, errors.New("ssh timeout"), now)
	state.record(false, nil, now.Add(10*time.Second))
	state.record(true, errors.New("ssh timeout"), now.Add(time.Minute))
	state.record(false, nil, now.Add(70*time.Second))
	state.record(true, errors.New("ssh timeout"), now.Add(2*time.Minute))
	if want := now.Add(7 * time.Minute); !state.openUntil.Equal(want) {
		t.Fatalf("circuit open until %v, want %v", state.openUntil, want)
	}
	state.record(true, nil, now.Add(8*time.Minute))
	if state.failures != 0 {
		t.Fatalf("failures after recovery = %d", state.failures)
	}
}

func TestTailnetPresenceDoesNotOverrideVPSSSHState(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "vpsdash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := config.Host{ID: "vps", TailnetName: "vps.example.ts.net", Kind: "vps", Local: true}
	c := New(config.Config{Hosts: []config.Host{h}}, s, nil)
	defer c.Close()
	if err := s.UpsertHost(ctx, store.Host{ID: h.ID, TailnetName: h.TailnetName, Kind: h.Kind}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHostPresence(ctx, h.ID, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := c.saveDevices(ctx, []Device{{ID: h.TailnetName, Online: true}}); err != nil {
		t.Fatal(err)
	}
	hosts, err := s.Hosts(ctx)
	if err != nil || len(hosts) != 1 || hosts[0].Online {
		t.Fatalf("SSH unreachable host changed by tailnet: %+v, %v", hosts, err)
	}
	if _, _, err := c.pollMetrics(ctx, h, MetricsSnapshot{}, false); err != nil {
		t.Fatal(err)
	}
	hosts, err = s.Hosts(ctx)
	if err != nil || len(hosts) != 1 || !hosts[0].Online {
		t.Fatalf("successful SSH poll did not restore host: %+v, %v", hosts, err)
	}
	if hosts[0].Latest == nil || hosts[0].Latest.CPU != nil {
		t.Fatalf("first CPU sample should be unknown: %+v", hosts[0].Latest)
	}
}

func TestUnavailableProjectProbeDoesNotQueueDownAlert(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "vpsdash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := config.Host{ID: "vps", TailnetName: "vps.example.ts.net", Kind: "vps", SSHKeyFile: filepath.Join(t.TempDir(), "missing-key")}
	if err := s.UpsertHost(ctx, store.Host{ID: h.ID, TailnetName: h.TailnetName, Kind: h.Kind}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCandidate(ctx, h.ID, "example.service", "systemd"); err != nil {
		t.Fatal(err)
	}
	projects, err := s.Projects(ctx)
	if err != nil || len(projects) != 1 {
		t.Fatalf("projects = %+v, %v", projects, err)
	}
	p := projects[0]
	if err := s.SetMonitored(ctx, p.ID, true, "", ""); err != nil {
		t.Fatal(err)
	}
	c := New(config.Config{Hosts: []config.Host{h}}, s, nil)
	defer c.Close()
	for i := 0; i < 3; i++ {
		c.checkProject(ctx, h, p)
	}
	projects, err = s.Projects(ctx)
	if err != nil || projects[0].CheckOK != nil {
		t.Fatalf("unavailable SSH probe recorded a project failure: %+v, %v", projects, err)
	}
	alerts, err := s.PendingAlerts(ctx)
	if err != nil || len(alerts) != 0 {
		t.Fatalf("unavailable SSH probe queued alerts: %+v, %v", alerts, err)
	}
	_, collectorErrors, _ := c.Snapshot()
	if collectorErrors["check:vps/example.service"] == "" {
		t.Fatal("SSH failure was not exposed as a collector error")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	p.HealthURL = server.URL
	c.healthHTTP = newHealthHTTPClient([]config.Host{{TailnetName: "127.0.0.1"}})
	for i := 0; i < 3; i++ {
		c.checkProject(ctx, h, p)
	}
	projects, err = s.Projects(ctx)
	if err != nil || projects[0].CheckOK == nil || *projects[0].CheckOK {
		t.Fatalf("observed HTTP failure was not recorded: %+v, %v", projects, err)
	}
	alerts, err = s.PendingAlerts(ctx)
	if err != nil || len(alerts) != 1 || alerts[0].Subject != "vps / example.service" {
		t.Fatalf("confirmed failures did not queue one alert: %+v, %v", alerts, err)
	}
	_, collectorErrors, _ = c.Snapshot()
	if collectorErrors["check:vps/example.service"] != "" {
		t.Fatalf("collector error persisted after a successful probe: %+v", collectorErrors)
	}
}

func TestHTTPHealthConnectionFailureIsAConfirmedFailure(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	client := newHealthHTTPClient([]config.Host{{TailnetName: "127.0.0.1"}})
	ok, detail, err := checkHTTP(context.Background(), client, url)
	if err != nil || ok || detail == "" {
		t.Fatalf("unreachable health URL = ok:%t detail:%q err:%v", ok, detail, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = checkHTTP(ctx, client, url)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled collector probe = %v, want context cancellation", err)
	}
}
