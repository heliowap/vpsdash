package collect

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/heliowap/vpsdash/internal/config"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type Executor struct {
	mu      sync.Mutex
	clients map[string]*ssh.Client
}

func NewExecutor() *Executor { return &Executor{clients: map[string]*ssh.Client{}} }

func (e *Executor) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for key, client := range e.clients {
		_ = client.Close()
		delete(e.clients, key)
	}
}

func (e *Executor) Run(ctx context.Context, host config.Host, script string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if host.Local {
		cmd := exec.CommandContext(ctx, "sh", "-s")
		cmd.Stdin = strings.NewReader(script)
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	return e.runRemote(ctx, host, "sh -s", script)
}

func (e *Executor) Check(ctx context.Context, host config.Host, source, name string) (string, error) {
	if host.Local {
		script, err := healthScript(source, name)
		if err != nil {
			return "", err
		}
		return e.Run(ctx, host, script)
	}
	command, err := healthCommand(source, name)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return e.runRemote(ctx, host, command, "")
}

func healthCommand(source, name string) (string, error) {
	if source != "systemd" && source != "docker" && source != "tmux" {
		return "", errors.New("unsupported project source")
	}
	if name == "" || len(name) > 256 || strings.ContainsAny(name, "\x00\r\n") {
		return "", errors.New("invalid project name")
	}
	return "vpsdash-health " + source + " " + base64.RawURLEncoding.EncodeToString([]byte(name)), nil
}

func (e *Executor) runRemote(ctx context.Context, host config.Host, command, input string) (string, error) {
	client, err := e.client(host)
	if err != nil {
		return "", err
	}
	session, err := client.NewSession()
	if err != nil {
		e.invalidate(host.ID)
		return "", err
	}
	defer session.Close()
	session.Stdin = strings.NewReader(input)
	type result struct {
		output []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		output, err := session.CombinedOutput(command)
		done <- result{output, err}
	}()
	select {
	case result := <-done:
		if result.err != nil {
			return string(result.output), fmt.Errorf("ssh %s: %w", host.ID, result.err)
		}
		return string(result.output), nil
	case <-ctx.Done():
		_ = session.Close()
		return "", ctx.Err()
	}
}

func (e *Executor) client(host config.Host) (*ssh.Client, error) {
	e.mu.Lock()
	if client := e.clients[host.ID]; client != nil {
		e.mu.Unlock()
		return client, nil
	}
	e.mu.Unlock()
	keyFile := host.SSHKeyFile
	if keyFile == "" {
		return nil, errors.New("ssh_key_file is required")
	}
	keyBytes, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	// Re-read known_hosts for each new handshake. An existing SSH connection
	// continues using the host identity authenticated when it was opened.
	known, err := knownhosts.New(filepath.Join(home, ".ssh", "known_hosts"))
	if err != nil {
		return nil, fmt.Errorf("known_hosts: %w", err)
	}
	if host.SSHUser == "" {
		return nil, errors.New("ssh_user is required")
	}
	sshConfig := &ssh.ClientConfig{User: host.SSHUser, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: known, HostKeyAlgorithms: []string{ssh.KeyAlgoED25519}, Timeout: 10 * time.Second}
	address := strings.TrimSuffix(host.TailnetName, ".") + ":22"
	client, err := ssh.Dial("tcp", address, sshConfig)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	if existing := e.clients[host.ID]; existing != nil {
		e.mu.Unlock()
		_ = client.Close()
		return existing, nil
	}
	e.clients[host.ID] = client
	e.mu.Unlock()
	return client, nil
}

func (e *Executor) invalidate(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if client := e.clients[id]; client != nil {
		_ = client.Close()
		delete(e.clients, id)
	}
}
