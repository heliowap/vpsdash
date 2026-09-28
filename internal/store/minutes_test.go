package store

import (
	"context"
	"testing"
	"time"
)

func TestJobMinutesFollowThirtyDayRetention(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 27, 3, 0, 0, 0, time.UTC)
	old := now.Add(-31 * 24 * time.Hour).Unix()
	recent := now.Add(-2 * time.Hour).Unix()
	if err := s.RecordRunUsage(ctx, "heliowap/vpsdash", RunScan{RunID: 1, RunAttempt: 1, CreatedAt: old}, []JobUsage{{JobID: 1, Backend: "self-hosted", StartedAt: old, CompletedAt: old + 60}}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRunUsage(ctx, "heliowap/vpsdash", RunScan{RunID: 2, RunAttempt: 1, CreatedAt: recent}, []JobUsage{{JobID: 2, Backend: "self-hosted", StartedAt: recent, CompletedAt: recent + 61}}, now); err != nil {
		t.Fatal(err)
	}
	// Recording the same attempt again must not double count.
	if err := s.RecordRunUsage(ctx, "heliowap/vpsdash", RunScan{RunID: 2, RunAttempt: 1, CreatedAt: recent}, []JobUsage{{JobID: 2, Backend: "self-hosted", StartedAt: recent, CompletedAt: recent + 61}}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.PruneHistory(ctx, now); err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM job_minutes`).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("jobs after prune = %d, %v", jobs, err)
	}
	// The 31-day-old scan survives until the listing can no longer return it.
	if scanned, err := s.RunScanned(ctx, "heliowap/vpsdash", 1, 1); err != nil || !scanned {
		t.Fatalf("recent scan pruned too early: %v %v", scanned, err)
	}
	if err := s.PruneHistory(ctx, now.Add(2*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if scanned, err := s.RunScanned(ctx, "heliowap/vpsdash", 1, 1); err != nil || scanned {
		t.Fatalf("expired scan kept: %v %v", scanned, err)
	}
	usage, err := s.BackendMinutes(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	backends := usage["heliowap/vpsdash"].Backends
	if len(backends) != 1 || backends[0].Jobs30d != 1 || backends[0].Minutes30d != 2 || backends[0].Minutes7d != 2 {
		t.Fatalf("usage = %+v", backends)
	}
}
