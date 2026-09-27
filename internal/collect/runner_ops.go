package collect

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/heliowap/vpsdash/internal/config"
	"github.com/heliowap/vpsdash/internal/store"
)

// RunnerUnitCommander sends restart and drain requests to the gh-agents
// forced SSH command. *Executor implements it.
type RunnerUnitCommander interface {
	RunnerUnitCommand(ctx context.Context, host config.Host, action, unit string) (string, error)
}

var (
	ErrUnknownRunnerUnit = errors.New("unknown runner unit")
	ErrUnitOpRunning     = errors.New("runner unit operation already running")
	ErrNoDrainRunning    = errors.New("no drain running for this unit")
)

const (
	defaultDrainPoll    = 15 * time.Second
	defaultDrainTimeout = 2 * time.Hour
	restartTimeout      = 7 * time.Minute
	// A GitHub reading older than this cannot prove the runner is busy.
	runnerReadingFresh = 2 * time.Minute
)

type activeUnitOp struct {
	action    string
	cancel    context.CancelFunc
	cancelled bool
}

type unitOps struct {
	mu      sync.Mutex
	active  map[string]*activeUnitOp
	poll    time.Duration
	timeout time.Duration
	base    context.Context
}

func unitOpKey(hostID, unit string) string { return hostID + "/" + unit }

// RunnerNameForUnit returns the runner name that gh-agents encodes in its
// unit name: actions.runner.<runner-name>.service.
func RunnerNameForUnit(unit string) string {
	return strings.TrimSuffix(strings.TrimPrefix(unit, "actions.runner."), ".service")
}

func (c *Collector) runnerAccount(hostID string) (config.RunnerUnitHost, config.Host, bool) {
	for _, account := range c.Config.RunnerUnitHosts {
		if account.HostID != hostID {
			continue
		}
		h, ok := c.host(hostID)
		if !ok {
			return account, config.Host{}, false
		}
		h.SSHUser = account.SSHUser
		h.SSHKeyFile = account.SSHKeyFile
		h.Local = false
		return account, h, true
	}
	return config.RunnerUnitHost{}, config.Host{}, false
}

// StartRunnerUnitOp validates that the unit is a runner service observed on
// a configured gh-agents host, records the request, and runs it in the
// background. Only one operation per unit runs at a time.
func (c *Collector) StartRunnerUnitOp(ctx context.Context, hostID, unit, action string) (store.UnitOp, error) {
	if action != "restart" && action != "drain" {
		return store.UnitOp{}, fmt.Errorf("invalid runner unit action %q", action)
	}
	if !config.IsRunnerServiceName(unit) {
		return store.UnitOp{}, ErrUnknownRunnerUnit
	}
	account, h, ok := c.runnerAccount(hostID)
	if !ok {
		return store.UnitOp{}, ErrUnknownRunnerUnit
	}
	projects, err := c.Store.Projects(ctx)
	if err != nil {
		return store.UnitOp{}, err
	}
	observed := false
	for _, p := range projects {
		if p.Native && p.HostID == hostID && p.Name == unit {
			observed = true
			break
		}
	}
	if !observed {
		return store.UnitOp{}, ErrUnknownRunnerUnit
	}
	key := unitOpKey(hostID, unit)
	c.ops.mu.Lock()
	defer c.ops.mu.Unlock()
	if c.ops.active[key] != nil {
		return store.UnitOp{}, ErrUnitOpRunning
	}
	op, err := c.Store.StartUnitOp(ctx, hostID, unit, action, time.Now())
	if err != nil {
		return store.UnitOp{}, err
	}
	timeout := restartTimeout
	if action == "drain" {
		timeout = c.ops.timeout
	}
	opCtx, cancel := context.WithTimeout(c.ops.base, timeout)
	active := &activeUnitOp{action: action, cancel: cancel}
	c.ops.active[key] = active
	log.Printf("runner unit %s requested: %s on %s (op %d)", action, unit, hostID, op.ID)
	go func() {
		defer cancel()
		status, detail := c.runUnitOp(opCtx, h, op, active)
		finishCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if err := c.Store.FinishUnitOp(finishCtx, op.ID, status, detail, time.Now()); err != nil {
			log.Printf("runner unit op %d: audit not saved: %v", op.ID, err)
		}
		log.Printf("runner unit %s %s: %s on %s (op %d): %s", action, status, unit, hostID, op.ID, detail)
		c.refreshRunnerUnits(account)
		c.ops.mu.Lock()
		delete(c.ops.active, key)
		c.ops.mu.Unlock()
	}()
	return op, nil
}

// CancelDrain stops waiting for a runner to become idle. The unit keeps
// running; a stop already sent to systemd is not undone.
func (c *Collector) CancelDrain(hostID, unit string) error {
	c.ops.mu.Lock()
	defer c.ops.mu.Unlock()
	active := c.ops.active[unitOpKey(hostID, unit)]
	if active == nil || active.action != "drain" {
		return ErrNoDrainRunning
	}
	active.cancelled = true
	active.cancel()
	log.Printf("runner unit drain cancel requested: %s on %s", unit, hostID)
	return nil
}

func (c *Collector) drainCancelled(active *activeUnitOp) bool {
	c.ops.mu.Lock()
	defer c.ops.mu.Unlock()
	return active.cancelled
}

func (c *Collector) runUnitOp(ctx context.Context, h config.Host, op store.UnitOp, active *activeUnitOp) (string, string) {
	if op.Action == "restart" {
		output, err := c.Units.RunnerUnitCommand(ctx, h, "runner-restart", op.Unit)
		if err != nil {
			return "failed", commandFailure(output, err)
		}
		return "done", "Unit reiniciada pelo systemd do gh-agents."
	}
	runner := RunnerNameForUnit(op.Unit)
	waiting := ""
	for {
		reason := ""
		busy, err := c.runnerBusyOnGitHub(ctx, runner)
		if err != nil {
			reason = "A leitura local da frota falhou."
		} else if busy {
			reason = "GitHub informa um job em andamento neste runner."
		} else if ctx.Err() == nil {
			output, err := c.Units.RunnerUnitCommand(ctx, h, "runner-drain", op.Unit)
			switch {
			case err == nil:
				return "done", "Unit parada sem job em andamento. Ela não recebe novos jobs até ser reiniciada."
			case errors.Is(err, ErrRunnerJobRunning):
				reason = "O host informa um job em andamento neste runner."
			case ctx.Err() == nil:
				return "failed", commandFailure(output, err)
			}
		}
		if reason != "" && reason != waiting {
			waiting = reason
			_ = c.Store.SetUnitOpDetail(ctx, op.ID, "Aguardando o job atual terminar. "+reason)
		}
		timer := time.NewTimer(c.ops.poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			if c.drainCancelled(active) {
				return "cancelled", "Drenagem cancelada antes da parada da unit."
			}
			return "expired", fmt.Sprintf("O job não terminou em %d min. A unit não foi parada.", int(c.ops.timeout.Minutes()))
		case <-timer.C:
		}
	}
}

// runnerBusyOnGitHub checks the latest fleet reading. The bridge still
// verifies locally that no Runner.Worker process remains before stopping.
func (c *Collector) runnerBusyOnGitHub(ctx context.Context, runner string) (bool, error) {
	runners, err := c.Store.Runners(ctx)
	if err != nil {
		return false, err
	}
	cutoff := time.Now().Add(-runnerReadingFresh).Unix()
	for _, r := range runners {
		if r.Name == runner && r.Busy && r.SeenAt >= cutoff {
			return true, nil
		}
	}
	return false, nil
}

func commandFailure(output string, err error) string {
	reason := strings.TrimSpace(output)
	if len(reason) > 300 {
		reason = reason[len(reason)-300:]
	}
	if reason == "" {
		reason = err.Error()
	}
	return "Falhou: " + reason
}

// refreshRunnerUnits records the unit state right after an operation so the
// panel does not wait for the next 30 s poll.
func (c *Collector) refreshRunnerUnits(account config.RunnerUnitHost) {
	lock, ok := c.hostLocks[account.HostID]
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.ops.base, 15*time.Second)
	defer cancel()
	lock.Lock()
	err := c.pollRunnerUnits(ctx, account)
	lock.Unlock()
	c.setError("runner-units:"+account.HostID, err)
}
