package store

import (
	"context"
	"testing"
	"time"
)

func TestAuditRecordsNewestFirstAndPrunesAfter90Days(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Unix(1_000_000, 0)
	for i, entry := range []AuditEntry{
		{Action: "step_up", ClientIP: "100.64.0.2", Outcome: "ok"},
		{Action: "attach_ro", HostID: "vps", Target: "dev", ClientIP: "100.64.0.2", Outcome: "ok"},
		{Action: "snippet", HostID: "vps", Target: "reiniciar", ClientIP: "100.64.0.2", Outcome: "failed"},
	} {
		if err := s.RecordAudit(ctx, entry, base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordAudit(ctx, AuditEntry{Action: "shell", Outcome: "ok"}, base); err == nil {
		t.Fatal("unknown audit action accepted")
	}
	entries, err := s.RecentAudit(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Action != "snippet" || entries[1].Target != "dev" {
		t.Fatalf("entries = %+v", entries)
	}
	if err := s.PruneHistory(ctx, base.Add(90*24*time.Hour+90*time.Second)); err != nil {
		t.Fatal(err)
	}
	entries, err = s.RecentAudit(ctx, 10)
	if err != nil || len(entries) != 1 || entries[0].Action != "snippet" {
		t.Fatalf("after prune = %+v, %v", entries, err)
	}
}
