package collect

import (
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseMetricsSnapshot(t *testing.T) {
	s, err := ParseMetricsSnapshot(fixture(t, "metrics.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if s.CPUTotal != 1543795367 || s.CPUIdle != 1478016135 {
		t.Fatalf("cpu counters = %d/%d", s.CPUTotal, s.CPUIdle)
	}
	if s.MemoryPct < 26.30 || s.MemoryPct > 26.33 {
		t.Errorf("memory = %.4f%%", s.MemoryPct)
	}
	if s.DiskPct < 19.43 || s.DiskPct > 19.45 {
		t.Errorf("disk = %.4f%%", s.DiskPct)
	}
	if s.UptimeSeconds != 3883193 {
		t.Errorf("uptime = %d", s.UptimeSeconds)
	}
}

func TestParseMetricsSnapshotWithWrappedFilesystemName(t *testing.T) {
	raw := strings.Replace(fixture(t, "metrics.txt"),
		"/dev/sda1        202051056 39281196 162753476      20% /",
		"/dev/mapper/very-long-root-filesystem-name\n202051056 39281196 162753476 20% /subvolume", 1)
	snapshot, err := ParseMetricsSnapshot(raw)
	if err != nil || snapshot.DiskPct < 19.43 || snapshot.DiskPct > 19.45 {
		t.Fatalf("wrapped df output = %+v, %v", snapshot, err)
	}
}

func TestCPUPercentUsesCounterDelta(t *testing.T) {
	prev := MetricsSnapshot{CPUTotal: 1000, CPUIdle: 700}
	next := MetricsSnapshot{CPUTotal: 1100, CPUIdle: 730}
	if got := CPUPercent(prev, next); got != 70 {
		t.Fatalf("CPU percent = %v", got)
	}
}

func TestParseTailnetStatus(t *testing.T) {
	devices, err := ParseTailnetStatus(fixture(t, "tailnet.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 3 {
		t.Fatalf("devices = %d", len(devices))
	}
	if devices[0].Name != "sample-vps" || !devices[0].Online {
		t.Errorf("self = %+v", devices[0])
	}
	if devices[2].Kind != "presence" || devices[2].Online {
		t.Errorf("windows = %+v", devices[2])
	}
}

func TestParseDiscovery(t *testing.T) {
	projects, err := ParseDiscovery(fixture(t, "discovery.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 3 {
		t.Fatalf("projects = %d", len(projects))
	}
	if projects[0].Source != "docker" || projects[0].Name != "cloudflare-os-caddy" {
		t.Errorf("docker = %+v", projects[0])
	}
	if projects[1].Source != "systemd" || projects[1].Name != "cloudflare-os.service" {
		t.Errorf("systemd = %+v", projects[1])
	}
}
