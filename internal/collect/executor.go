package collect

import (
	"bytes"
	"context"
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
	session.Stdin = strings.NewReader(script)
	var output bytes.Buffer
	session.Stdout = &output
	session.Stderr = &output
	done := make(chan error, 1)
	go func() { done <- session.Run("sh -s") }()
	select {
	case err := <-done:
		if err != nil {
			return output.String(), fmt.Errorf("ssh %s: %w", host.ID, err)
		}
		return output.String(), nil
	case <-ctx.Done():
		_ = session.Close()
		e.invalidate(host.ID)
		return output.String(), ctx.Err()
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
		home, _ := os.UserHomeDir()
		keyFile = filepath.Join(home, ".ssh", "id_ed25519")
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
	known, err := knownhosts.New(filepath.Join(home, ".ssh", "known_hosts"))
	if err != nil {
		return nil, fmt.Errorf("known_hosts: %w", err)
	}
	if host.SSHUser == "" {
		return nil, errors.New("ssh_user is required")
	}
	sshConfig := &ssh.ClientConfig{User: host.SSHUser, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: known, Timeout: 10 * time.Second}
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
