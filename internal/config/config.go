package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Host struct {
	ID          string `json:"id"`
	TailnetName string `json:"tailnet_name"`
	Kind        string `json:"kind"`
	SSHUser     string `json:"ssh_user,omitempty"`
	SSHKeyFile  string `json:"ssh_key_file,omitempty"`
	Local       bool   `json:"local,omitempty"`
}

type Repository struct {
	Name            string `json:"name"`
	AgentSwitchable bool   `json:"agent_switchable"`
	CISwitchable    bool   `json:"ci_switchable"`
	RunnerHostID    string `json:"runner_host_id,omitempty"`
}

type Config struct {
	Listen       string       `json:"listen"`
	Database     string       `json:"database"`
	Hosts        []Host       `json:"hosts"`
	Repositories []Repository `json:"repositories"`
}

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func Load(path string) (Config, error) {
	c := Config{Listen: "127.0.0.1:8484"}
	home, err := os.UserHomeDir()
	if err != nil {
		return c, err
	}
	c.Database = filepath.Join(home, "vpsdash.db")
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return errors.New("listen must bind to loopback for tailscale serve")
	}
	seenHosts := map[string]bool{}
	for _, h := range c.Hosts {
		if !safeName.MatchString(h.ID) || h.TailnetName == "" {
			return fmt.Errorf("invalid host: %q", h.ID)
		}
		if seenHosts[h.ID] {
			return fmt.Errorf("duplicate host: %s", h.ID)
		}
		seenHosts[h.ID] = true
		if h.Kind != "vps" && h.Kind != "presence" {
			return fmt.Errorf("invalid kind for host %s", h.ID)
		}
		if h.Kind == "vps" && !h.Local && h.SSHUser == "" {
			return fmt.Errorf("host %s needs ssh_user", h.ID)
		}
	}
	seenRepos := map[string]bool{}
	for _, repo := range c.Repositories {
		parts := strings.Split(repo.Name, "/")
		if len(parts) != 2 || !safeName.MatchString(parts[0]) || !safeName.MatchString(parts[1]) {
			return fmt.Errorf("invalid repository: %q", repo.Name)
		}
		if seenRepos[repo.Name] {
			return fmt.Errorf("duplicate repository: %s", repo.Name)
		}
		seenRepos[repo.Name] = true
		if repo.RunnerHostID != "" && !seenHosts[repo.RunnerHostID] {
			return fmt.Errorf("unknown runner host %s", repo.RunnerHostID)
		}
	}
	return nil
}

// ReadEnvFile reads a 0600 KEY=VALUE file without executing shell expansions.
func ReadEnvFile(path string) (map[string]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("%s must be readable only by its owner", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || !safeName.MatchString(key) {
			return nil, fmt.Errorf("invalid env line in %s", path)
		}
		values[key] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return values, scanner.Err()
}
