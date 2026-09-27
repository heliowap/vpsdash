package collect

import (
	"context"
	"fmt"
	"time"

	"github.com/heliowap/vpsdash/internal/config"
	"github.com/heliowap/vpsdash/internal/store"
)

func (c *Collector) runnerUnitLoop(ctx context.Context, account config.RunnerUnitHost) {
	lock, ok := c.hostLocks[account.HostID]
	if !ok {
		c.setError("runner-units:"+account.HostID, fmt.Errorf("unknown runner unit host %s", account.HostID))
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		lock.Lock()
		err := c.pollRunnerUnits(ctx, account)
		lock.Unlock()
		c.setError("runner-units:"+account.HostID, err)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *Collector) pollRunnerUnits(ctx context.Context, account config.RunnerUnitHost) error {
	h, ok := c.host(account.HostID)
	if !ok {
		return fmt.Errorf("unknown runner unit host %s", account.HostID)
	}
	h.ID = "runner-units:" + h.ID // SSH clients are cached by ID; this account needs its own client.
	h.SSHUser = account.SSHUser
	h.SSHKeyFile = account.SSHKeyFile
	h.Local = false
	raw, err := c.Executor.Run(ctx, h, RunnerUnitsScript)
	if err != nil {
		return err
	}
	units, err := ParseRunnerUnits(raw)
	if err != nil {
		return err
	}
	return c.saveRunnerUnits(ctx, account.HostID, units, time.Now())
}

func (c *Collector) saveRunnerUnits(ctx context.Context, hostID string, units map[string]RunnerUnit, now time.Time) error {
	previous, err := c.Store.Projects(ctx)
	if err != nil {
		return err
	}
	ops, err := c.Store.LatestUnitOps(ctx)
	if err != nil {
		return err
	}
	latest := map[string]store.UnitOp{}
	for _, op := range ops {
		if op.HostID == hostID {
			latest[op.Unit] = op
		}
	}
	for _, unit := range units {
		p, err := c.Store.UpsertNativeRunnerUnit(ctx, hostID, unit.Name)
		if err != nil {
			return err
		}
		detail := unit.LoadState + "/" + unit.ActiveState
		if reason := plannedStopReason(latest[unit.Name], unit, now); reason != "" && !unit.Healthy() {
			// The operator stopped or is restarting this unit on purpose:
			// keep the reading, but as a neutral row that neither counts
			// toward nor extends a project_down failure streak.
			if err := c.Store.RecordPlannedCheck(ctx, p.ID, detail+" ("+reason+")", now); err != nil {
				return err
			}
			continue
		}
		if err := c.recordProjectCheck(ctx, p, unit.Healthy(), detail, now); err != nil {
			return err
		}
	}
	for _, p := range previous {
		if p.HostID != hostID || !p.Native {
			continue
		}
		if _, present := units[p.Name]; !present {
			if err := c.recordProjectCheck(ctx, p, false, "unit absent", now); err != nil {
				return err
			}
		}
	}
	return nil
}

// restartSettleGrace covers a unit systemd still reports as activating or
// deactivating right after a panel restart finished.
const restartSettleGrace = 2 * time.Minute

// plannedStopReason explains why a non-active reading is expected: the unit
// was drained, a panel operation on it is still running, or systemd is still
// settling a restart the panel just finished. It returns "" otherwise.
func plannedStopReason(op store.UnitOp, unit RunnerUnit, now time.Time) string {
	switch {
	case op.ID == 0:
		return ""
	case op.Drained():
		return "drenada pelo painel"
	case op.Status == "running":
		return "operação do painel em andamento"
	case op.Action == "restart" && op.Status == "done" && unit.Transitioning() &&
		now.Sub(time.Unix(op.FinishedAt, 0)) < restartSettleGrace:
		return "reiniciada pelo painel"
	}
	return ""
}
