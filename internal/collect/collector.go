package collect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/heliowap/vpsdash/internal/config"
	"github.com/heliowap/vpsdash/internal/githubapp"
	"github.com/heliowap/vpsdash/internal/store"
)

type FleetAPI interface {
	Runners(context.Context, string) ([]githubapp.Runner, error)
	Runs(context.Context, string, string) ([]githubapp.WorkflowRun, error)
	Jobs(context.Context, string, int64) ([]githubapp.WorkflowJob, error)
}

type Collector struct {
	Config    config.Config
	Store     *store.Store
	Executor  *Executor
	GitHub    FleetAPI
	mu        sync.RWMutex
	hostLocks map[string]*sync.Mutex
	queued    map[string][]githubapp.WorkflowRun
	errors    map[string]string
	lastFleet time.Time
}

type hostCircuit struct {
	failures  int
	openUntil time.Time
}

func (c *hostCircuit) record(attempted bool, err error, now time.Time) bool {
	if !attempted {
		return false
	}
	if err == nil {
		c.failures = 0
		c.openUntil = time.Time{}
		return false
	}
	c.failures++
	if c.failures < 3 {
		return false
	}
	c.failures = 0
	c.openUntil = now.Add(5 * time.Minute)
	return true
}

func New(c config.Config, s *store.Store, github FleetAPI) *Collector {
	locks := map[string]*sync.Mutex{}
	for _, h := range c.Hosts {
		locks[h.ID] = &sync.Mutex{}
	}
	return &Collector{Config: c, Store: s, Executor: NewExecutor(), GitHub: github, hostLocks: locks, queued: map[string][]githubapp.WorkflowRun{}, errors: map[string]string{}}
}

func (c *Collector) Start(ctx context.Context) error {
	for _, h := range c.Config.Hosts {
		if err := c.Store.UpsertHost(ctx, store.Host{ID: h.ID, TailnetName: h.TailnetName, Kind: h.Kind, SSHUser: h.SSHUser}); err != nil {
			return err
		}
	}
	go c.tailnetLoop(ctx)
	go c.healthLoop(ctx)
	for _, runnerHost := range c.Config.RunnerUnitHosts {
		go c.runnerUnitLoop(ctx, runnerHost)
	}
	go c.fleetLoop(ctx)
	go c.maintenanceLoop(ctx)
	for _, h := range c.Config.Hosts {
		if h.Kind == "vps" {
			go c.hostLoop(ctx, h)
		}
	}
	return nil
}

func (c *Collector) Close() { c.Executor.Close() }

func (c *Collector) Snapshot() (map[string][]githubapp.WorkflowRun, map[string]string, time.Time) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	queued := map[string][]githubapp.WorkflowRun{}
	for k, v := range c.queued {
		queued[k] = append([]githubapp.WorkflowRun(nil), v...)
	}
	errors := map[string]string{}
	for k, v := range c.errors {
		errors[k] = v
	}
	return queued, errors, c.lastFleet
}

func (c *Collector) setError(scope string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil {
		delete(c.errors, scope)
	} else {
		c.errors[scope] = err.Error()
		log.Printf("collector %s: %v", scope, err)
	}
}

func (c *Collector) hostLoop(ctx context.Context, h config.Host) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	var lastMetrics, lastDiscovery, lastSessions time.Time
	var previous MetricsSnapshot
	var havePrevious bool
	var circuit hostCircuit
	for {
		now := time.Now()
		if !now.Before(circuit.openUntil) {
			lock := c.hostLocks[h.ID]
			lock.Lock()
			var err error
			attempted := false
			if now.Sub(lastMetrics) >= 60*time.Second {
				attempted = true
				previous, havePrevious, err = c.pollMetrics(ctx, h, previous, havePrevious)
				lastMetrics = now
			}
			if err == nil && now.Sub(lastDiscovery) >= 5*time.Minute {
				attempted = true
				err = c.pollDiscovery(ctx, h)
				lastDiscovery = now
			}
			if err == nil && now.Sub(lastSessions) >= 60*time.Second {
				attempted = true
				err = c.pollSessions(ctx, h)
				lastSessions = now
			}
			lock.Unlock()
			if attempted {
				c.setError("host:"+h.ID, err)
			}
			if circuit.record(attempted, err, now) {
				_ = c.Store.SetHostPresence(ctx, h.ID, false, now)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *Collector) pollMetrics(ctx context.Context, h config.Host, previous MetricsSnapshot, havePrevious bool) (MetricsSnapshot, bool, error) {
	raw, err := c.Executor.Run(ctx, h, MetricsScript)
	if err != nil {
		return previous, havePrevious, err
	}
	snapshot, err := ParseMetricsSnapshot(raw)
	if err != nil {
		return previous, havePrevious, err
	}
	metric := store.Metric{Memory: snapshot.MemoryPct, Disk: snapshot.DiskPct, Uptime: snapshot.UptimeSeconds}
	if havePrevious {
		cpu := CPUPercent(previous, snapshot)
		metric.CPU = &cpu
	}
	if err := c.Store.RecordMetric(ctx, h.ID, metric, time.Now()); err != nil {
		return previous, havePrevious, err
	}
	if err := c.Store.SetHostPresence(ctx, h.ID, true, time.Now()); err != nil {
		return previous, havePrevious, err
	}
	return snapshot, true, nil
}

func (c *Collector) pollDiscovery(ctx context.Context, h config.Host) error {
	raw, err := c.Executor.Run(ctx, h, DiscoveryScript)
	if err != nil {
		return err
	}
	candidates, err := ParseDiscovery(raw)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if candidate.Source == "systemd" && c.Config.IsNativeRunnerUnit(h.ID, candidate.Name) {
			continue
		}
		if err := c.Store.UpsertCandidate(ctx, h.ID, candidate.Name, candidate.Source); err != nil {
			return err
		}
	}
	return nil
}

func (c *Collector) pollSessions(ctx context.Context, h config.Host) error {
	raw, err := c.Executor.Run(ctx, h, SessionsScript)
	if err != nil {
		return err
	}
	sessions, err := ParseSessions(raw)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		x := store.Session{HostID: h.ID, Name: session.Name, PanePID: session.PanePID, CWD: session.CWD, Agent: session.Agent}
		if err := c.Store.UpsertSession(ctx, x, time.Now()); err != nil {
			return err
		}
	}
	return nil
}

func (c *Collector) tailnetLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		pollCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		command := exec.CommandContext(pollCtx, "tailscale", "status", "--json")
		output, err := command.Output()
		cancel()
		if err == nil {
			var devices []Device
			devices, err = ParseTailnetStatus(string(output))
			if err == nil {
				err = c.saveDevices(ctx, devices)
			}
		}
		c.setError("tailnet", err)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *Collector) saveDevices(ctx context.Context, devices []Device) error {
	for _, d := range devices {
		id, kind, user := strings.Split(d.ID, ".")[0], "presence", ""
		for _, h := range c.Config.Hosts {
			if strings.EqualFold(strings.TrimSuffix(h.TailnetName, "."), d.ID) {
				id, kind, user = h.ID, h.Kind, h.SSHUser
				break
			}
		}
		if err := c.Store.UpsertHost(ctx, store.Host{ID: id, TailnetName: d.ID, Kind: kind, SSHUser: user}); err != nil {
			return err
		}
		if kind == "vps" {
			continue
		}
		if err := c.Store.SetHostPresence(ctx, id, d.Online, time.Now()); err != nil {
			return err
		}
	}
	return nil
}

func (c *Collector) healthLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		projects, err := c.Store.Projects(ctx)
		if err == nil {
			c.checkProjects(ctx, projects)
		}
		c.setError("health", err)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *Collector) checkProjects(ctx context.Context, projects []store.Project) {
	groups := map[string][]store.Project{}
	for _, p := range projects {
		if p.Monitored && !p.Native {
			groups[p.HostID] = append(groups[p.HostID], p)
		}
	}
	var wg sync.WaitGroup
	for hostID, items := range groups {
		h, ok := c.host(hostID)
		if !ok {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			lock := c.hostLocks[h.ID]
			lock.Lock()
			defer lock.Unlock()
			for _, p := range items {
				c.checkProject(ctx, h, p)
			}
		}()
	}
	wg.Wait()
}

func (c *Collector) host(id string) (config.Host, bool) {
	for _, h := range c.Config.Hosts {
		if h.ID == id {
			return h, true
		}
	}
	return config.Host{}, false
}

func (c *Collector) checkProject(ctx context.Context, h config.Host, p store.Project) {
	err := c.projectHealth(ctx, h, p)
	detail := "healthy"
	if err != nil {
		detail = err.Error()
	}
	if recordErr := c.recordProjectCheck(ctx, p, err == nil, detail, time.Now()); recordErr != nil {
		c.setError("check:"+p.Name, recordErr)
	}
}

func (c *Collector) recordProjectCheck(ctx context.Context, p store.Project, ok bool, detail string, now time.Time) error {
	if err := c.Store.RecordCheck(ctx, p.ID, ok, detail, now); err != nil {
		return err
	}
	if !ok {
		count, countErr := c.Store.ConsecutiveFailures(ctx, p.ID)
		if countErr != nil {
			return countErr
		}
		if count >= 3 {
			return c.Store.QueueAlert(ctx, "project_down", p.HostID+" / "+p.Name, detail)
		}
	}
	return nil
}

func (c *Collector) projectHealth(ctx context.Context, h config.Host, p store.Project) error {
	if p.HealthURL != "" {
		return checkHTTP(ctx, p.HealthURL)
	}
	names := []string{p.Name}
	if p.Expected != "" {
		if err := json.Unmarshal([]byte(p.Expected), &names); err != nil {
			return err
		}
	}
	if len(names) == 0 {
		return errors.New("no expected services configured")
	}
	for _, name := range names {
		output, err := c.Executor.Check(ctx, h, p.Source, name)
		if err != nil {
			reason := strings.TrimSpace(output)
			if reason == "" {
				reason = err.Error()
			}
			return fmt.Errorf("%s: %s", name, reason)
		}
		if strings.TrimSpace(output) != "active" {
			return fmt.Errorf("%s is %s", name, strings.TrimSpace(output))
		}
	}
	return nil
}

func checkHTTP(ctx context.Context, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("invalid health URL")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func healthScript(source, name string) (string, error) {
	q := shellQuote(name)
	switch source {
	case "systemd":
		if strings.HasPrefix(name, "user:") {
			return "systemctl --user is-active " + shellQuote(strings.TrimPrefix(name, "user:")) + " || true", nil
		}
		return "systemctl is-active " + q + " || true", nil
	case "docker":
		return "if [ \"$(docker inspect -f '{{.State.Running}}' " + q + " 2>/dev/null)\" = true ]; then echo active; else echo inactive; fi", nil
	case "tmux":
		return "if tmux has-session -t " + q + " 2>/dev/null; then echo active; else echo inactive; fi", nil
	default:
		return "", errors.New("unsupported project source")
	}
}

func (c *Collector) fleetLoop(ctx context.Context) {
	if c.GitHub == nil {
		return
	}
	interval := 30 * time.Second
	for {
		busy := c.pollFleet(ctx)
		if busy {
			interval = 10 * time.Second
		} else {
			interval = 30 * time.Second
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (c *Collector) pollFleet(ctx context.Context) bool {
	busy := false
	observed := false
	queued := map[string][]githubapp.WorkflowRun{}
	for _, repo := range c.Config.Repositories {
		runners, err := c.GitHub.Runners(ctx, repo.Name)
		if err != nil {
			c.setError("github:"+repo.Name, err)
			continue
		}
		observed = true
		jobs := map[string]string{}
		repoBusy := false
		for _, r := range runners {
			if r.Busy {
				repoBusy = true
				busy = true
			}
		}
		if repoBusy {
			active, activeErr := c.GitHub.Runs(ctx, repo.Name, "in_progress")
			if activeErr == nil {
				for i, run := range active {
					if i >= 20 {
						break
					}
					items, jobsErr := c.GitHub.Jobs(ctx, repo.Name, run.ID)
					if jobsErr != nil {
						continue
					}
					for _, job := range items {
						if job.Status == "in_progress" {
							jobs[job.RunnerName] = job.Name
						}
					}
				}
			}
		}
		for _, r := range runners {
			if r.Busy {
				busy = true
			}
			if err := c.Store.UpsertRunner(ctx, store.Runner{Repo: repo.Name, ID: r.ID, Name: r.Name, Status: r.Status, Busy: r.Busy, Job: jobs[r.Name]}, time.Now()); err != nil {
				c.setError("github:"+repo.Name, err)
			}
		}
		runs, err := c.GitHub.Runs(ctx, repo.Name, "queued")
		if err != nil {
			c.setError("github:"+repo.Name, err)
			continue
		}
		queued[repo.Name] = runs
		c.setError("github:"+repo.Name, nil)
	}
	c.mu.Lock()
	c.queued = queued
	if observed {
		c.lastFleet = time.Now()
	}
	c.mu.Unlock()
	return busy
}

func (c *Collector) maintenanceLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	lastPrune, lastVacuum := "", ""
	for {
		now := time.Now()
		date := now.Format("2006-01-02")
		c.setError("prune-current", c.Store.PruneCurrent(ctx, now))
		if now.Hour() == 3 && now.Minute() >= 0 && lastPrune != date {
			err := c.Store.PruneHistory(ctx, now)
			c.setError("prune-history", err)
			if err == nil {
				lastPrune = date
			}
		}
		if now.Weekday() == time.Sunday && now.Hour() == 3 && now.Minute() >= 30 && lastVacuum != date {
			err := c.Store.Vacuum(ctx)
			c.setError("vacuum", err)
			if err == nil {
				lastVacuum = date
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
