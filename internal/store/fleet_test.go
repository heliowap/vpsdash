package store

import (
	"context"
	"testing"
	"time"
)

func TestRunnerExpiresButLastSessionAndUnsentAlertPersist(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Unix(100, 0)
	if err := s.UpsertHost(ctx, Host{ID: "vps", TailnetName: "vps.example.ts.net", Kind: "vps"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertRunner(ctx, Runner{Repo: "heliowap/vpsdash", ID: 7, Name: "agent-1", Status: "online", Busy: true, Job: "review"}, at); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSession(ctx, Session{HostID: "vps", Name: "work", PanePID: 42}, at); err != nil {
		t.Fatal(err)
	}
	if err := s.QueueAlert(ctx, "runner_offline", "agent-1", "runner offline"); err != nil {
		t.Fatal(err)
	}
	if err := s.PruneRunners(ctx, at.Add(9*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if runners, err := s.Runners(ctx); err != nil || len(runners) != 1 {
		t.Fatalf("recent runners = %+v, %v", runners, err)
	}
	if sessions, err := s.Sessions(ctx); err != nil || len(sessions) != 1 {
		t.Fatalf("recent sessions = %+v, %v", sessions, err)
	}
	if err := s.PruneRunners(ctx, at.Add(11*time.Minute)); err != nil {
		t.Fatal(err)
	}
	runners, err := s.Runners(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runners) != 0 {
		t.Fatalf("stale runners = %+v", runners)
	}
	sessions, err := s.Sessions(ctx)
	if err != nil || len(sessions) != 1 || sessions[0].SeenAt != at.Unix() {
		t.Fatalf("last observed session = %+v, %v", sessions, err)
	}
	alerts, err := s.PendingAlerts(ctx)
	if err != nil || len(alerts) != 1 {
		t.Fatalf("pending alerts = %+v, %v", alerts, err)
	}
}

func TestSuccessfulSessionSnapshotReplacesOldSessions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.UpsertHost(ctx, Host{ID: "vps", TailnetName: "vps.example.ts.net", Kind: "vps"}); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(100, 0)
	if err := s.ReplaceSessions(ctx, "vps", []Session{{Name: "old"}}, at); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceSessions(ctx, "vps", []Session{{Name: "current"}}, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	sessions, err := s.Sessions(ctx)
	if err != nil || len(sessions) != 1 || sessions[0].Name != "current" {
		t.Fatalf("replaced sessions = %+v, %v", sessions, err)
	}
	if err := s.ReplaceSessions(ctx, "vps", nil, at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	sessions, err = s.Sessions(ctx)
	if err != nil || len(sessions) != 0 {
		t.Fatalf("empty successful snapshot = %+v, %v", sessions, err)
	}
}
