package store

import (
	"context"
	"database/sql"
	"path/filepath"
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

func TestAuditMigrationKeepsRowsAndAcceptsSendKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vpsdash.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 11; i++ {
		if _, err := db.Exec(migrations[i]); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations(version) VALUES (?)`, i+1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO audit_log(ts,action,host_id,target,client_ip,outcome) VALUES (10,'attach_ro','vps','dev','100.64.0.2','ok')`); err != nil {
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
	ctx := context.Background()
	if err := s.RecordAudit(ctx, AuditEntry{Action: "send_keys", HostID: "vps", Target: "dev", ClientIP: "100.64.0.2", Outcome: "ok"}, time.Unix(20, 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAudit(ctx, AuditEntry{Action: "type", Outcome: "ok"}, time.Unix(30, 0)); err == nil {
		t.Fatal("unknown audit action accepted after migration")
	}
	entries, err := s.RecentAudit(ctx, 10)
	if err != nil || len(entries) != 2 || entries[0].Action != "send_keys" || entries[1].Action != "attach_ro" || entries[1].Target != "dev" {
		t.Fatalf("entries = %+v, %v", entries, err)
	}
}
