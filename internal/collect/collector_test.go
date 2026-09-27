package collect

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
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

type hostProbe struct{ calls map[string]int }

func TestPollHostReportsMissingLock(t *testing.T) {
	c := &Collector{hostLocks: map[string]*sync.Mutex{}, errors: map[string]string{}}
	c.pollHost(context.Background(), config.Host{ID: "missing"}, time.Now(), &hostPollState{})
	_, problems, _ := c.Snapshot()
	if problems["host:missing"] == "" {
		t.Fatal("missing host lock was not reported")
	}
}

func (p *hostProbe) Run(_ context.Context, _ config.Host, script string) (string, error) {
	p.calls[script]++
	switch script {
	case MetricsScript:
		return "malformed metrics", nil
	case DiscoveryScript:
		return "docker\tweb\tactive\n", nil
	case SessionsScript:
		return "session\t12\tsh\t/srv\n--PROCESSES--\n", nil
	default:
		return "", errors.New("unexpected script")
	}
}
func (p *hostProbe) Check(context.Context, config.Host, string, string) (string, error) {
	return "", nil
}
func (p *hostProbe) Close() {}

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

func TestMalformedMetricsDoNotStarveOtherHostCollectorsOrTripCircuit(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "vpsdash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := config.Host{ID: "vps", TailnetName: "vps.example.ts.net", Kind: "vps"}
	if err := s.UpsertHost(ctx, store.Host{ID: h.ID, TailnetName: h.TailnetName, Kind: h.Kind}); err != nil {
		t.Fatal(err)
	}
	c := New(config.Config{Hosts: []config.Host{h}}, s, nil)
	probe := &hostProbe{calls: map[string]int{}}
	c.Executor = probe
	var state hostPollState
	now := time.Now()
	for i := 0; i < 3; i++ {
		c.pollHost(ctx, h, now.Add(time.Duration(i)*time.Minute), &state)
	}
	if probe.calls[MetricsScript] != 3 || probe.calls[DiscoveryScript] != 1 || probe.calls[SessionsScript] != 3 {
		t.Fatalf("host collectors starved after metrics parse failure: %+v", probe.calls)
	}
	_, errs, _ := c.Snapshot()
	if errs["metrics:vps"] == "" || errs["host:vps"] != "" || errs["sessions:vps"] != "" {
		t.Fatalf("collector errors were conflated: %+v", errs)
	}
	if state.circuit.failures != 0 || !state.circuit.openUntil.IsZero() {
		t.Fatalf("parse error opened host circuit: %+v", state.circuit)
	}
	hosts, err := s.Hosts(ctx)
	if err != nil || len(hosts) != 1 || !hosts[0].Online {
		t.Fatalf("reachable host became offline: %+v, %v", hosts, err)
	}
	projects, err := s.Projects(ctx)
	if err != nil || len(projects) != 1 || projects[0].Name != "web" {
		t.Fatalf("discovery did not continue: %+v, %v", projects, err)
	}
	sessions, err := s.Sessions(ctx)
	if err != nil || len(sessions) != 1 || sessions[0].Name != "session" {
		t.Fatalf("sessions did not continue: %+v, %v", sessions, err)
	}
}

func TestProjectSweepDoesNotBlockHostMetricsLock(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "vpsdash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	h := config.Host{ID: "vps", TailnetName: "127.0.0.1", Kind: "vps"}
	if err := st.UpsertHost(ctx, store.Host{ID: h.ID, TailnetName: h.TailnetName, Kind: h.Kind}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertCandidate(ctx, h.ID, "web", "docker"); err != nil {
		t.Fatal(err)
	}
	projects, err := st.Projects(ctx)
	if err != nil || len(projects) != 1 {
		t.Fatalf("projects = %+v, %v", projects, err)
	}
	if err := st.SetMonitored(ctx, projects[0].ID, true, server.URL, ""); err != nil {
		t.Fatal(err)
	}
	projects, err = st.Projects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := New(config.Config{Hosts: []config.Host{h}}, st, nil)
	defer c.Close()
	lock := c.hostLocks[h.ID]
	lock.Lock()
	defer lock.Unlock()
	done := make(chan struct{})
	go func() { c.checkProjects(ctx, projects); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("project sweep waited for the host metrics lock")
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
	history, err := s.ProjectIncidents(ctx, p.ID, time.Now())
	if err != nil || len(history.Incidents) != 1 || history.Incidents[0].State != "open" ||
		history.Incidents[0].FailedChecks != 3 || history.Incidents[0].AlertAt != history.Incidents[0].LastFailureAt {
		t.Fatalf("incident history did not link the queued alert: %+v, %v", history, err)
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

type sessionScreens struct{ screens []string }

func (p *sessionScreens) Run(_ context.Context, _ config.Host, script string) (string, error) {
	if script != SessionsScript {
		return "", errors.New("not collected in this test")
	}
	screen := p.screens[0]
	p.screens = p.screens[1:]
	return "agent\t100\tbash\t/srv\t%1\nshell\t200\tbash\t/srv\t%2\n--PROCESSES--\n100 1 Ss 0 bash bash\n101 100 Sl 9 codex codex exec\n200 1 Ss 0 bash bash\n--SCREENS--\n%1\t" + screen + "\t› \n%2\t7\t$\n", nil
}
func (p *sessionScreens) Check(context.Context, config.Host, string, string) (string, error) {
	return "", nil
}
func (p *sessionScreens) Close() {}

func TestSessionPollsClassifyAgentStateAcrossSamples(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "vpsdash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := config.Host{ID: "vps", TailnetName: "vps.example.ts.net", Kind: "vps"}
	if err := s.UpsertHost(ctx, store.Host{ID: h.ID, TailnetName: h.TailnetName, Kind: h.Kind}); err != nil {
		t.Fatal(err)
	}
	c := New(config.Config{Hosts: []config.Host{h}}, s, nil)
	c.Executor = &sessionScreens{screens: []string{"1", "1", "2"}}
	state := hostPollState{lastMetrics: time.Unix(1<<40, 0), lastDiscovery: time.Unix(1<<40, 0)}
	start := time.Now()
	var observed [][]string
	for i := 0; i < 3; i++ {
		c.pollHost(ctx, h, start.Add(time.Duration(i)*time.Minute), &state)
		sessions, err := s.Sessions(ctx)
		if err != nil {
			t.Fatal(err)
		}
		row := []string{}
		for _, session := range sessions {
			row = append(row, session.Name+"="+session.State)
		}
		observed = append(observed, row)
	}
	want := [][]string{{"agent=", "shell="}, {"agent=waiting", "shell="}, {"agent=working", "shell="}}
	for i := range want {
		if strings.Join(observed[i], ",") != strings.Join(want[i], ",") {
			t.Fatalf("poll %d states = %v, want %v", i, observed[i], want[i])
		}
	}
}
