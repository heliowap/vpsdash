package collect

import (
	"bufio"
	"fmt"
	"strings"

	"github.com/heliowap/vpsdash/internal/config"
)

type RunnerUnit struct {
	Name        string
	LoadState   string
	ActiveState string
}

func (u RunnerUnit) Healthy() bool {
	return u.LoadState == "loaded" && u.ActiveState == "active"
}

// Stopped reports a unit systemd has stopped or is stopping.
func (u RunnerUnit) Stopped() bool {
	switch u.ActiveState {
	case "inactive", "failed", "deactivating":
		return true
	}
	return false
}

// Transitioning reports a unit systemd is still starting or stopping.
func (u RunnerUnit) Transitioning() bool {
	switch u.ActiveState {
	case "activating", "deactivating", "reloading":
		return true
	}
	return false
}

func ParseRunnerUnits(raw string) (map[string]RunnerUnit, error) {
	units := map[string]RunnerUnit{}
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) != 3 || !config.IsRunnerUnitName(fields[0]) || fields[1] == "" || fields[2] == "" {
			return nil, fmt.Errorf("invalid runner unit snapshot line %q", scanner.Text())
		}
		if _, exists := units[fields[0]]; exists {
			return nil, fmt.Errorf("duplicate runner unit %q", fields[0])
		}
		units[fields[0]] = RunnerUnit{Name: fields[0], LoadState: fields[1], ActiveState: fields[2]}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if _, exists := units["gh-agents-cleanup.timer"]; !exists {
		return nil, fmt.Errorf("runner unit snapshot missing cleanup timer")
	}
	return units, nil
}
