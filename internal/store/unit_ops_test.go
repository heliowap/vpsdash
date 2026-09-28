package store

import (
	"context"
	"testing"
	"time"
)

func TestUnitOpsKeepLatestAuditPerUnit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Unix(1000, 0)
	unit := "actions.runner.owner--repo-1.service"
	drain, err := s.StartUnitOp(ctx, "vps", unit, "drain", at)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetUnitOpDetail(ctx, drain.ID, "Aguardando"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishUnitOp(ctx, drain.ID, "done", "Unit parada.", at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishUnitOp(ctx, drain.ID, "failed", "twice", at.Add(time.Minute)); err == nil {
		t.Fatal("finished operation was finished again")
	}
	ops, err := s.LatestUnitOps(ctx)
	if err != nil || len(ops) != 1 || !ops[0].Drained() || ops[0].Detail != "Unit parada." {
		t.Fatalf("latest ops = %+v, %v", ops, err)
	}
	restart, err := s.StartUnitOp(ctx, "vps", unit, "restart", at.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AbandonRunningUnitOps(ctx, at.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	ops, err = s.LatestUnitOps(ctx)
	if err != nil || len(ops) != 1 || ops[0].ID != restart.ID || ops[0].Status != "failed" || ops[0].Drained() {
		t.Fatalf("abandoned restart = %+v, %v", ops, err)
	}
	if err := s.PruneHistory(ctx, at.Add(100*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	ops, err = s.LatestUnitOps(ctx)
	if err != nil || len(ops) != 1 || ops[0].ID != restart.ID {
		t.Fatalf("retention removed the latest operation: %+v, %v", ops, err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM runner_unit_ops`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("old audit rows = %d, %v", count, err)
	}
}
