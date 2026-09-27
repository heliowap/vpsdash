package collect

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heliowap/vpsdash/internal/config"
	"golang.org/x/crypto/ssh"
)

func TestExecutorKeepsConcurrentSSHOutput(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{NoClientAuth: true}
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	slowStarted := make(chan struct{})
	slowRelease := make(chan struct{})
	defer close(slowRelease)
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go serveFakeSSHConnection(connection, serverConfig, slowStarted, slowRelease)
		}
	}()
	client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{
		User: "test", HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	healthClient, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{
		User: "test", HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	executor := NewExecutor()
	executor.clients["test"] = client
	executor.clients["health:test"] = healthClient
	defer executor.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := executor.runRemote(ctx, config.Host{ID: "test"}, "sh -s", "echo input\n")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(output, "stdout\n") != 128 || strings.Count(output, "stderr\n") != 128 {
		t.Fatalf("SSH output was truncated: %d stdout lines, %d stderr lines", strings.Count(output, "stdout\n"), strings.Count(output, "stderr\n"))
	}
	slowCtx, stopSlow := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stopSlow()
	slowResult := make(chan error, 1)
	go func() {
		_, err := executor.Check(slowCtx, config.Host{ID: "test"}, "systemd", "hold.service")
		slowResult <- err
	}()
	select {
	case <-slowStarted:
	case <-time.After(time.Second):
		t.Fatal("slow SSH session did not start")
	}
	if err := <-slowResult; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow SSH session = %v", err)
	}
	if executor.clients["health:test"] != nil {
		t.Fatal("timed-out health connection was not invalidated")
	}
	if _, err := executor.runRemote(ctx, config.Host{ID: "test"}, "sh -s", "echo after-timeout\n"); err != nil {
		t.Fatalf("another SSH session lost its pooled client after a caller timeout: %v", err)
	}
}

func serveFakeSSHConnection(connection net.Conn, cfg *ssh.ServerConfig, slowStarted, slowRelease chan struct{}) {
	server, channels, requests, err := ssh.NewServerConn(connection, cfg)
	if err != nil {
		return
	}
	defer server.Close()
	go ssh.DiscardRequests(requests)
	for pending := range channels {
		channel, requests, err := pending.Accept()
		if err != nil {
			return
		}
		go func() {
			defer channel.Close()
			inputDone := make(chan struct{})
			go func() {
				_, _ = io.Copy(io.Discard, channel)
				close(inputDone)
			}()
			for request := range requests {
				if request.Type != "exec" {
					request.Reply(false, nil)
					continue
				}
				request.Reply(true, nil)
				var execRequest struct{ Command string }
				if err := ssh.Unmarshal(request.Payload, &execRequest); err == nil && strings.HasPrefix(execRequest.Command, "vpsdash-health ") {
					close(slowStarted)
					<-slowRelease
					return
				}
				var writes sync.WaitGroup
				writes.Add(2)
				go func() {
					defer writes.Done()
					for range 128 {
						_, _ = io.WriteString(channel, "stdout\n")
					}
				}()
				go func() {
					defer writes.Done()
					for range 128 {
						_, _ = io.WriteString(channel.Stderr(), "stderr\n")
					}
				}()
				writes.Wait()
				<-inputDone
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				return
			}
		}()
	}
}

func TestExecutorBoundsStalledSessionOpen(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{NoClientAuth: true}
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	release := make(chan struct{})
	defer close(release)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		server, channels, requests, err := ssh.NewServerConn(connection, serverConfig)
		if err != nil {
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(requests)
		select {
		case <-channels: // Leave the channel-open request unanswered.
			<-release
		case <-release:
		}
	}()
	client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{
		User: "test", HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	executor := NewExecutor()
	executor.clients["test"] = client
	defer executor.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = executor.runRemote(ctx, config.Host{ID: "test"}, "sh -s", "")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("stalled session open = %v after %v", err, time.Since(started))
	}
	if executor.clients["test"] != nil {
		t.Fatal("stalled connection stayed in the pool")
	}
}

func TestFileCommandOnlySendsFileReads(t *testing.T) {
	executor := NewExecutor()
	remote := config.Host{ID: "vps", Kind: "vps", SSHUser: "operator", SSHKeyFile: "/nonexistent/key"}
	for _, command := range []string{"sh -s", "vpsdash-health systemd eA", "vpsdash-files list eA\nsh -s"} {
		if _, err := executor.FileCommand(context.Background(), remote, command); err == nil || !strings.Contains(err.Error(), "invalid file command") {
			t.Errorf("%q = %v", command, err)
		}
	}
	if _, err := executor.FileCommand(context.Background(), config.Host{ID: "local", Local: true}, "vpsdash-files list eA"); err == nil {
		t.Error("local host was sent over SSH")
	}
	if len(executor.clients) != 0 {
		t.Fatalf("rejected commands opened connections: %v", executor.clients)
	}
}
