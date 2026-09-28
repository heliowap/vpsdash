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
	"strconv"
	"strings"
	"unicode"
)

type Host struct {
	ID          string `json:"id"`
	TailnetName string `json:"tailnet_name"`
	Kind        string `json:"kind"`
	SSHUser     string `json:"ssh_user,omitempty"`
	SSHKeyFile  string `json:"ssh_key_file,omitempty"`
	// SSHPort defaults to 22 for both the collector and the interactive key.
	SSHPort int  `json:"ssh_port,omitempty"`
	Local   bool `json:"local,omitempty"`
	// FileRoots enables read-only file browsing below these directories.
	// Remote hosts must also list them in the bridge's own roots file.
	FileRoots []string `json:"file_roots,omitempty"`
	// InteractiveKeyFile is a second key, authorized with restrict,pty for the
	// same ssh_user. Terminal, tmux attach, and snippets are disabled without it.
	InteractiveKeyFile string    `json:"interactive_key_file,omitempty"`
	Snippets           []Snippet `json:"snippets,omitempty"`
}

// Snippet is a fixed command from the private inventory. The argument list
// is quoted word by word for the remote shell; the panel never accepts
// arguments from a request.
type Snippet struct {
	Name           string   `json:"name"`
	Argv           []string `json:"argv"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
}

const DefaultSnippetTimeout = 30
const MaxSnippetTimeout = 300

// InteractiveUser is the only account an interactive key may log in as. The
// PTY runs with that account's rights, so root, the fleet account
// (gh-agents), the service account (vpsdash), and any runner_unit_hosts
// account are refused even if the allow list ever grows.
const InteractiveUser = "helio"

var isolatedAccounts = map[string]bool{"root": true, "gh-agents": true, "vpsdash": true}

// ValidateInteractiveUser applies the isolation rule for an interactive
// ssh_user: it must be InteractiveUser and must not be an isolated account
// or an account the runner collector uses.
func (c Config) ValidateInteractiveUser(user string) error {
	if user == "" {
		return errors.New("ssh_user is required")
	}
	if isolatedAccounts[user] {
		return fmt.Errorf("ssh_user %q is isolated from interactive access", user)
	}
	for _, runner := range c.RunnerUnitHosts {
		if runner.SSHUser == user {
			return fmt.Errorf("ssh_user %q is a runner_unit_hosts account", user)
		}
	}
	if user != InteractiveUser {
		return fmt.Errorf("ssh_user %q is not allowed; interactive access logs in only as %s", user, InteractiveUser)
	}
	return nil
}

// Address returns the SSH endpoint for the host.
func (h Host) Address() string {
	port := h.SSHPort
	if port == 0 {
		port = 22
	}
	return net.JoinHostPort(strings.TrimSuffix(h.TailnetName, "."), strconv.Itoa(port))
}

// Interactive reports whether the host has a key for terminal access.
func (h Host) Interactive() bool { return h.InteractiveKeyFile != "" }

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
	Listen string `json:"listen"`
	// PrivateListen is the loopback listener that only Tailscale Serve may
	// reach. Terminal, attach, and snippet routes exist only on it.
	PrivateListen      string           `json:"private_listen,omitempty"`
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

func loopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return errors.New("must bind to a loopback IP")
	}
	return nil
}

func validSnippetText(value string, limit int) bool {
	if value == "" || len(value) > limit {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return false
		}
	}
	return true
}

func (c Config) Validate() error {
	if err := loopbackAddress(c.Listen); err != nil {
		return fmt.Errorf("listen %w for the public proxy and tailscale serve", err)
	}
	if c.PrivateListen != "" {
		if err := loopbackAddress(c.PrivateListen); err != nil {
			return fmt.Errorf("private_listen %w", err)
		}
		if c.PrivateListen == c.Listen {
			return errors.New("private_listen must differ from listen")
		}
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
		if h.SSHPort < 0 || h.SSHPort > 65535 {
			return fmt.Errorf("invalid ssh_port for host %s", h.ID)
		}
	}
	// Interactive keys share seenKeys with host and runner collector keys, so
	// no key is authorized for both the read-only bridge and a PTY.
	for _, h := range c.Hosts {
		if h.InteractiveKeyFile == "" {
			if len(h.Snippets) > 0 {
				return fmt.Errorf("host %s has snippets without interactive_key_file", h.ID)
			}
			continue
		}
		if h.Kind != "vps" || h.Local || h.SSHUser == "" {
			return fmt.Errorf("host %s needs kind vps with ssh_user for interactive access", h.ID)
		}
		if err := c.ValidateInteractiveUser(h.SSHUser); err != nil {
			return fmt.Errorf("host %s: %w", h.ID, err)
		}
		if !filepath.IsAbs(h.InteractiveKeyFile) {
			return fmt.Errorf("host %s needs an absolute interactive_key_file", h.ID)
		}
		if seenKeys[h.InteractiveKeyFile] {
			return fmt.Errorf("interactive key of host %s reuses another SSH key", h.ID)
		}
		seenKeys[h.InteractiveKeyFile] = true
		seenSnippets := map[string]bool{}
		for _, snippet := range h.Snippets {
			if !validSnippetText(snippet.Name, 64) || seenSnippets[snippet.Name] {
				return fmt.Errorf("host %s has an invalid or duplicate snippet name %q", h.ID, snippet.Name)
			}
			seenSnippets[snippet.Name] = true
			if len(snippet.Argv) == 0 || len(snippet.Argv) > 32 {
				return fmt.Errorf("snippet %s/%s needs between 1 and 32 arguments", h.ID, snippet.Name)
			}
			for _, arg := range snippet.Argv {
				if strings.ContainsAny(arg, "\x00\r\n") || len(arg) > 1024 {
					return fmt.Errorf("snippet %s/%s has an invalid argument", h.ID, snippet.Name)
				}
			}
			if snippet.Argv[0] == "" {
				return fmt.Errorf("snippet %s/%s needs a program name", h.ID, snippet.Name)
			}
			if snippet.TimeoutSeconds < 0 || snippet.TimeoutSeconds > MaxSnippetTimeout {
				return fmt.Errorf("snippet %s/%s timeout must be between 1 and %d seconds", h.ID, snippet.Name, MaxSnippetTimeout)
			}
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
