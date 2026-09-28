package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heliowap/vpsdash/internal/auth"
	"github.com/heliowap/vpsdash/internal/config"
	"github.com/heliowap/vpsdash/internal/store"
)

func TestMinutesEndpointReportsCollectionStatePerConfiguredRepository(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "vpsdash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Now()
	started := now.Add(-time.Hour).Unix()
	if err := s.RecordRunUsage(ctx, "heliowap/vpsdash", store.RunScan{RunID: 1, RunAttempt: 1, CreatedAt: started}, []store.JobUsage{{JobID: 1, Backend: "self-hosted", StartedAt: started, CompletedAt: started + 150}}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkMinutesCollected(ctx, "heliowap/vpsdash", now, now.Add(-30*24*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	// Usage of a repository removed from the inventory is not reported.
	if err := s.RecordRunUsage(ctx, "heliowap/removed", store.RunScan{RunID: 2, RunAttempt: 1, CreatedAt: started}, []store.JobUsage{{JobID: 2, Backend: "outros", StartedAt: started, CompletedAt: started + 60}}, now); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(hash, []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Repositories: []config.Repository{{Name: "heliowap/vpsdash"}, {Name: "heliowap/other"}}}
	h := New(cfg, s, nil, nil, a).Handler()
	get := func(cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/minutes", nil)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if got := get(nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated minutes = %d", got)
	}
	login := httptest.NewRecorder()
	h.ServeHTTP(login, httptest.NewRequest("POST", "/api/login", bytes.NewReader([]byte(`{"password":"correct horse battery staple"}`))))
	if login.Code != 200 {
		t.Fatalf("login = %d", login.Code)
	}
	w := get(login.Result().Cookies()[0])
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("minutes = %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
	var body struct {
		CollectorEnabled bool                `json:"collector_enabled"`
		Repositories     []store.RepoMinutes `json:"repositories"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.CollectorEnabled {
		t.Fatal("collector reported enabled without a GitHub App")
	}
	if len(body.Repositories) != 2 || body.Repositories[0].Repo != "heliowap/vpsdash" || body.Repositories[1].Repo != "heliowap/other" {
		t.Fatalf("repositories = %+v", body.Repositories)
	}
	collected := body.Repositories[0]
	if collected.CollectedAt != now.Unix() || len(collected.Backends) != 1 || collected.Backends[0].Minutes30d != 3 {
		t.Fatalf("collected repository = %+v", collected)
	}
	pending := body.Repositories[1]
	if pending.CollectedAt != 0 || pending.AttemptedAt != 0 || pending.Backends == nil || len(pending.Backends) != 0 {
		t.Fatalf("uncollected repository = %+v", pending)
	}
}
