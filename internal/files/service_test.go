package files

import (
	"context"
	"errors"
	"testing"

	"github.com/heliowap/vpsdash/internal/config"
)

// bridgeRemote answers through the real SSH bridge script.
type bridgeRemote struct {
	t        *testing.T
	f        fixture
	commands []string
}

func (b *bridgeRemote) FileCommand(_ context.Context, host config.Host, command string) (string, error) {
	b.commands = append(b.commands, host.ID+" "+command)
	output, err := runBridge(b.t, b.f.rootsFile, command)
	return string(output), err
}

type staticRemote string

func (s staticRemote) FileCommand(context.Context, config.Host, string) (string, error) {
	return string(s), nil
}

func TestServiceGatesRequestsByInventory(t *testing.T) {
	f := buildFixture(t)
	remote := &bridgeRemote{t: t, f: f}
	service := &Service{Remote: remote, Config: config.Config{Hosts: []config.Host{
		{ID: "remote", Kind: "vps", FileRoots: []string{f.root}},
		{ID: "narrow", Kind: "vps", FileRoots: []string{f.root + "/src"}},
		{ID: "local", Kind: "vps", Local: true, FileRoots: []string{f.root}},
		{ID: "off", Kind: "vps"},
		{ID: "phone", Kind: "presence"},
	}}}
	ctx := context.Background()
	for _, host := range []string{"remote", "local"} {
		listing, err := service.List(ctx, host, f.root)
		if err != nil || len(listing.Entries) == 0 {
			t.Fatalf("%s list = %+v, %v", host, listing, err)
		}
		if _, err := service.Read(ctx, host, f.root+"/.env", 0); code(err) != CodeBlocked {
			t.Fatalf("%s .env = %v", host, err)
		}
		content, err := service.Read(ctx, host, f.root+"/readme.md", 0)
		if err != nil || content.Content != "# Olá, operador\n" {
			t.Fatalf("%s readme = %+v, %v", host, content, err)
		}
	}
	before := len(remote.commands)
	for _, tc := range []struct {
		host, path string
		want       string
	}{
		{"narrow", f.root, CodeOutside},
		{"narrow", f.root + "/readme.md", CodeOutside},
		{"remote", "/etc", CodeOutside},
		{"remote", f.root + "/../outside", CodeInvalid},
		{"off", f.root, CodeDisabled},
	} {
		if _, err := service.List(ctx, tc.host, tc.path); code(err) != tc.want {
			t.Errorf("%s %s = %v, want %s", tc.host, tc.path, err, tc.want)
		}
	}
	if len(remote.commands) != before {
		t.Fatalf("requests outside the inventory reached the host: %v", remote.commands[before:])
	}
	for _, host := range []string{"phone", "unknown"} {
		if _, err := service.List(ctx, host, f.root); !errors.Is(err, ErrUnknownHost) {
			t.Errorf("%s = %v", host, err)
		}
	}
	if _, err := service.Read(ctx, "remote", f.root+"/readme.md", -1); code(err) != CodeInvalid {
		t.Errorf("negative offset = %v", err)
	}
}

func TestServiceRejectsMismatchedBridgeReplies(t *testing.T) {
	cfg := config.Config{Hosts: []config.Host{{ID: "vps", Kind: "vps", FileRoots: []string{"/srv"}}}}
	ctx := context.Background()
	service := &Service{Config: cfg, Remote: staticRemote(`{"path":"/srv/other","entries":[]}`)}
	if _, err := service.List(ctx, "vps", "/srv"); err == nil {
		t.Fatal("listing for another path was accepted")
	}
	service.Remote = staticRemote(`{"path":"/srv/a","offset":5}`)
	if _, err := service.Read(ctx, "vps", "/srv/a", 0); err == nil {
		t.Fatal("page for another offset was accepted")
	}
	service.Remote = staticRemote(`{"error":"blocked"}`)
	if _, err := service.Read(ctx, "vps", "/srv/a", 0); code(err) != CodeBlocked {
		t.Fatalf("bridge refusal = %v", err)
	}
	service.Remote = staticRemote(`vpsdash: comando SSH não permitido`)
	if _, err := service.List(ctx, "vps", "/srv"); err == nil {
		t.Fatal("non-JSON reply was accepted")
	}
	service.Remote = staticRemote(`{"path":"/srv","entries":null}`)
	if listing, err := service.List(ctx, "vps", "/srv"); err != nil || listing.Entries == nil {
		t.Fatalf("empty listing = %+v, %v", listing, err)
	}
}

func TestSecretNames(t *testing.T) {
	for _, name := range []string{".env", ".ENV.local", "prod.env", "server.pem", "tls.key", "id_rsa", "id_ed25519.pub", "cert.p12", ".ssh", ".gnupg", ".git", ".aws", "my-secret.txt", "credentials.json", "db_password", ".zsh_history", ".netrc", ".npmrc", ".pgpass", "vault.kdbx"} {
		if !SecretName(name) {
			t.Errorf("%s should be blocked", name)
		}
	}
	for _, name := range []string{"README.md", ".github", ".gitignore", ".gitattributes", "environment.go", "keyboard.go", "monkey.ts", "main.go", "docker-compose.yml", "identity.md"} {
		if SecretName(name) {
			t.Errorf("%s should be visible", name)
		}
	}
}
