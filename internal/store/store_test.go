package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
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
	// A planned reading is shown as failed but breaks the streak.
	if err := s.RecordPlannedCheck(ctx, projects[0].ID, "inactive (drenada)", time.Unix(101, 0)); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ConsecutiveFailures(ctx, projects[0].ID); err != nil || got != 0 {
		t.Fatalf("failures after planned check = %d, %v", got, err)
	}
	projects, err = s.Projects(ctx)
	if err != nil || projects[0].CheckOK == nil || *projects[0].CheckOK || !projects[0].CheckPlanned {
		t.Fatalf("planned reading = %+v, %v", projects[0], err)
	}
	if err := s.RecordCheck(ctx, projects[0].ID, false, "inactive", time.Unix(102, 0)); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ConsecutiveFailures(ctx, projects[0].ID); err != nil || got != 1 {
		t.Fatalf("failures after planned check and a real failure = %d, %v", got, err)
	}
	if projects, err = s.Projects(ctx); err != nil || projects[0].CheckPlanned {
		t.Fatalf("real failure still planned = %+v, %v", projects[0], err)
	}
}

func TestConsecutiveFailuresUsesBoundedIndexScan(t *testing.T) {
	s := newTestStore(t)
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN SELECT ok,planned FROM checks WHERE project_id=? ORDER BY ts DESC,rowid DESC LIMIT 3`, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	indexed := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "idx_checks_proj_ts") {
			indexed = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !indexed {
		t.Fatal("latest-three failure check did not use the project/time index")
	}
}

func TestNativeRunnerUnitIsMonitoredWithoutPromotion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.UpsertHost(ctx, Host{ID: "vps", TailnetName: "vps.example.ts.net", Kind: "vps"}); err != nil {
		t.Fatal(err)
	}
	unit, err := s.UpsertNativeRunnerUnit(ctx, "vps", "actions.runner.owner-repo.own--repo-1.service")
	if err != nil {
		t.Fatal(err)
	}
	if !unit.Monitored || unit.Source != "systemd" || unit.Expected != `["user:actions.runner.owner-repo.own--repo-1.service"]` {
		t.Fatalf("native unit = %+v", unit)
	}
	if err := s.UpsertCandidate(ctx, "vps", unit.Name, "systemd"); err != nil {
		t.Fatal(err)
	}
	projects, err := s.Projects(ctx)
	if err != nil || len(projects) != 1 || !projects[0].Monitored || !projects[0].Native {
		t.Fatalf("project after discovery = %+v, %v", projects, err)
	}
	if err := s.SetMonitored(ctx, unit.ID, false, "", ""); err == nil {
		t.Fatal("native unit was demoted through SetMonitored")
	}
}

func TestMigrationMarksExistingProjectsNonNative(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vpsdash.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := db.Exec(migrations[i]); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations(version) VALUES (?)`, i+1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO hosts(id,tailnet_name,kind) VALUES ('vps','vps.example.ts.net','vps')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projects(host_id,name,source,monitored) VALUES ('vps','web.service','systemd',1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	projects, err := s.Projects(context.Background())
	if err != nil || len(projects) != 1 || projects[0].Native || !projects[0].Monitored {
		t.Fatalf("migrated projects = %+v, %v", projects, err)
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

func TestMetricHistoryDoesNotWaitForDashboardConnection(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.UpsertHost(ctx, Host{ID: "vps", TailnetName: "vps.example.invalid", Kind: "vps"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordMetric(ctx, "vps", Metric{Memory: 50, Disk: 30, Uptime: 100}, time.Unix(300, 0)); err != nil {
		t.Fatal(err)
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	points, err := s.MetricHistory(readCtx, "vps", 0, 400)
	if err != nil || len(points) != 1 {
		t.Fatalf("history blocked by dashboard connection: %+v, %v", points, err)
	}
}

func TestSQLiteConnectionPolicySurvivesReconnect(t *testing.T) {
	s := newTestStore(t)
	for _, db := range []*sql.DB{s.db, s.history} {
		db.SetConnMaxLifetime(time.Nanosecond)
	}
	for i := 0; i < 2; i++ {
		var foreignKeys, busyTimeout, queryOnly int
		if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			t.Fatal(err)
		}
		if err := s.history.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
			t.Fatal(err)
		}
		if err := s.history.QueryRow("PRAGMA query_only").Scan(&queryOnly); err != nil {
			t.Fatal(err)
		}
		if foreignKeys != 1 || busyTimeout != 5000 || queryOnly != 1 {
			t.Fatalf("connection policy after reconnect = foreign_keys:%d busy_timeout:%d query_only:%d", foreignKeys, busyTimeout, queryOnly)
		}
		if _, err := s.history.Exec("INSERT INTO hosts(id,tailnet_name,kind) VALUES ('bad','bad','vps')"); err == nil {
			t.Fatal("history connection accepted a write")
		}
	}
	if err := s.Vacuum(context.Background()); err != nil {
		t.Fatalf("maintenance with read-only history handle: %v", err)
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
