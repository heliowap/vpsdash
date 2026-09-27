package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestProjectIncidentsDerivesRunsWithinRetention(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	if err := s.UpsertHost(ctx, Host{ID: "vps", TailnetName: "vps.tailnet.example.invalid", Kind: "vps"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"web.service", "old.service"} {
		if err := s.UpsertCandidate(ctx, "vps", name, "systemd"); err != nil {
			t.Fatal(err)
		}
	}
	projects, err := s.Projects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, p := range projects {
		ids[p.Name] = p.ID
		if err := s.SetMonitored(ctx, p.ID, true, "", `["`+p.Name+`"]`); err != nil {
			t.Fatal(err)
		}
	}
	web := ids["web.service"]
	check := func(id int64, ok bool, detail string, at time.Time) {
		t.Helper()
		if err := s.RecordCheck(ctx, id, ok, detail, at); err != nil {
			t.Fatal(err)
		}
	}
	day := 24 * time.Hour
	// Beyond check retention: must not surface even before the nightly prune.
	check(web, false, "expired", now.Add(-31*day))
	base := now.Add(-20 * day)
	check(web, true, "healthy", base)
	check(web, false, "web.service is failed", base.Add(30*time.Second))
	check(web, false, "web.service is failed", base.Add(60*time.Second))
	check(web, false, "web.service is activating", base.Add(90*time.Second))
	check(web, true, "healthy", base.Add(120*time.Second))
	check(web, false, "web.service is inactive", now.Add(-day))
	check(web, true, "healthy", now.Add(-day+30*time.Second))
	check(web, false, "HTTP 502", now.Add(-60*time.Second))
	check(web, false, "HTTP 503", now.Add(-30*time.Second))

	alert := func(at time.Time, sent bool) {
		t.Helper()
		if err := s.QueueProjectAlert(ctx, web, "project_down", "vps / web.service", "down at "+at.Format(time.RFC3339), at); err != nil {
			t.Fatal(err)
		}
		if sent {
			if _, err := s.db.Exec(`UPDATE alerts SET sent_at=? WHERE sent_at=0`, at.Unix()); err != nil {
				t.Fatal(err)
			}
		}
	}
	alert(now.Add(-100*day), true) // beyond alert retention
	alert(now.Add(-60*day), true)  // checks expired, alert retained
	alert(base.Add(90*time.Second), false)
	if err := s.QueueAlert(ctx, "project_down", "vps / other.service", "legacy"); err != nil {
		t.Fatal(err)
	}

	history, err := s.ProjectIncidents(ctx, web, now)
	if err != nil {
		t.Fatal(err)
	}
	if !history.Monitored || history.CheckCount != 9 || history.FirstCheckAt != base.Unix() || history.LastCheckAt != now.Add(-30*time.Second).Unix() {
		t.Fatalf("history summary = %+v", history)
	}
	got := history.Incidents
	if len(got) != 4 {
		t.Fatalf("incidents = %+v", got)
	}
	open := got[0]
	if open.State != "open" || open.FailedChecks != 2 || open.Detail != "HTTP 503" || open.RecoveredAt != 0 || open.AlertAt != 0 || !open.StartKnown {
		t.Fatalf("open incident = %+v", open)
	}
	short := got[1]
	if short.State != "recovered" || short.FailedChecks != 1 || short.RecoveredAt != now.Add(-day+30*time.Second).Unix() || short.AlertAt != 0 {
		t.Fatalf("short incident = %+v", short)
	}
	alerted := got[2]
	if alerted.State != "recovered" || alerted.FailedChecks != 3 || alerted.StartedAt != base.Add(30*time.Second).Unix() ||
		alerted.LastFailureAt != base.Add(90*time.Second).Unix() || alerted.AlertAt != base.Add(90*time.Second).Unix() || alerted.Detail != "web.service is activating" {
		t.Fatalf("alerted incident = %+v", alerted)
	}
	expired := got[3]
	if expired.Source != "alert" || expired.State != "unresolved" || expired.StartedAt != now.Add(-60*day).Unix() || expired.FailedChecks != 0 {
		t.Fatalf("alert-only incident = %+v", expired)
	}

	old := ids["old.service"]
	check(old, false, "inactive", now.Add(-31*day))
	check(old, false, "inactive", now.Add(-29*day))
	check(old, true, "healthy", now.Add(-29*day+time.Minute))
	history, err = s.ProjectIncidents(ctx, old, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Incidents) != 1 || history.Incidents[0].StartKnown || history.Incidents[0].FailedChecks != 1 {
		t.Fatalf("run crossing the retention horizon = %+v", history.Incidents)
	}

	if _, err := s.ProjectIncidents(ctx, 9999, now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing project error = %v", err)
	}
}
