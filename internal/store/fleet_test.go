package store

import (
	"context"
	"testing"
	"time"
)

func TestCurrentStateExpiresButUnsentAlertPersists(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Unix(100, 0)
	if err := s.UpsertRunner(ctx, Runner{Repo: "heliowap/vpsdash", ID: 7, Name: "agent-1", Status: "online", Busy: true, Job: "review"}, at); err != nil {
		t.Fatal(err)
	}
	if err := s.QueueAlert(ctx, "runner_offline", "agent-1", "runner offline"); err != nil {
		t.Fatal(err)
	}
	if err := s.Prune(ctx, at.Add(11*time.Minute)); err != nil {
		t.Fatal(err)
	}
	runners, err := s.Runners(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runners) != 0 {
		t.Fatalf("stale runners = %+v", runners)
	}
	alerts, err := s.PendingAlerts(ctx)
	if err != nil || len(alerts) != 1 {
		t.Fatalf("pending alerts = %+v, %v", alerts, err)
	}
}
