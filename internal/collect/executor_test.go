package collect

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
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
		for pending := range channels {
			channel, requests, err := pending.Accept()
			if err != nil {
				return
			}
			go func() {
				defer channel.Close()
				go io.Copy(io.Discard, channel)
				for request := range requests {
					if request.Type != "exec" {
						request.Reply(false, nil)
						continue
					}
					request.Reply(true, nil)
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
					_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
					return
				}
			}()
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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := executor.runRemote(ctx, config.Host{ID: "test"}, "sh -s", "echo input\n")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(output, "stdout\n") != 128 || strings.Count(output, "stderr\n") != 128 {
		t.Fatalf("SSH output was truncated: %d stdout lines, %d stderr lines", strings.Count(output, "stdout\n"), strings.Count(output, "stderr\n"))
	}
}
