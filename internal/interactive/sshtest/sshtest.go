// Package sshtest is an in-process SSH server for tests of the interactive
// bridge. It accepts one authorized key, records PTY, resize, and exec
// requests, echoes shell input, and runs exec requests with sh -c in Dir so a
// test can prove that quoted arguments stay literal.
package sshtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// Resize is one PTY size observed by the server.
type Resize struct{ Cols, Rows int }

// Server is a minimal SSH server bound to loopback.
type Server struct {
	Addr     string
	Dir      string
	listener net.Listener
	config   *ssh.ServerConfig
	hostKey  ssh.PublicKey

	mu         sync.Mutex
	authorized [][]byte
	exec       func(command string, stdin io.Reader, stdout io.Writer) int
	commands   []string
	ptys       []string
	resizes    []Resize
	inputs     []byte
	users      []string
	wg         sync.WaitGroup
}

// GenerateKey writes an unencrypted OpenSSH Ed25519 private key to path.
func GenerateKey(path string) (ssh.PublicKey, error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		return nil, err
	}
	return signer.PublicKey(), nil
}

// Start listens on 127.0.0.1 and accepts only the authorized key.
func Start(authorized ssh.PublicKey, dir string) (*Server, error) {
	_, hostPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPrivate)
	if err != nil {
		return nil, err
	}
	s := &Server{Dir: dir, hostKey: hostSigner.PublicKey()}
	s.Authorize(authorized)
	s.config = &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, allowed := range s.authorized {
			if string(key.Marshal()) == string(allowed) {
				s.users = append(s.users, meta.User())
				return &ssh.Permissions{}, nil
			}
		}
		return nil, fmt.Errorf("unknown key")
	}}
	s.config.AddHostKey(hostSigner)
	s.listener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s.Addr = s.listener.Addr().String()
	s.wg.Add(1)
	go s.serve()
	return s, nil
}

// Authorize accepts another client key.
func (s *Server) Authorize(key ssh.PublicKey) {
	if key == nil {
		return
	}
	s.mu.Lock()
	s.authorized = append(s.authorized, key.Marshal())
	s.mu.Unlock()
}

// HandleExec replaces the default "sh -c" handling of non-PTY exec requests.
func (s *Server) HandleExec(handler func(command string, stdin io.Reader, stdout io.Writer) int) {
	s.mu.Lock()
	s.exec = handler
	s.mu.Unlock()
}

// KnownHostsLine returns the known_hosts entry for the server address.
func (s *Server) KnownHostsLine() string {
	host, port, _ := net.SplitHostPort(s.Addr)
	return fmt.Sprintf("[%s]:%s %s", host, port, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.hostKey))))
}

// WriteKnownHosts writes a known_hosts file containing only this server.
func (s *Server) WriteKnownHosts(dir string) (string, error) {
	path := filepath.Join(dir, "known_hosts")
	return path, os.WriteFile(path, []byte(s.KnownHostsLine()+"\n"), 0600)
}

// Port returns the listening port.
func (s *Server) Port() int {
	_, port, _ := net.SplitHostPort(s.Addr)
	var value int
	fmt.Sscanf(port, "%d", &value)
	return value
}

// Close stops accepting connections.
func (s *Server) Close() error { return s.listener.Close() }

// Commands returns every exec command string received.
func (s *Server) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

// PTYs returns every PTY request as "term cols x rows".
func (s *Server) PTYs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ptys...)
}

// Resizes returns every window-change request.
func (s *Server) Resizes() []Resize {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Resize(nil), s.resizes...)
}

// Users returns the authenticated SSH users.
func (s *Server) Users() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.users...)
}

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	_, chans, reqs, err := ssh.NewServerConn(conn, s.config)
	if err != nil {
		_ = conn.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go s.session(channel, requests)
	}
}

func exitStatus(channel ssh.Channel, code int) {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, uint32(code))
	_, _ = channel.SendRequest("exit-status", false, payload)
	_ = channel.Close()
}

func parseString(payload []byte) (string, []byte, bool) {
	if len(payload) < 4 {
		return "", nil, false
	}
	size := binary.BigEndian.Uint32(payload)
	if uint32(len(payload)-4) < size {
		return "", nil, false
	}
	return string(payload[4 : 4+size]), payload[4+size:], true
}

func (s *Server) session(channel ssh.Channel, requests <-chan *ssh.Request) {
	pty := false
	for req := range requests {
		switch req.Type {
		case "pty-req":
			term, rest, ok := parseString(req.Payload)
			if !ok || len(rest) < 8 {
				_ = req.Reply(false, nil)
				continue
			}
			cols, rows := binary.BigEndian.Uint32(rest), binary.BigEndian.Uint32(rest[4:])
			s.mu.Lock()
			s.ptys = append(s.ptys, fmt.Sprintf("%s %dx%d", term, cols, rows))
			s.mu.Unlock()
			pty = true
			_ = req.Reply(true, nil)
		case "window-change":
			if len(req.Payload) >= 8 {
				s.mu.Lock()
				s.resizes = append(s.resizes, Resize{int(binary.BigEndian.Uint32(req.Payload)), int(binary.BigEndian.Uint32(req.Payload[4:]))})
				s.mu.Unlock()
			}
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
		case "env":
			_ = req.Reply(true, nil)
		case "shell":
			_ = req.Reply(true, nil)
			go s.echoShell(channel)
		case "exec":
			command, _, ok := parseString(req.Payload)
			if !ok {
				_ = req.Reply(false, nil)
				continue
			}
			s.mu.Lock()
			s.commands = append(s.commands, command)
			s.mu.Unlock()
			_ = req.Reply(true, nil)
			if pty && strings.HasPrefix(command, "'tmux' ") {
				go s.echoShell(channel)
				continue
			}
			go s.run(channel, command)
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

// echoShell answers each line with "eco: <line>" and exits on "exit".
func (s *Server) echoShell(channel ssh.Channel) {
	_, _ = io.WriteString(channel, "fake-shell$ ")
	line := []byte{}
	buffer := make([]byte, 1024)
	for {
		n, err := channel.Read(buffer)
		if err != nil {
			exitStatus(channel, 0)
			return
		}
		s.mu.Lock()
		s.inputs = append(s.inputs, buffer[:n]...)
		s.mu.Unlock()
		for _, b := range buffer[:n] {
			if b == '\r' || b == '\n' {
				text := string(line)
				line = line[:0]
				if text == "exit" {
					_, _ = io.WriteString(channel, "\r\nlogout\r\n")
					exitStatus(channel, 0)
					return
				}
				_, _ = io.WriteString(channel, "\r\neco: "+text+"\r\nfake-shell$ ")
				continue
			}
			line = append(line, b)
			_, _ = channel.Write([]byte{b})
		}
	}
}

func (s *Server) run(channel ssh.Channel, command string) {
	s.mu.Lock()
	handler := s.exec
	s.mu.Unlock()
	if handler != nil {
		exitStatus(channel, handler(command, channel, channel))
		return
	}
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = s.Dir
	cmd.Stdout = channel
	cmd.Stderr = channel.Stderr()
	code := 0
	if err := cmd.Run(); err != nil {
		code = 127
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		}
	}
	exitStatus(channel, code)
}
