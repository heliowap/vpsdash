package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heliowap/vpsdash/internal/auth"
	"github.com/heliowap/vpsdash/internal/collect"
	"github.com/heliowap/vpsdash/internal/config"
	"github.com/heliowap/vpsdash/internal/store"
)

type fakeRunnerBridge struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeRunnerBridge) RunnerUnitCommand(_ context.Context, host config.Host, action, unit string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, host.SSHUser+" "+action+" "+unit)
	return "", nil
}

func (f *fakeRunnerBridge) Run(context.Context, config.Host, string) (string, error) {
	return "", errors.New("snapshot unavailable in test")
}
func (f *fakeRunnerBridge) Check(context.Context, config.Host, string, string) (string, error) {
	return "", errors.New("unexpected check")
}
func (f *fakeRunnerBridge) Close() {}

func (f *fakeRunnerBridge) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func TestRunnerUnitOperationsRequireSessionCSRFAndObservedUnit(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "vpsdash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertHost(ctx, store.Host{ID: "vps", TailnetName: "vps.example.invalid", Kind: "vps"}); err != nil {
		t.Fatal(err)
	}
	unit := "actions.runner.owner--repo-1.service"
	for _, name := range []string{unit, "gh-agents-cleanup.timer"} {
		if _, err := st.UpsertNativeRunnerUnit(ctx, "vps", name); err != nil {
			t.Fatal(err)
		}
	}
	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(hash, []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Hosts:           []config.Host{{ID: "vps", TailnetName: "vps.example.invalid", Kind: "vps", SSHUser: "helio", SSHKeyFile: "/keys/helio"}},
		RunnerUnitHosts: []config.RunnerUnitHost{{HostID: "vps", SSHUser: "gh-agents", SSHKeyFile: "/keys/gh-agents"}},
	}
	collector := collect.New(cfg, st, nil)
	bridge := &fakeRunnerBridge{}
	collector.Executor = bridge
	collector.Units = bridge
	h := New(cfg, st, collector, nil, a).Handler()
	login := httptest.NewRecorder()
	h.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"password":"correct horse battery staple"}`)))
	if login.Code != http.StatusOK {
		t.Fatalf("login = %d", login.Code)
	}
	cookie := login.Result().Cookies()[0]
	var session struct {
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	post := func(path string, withCookie bool, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, nil)
		if withCookie {
			r.AddCookie(cookie)
		}
		if csrf != "" {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	restart := "/api/runner-units/vps/" + unit + "/restart"
	for _, tc := range []struct {
		path       string
		withCookie bool
		csrf       string
		want       int
	}{
		{restart, false, "", http.StatusUnauthorized},
		{restart, true, "", http.StatusForbidden},
		{restart, true, "wrong", http.StatusForbidden},
		{"/api/runner-units/vps/actions.runner.owner--repo-9.service/restart", true, session.CSRF, http.StatusNotFound},
		{"/api/runner-units/vps/gh-agents-cleanup.timer/drain", true, session.CSRF, http.StatusNotFound},
		{"/api/runner-units/other/" + unit + "/drain", true, session.CSRF, http.StatusNotFound},
		{"/api/runner-units/vps/" + unit + "/drain/cancel", true, session.CSRF, http.StatusConflict},
	} {
		if got := post(tc.path, tc.withCookie, tc.csrf); got.Code != tc.want {
			t.Fatalf("POST %s = %d, want %d: %s", tc.path, got.Code, tc.want, got.Body.String())
		}
	}
	if calls := bridge.Calls(); len(calls) != 0 {
		t.Fatalf("rejected requests reached the bridge: %v", calls)
	}
	accepted := post(restart, true, session.CSRF)
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("restart = %d: %s", accepted.Code, accepted.Body.String())
	}
	var op store.UnitOp
	if err := json.Unmarshal(accepted.Body.Bytes(), &op); err != nil || op.Action != "restart" || op.Status != "running" {
		t.Fatalf("accepted op = %+v, %v", op, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		r := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var dashboard struct {
			UnitOps []store.UnitOp `json:"unit_ops"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &dashboard); err != nil {
			t.Fatal(err)
		}
		if len(dashboard.UnitOps) == 1 && dashboard.UnitOps[0].Status == "done" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dashboard unit ops = %+v", dashboard.UnitOps)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if calls := bridge.Calls(); len(calls) != 1 || calls[0] != "gh-agents runner-restart "+unit {
		t.Fatalf("bridge calls = %v", calls)
	}
}
