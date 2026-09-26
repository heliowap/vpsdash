package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/heliowap/vpsdash/internal/auth"
	"github.com/heliowap/vpsdash/internal/config"
	"github.com/heliowap/vpsdash/internal/store"
	"github.com/heliowap/vpsdash/internal/web"
)

type fakeGitHub struct{ updates []string }

func (f *fakeGitHub) Variable(context.Context, string, string) (string, error) { return "", nil }
func (f *fakeGitHub) SetVariable(_ context.Context, repo, name, value string) error {
	f.updates = append(f.updates, repo+":"+name+"="+value)
	return nil
}
func (f *fakeGitHub) Rerun(context.Context, string, int64) error { return nil }

func TestAuthAndSwitchFlow(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "vpsdash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertHost(context.Background(), store.Host{ID: "vps", TailnetName: "vps.example.ts.net", Kind: "vps"}); err != nil {
		t.Fatal(err)
	}
	native, err := s.UpsertNativeRunnerUnit(context.Background(), "vps", "gh-agents-cleanup.timer")
	if err != nil {
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
	gh := &fakeGitHub{}
	cfg := config.Config{Repositories: []config.Repository{{Name: "heliowap/vpsdash", AgentSwitchable: true}}}
	h := New(cfg, s, nil, gh, a).Handler()
	request := func(method, path string, body []byte, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		if cookie != nil {
			r.AddCookie(cookie)
		}
		if csrf != "" {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if got := request("GET", "/api/dashboard", nil, nil, "").Code; got != 401 {
		t.Fatalf("unauthenticated dashboard = %d", got)
	}
	login := request("POST", "/api/login", []byte(`{"password":"correct horse battery staple"}`), nil, "")
	if login.Code != 200 {
		t.Fatalf("login = %d: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	var session struct {
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if got := request("GET", "/api/dashboard", nil, cookie, "").Code; got != 200 {
		t.Fatalf("dashboard = %d", got)
	}
	var dashboard struct {
		Projects []store.Project `json:"projects"`
	}
	if err := json.Unmarshal(request("GET", "/api/dashboard", nil, cookie, "").Body.Bytes(), &dashboard); err != nil {
		t.Fatal(err)
	}
	if len(dashboard.Projects) != 1 || !dashboard.Projects[0].Native {
		t.Fatalf("native project missing from dashboard: %+v", dashboard.Projects)
	}
	if got := request("PATCH", "/api/projects/"+strconv.FormatInt(native.ID, 10), []byte(`{"monitored":false}`), cookie, session.CSRF).Code; got != 403 {
		t.Fatalf("native project editable: %d", got)
	}
	body := []byte(`{"variable":"AGENT_RUNNER","label":"ubuntu-latest"}`)
	path := "/api/repos/heliowap/vpsdash/switch"
	if got := request("POST", path, body, cookie, "").Code; got != 403 {
		t.Fatalf("missing CSRF = %d", got)
	}
	if got := request("POST", path, body, cookie, session.CSRF).Code; got != 200 {
		t.Fatalf("switch = %d", got)
	}
	if len(gh.updates) != 1 || gh.updates[0] != "heliowap/vpsdash:AGENT_RUNNER=ubuntu-latest" {
		t.Fatalf("updates = %v", gh.updates)
	}
}

func TestLoginRateLimitUsesHostWithoutEphemeralPort(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "vpsdash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(hash, []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	h := New(config.Config{}, s, nil, nil, a).Handler()
	for i := 0; i < 6; i++ {
		r := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"password":"wrong"}`))
		r.RemoteAddr = "127.0.0.1:" + strconv.Itoa(10000+i)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := http.StatusUnauthorized
		if i == 5 {
			want = http.StatusTooManyRequests
		}
		if w.Code != want {
			t.Fatalf("attempt %d: status %d, want %d", i+1, w.Code, want)
		}
	}
}

func TestEmbeddedPWAAssets(t *testing.T) {
	h := (&Server{Static: http.FS(web.Dist())}).Handler()
	for _, tc := range []struct {
		path, contentType, cacheControl string
	}{
		{"/", "text/html", "no-cache"},
		{"/manifest.webmanifest", "application/manifest+json", "no-cache"},
		{"/sw.js", "text/javascript", "no-cache"},
		{"/icons/vpsdash-192.png", "image/png", ""},
		{"/icons/vpsdash-512.png", "image/png", ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d", w.Code)
			}
			if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, tc.contentType) {
				t.Fatalf("content type = %q", got)
			}
			if got := w.Header().Get("Cache-Control"); got != tc.cacheControl {
				t.Fatalf("cache control = %q", got)
			}
			if tc.contentType == "image/png" && !bytes.HasPrefix(w.Body.Bytes(), []byte("\x89PNG\r\n\x1a\n")) {
				t.Fatal("embedded icon is not a PNG")
			}
		})
	}
}
