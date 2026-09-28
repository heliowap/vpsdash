package collect

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/heliowap/vpsdash/internal/store"
)

const (
	unitStoppedSnapshot = opsUnit + "\tloaded\tinactive\ngh-agents-cleanup.timer\tloaded\tactive\n"
)

// startBlockedDrain starts a drain whose runner-drain request stays in flight
// until the operation context ends, as a slow SSH call to the host would.
func startBlockedDrain(t *testing.T, c *Collector, fake *fakeUnits) store.UnitOp {
	t.Helper()
	fake.mu.Lock()
	fake.release = make(chan struct{})
	fake.mu.Unlock()
	op, err := c.StartRunnerUnitOp(context.Background(), "vps", opsUnit, "drain")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(fake.Calls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if calls := fake.Calls(); len(calls) != 1 || calls[0] != "gh-agents@vps runner-drain "+opsUnit {
		t.Fatalf("drain request not in flight: %v", calls)
	}
	return op
}

func pollUnitsTimes(t *testing.T, c *Collector, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := c.pollRunnerUnits(context.Background(), c.Config.RunnerUnitHosts[0]); err != nil {
			t.Fatal(err)
		}
	}
}

func unitProject(t *testing.T, s *store.Store) store.Project {
	t.Helper()
	projects, err := s.Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range projects {
		if p.Name == opsUnit {
			return p
		}
	}
	t.Fatal("unit project missing")
	return store.Project{}
}

func assertNoAlerts(t *testing.T, s *store.Store, when string) {
	t.Helper()
	if alerts, err := s.PendingAlerts(context.Background()); err != nil || len(alerts) != 0 {
		t.Fatalf("%s: alerts = %+v, %v", when, alerts, err)
	}
}

func TestRunnerUnitDrainCancelledInFlightConfirmsStoppedUnit(t *testing.T) {
	c, s, fake := newOpsCollector(t)
	op := startBlockedDrain(t, c, fake)
	// The host stopped the unit even though its answer never came back.
	fake.setSnapshot(unitStoppedSnapshot)
	if err := c.CancelDrain("vps", opsUnit); err != nil {
		t.Fatal(err)
	}
	done := waitUnitOp(t, c, s, op.ID)
	if !done.Drained() || done.StopUnconfirmed ||
		!strings.Contains(done.Detail, "cancelada com o pedido de parada já enviado") || !strings.Contains(done.Detail, "loaded/inactive") {
		t.Fatalf("drain stopped by an in-flight request = %+v", done)
	}
	pollUnitsTimes(t, c, 3)
	assertNoAlerts(t, s, "stopped drain")
	if p := unitProject(t, s); p.CheckOK == nil || *p.CheckOK || !p.CheckPlanned {
		t.Fatalf("stopped drain not a planned stop: %+v", p)
	}
}

func TestRunnerUnitDrainCancelledInFlightReportsRunningUnit(t *testing.T) {
	c, s, fake := newOpsCollector(t)
	op := startBlockedDrain(t, c, fake)
	if err := c.CancelDrain("vps", opsUnit); err != nil {
		t.Fatal(err)
	}
	done := waitUnitOp(t, c, s, op.ID)
	if done.Status != "cancelled" || done.Drained() || done.StopUnconfirmed || !strings.Contains(done.Detail, "loaded/active: ela não foi parada") {
		t.Fatalf("drain the host did not stop = %+v", done)
	}
	pollUnitsTimes(t, c, 3)
	assertNoAlerts(t, s, "running unit")
	if p := unitProject(t, s); p.CheckOK == nil || !*p.CheckOK {
		t.Fatalf("running unit not healthy: %+v", p)
	}
}

func TestRunnerUnitDrainInterruptedByShutdownStaysNeutralUntilConfirmed(t *testing.T) {
	c, s, fake := newOpsCollector(t)
	ctx := context.Background()
	base, shutdown := context.WithCancel(ctx)
	c.ops.base = base
	op := startBlockedDrain(t, c, fake)
	fake.mu.Lock()
	fake.snapshot = unitStoppedSnapshot
	fake.runErr = errors.New("ssh vps: connection closed")
	fake.mu.Unlock()
	shutdown()
	done := waitUnitOp(t, c, s, op.ID)
	if done.Status != "expired" || !done.StopUnconfirmed || done.Drained() ||
		!strings.Contains(done.Detail, "encerrado com o pedido de parada já enviado") || !strings.Contains(done.Detail, "Estado da unit não confirmado") {
		t.Fatalf("unconfirmed drain = %+v", done)
	}
	// While the host cannot be read, no reading counts against the unit.
	if err := c.pollRunnerUnits(ctx, c.Config.RunnerUnitHosts[0]); err == nil {
		t.Fatal("poll succeeded while the host was unreachable")
	}
	assertNoAlerts(t, s, "unreachable host")
	// Once the host answers, the stopped unit is the drain's planned stop.
	fake.mu.Lock()
	fake.runErr = nil
	fake.mu.Unlock()
	pollUnitsTimes(t, c, 3)
	assertNoAlerts(t, s, "confirmed drain")
	ops, err := s.LatestUnitOps(ctx)
	if err != nil || len(ops) != 1 || !ops[0].Drained() || ops[0].StopUnconfirmed || !strings.Contains(ops[0].Detail, "loaded/inactive") {
		t.Fatalf("resolved drain = %+v, %v", ops, err)
	}
	if p := unitProject(t, s); p.CheckOK == nil || *p.CheckOK || !p.CheckPlanned {
		t.Fatalf("confirmed drain not a planned stop: %+v", p)
	}
}

func TestUnconfirmedDrainOfRunningUnitCountsLaterFailures(t *testing.T) {
	c, s, _ := newOpsCollector(t)
	ctx := context.Background()
	op, err := s.StartUnitOp(ctx, "vps", opsUnit, "drain", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AbandonRunningUnitOps(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	ops, err := s.LatestUnitOps(ctx)
	if err != nil || len(ops) != 1 || !ops[0].StopUnconfirmed || !strings.Contains(ops[0].Detail, "não confirmado") {
		t.Fatalf("abandoned drain = %+v, %v", ops, err)
	}
	pollUnitsTimes(t, c, 1) // the unit is active: the drain did not stop it
	ops, err = s.LatestUnitOps(ctx)
	if err != nil || len(ops) != 1 || ops[0].ID != op.ID || ops[0].Status != "failed" || ops[0].StopUnconfirmed || !strings.Contains(ops[0].Detail, "não foi parada") {
		t.Fatalf("resolved abandoned drain = %+v, %v", ops, err)
	}
	failed := map[string]RunnerUnit{
		opsUnit:                   {Name: opsUnit, LoadState: "loaded", ActiveState: "failed"},
		"gh-agents-cleanup.timer": {Name: "gh-agents-cleanup.timer", LoadState: "loaded", ActiveState: "active"},
	}
	for i := 0; i < 2; i++ {
		if err := c.saveRunnerUnits(ctx, "vps", failed, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	assertNoAlerts(t, s, "two real failures")
	if err := c.saveRunnerUnits(ctx, "vps", failed, time.Now()); err != nil {
		t.Fatal(err)
	}
	if alerts, err := s.PendingAlerts(ctx); err != nil || len(alerts) != 1 || alerts[0].Kind != store.AlertRunnerOffline || !strings.Contains(alerts[0].Subject, opsUnit) {
		t.Fatalf("later genuine failure = %+v, %v", alerts, err)
	}
}
