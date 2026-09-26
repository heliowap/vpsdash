package collect

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heliowap/vpsdash/internal/config"
	"github.com/heliowap/vpsdash/internal/store"
)

func TestParseRunnerUnits(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "runner_units.txt"))
	if err != nil {
		t.Fatal(err)
	}
	units, err := ParseRunnerUnits(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(units) == 0 || units["gh-agents-cleanup.timer"].Name == "" {
		t.Fatalf("parsed runner units = %+v", units)
	}
	for name, unit := range units {
		if unit.Name != name || unit.LoadState == "" || unit.ActiveState == "" {
			t.Fatalf("invalid parsed unit: %+v", unit)
		}
	}
	if !(RunnerUnit{LoadState: "loaded", ActiveState: "active"}).Healthy() || (RunnerUnit{LoadState: "loaded", ActiveState: "failed"}).Healthy() {
		t.Fatal("runner unit health classification is incorrect")
	}
	for _, malformed := range []string{
		"gh-agents-cleanup.timer\tloaded\n",
		"gh-agents-cleanup.timer\tloaded\tactive\nother.service\tloaded\tactive\n",
		"gh-agents-cleanup.timer\tloaded\tactive\ngh-agents-cleanup.timer\tloaded\tactive\n",
		"actions.runner.owner-repo.owner--repo-1.service\tloaded\tactive\n",
	} {
		if _, err := ParseRunnerUnits(malformed); err == nil {
			t.Fatalf("accepted malformed snapshot %q", malformed)
		}
	}
}

func TestRunnerUnitFixtureSanitizer(t *testing.T) {
	raw := "actions.runner.private-repo.owner--repo-1.service\tloaded\tactive\ngh-agents-cleanup.timer\tloaded\tactive\n"
	cmd := exec.Command("awk", "-f", filepath.Join("..", "..", "scripts", "sanitize-runner-units.awk"))
	cmd.Stdin = strings.NewReader(raw)
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(output), "private-repo") || !strings.Contains(string(output), "actions.runner.captured-1.service\tloaded\tactive") {
		t.Fatalf("sanitized fixture = %q", output)
	}
	if _, err := ParseRunnerUnits(string(output)); err != nil {
		t.Fatalf("sanitized fixture did not parse: %v", err)
	}
	cmd = exec.Command("awk", "-f", filepath.Join("..", "..", "scripts", "sanitize-runner-units.awk"))
	cmd.Stdin = strings.NewReader(raw + "unexpected.service\tloaded\tactive\n")
	if _, err := cmd.Output(); err == nil {
		t.Fatal("sanitizer accepted an unexpected unit name")
	}
}

func TestRunnerUnitsStayMonitoredWhenAUnitDisappears(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "vpsdash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertHost(ctx, store.Host{ID: "vps", TailnetName: "vps.example.ts.net", Kind: "vps"}); err != nil {
		t.Fatal(err)
	}
	h := config.Host{ID: "vps", TailnetName: "vps.example.ts.net", Kind: "vps", Local: true}
	c := New(config.Config{Hosts: []config.Host{h}, RunnerUnitHosts: []config.RunnerUnitHost{{HostID: "vps"}}}, s, nil)
	defer c.Close()
	active := map[string]RunnerUnit{
		"actions.runner.owner-repo.owner--repo-1.service": {Name: "actions.runner.owner-repo.owner--repo-1.service", LoadState: "loaded", ActiveState: "active"},
		"gh-agents-cleanup.timer":                         {Name: "gh-agents-cleanup.timer", LoadState: "loaded", ActiveState: "active"},
	}
	base := time.Unix(1000, 0)
	if err := c.saveRunnerUnits(ctx, "vps", active, base); err != nil {
		t.Fatal(err)
	}
	missing := map[string]RunnerUnit{"gh-agents-cleanup.timer": active["gh-agents-cleanup.timer"]}
	for i := 1; i <= 3; i++ {
		if err := c.saveRunnerUnits(ctx, "vps", missing, base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	projects, err := s.Projects(ctx)
	if err != nil || len(projects) != 2 {
		t.Fatalf("projects = %+v, %v", projects, err)
	}
	var runner *store.Project
	for i := range projects {
		if projects[i].Name == "actions.runner.owner-repo.owner--repo-1.service" {
			runner = &projects[i]
		}
	}
	if runner == nil || !runner.Monitored || runner.CheckOK == nil || *runner.CheckOK {
		t.Fatalf("missing runner was not marked unhealthy: %+v", runner)
	}
	alerts, err := s.PendingAlerts(ctx)
	if err != nil || len(alerts) != 1 || alerts[0].Subject != "vps / actions.runner.owner-repo.owner--repo-1.service" {
		t.Fatalf("alerts = %+v, %v", alerts, err)
	}
	if err := c.saveRunnerUnits(ctx, "vps", active, base.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	projects, err = s.Projects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range projects {
		if p.Name == runner.Name && (p.CheckOK == nil || !*p.CheckOK) {
			t.Fatalf("recovered runner still unhealthy: %+v", p)
		}
	}
	c.Config.RunnerUnitHosts = nil
	c.checkProjects(ctx, projects)
	projects, err = s.Projects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range projects {
		if p.CheckedAt != base.Add(4*time.Minute).Unix() {
			t.Fatalf("ordinary health loop checked native unit after account removal: %+v", p)
		}
	}
}
