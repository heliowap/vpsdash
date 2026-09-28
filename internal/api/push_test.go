package api

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/heliowap/vpsdash/internal/auth"
	"github.com/heliowap/vpsdash/internal/config"
	"github.com/heliowap/vpsdash/internal/store"
	"github.com/heliowap/vpsdash/internal/webpush"
)

func TestPushSubscriptionFlowRequiresSessionAndCSRF(t *testing.T) {
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
	server := New(config.Config{}, s, nil, nil, a)
	h := server.Handler()
	request := func(method, path, body string, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
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
	if got := request("GET", "/api/push", "", nil, "").Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated push status = %d", got)
	}
	login := request("POST", "/api/login", `{"password":"correct horse battery staple"}`, nil, "")
	var session struct{ CSRF string }
	if err := json.Unmarshal(login.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	cookie := login.Result().Cookies()[0]

	// Without VAPID keys the panel says so and refuses subscriptions.
	status := request("GET", "/api/push", "", cookie, "")
	if status.Code != 200 || !strings.Contains(status.Body.String(), `"configured":false`) {
		t.Fatalf("status without keys = %d %s", status.Code, status.Body.String())
	}
	var received []webpush.Message
	var mu sync.Mutex
	device, _ := ecdh.P256().GenerateKey(rand.Reader)
	secret := make([]byte, 16)
	_, _ = rand.Read(secret)
	push := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		plain, err := webpush.Decrypt(device, secret, body)
		var m webpush.Message
		if err == nil {
			err = json.Unmarshal(plain, &m)
		}
		if err != nil || !strings.HasPrefix(r.Header.Get("Authorization"), "vapid t=") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		received = append(received, m)
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	defer push.Close()
	subscription := `{"endpoint":"` + push.URL + `/device","expirationTime":null,"keys":{"p256dh":"` + base64.RawURLEncoding.EncodeToString(device.PublicKey().Bytes()) + `","auth":"` + base64.RawURLEncoding.EncodeToString(secret) + `"}}`
	if got := request("POST", "/api/push/subscriptions", subscription, cookie, session.CSRF).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("subscribe without keys = %d", got)
	}

	private, public, err := webpush.GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	keys, err := webpush.ParseKeys(private, public, "mailto:operador@example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	server.Push = webpush.New(keys, s)
	status = request("GET", "/api/push", "", cookie, "")
	if !strings.Contains(status.Body.String(), `"public_key":"`+public+`"`) {
		t.Fatalf("status = %s", status.Body.String())
	}
	// The default allowlist rejects endpoints outside browser push services.
	if got := request("POST", "/api/push/subscriptions", subscription, cookie, session.CSRF); got.Code != http.StatusBadRequest {
		t.Fatalf("loopback endpoint accepted: %d %s", got.Code, got.Body.String())
	}
	server.Push.AllowEndpoint = func(u *url.URL) bool { return strings.HasPrefix(u.String(), push.URL+"/") }
	if got := request("POST", "/api/push/subscriptions", subscription, cookie, ""); got.Code != http.StatusForbidden {
		t.Fatalf("subscribe without CSRF = %d", got.Code)
	}
	bad := strings.Replace(subscription, `"auth":"`, `"auth":"AA`, 1)
	if got := request("POST", "/api/push/subscriptions", bad, cookie, session.CSRF).Code; got != http.StatusBadRequest {
		t.Fatalf("invalid auth secret = %d", got)
	}
	if got := request("POST", "/api/push/subscriptions", subscription, cookie, session.CSRF); got.Code != http.StatusCreated {
		t.Fatalf("subscribe = %d %s", got.Code, got.Body.String())
	}
	endpoint := `{"endpoint":"` + push.URL + `/device"}`
	if got := request("POST", "/api/push/test", endpoint, cookie, session.CSRF); got.Code != http.StatusOK {
		t.Fatalf("test push = %d %s", got.Code, got.Body.String())
	}
	mu.Lock()
	if len(received) != 1 || received[0].Title != "Teste do vpsdash" {
		t.Fatalf("received = %+v", received)
	}
	mu.Unlock()
	if got := request("DELETE", "/api/push/subscriptions", endpoint, cookie, session.CSRF).Code; got != http.StatusOK {
		t.Fatalf("unsubscribe = %d", got)
	}
	if count, err := s.CountPushSubscriptions(t.Context()); err != nil || count != 0 {
		t.Fatalf("subscriptions after unsubscribe = %d, %v", count, err)
	}
	if got := request("POST", "/api/push/test", endpoint, cookie, session.CSRF).Code; got != http.StatusNotFound {
		t.Fatalf("test after unsubscribe = %d", got)
	}
}
