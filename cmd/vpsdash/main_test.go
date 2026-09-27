package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heliowap/vpsdash/internal/api"
	"github.com/heliowap/vpsdash/internal/auth"
	"github.com/heliowap/vpsdash/internal/collect"
	"github.com/heliowap/vpsdash/internal/config"
	"github.com/heliowap/vpsdash/internal/store"
)

func TestCheckConfigReportsInvalidServeHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, tc := range []struct {
		name, serveHost string
		valid           bool
	}{
		{"local host in inventory", "sample-vps.example.invalid", true},
		{"local host missing", "missing.example.invalid", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := `{"hosts":[{"id":"sample-vps","tailnet_name":"sample-vps.example.invalid","kind":"presence"}],"tailscale_serve_host":"` + tc.serveHost + `"}`
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			err := checkConfig([]string{"--config", path})
			if (err == nil) != tc.valid {
				t.Fatalf("valid = %t, error = %v", tc.valid, err)
			}
			if err != nil && !strings.Contains(err.Error(), path) {
				t.Fatalf("missing inventory path in error: %v", err)
			}
		})
	}
}

func TestMissingGitHubAppKeepsDashboardAvailable(t *testing.T) {
	dir := t.TempDir()
	gh, err := loadGitHub(filepath.Join(dir, "missing-github-app.env"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "vpsdash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Config{Repositories: []config.Repository{{Name: "example/repo"}}}
	collector := collect.New(cfg, st, gh)
	if collector.GitHub != nil {
		t.Fatal("missing GitHub App enabled the fleet collector")
	}
	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(hash, []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	handler := api.New(cfg, st, collector, gh, a).Handler()
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"password":"correct horse battery staple"}`)))
	if login.Code != http.StatusOK {
		t.Fatalf("login: %d: %s", login.Code, login.Body.String())
	}
	request := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	request.AddCookie(login.Result().Cookies()[0])
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("dashboard: %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Repositories []struct {
			Error string `json:"error"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Repositories) != 1 || body.Repositories[0].Error != "GitHub App não provisionado" {
		t.Fatalf("dashboard repositories: %+v", body.Repositories)
	}
}
