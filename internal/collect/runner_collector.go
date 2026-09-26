package collect

import (
	"context"
	"fmt"
	"time"

	"github.com/heliowap/vpsdash/internal/config"
)

func (c *Collector) runnerUnitLoop(ctx context.Context, account config.RunnerUnitHost) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		lock := c.hostLocks[account.HostID]
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
	for _, unit := range units {
		p, err := c.Store.UpsertNativeRunnerUnit(ctx, hostID, unit.Name)
		if err != nil {
			return err
		}
		detail := unit.LoadState + "/" + unit.ActiveState
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
