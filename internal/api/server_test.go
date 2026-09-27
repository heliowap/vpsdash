package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestLoginRateLimitSeparatesClientsBehindLocalProxy(t *testing.T) {
	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(hash, []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	h := New(config.Config{TrustProxyHeader: true}, nil, nil, nil, a).Handler()
	request := func(clientIP string) int {
		r := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"password":"wrong"}`))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("X-Real-IP", clientIP)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for i := 0; i < 5; i++ {
		if got := request("198.51.100.10"); got != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d", i+1, got)
		}
	}
	if got := request("198.51.100.10"); got != http.StatusTooManyRequests {
		t.Fatalf("rate-limited client = %d", got)
	}
	if got := request("198.51.100.11"); got != http.StatusUnauthorized {
		t.Fatalf("other client = %d", got)
	}
}

func TestLoginRateLimitSeparatesTailscaleServeClients(t *testing.T) {
	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(hash, []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	h := New(config.Config{TrustProxyHeader: true, TailscaleServeHost: "sample-vps.example.invalid"}, nil, nil, nil, a).Handler()
	request := func(clientIP, spoofedRealIP, host string) int {
		r := httptest.NewRequest("POST", "https://sample-vps.example.invalid:8443/api/login", strings.NewReader(`{"password":"wrong"}`))
		r.Host = host
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("X-Forwarded-For", clientIP)
		r.Header.Set("X-Real-IP", spoofedRealIP)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for i := 0; i < 5; i++ {
		host := "sample-vps.example.invalid:8443"
		if i%2 == 1 {
			host = "app.example.com"
		}
		if got := request("100.101.102.103", "203.0.113."+strconv.Itoa(i+1), host); got != http.StatusUnauthorized {
			t.Fatalf("tailnet attempt %d = %d", i+1, got)
		}
	}
	if got := request("100.101.102.103", "203.0.113.60", "app.example.com"); got != http.StatusTooManyRequests {
		t.Fatalf("rate-limited tailnet client = %d", got)
	}
	if got := request("100.101.102.104", "203.0.113.60", "app.example.com"); got != http.StatusUnauthorized {
		t.Fatalf("other tailnet client = %d", got)
	}
}

func TestLoginClientIPKeepsProxyRoutesSeparate(t *testing.T) {
	s := &Server{Config: config.Config{TrustProxyHeader: true, TailscaleServeHost: "sample-vps.example.invalid"}}
	for _, tc := range []struct {
		host, remote, forwarded, realIP, want string
	}{
		{"sample-vps.example.invalid", "127.0.0.1:1234", "fd7a:115c:a1e0::123", "203.0.113.9", "fd7a:115c:a1e0::123"},
		{"sample-vps.example.invalid", "127.0.0.1:1234", "100.101.102.103, 100.101.102.104", "203.0.113.9", "127.0.0.1"},
		{"sample-vps.example.invalid", "127.0.0.1:1234", "203.0.113.8", "203.0.113.9", "127.0.0.1"},
		{"app.example.com", "127.0.0.1:1234", "100.101.102.103", "203.0.113.9", "100.101.102.103"},
		{"app.example.com", "127.0.0.1:1234", "198.51.100.10", "203.0.113.9", "203.0.113.9"},
		{"sample-vps.example.invalid", "198.51.100.9:1234", "100.101.102.103", "203.0.113.9", "198.51.100.9"},
	} {
		r := httptest.NewRequest(http.MethodPost, "/api/login", nil)
		r.Host, r.RemoteAddr = tc.host, tc.remote
		r.Header.Set("X-Forwarded-For", tc.forwarded)
		r.Header.Set("X-Real-IP", tc.realIP)
		if got := s.loginClientIP(r); got != tc.want {
			t.Errorf("%s from %s = %q, want %q", tc.host, tc.remote, got, tc.want)
		}
	}
}

func TestLoginRateLimitIgnoresUntrustedProxyHeader(t *testing.T) {
	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(hash, []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	h := New(config.Config{TrustProxyHeader: true}, nil, nil, nil, a).Handler()
	for i := 0; i < 6; i++ {
		r := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"password":"wrong"}`))
		r.RemoteAddr = "198.51.100.10:12345"
		r.Header.Set("X-Real-IP", "203.0.113."+strconv.Itoa(i+1))
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

func TestLoginRejectsSpoofedLoopbackHeaderAndGlobalSpray(t *testing.T) {
	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(hash, []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	h := New(config.Config{}, nil, nil, nil, a).Handler()
	for i := 0; i < 6; i++ {
		r := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"password":"wrong"}`))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("X-Real-IP", "203.0.113."+strconv.Itoa(i+1))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := http.StatusUnauthorized
		if i == 5 {
			want = http.StatusTooManyRequests
		}
		if w.Code != want {
			t.Fatalf("spoofed attempt %d = %d, want %d", i+1, w.Code, want)
		}
	}
	s := New(config.Config{TrustProxyHeader: true}, nil, nil, nil, a)
	now := time.Now()
	for i := 0; i < 20; i++ {
		ip := "198.51.100." + strconv.Itoa(i+1)
		if !s.reserveLogin(ip, now) {
			t.Fatalf("global attempt %d was refused", i+1)
		}
		s.finishLogin(ip, false, now)
	}
	if s.reserveLogin("203.0.113.200", now) {
		t.Fatal("global failure budget was bypassed by changing client IP")
	}
	if !s.reserveLogin("203.0.113.200", now.Add(6*time.Minute)) {
		t.Fatal("expired global failures still block login")
	}
}

func TestLoginReservationsCountInFlightAttempts(t *testing.T) {
	a, err := auth.New("$argon2id$test", []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	s := New(config.Config{}, nil, nil, nil, a)
	now := time.Now()
	for i := 0; i < 5; i++ {
		if !s.reserveLogin("198.51.100.10", now) {
			t.Fatalf("attempt %d was refused", i+1)
		}
	}
	if s.reserveLogin("198.51.100.10", now) {
		t.Fatal("sixth concurrent attempt was accepted")
	}
	s.finishLogin("198.51.100.10", false, now)
	if s.reserveLogin("198.51.100.10", now) {
		t.Fatal("failed attempt released the rate limit")
	}
	s.finishLogin("198.51.100.10", true, now)
	if !s.reserveLogin("198.51.100.10", now) {
		t.Fatal("successful login did not clear previous failures")
	}
}

func TestLoginBoundsConcurrentPasswordVerification(t *testing.T) {
	a, err := auth.New("$argon2id$test", []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	s := New(config.Config{}, nil, nil, nil, a)
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	s.verifyPassword = func(string) bool {
		started <- struct{}{}
		<-release
		return false
	}
	h := s.Handler()
	request := func() int {
		r := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"password":"wrong"}`))
		r.RemoteAddr = "198.51.100.10:12345"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	responses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() { responses <- request() }()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("password verifier did not start")
		}
	}
	overloaded := make(chan int, 6)
	for i := 0; i < 6; i++ {
		go func() { overloaded <- request() }()
	}
	for i := 0; i < 6; i++ {
		select {
		case got := <-overloaded:
			if got != http.StatusTooManyRequests {
				t.Fatalf("request during verification = %d", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("request waited for an available password verifier")
		}
	}
	close(release)
	released = true
	for i := 0; i < 2; i++ {
		if got := <-responses; got != http.StatusUnauthorized {
			t.Fatalf("verified request = %d", got)
		}
	}
}

func TestEmbeddedPWAAssets(t *testing.T) {
	h := (&Server{Static: http.FS(web.Dist())}).Handler()
	assets, err := fs.Glob(web.Dist(), "assets/*.js")
	if err != nil || len(assets) == 0 {
		t.Fatalf("embedded JS assets = %v, %v", assets, err)
	}
	fonts, err := fs.Glob(web.Dist(), "assets/*.woff")
	if err != nil || len(fonts) == 0 {
		t.Fatalf("embedded WOFF assets = %v, %v", fonts, err)
	}
	for _, tc := range []struct {
		path, contentType, cacheControl string
	}{
		{"/", "text/html", "no-cache"},
		{"/manifest.webmanifest", "application/manifest+json", "no-cache"},
		{"/sw.js", "text/javascript", "no-cache"},
		{"/icons/vpsdash-192.png", "image/png", "public, max-age=3600"},
		{"/icons/vpsdash-512.png", "image/png", "public, max-age=3600"},
		{"/" + assets[0], "text/javascript", "public, max-age=31536000, immutable"},
		{"/" + fonts[0], "font/woff", "public, max-age=31536000, immutable"},
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
			if etag := w.Header().Get("ETag"); etag == "" {
				t.Fatal("asset has no ETag")
			}
			if tc.contentType == "image/png" && !bytes.HasPrefix(w.Body.Bytes(), []byte("\x89PNG\r\n\x1a\n")) {
				t.Fatal("embedded icon is not a PNG")
			}
		})
	}
	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/icons/vpsdash-192.png", nil))
	if first.Header().Get("Strict-Transport-Security") != "max-age=31536000" || strings.Contains(first.Header().Get("Content-Security-Policy"), "unsafe-inline") {
		t.Fatal("public security headers are too permissive")
	}
	revalidated := httptest.NewRequest(http.MethodGet, "/icons/vpsdash-192.png", nil)
	revalidated.Header.Set("If-None-Match", first.Header().Get("ETag"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, revalidated)
	if w.Code != http.StatusNotModified {
		t.Fatalf("icon revalidation = %d", w.Code)
	}
}

func TestPublicHandlerLimitsBodiesAndMethods(t *testing.T) {
	a, err := auth.New("$argon2id$test", []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	s := New(config.Config{}, nil, nil, nil, a)
	s.Static = http.FS(web.Dist())
	h := s.Handler()
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{http.MethodPost, "/api/login", `{"password":"` + strings.Repeat("x", 33<<10) + `"}`, http.StatusBadRequest},
		{http.MethodPost, "/api/login", `{"password":"wrong"}{"password":"again"}`, http.StatusBadRequest},
		{http.MethodPost, "/missing", "", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api", "", http.StatusUnauthorized},
		{http.MethodGet, "/assets/old-hash.js", "", http.StatusNotFound},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		if w.Code != tc.want {
			t.Fatalf("%s %s = %d, want %d", tc.method, tc.path, w.Code, tc.want)
		}
	}
	navigation := httptest.NewRequest(http.MethodGet, "/projects", nil)
	navigation.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, navigation)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("SPA navigation = %d, %q", w.Code, w.Header().Get("Content-Type"))
	}
}

func TestProjectRejectsLoopbackHealthURL(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "vpsdash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertHost(ctx, store.Host{ID: "vps", TailnetName: "vps.example.ts.net", Kind: "vps"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertCandidate(ctx, "vps", "example.service", "systemd"); err != nil {
		t.Fatal(err)
	}
	projects, err := st.Projects(ctx)
	if err != nil || len(projects) != 1 {
		t.Fatalf("projects = %+v, %v", projects, err)
	}
	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(hash, []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Hosts: []config.Host{{ID: "vps", TailnetName: "vps.example.ts.net", Kind: "vps"}}}
	h := New(cfg, st, nil, nil, a).Handler()
	login := httptest.NewRecorder()
	h.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"password":"correct horse battery staple"}`)))
	if login.Code != http.StatusOK {
		t.Fatalf("login = %d: %s", login.Code, login.Body.String())
	}
	var session struct {
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPatch, "/api/projects/"+strconv.FormatInt(projects[0].ID, 10),
		strings.NewReader(`{"monitored":true,"health_url":"http://127.0.0.1:8484/healthz"}`))
	r.AddCookie(login.Result().Cookies()[0])
	r.Header.Set("X-CSRF-Token", session.CSRF)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("private health URL = %d: %s", w.Code, w.Body.String())
	}
}
