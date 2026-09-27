package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadUsesLoopbackAndValidatesInventory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.json")
	data := `{"hosts":[{"id":"vps","tailnet_name":"vps.tailnet.ts.net","kind":"vps","local":true}],"repositories":[{"name":"heliowap/vpsdash","agent_switchable":true}]}`
	if err := os.WriteFile(file, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:8484" || len(c.Hosts) != 1 || c.Repositories[0].Name != "heliowap/vpsdash" {
		t.Fatalf("config = %+v", c)
	}
}

func TestLoadRejectsPublicBind(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(file, []byte(`{"listen":"0.0.0.0:8484"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(file); err == nil {
		t.Fatal("public bind accepted")
	}
}

func TestLoadRequiresDistinctKeysForRemoteHosts(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.json")
	for _, tc := range []struct {
		name, data string
		valid      bool
	}{
		{"missing key", `{"hosts":[{"id":"a","tailnet_name":"a.example.ts.net","kind":"vps","ssh_user":"operator"}]}`, false},
		{"reused key", `{"hosts":[{"id":"a","tailnet_name":"a.example.ts.net","kind":"vps","ssh_user":"operator","ssh_key_file":"/keys/shared"},{"id":"b","tailnet_name":"b.example.ts.net","kind":"vps","ssh_user":"operator","ssh_key_file":"/keys/shared"}]}`, false},
		{"separate keys", `{"hosts":[{"id":"a","tailnet_name":"a.example.ts.net","kind":"vps","ssh_user":"operator","ssh_key_file":"/keys/a"},{"id":"b","tailnet_name":"b.example.ts.net","kind":"vps","ssh_user":"operator","ssh_key_file":"/keys/b"}]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(file, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(file)
			if (err == nil) != tc.valid {
				t.Fatalf("config valid = %t, error = %v", tc.valid, err)
			}
		})
	}
}

func TestExampleInventoryIsValid(t *testing.T) {
	example, err := Load(filepath.Join("..", "..", "config.example.json"))
	if err != nil {
		t.Fatalf("example inventory: %v", err)
	}
	// install-service.sh copies the example verbatim; file reading must stay opt-in.
	for _, h := range example.Hosts {
		if len(h.FileRoots) > 0 {
			t.Fatalf("example host %s enables file_roots", h.ID)
		}
	}
	c := Config{Listen: "127.0.0.1:8484", TailscaleServeHost: "other.example.invalid", Hosts: []Host{{ID: "vps", TailnetName: "vps.example.invalid", Kind: "presence"}}}
	if err := c.Validate(); err == nil {
		t.Fatal("Serve hostname outside inventory was accepted")
	}
}

func TestLoadValidatesRunnerUnitAccount(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.json")
	base := `{"hosts":[{"id":"vps","tailnet_name":"vps.example.ts.net","kind":"vps","ssh_user":"operator","ssh_key_file":"/keys/operator"}],"runner_unit_hosts":[%s]}`
	for _, tc := range []struct {
		name, account string
		valid         bool
	}{
		{"separate key", `{"host_id":"vps","ssh_user":"gh-agents","ssh_key_file":"/keys/runner"}`, true},
		{"unknown host", `{"host_id":"missing","ssh_user":"gh-agents","ssh_key_file":"/keys/runner"}`, false},
		{"shared key", `{"host_id":"vps","ssh_user":"gh-agents","ssh_key_file":"/keys/operator"}`, false},
		{"missing key", `{"host_id":"vps","ssh_user":"gh-agents"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(file, []byte(fmt.Sprintf(base, tc.account)), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(file)
			if (err == nil) != tc.valid {
				t.Fatalf("valid = %t, error = %v", tc.valid, err)
			}
		})
	}
}

func TestNativeRunnerUnitIdentification(t *testing.T) {
	c := Config{RunnerUnitHosts: []RunnerUnitHost{{HostID: "vps"}}}
	for _, tc := range []struct {
		host, name string
		want       bool
	}{
		{"vps", "actions.runner.owner-repo.owner--repo-1.service", true},
		{"vps", "gh-agents-cleanup.timer", true},
		{"other", "gh-agents-cleanup.timer", false},
		{"vps", "other.service", false},
		{"vps", "actions.runner.bad/name.service", false},
	} {
		if got := c.IsNativeRunnerUnit(tc.host, tc.name); got != tc.want {
			t.Errorf("IsNativeRunnerUnit(%q, %q) = %t, want %t", tc.host, tc.name, got, tc.want)
		}
	}
}

func TestReadEnvFileRequiresPrivatePermissions(t *testing.T) {
	file := filepath.Join(t.TempDir(), "auth.env")
	if err := os.WriteFile(file, []byte("VPSDASH_SESSION_KEY=abc\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEnvFile(file); err == nil {
		t.Fatal("world-readable secret file accepted")
	}
	if err := os.Chmod(file, 0600); err != nil {
		t.Fatal(err)
	}
	vars, err := ReadEnvFile(file)
	if err != nil || vars["VPSDASH_SESSION_KEY"] != "abc" {
		t.Fatalf("vars = %v, %v", vars, err)
	}
}

func TestValidateFileRoots(t *testing.T) {
	vps := func(roots ...string) Host {
		return Host{ID: "a", TailnetName: "a.example.invalid", Kind: "vps", Local: true, FileRoots: roots}
	}
	for _, tc := range []struct {
		name  string
		host  Host
		valid bool
	}{
		{"none", vps(), true},
		{"clean roots", vps("/home/operator/Projetos", "/srv/app"), true},
		{"relative", vps("Projetos"), false},
		{"filesystem root", vps("/"), false},
		{"dot dot", vps("/srv/../etc"), false},
		{"trailing slash", vps("/srv/"), false},
		{"duplicate", vps("/srv", "/srv"), false},
		{"presence host", Host{ID: "a", TailnetName: "a.example.invalid", Kind: "presence", FileRoots: []string{"/srv"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := (Config{Listen: "127.0.0.1:8484", Hosts: []Host{tc.host}}).Validate()
			if (err == nil) != tc.valid {
				t.Fatalf("valid = %t, error = %v", tc.valid, err)
			}
		})
	}
}

func TestInteractiveInventoryRules(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.json")
	host := func(extra string) string {
		return `{"id":"a","tailnet_name":"a.example.ts.net","kind":"vps","ssh_user":"helio","ssh_key_file":"/keys/a"` + extra + `}`
	}
	for _, tc := range []struct {
		name, data string
		valid      bool
	}{
		{"private listener", `{"private_listen":"127.0.0.1:8485","hosts":[` + host(`,"interactive_key_file":"/keys/a-interactive","snippets":[{"name":"Uso de disco","argv":["df","-h","/"]}]`) + `]}`, true},
		{"public private listener", `{"private_listen":"0.0.0.0:8485"}`, false},
		{"same listener", `{"listen":"127.0.0.1:8484","private_listen":"127.0.0.1:8484"}`, false},
		{"collector key reused", `{"hosts":[` + host(`,"interactive_key_file":"/keys/a"`) + `]}`, false},
		{"runner key reused", `{"hosts":[` + host(`,"interactive_key_file":"/keys/runner"`) + `],"runner_unit_hosts":[{"host_id":"a","ssh_user":"gh-agents","ssh_key_file":"/keys/runner"}]}`, false},
		{"relative key", `{"hosts":[` + host(`,"interactive_key_file":"keys/i"`) + `]}`, false},
		{"snippets without key", `{"hosts":[` + host(`,"snippets":[{"name":"x","argv":["true"]}]`) + `]}`, false},
		{"empty argv", `{"hosts":[` + host(`,"interactive_key_file":"/keys/i","snippets":[{"name":"x","argv":[]}]`) + `]}`, false},
		{"newline argument", `{"hosts":[` + host(`,"interactive_key_file":"/keys/i","snippets":[{"name":"x","argv":["sh","a\nb"]}]`) + `]}`, false},
		{"duplicate name", `{"hosts":[` + host(`,"interactive_key_file":"/keys/i","snippets":[{"name":"x","argv":["true"]},{"name":"x","argv":["false"]}]`) + `]}`, false},
		{"timeout too long", `{"hosts":[` + host(`,"interactive_key_file":"/keys/i","snippets":[{"name":"x","argv":["true"],"timeout_seconds":301}]`) + `]}`, false},
		{"presence host", `{"hosts":[{"id":"p","tailnet_name":"p.example.ts.net","kind":"presence","interactive_key_file":"/keys/p"}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(file, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(file)
			if (err == nil) != tc.valid {
				t.Fatalf("config valid = %t, error = %v", tc.valid, err)
			}
		})
	}
}
