package collect

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// MetricsSnapshot contains the cumulative CPU counters and the current host measurements.
type MetricsSnapshot struct {
	CPUTotal      uint64
	CPUIdle       uint64
	MemoryPct     float64
	DiskPct       float64
	UptimeSeconds int64
}

func ParseMetricsSnapshot(raw string) (MetricsSnapshot, error) {
	var result MetricsSnapshot
	var totalMem, availableMem, diskTotal, diskUsed uint64
	var hasCPU, hasUptime bool
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "cpu":
			if len(fields) < 5 {
				return result, errors.New("cpu counters are incomplete")
			}
			for _, field := range fields[1:] {
				value, err := strconv.ParseUint(field, 10, 64)
				if err != nil {
					return result, fmt.Errorf("cpu counter: %w", err)
				}
				result.CPUTotal += value
			}
			idle, _ := strconv.ParseUint(fields[4], 10, 64)
			result.CPUIdle = idle
			if len(fields) > 5 {
				wait, _ := strconv.ParseUint(fields[5], 10, 64)
				result.CPUIdle += wait
			}
			hasCPU = true
		case "MemTotal:":
			if len(fields) < 2 {
				return result, errors.New("MemTotal is missing")
			}
			totalMem, _ = strconv.ParseUint(fields[1], 10, 64)
		case "MemAvailable:":
			if len(fields) < 2 {
				return result, errors.New("MemAvailable is missing")
			}
			availableMem, _ = strconv.ParseUint(fields[1], 10, 64)
		default:
			// df -P may wrap a long filesystem name onto the preceding line.
			// Its final five fields remain size, used, available, use%, mount.
			if len(fields) >= 5 && strings.HasSuffix(fields[len(fields)-2], "%") {
				total, totalErr := strconv.ParseUint(fields[len(fields)-5], 10, 64)
				used, usedErr := strconv.ParseUint(fields[len(fields)-4], 10, 64)
				if totalErr == nil && usedErr == nil {
					diskTotal, diskUsed = total, used
				}
			} else if len(fields) == 2 {
				up, err := strconv.ParseFloat(fields[0], 64)
				if err == nil {
					result.UptimeSeconds = int64(up)
					hasUptime = true
				}
			}
		}
	}
	if !hasCPU || totalMem == 0 || diskTotal == 0 || !hasUptime {
		return result, errors.New("metrics output is incomplete")
	}
	result.MemoryPct = 100 * float64(totalMem-availableMem) / float64(totalMem)
	result.DiskPct = 100 * float64(diskUsed) / float64(diskTotal)
	return result, nil
}

func CPUPercent(previous, current MetricsSnapshot) float64 {
	if current.CPUTotal <= previous.CPUTotal || current.CPUIdle < previous.CPUIdle {
		return 0
	}
	total := current.CPUTotal - previous.CPUTotal
	idle := current.CPUIdle - previous.CPUIdle
	return math.Round(10000*(1-float64(idle)/float64(total))) / 100
}

type Device struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	OS     string `json:"os"`
	Online bool   `json:"online"`
	Kind   string `json:"kind"`
}

func ParseTailnetStatus(raw string) ([]Device, error) {
	var status struct {
		Self struct {
			HostName, DNSName, OS string
			Online                bool
		}
		Peer map[string]struct {
			HostName, DNSName, OS string
			Online                bool
		}
	}
	if err := json.Unmarshal([]byte(raw), &status); err != nil {
		return nil, err
	}
	if status.Self.DNSName == "" {
		return nil, errors.New("tailscale self device is missing")
	}
	result := []Device{{ID: strings.TrimSuffix(status.Self.DNSName, "."), Name: status.Self.HostName, OS: status.Self.OS, Online: status.Self.Online, Kind: "presence"}}
	for _, peer := range status.Peer {
		if peer.DNSName == "" {
			continue
		}
		result = append(result, Device{ID: strings.TrimSuffix(peer.DNSName, "."), Name: peer.HostName, OS: peer.OS, Online: peer.Online, Kind: "presence"})
	}
	sort.Slice(result[1:], func(i, j int) bool { return result[i+1].ID < result[j+1].ID })
	return result, nil
}

type Candidate struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

func ParseDiscovery(raw string) ([]Candidate, error) {
	var candidates []Candidate
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 || fields[1] == "" {
			return nil, fmt.Errorf("invalid discovery line: %q", line)
		}
		source := fields[0]
		if source != "docker" && source != "systemd" && source != "tmux" {
			return nil, fmt.Errorf("unknown discovery source: %q", source)
		}
		candidates = append(candidates, Candidate{Name: fields[1], Source: source})
	}
	return candidates, nil
}
