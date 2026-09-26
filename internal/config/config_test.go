package config

import (
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
