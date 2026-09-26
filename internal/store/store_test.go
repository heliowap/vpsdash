package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "vpsdash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMigrationsAndCandidatePromotion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.UpsertHost(ctx, Host{ID: "vps", TailnetName: "vps.tailnet.ts.net", Kind: "vps", SSHUser: "operator"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCandidate(ctx, "vps", "web.service", "systemd"); err != nil {
		t.Fatal(err)
	}
	projects, err := s.Projects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Monitored {
		t.Fatalf("candidate = %+v", projects)
	}
	if err := s.SetMonitored(ctx, projects[0].ID, true, "", `["web.service"]`); err != nil {
		t.Fatal(err)
	}
	projects, err = s.Projects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !projects[0].Monitored || projects[0].Expected != `["web.service"]` {
		t.Fatalf("promoted = %+v", projects[0])
	}
	if err := s.RecordCheck(ctx, projects[0].ID, false, "inactive", time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ConsecutiveFailures(ctx, projects[0].ID); err != nil || got != 1 {
		t.Fatalf("failures = %d, %v", got, err)
	}
}

func TestHostMetricsAndRetention(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.UpsertHost(ctx, Host{ID: "vps", TailnetName: "vps.tailnet.ts.net", Kind: "vps"}); err != nil {
		t.Fatal(err)
	}
	old := time.Unix(100, 0)
	latest := time.Unix(200, 0)
	oldCPU, latestCPU := 25.0, 30.0
	_ = s.RecordMetric(ctx, "vps", Metric{CPU: &oldCPU, Memory: 50, Disk: 75, Uptime: 3600}, old)
	_ = s.RecordMetric(ctx, "vps", Metric{CPU: &latestCPU, Memory: 52, Disk: 75, Uptime: 3700}, latest)
	hosts, err := s.Hosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0].Latest == nil || hosts[0].Latest.CPU == nil || *hosts[0].Latest.CPU != 30 {
		t.Fatalf("hosts = %+v", hosts)
	}
	if err := s.PruneHistory(ctx, time.Unix(100+31*86400, 0)); err != nil {
		t.Fatal(err)
	}
	points, err := s.MetricHistory(ctx, "vps", 0, time.Unix(100+31*86400, 0).Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 0 {
		t.Fatalf("expired points = %+v", points)
	}
}

func TestFirstCPUSampleRemainsUnknown(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.UpsertHost(ctx, Host{ID: "vps", TailnetName: "vps.example.ts.net", Kind: "vps"}); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(300, 0)
	if err := s.RecordMetric(ctx, "vps", Metric{Memory: 50, Disk: 30, Uptime: 100}, now); err != nil {
		t.Fatal(err)
	}
	hosts, err := s.Hosts(ctx)
	if err != nil || len(hosts) != 1 || hosts[0].Latest == nil || hosts[0].Latest.CPU != nil {
		t.Fatalf("latest metric = %+v, %v", hosts, err)
	}
	points, err := s.MetricHistory(ctx, "vps", 0, 400)
	if err != nil || len(points) != 1 || points[0].CPU != nil {
		t.Fatalf("history = %+v, %v", points, err)
	}
}

func TestMetricHistoryBoundsThirtyDaysToDisplaySize(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.UpsertHost(ctx, Host{ID: "vps", TailnetName: "vps.example.ts.net", Kind: "vps"}); err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1_800_000_000, 0)
	for i := 0; i < 30*24*2; i++ {
		at := start.Add(time.Duration(i) * 30 * time.Minute)
		cpu := float64(i % 100)
		if err := s.RecordMetric(ctx, "vps", Metric{CPU: &cpu}, at); err != nil {
			t.Fatal(err)
		}
	}
	points, err := s.MetricHistory(ctx, "vps", start.Unix(), start.Add(30*24*time.Hour).Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(points) > 720 || len(points) < 700 {
		t.Fatalf("history points = %d, want about 720", len(points))
	}
}
