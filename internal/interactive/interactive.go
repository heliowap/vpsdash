// Package interactive opens write-capable SSH sessions with the per-host
// interactive key. It never uses the collector key and never goes through the
// read-only bridge; the API only exposes it on the private listener.
package interactive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/heliowap/vpsdash/internal/config"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ErrDisabled reports a host without interactive_key_file.
var ErrDisabled = errors.New("interactive access is not configured for this host")

// Dialer connects with a host's interactive key and verifies its host key
// against KnownHostsFile.
type Dialer struct {
	KnownHostsFile string
	Timeout        time.Duration
}

func (d Dialer) timeout() time.Duration {
	if d.Timeout > 0 {
		return d.Timeout
	}
	return 10 * time.Second
}

// Dial opens a new SSH connection for one interactive use. Connections are
// not pooled: closing the terminal or finishing a snippet closes it.
func (d Dialer) Dial(ctx context.Context, host config.Host) (*ssh.Client, error) {
	if !host.Interactive() {
		return nil, ErrDisabled
	}
	if host.SSHUser == "" {
		return nil, errors.New("ssh_user is required")
	}
	keyBytes, err := os.ReadFile(host.InteractiveKeyFile)
	if err != nil {
		return nil, fmt.Errorf("interactive key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("interactive key: %w", err)
	}
	known, err := knownhosts.New(d.KnownHostsFile)
	if err != nil {
		return nil, fmt.Errorf("known_hosts: %w", err)
	}
	cfg := &ssh.ClientConfig{
		User:              host.SSHUser,
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback:   known,
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		Timeout:           d.timeout(),
	}
	dialCtx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()
	address := host.Address()
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", address)
	if err != nil {
		return nil, err
	}
	deadline, _ := dialCtx.Deadline()
	_ = conn.SetDeadline(deadline)
	c, chans, reqs, err := ssh.NewClientConn(conn, address, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), nil
}

// Quote returns one POSIX shell word that the remote login shell turns back
// into exactly arg. Every word is single-quoted, so no expansion, globbing,
// assignment, or reserved word applies.
func Quote(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// CommandLine quotes argv for the SSH exec request. sshd hands the string
// to the account's shell; quoting keeps each argument literal.
func CommandLine(argv []string) string {
	words := make([]string, len(argv))
	for i, arg := range argv {
		words[i] = Quote(arg)
	}
	return strings.Join(words, " ")
}

var controlChars = regexp.MustCompile(`[\x00-\x1f\x7f]`)

// ValidSessionName rejects names that cannot be a collected tmux session.
func ValidSessionName(name string) bool {
	return name != "" && len(name) <= 256 && !controlChars.MatchString(name)
}

// AttachArgv builds the tmux command. The "=" prefix makes tmux match the
// session name exactly instead of treating it as a prefix or a target spec.
func AttachArgv(session string, readOnly bool) []string {
	argv := []string{"tmux", "attach-session"}
	if readOnly {
		argv = append(argv, "-r")
	}
	return append(argv, "-t", "="+session)
}

// PTY is a remote pseudo-terminal running a login shell or one command.
type PTY struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	output  *io.PipeReader
	done    chan error
	once    sync.Once
}

// OpenPTY requests a PTY and starts argv, or the login shell when argv is
// empty.
func (d Dialer) OpenPTY(ctx context.Context, host config.Host, argv []string, cols, rows int) (*PTY, error) {
	client, err := d.Dial(ctx, host)
	if err != nil {
		return nil, err
	}
	session, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	fail := func(err error) (*PTY, error) {
		_ = session.Close()
		_ = client.Close()
		return nil, err
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 38400, ssh.TTY_OP_OSPEED: 38400}
	if err := session.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		return fail(fmt.Errorf("pty: %w", err))
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		return fail(err)
	}
	// Both streams feed one pipe so neither can stall the SSH channel.
	reader, writer := io.Pipe()
	session.Stdout = writer
	session.Stderr = writer
	if len(argv) == 0 {
		err = session.Shell()
	} else {
		err = session.Start(CommandLine(argv))
	}
	if err != nil {
		_ = writer.Close()
		return fail(err)
	}
	p := &PTY{client: client, session: session, stdin: stdin, output: reader, done: make(chan error, 1)}
	go func() {
		err := session.Wait()
		_ = writer.Close()
		p.done <- err
	}()
	return p, nil
}

func (p *PTY) Read(b []byte) (int, error)  { return p.output.Read(b) }
func (p *PTY) Write(b []byte) (int, error) { return p.stdin.Write(b) }

// Resize forwards a window-change request.
func (p *PTY) Resize(cols, rows int) error { return p.session.WindowChange(rows, cols) }

// Done delivers the remote program's exit once.
func (p *PTY) Done() <-chan error { return p.done }

// Close ends the session and the connection.
func (p *PTY) Close() error {
	p.once.Do(func() {
		_ = p.stdin.Close()
		_ = p.session.Close()
		_ = p.client.Close()
		_ = p.output.Close()
	})
	return nil
}

// Result is the bounded outcome of a snippet.
type Result struct {
	Output    string `json:"output"`
	Truncated bool   `json:"truncated"`
	ExitCode  int    `json:"exit_code"`
	TimedOut  bool   `json:"timed_out"`
}

type cappedBuffer struct {
	mu        sync.Mutex
	data      []byte
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	room := b.limit - len(b.data)
	if room < len(p) {
		b.truncated = true
		if room > 0 {
			b.data = append(b.data, p[:room]...)
		}
		return len(p), nil
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

// Run executes argv without a PTY. Output (stdout and stderr together) is
// capped at limit bytes; the command is stopped when timeout expires.
func (d Dialer) Run(ctx context.Context, host config.Host, argv []string, timeout time.Duration, limit int) (Result, error) {
	if len(argv) == 0 {
		return Result{}, errors.New("empty command")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, err := d.Dial(ctx, host)
	if err != nil {
		return Result{}, err
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return Result{}, err
	}
	defer session.Close()
	output := &cappedBuffer{limit: limit}
	session.Stdout = output
	session.Stderr = output
	done := make(chan error, 1)
	go func() { done <- session.Run(CommandLine(argv)) }()
	result := Result{}
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		_ = client.Close()
		result.TimedOut = true
		result.ExitCode = -1
		err = nil
	}
	output.mu.Lock()
	result.Output = strings.ToValidUTF8(string(output.data), "�")
	result.Truncated = output.truncated
	output.mu.Unlock()
	if err != nil {
		var exit *ssh.ExitError
		if errors.As(err, &exit) {
			result.ExitCode = exit.ExitStatus()
			return result, nil
		}
		return result, err
	}
	return result, nil
}
