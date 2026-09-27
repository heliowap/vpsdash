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
	// FileRoots enables read-only file browsing below these directories.
	// Remote hosts must also list them in the bridge's own roots file.
	FileRoots []string `json:"file_roots,omitempty"`
}

type Repository struct {
	Name            string `json:"name"`
	AgentSwitchable bool   `json:"agent_switchable"`
	CISwitchable    bool   `json:"ci_switchable"`
	RunnerHostID    string `json:"runner_host_id,omitempty"`
}

type RunnerUnitHost struct {
	HostID     string `json:"host_id"`
	SSHUser    string `json:"ssh_user"`
	SSHKeyFile string `json:"ssh_key_file"`
}

type Config struct {
	Listen             string           `json:"listen"`
	Database           string           `json:"database"`
	TrustProxyHeader   bool             `json:"trust_proxy_header,omitempty"`
	TailscaleServeHost string           `json:"tailscale_serve_host,omitempty"`
	Hosts              []Host           `json:"hosts"`
	RunnerUnitHosts    []RunnerUnitHost `json:"runner_unit_hosts,omitempty"`
	Repositories       []Repository     `json:"repositories"`
}

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var runnerServiceName = regexp.MustCompile(`^actions\.runner\.[A-Za-z0-9._-]+\.service$`)

func IsRunnerUnitName(name string) bool {
	return len(name) <= 255 && (name == "gh-agents-cleanup.timer" || runnerServiceName.MatchString(name))
}

// IsRunnerServiceName accepts only runner service units, which the panel may
// restart or drain. The cleanup timer stays read-only.
func IsRunnerServiceName(name string) bool {
	return len(name) <= 255 && runnerServiceName.MatchString(name)
}

func (c Config) IsNativeRunnerUnit(hostID, name string) bool {
	if !IsRunnerUnitName(name) {
		return false
	}
	for _, runner := range c.RunnerUnitHosts {
		if runner.HostID == hostID {
			return true
		}
	}
	return false
}

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
	hostKinds := map[string]string{}
	seenKeys := map[string]bool{}
	serveHostFound := c.TailscaleServeHost == ""
	for _, h := range c.Hosts {
		if !safeName.MatchString(h.ID) || h.TailnetName == "" {
			return fmt.Errorf("invalid host: %q", h.ID)
		}
		if seenHosts[h.ID] {
			return fmt.Errorf("duplicate host: %s", h.ID)
		}
		seenHosts[h.ID] = true
		hostKinds[h.ID] = h.Kind
		if strings.EqualFold(strings.TrimSuffix(h.TailnetName, "."), c.TailscaleServeHost) {
			serveHostFound = true
		}
		if h.Kind != "vps" && h.Kind != "presence" {
			return fmt.Errorf("invalid kind for host %s", h.ID)
		}
		if err := validateFileRoots(h); err != nil {
			return err
		}
		if h.Kind == "vps" && !h.Local {
			if h.SSHUser == "" {
				return fmt.Errorf("host %s needs ssh_user", h.ID)
			}
			if !filepath.IsAbs(h.SSHKeyFile) {
				return fmt.Errorf("host %s needs an absolute ssh_key_file", h.ID)
			}
			if seenKeys[h.SSHKeyFile] {
				return fmt.Errorf("SSH key is reused by host %s", h.ID)
			}
			seenKeys[h.SSHKeyFile] = true
		}
	}
	if !serveHostFound {
		return errors.New("tailscale_serve_host must name a host in the inventory")
	}
	seenRunnerHosts := map[string]bool{}
	for _, runner := range c.RunnerUnitHosts {
		if !seenHosts[runner.HostID] || hostKinds[runner.HostID] != "vps" {
			return fmt.Errorf("runner unit host %s must reference a VPS", runner.HostID)
		}
		if seenRunnerHosts[runner.HostID] {
			return fmt.Errorf("duplicate runner unit host %s", runner.HostID)
		}
		seenRunnerHosts[runner.HostID] = true
		if runner.SSHUser == "" || !filepath.IsAbs(runner.SSHKeyFile) {
			return fmt.Errorf("runner unit host %s needs ssh_user and an absolute ssh_key_file", runner.HostID)
		}
		if seenKeys[runner.SSHKeyFile] {
			return fmt.Errorf("SSH key is reused by runner unit host %s", runner.HostID)
		}
		seenKeys[runner.SSHKeyFile] = true
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

func validateFileRoots(h Host) error {
	if len(h.FileRoots) > 0 && h.Kind != "vps" {
		return fmt.Errorf("host %s: file_roots requires kind vps", h.ID)
	}
	seen := map[string]bool{}
	for _, root := range h.FileRoots {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" || strings.ContainsRune(root, 0) || len(root) > 4096 {
			return fmt.Errorf("host %s: file root %q must be a clean absolute directory other than /", h.ID, root)
		}
		if seen[root] {
			return fmt.Errorf("host %s: duplicate file root %s", h.ID, root)
		}
		seen[root] = true
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
