package webpush_test

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heliowap/vpsdash/internal/store"
	"github.com/heliowap/vpsdash/internal/webpush"
)

type pushedRequest struct {
	header  http.Header
	message webpush.Message
	err     error
}

// fakePushService plays both the push service and the browser: it checks
// the VAPID credentials and decrypts each payload with the subscriber key.
type fakePushService struct {
	t        *testing.T
	server   *httptest.Server
	device   *ecdh.PrivateKey
	auth     []byte
	mu       sync.Mutex
	status   []int
	received []pushedRequest
}

func newFakePushService(t *testing.T) *fakePushService {
	t.Helper()
	device, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakePushService{t: t, device: device, auth: make([]byte, 16)}
	_, _ = rand.Read(f.auth)
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakePushService) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	got := pushedRequest{header: r.Header.Clone()}
	plain, err := webpush.Decrypt(f.device, f.auth, body)
	if err == nil {
		err = json.Unmarshal(plain, &got.message)
	}
	got.err = err
	f.mu.Lock()
	f.received = append(f.received, got)
	status := http.StatusCreated
	if len(f.status) > 0 {
		status, f.status = f.status[0], f.status[1:]
	}
	f.mu.Unlock()
	if status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "120")
	}
	w.WriteHeader(status)
}

func (f *fakePushService) respond(statuses ...int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = append(f.status, statuses...)
}

func (f *fakePushService) requests() []pushedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pushedRequest(nil), f.received...)
}

func (f *fakePushService) subscription(path string) store.PushSubscription {
	return store.PushSubscription{
		Endpoint: f.server.URL + path,
		P256DH:   base64.RawURLEncoding.EncodeToString(f.device.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(f.auth),
	}
}

type fixture struct {
	store   *store.Store
	service *webpush.Service
	push    *fakePushService
	keys    *webpush.Keys
	now     time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "vpsdash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	private, public, err := webpush.GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	keys, err := webpush.ParseKeys(private, public, "mailto:operador@example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{store: st, keys: keys, push: newFakePushService(t), now: time.Now()}
	f.service = webpush.New(keys, st)
	f.service.AllowEndpoint = func(u *url.URL) bool { return strings.HasPrefix(u.String(), f.push.server.URL+"/") }
	f.service.Now = func() time.Time { return f.now }
	return f
}

func (f *fixture) subscribe(t *testing.T, path string) store.PushSubscription {
	t.Helper()
	sub := f.push.subscription(path)
	if err := f.store.SavePushSubscription(context.Background(), sub, f.now); err != nil {
		t.Fatal(err)
	}
	saved, err := f.store.PushSubscriptionByEndpoint(context.Background(), sub.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestQueuedAlertReachesDeviceWithVAPIDAndEncryptedPayload(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.subscribe(t, "/device-1")
	if err := f.store.QueueAlert(ctx, store.AlertProjectDown, "vps / api", "api is failed"); err != nil {
		t.Fatal(err)
	}
	if err := f.service.DeliverDue(ctx); err != nil {
		t.Fatal(err)
	}
	requests := f.push.requests()
	if len(requests) != 1 {
		t.Fatalf("push requests = %d", len(requests))
	}
	got := requests[0]
	if got.err != nil {
		t.Fatalf("device could not decrypt payload: %v", got.err)
	}
	if got.header.Get("Content-Encoding") != "aes128gcm" || got.header.Get("TTL") != "86400" || got.header.Get("Urgency") != "high" {
		t.Fatalf("headers = %v", got.header)
	}
	claims, publicKey, err := webpush.VerifyAuthorization(got.header.Get("Authorization"))
	if err != nil {
		t.Fatalf("authorization %q: %v", got.header.Get("Authorization"), err)
	}
	if publicKey != f.keys.Public || claims["aud"] != f.push.server.URL || claims["sub"] != "mailto:operador@example.invalid" {
		t.Fatalf("claims = %v, k = %s", claims, publicKey)
	}
	if exp, _ := claims["exp"].(float64); int64(exp) <= f.now.Unix() || int64(exp) > f.now.Add(24*time.Hour).Unix() {
		t.Fatalf("exp = %v", claims["exp"])
	}
	want := webpush.Message{Title: "Projeto falhou: vps / api", Body: "api is failed", URL: "/#projects", Tag: "project_down:vps / api"}
	if got.message != want {
		t.Fatalf("message = %+v", got.message)
	}
	if due, err := f.store.DuePushDeliveries(ctx, f.now.Add(48*time.Hour), 10); err != nil || len(due) != 0 {
		t.Fatalf("deliveries left = %+v, %v", due, err)
	}
	// E-mail keeps its own queue for production incidents.
	if pending, err := f.store.PendingAlerts(ctx); err != nil || len(pending) != 1 || pending[0].Channel != "smtp" {
		t.Fatalf("smtp alerts = %+v, %v", pending, err)
	}
	// A repeated alert within the hour is not pushed again.
	if err := f.store.QueueAlert(ctx, store.AlertProjectDown, "vps / api", "api is failed"); err != nil {
		t.Fatal(err)
	}
	if due, err := f.store.DuePushDeliveries(ctx, f.now.Add(time.Minute), 10); err != nil || len(due) != 0 {
		t.Fatalf("repeated alert queued deliveries = %+v, %v", due, err)
	}
}

func TestExpiredSubscriptionIsDropped(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusGone} {
		f := newFixture(t)
		ctx := context.Background()
		f.subscribe(t, "/gone")
		kept := f.subscribe(t, "/kept")
		f.push.respond(status)
		if err := f.store.QueueAlert(ctx, store.AlertRunnerOffline, "vps / runner", "unit absent"); err != nil {
			t.Fatal(err)
		}
		if err := f.service.DeliverDue(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.PushSubscriptionByEndpoint(ctx, f.push.server.URL+"/gone"); err == nil {
			t.Fatalf("HTTP %d kept the subscription", status)
		}
		if count, err := f.store.CountPushSubscriptions(ctx); err != nil || count != 1 {
			t.Fatalf("subscriptions = %d, %v", count, err)
		}
		if _, err := f.store.PushSubscriptionByEndpoint(ctx, kept.Endpoint); err != nil {
			t.Fatal(err)
		}
		if due, err := f.store.DuePushDeliveries(ctx, f.now.Add(48*time.Hour), 10); err != nil || len(due) != 0 {
			t.Fatalf("deliveries left = %+v, %v", due, err)
		}
	}
}

func TestFailedDeliveryRetriesWithBackoff(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.subscribe(t, "/flaky")
	f.push.respond(http.StatusServiceUnavailable, http.StatusTooManyRequests)
	if err := f.store.QueueAlert(ctx, store.AlertProjectDown, "vps / api", "down"); err != nil {
		t.Fatal(err)
	}
	if err := f.service.DeliverDue(ctx); err != nil {
		t.Fatal(err)
	}
	due, err := f.store.DuePushDeliveries(ctx, f.now.Add(29*time.Second), 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("retry before backoff = %+v, %v", due, err)
	}
	due, err = f.store.DuePushDeliveries(ctx, f.now.Add(30*time.Second), 10)
	if err != nil || len(due) != 1 || due[0].Attempts != 1 {
		t.Fatalf("retry after backoff = %+v, %v", due, err)
	}
	f.now = f.now.Add(30 * time.Second)
	if err := f.service.DeliverDue(ctx); err != nil {
		t.Fatal(err)
	}
	// 429 with Retry-After: 120 waits longer than the 60 s backoff.
	if due, err := f.store.DuePushDeliveries(ctx, f.now.Add(119*time.Second), 10); err != nil || len(due) != 0 {
		t.Fatalf("Retry-After ignored: %+v, %v", due, err)
	}
	f.now = f.now.Add(120 * time.Second)
	if err := f.service.DeliverDue(ctx); err != nil {
		t.Fatal(err)
	}
	requests := f.push.requests()
	if len(requests) != 3 || requests[2].err != nil || requests[2].message.Title != "Projeto falhou: vps / api" {
		t.Fatalf("requests = %+v", requests)
	}
	if due, err := f.store.DuePushDeliveries(ctx, f.now.Add(48*time.Hour), 10); err != nil || len(due) != 0 {
		t.Fatalf("deliveries left = %+v, %v", due, err)
	}
}

func TestPermanentFailureDropsDeliveryButKeepsSubscription(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	sub := f.subscribe(t, "/bad-request")
	f.push.respond(http.StatusBadRequest)
	if err := f.store.QueueAlert(ctx, store.AlertProjectDown, "vps / api", "down"); err != nil {
		t.Fatal(err)
	}
	if err := f.service.DeliverDue(ctx); err != nil {
		t.Fatal(err)
	}
	if due, err := f.store.DuePushDeliveries(ctx, f.now.Add(48*time.Hour), 10); err != nil || len(due) != 0 {
		t.Fatalf("deliveries left = %+v, %v", due, err)
	}
	if _, err := f.store.PushSubscriptionByEndpoint(ctx, sub.Endpoint); err != nil {
		t.Fatal(err)
	}
}

func TestAgentWaitingIsPushOnly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.subscribe(t, "/device")
	if err := f.store.UpsertHost(ctx, store.Host{ID: "vps", TailnetName: "vps.example.invalid", Kind: "vps"}); err != nil {
		t.Fatal(err)
	}
	working := []store.Session{{Name: "dev", Agent: "claude", CWD: "/srv/app", State: "working"}}
	waiting := []store.Session{{Name: "dev", Agent: "claude", CWD: "/srv/app", State: "waiting"}}
	for i, snapshot := range [][]store.Session{working, waiting, waiting} {
		if err := f.store.ReplaceSessions(ctx, "vps", snapshot, f.now.Add(time.Duration(i-2)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.service.DeliverDue(ctx); err != nil {
		t.Fatal(err)
	}
	requests := f.push.requests()
	if len(requests) != 1 {
		t.Fatalf("a session still waiting was notified again: %d requests", len(requests))
	}
	got := requests[0]
	if got.err != nil || got.message.Title != "Agente esperando input: vps / dev" || got.message.URL != "/#sessions?host=vps&session=dev" || got.message.Host != "vps" || got.message.Session != "dev" || got.header.Get("TTL") != "1800" {
		t.Fatalf("request = %+v", got)
	}
	if !strings.Contains(got.message.Body, "claude aguarda sua resposta na sessão dev") {
		t.Fatalf("body = %q", got.message.Body)
	}
	if pending, err := f.store.PendingAlerts(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("agent waiting reached e-mail: %+v, %v", pending, err)
	}
}

func TestNoSubscriptionQueuesNoPush(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.store.QueueAlert(ctx, store.AlertProjectDown, "vps / api", "down"); err != nil {
		t.Fatal(err)
	}
	f.subscribe(t, "/late")
	if err := f.service.DeliverDue(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.push.requests()) != 0 {
		t.Fatal("alert queued before any subscription was pushed")
	}
}

func TestSendTestReportsExpiredSubscription(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	sub := f.subscribe(t, "/device")
	if err := f.service.SendTest(ctx, sub.Endpoint); err != nil {
		t.Fatal(err)
	}
	if got := f.push.requests(); len(got) != 1 || got[0].err != nil || got[0].message.Title != "Teste do vpsdash" {
		t.Fatalf("test push = %+v", got)
	}
	f.push.respond(http.StatusGone)
	if err := f.service.SendTest(ctx, sub.Endpoint); !errors.Is(err, webpush.ErrGone) {
		t.Fatalf("expired test err = %v", err)
	}
	if count, _ := f.store.CountPushSubscriptions(ctx); count != 0 {
		t.Fatal("expired subscription kept after test")
	}
}

func TestEndpointAllowlist(t *testing.T) {
	service := webpush.New(nil, nil)
	for endpoint, ok := range map[string]bool{
		"https://fcm.googleapis.com/fcm/send/abc":                    true,
		"https://jmt17.google.com/fcm/send/abc":                      true,
		"https://google.com/fcm/send/abc":                            false,
		"https://updates.push.services.mozilla.com/wpush/v2/abc":     true,
		"https://web.push.apple.com/QGx":                             true,
		"https://wns2-par02p.notify.windows.com/w/?token=abc":        true,
		"http://fcm.googleapis.com/fcm/send/abc":                     false,
		"https://fcm.googleapis.com:8443/fcm/send/abc":               false,
		"https://user@fcm.googleapis.com/fcm/send/abc":               false,
		"https://fcm.googleapis.com.example.invalid/fcm/send/abc":    false,
		"https://127.0.0.1/push":                                     false,
		"https://evilpush.apple.com.attacker.example.invalid/device": false,
	} {
		if err := service.ValidateEndpoint(endpoint); (err == nil) != ok {
			t.Errorf("%s: err = %v", endpoint, err)
		}
	}
}

func TestParseKeysRejectsMismatchedPairAndSubject(t *testing.T) {
	private, public, err := webpush.GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := webpush.GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := webpush.ParseKeys(private, other, "mailto:a@example.invalid"); err == nil {
		t.Fatal("mismatched public key accepted")
	}
	if _, err := webpush.ParseKeys(private, public, "http://example.invalid"); err == nil {
		t.Fatal("http subject accepted")
	}
	if _, err := webpush.ParseKeys(private, public, "https://app.example.invalid"); err != nil {
		t.Fatal(err)
	}
}

func TestAgentWaitingMessageFocusesTheSession(t *testing.T) {
	m := webpush.MessageFor(store.AlertAgentWaiting, "vps / agente 1&x=#/ok", "corpo")
	if m.Host != "vps" || m.Session != "agente 1&x=#/ok" {
		t.Fatalf("focus = %q %q", m.Host, m.Session)
	}
	// The URL stays a same-origin path; the session name is encoded inside
	// the hash so it cannot add parameters or leave the page.
	if m.URL != "/#sessions?host=vps&session=agente+1%26x%3D%23%2Fok" {
		t.Fatalf("url = %q", m.URL)
	}
	for _, subject := range []string{"sem separador", " / dev", "vps / "} {
		if m := webpush.MessageFor(store.AlertAgentWaiting, subject, ""); m.URL != "/#sessions" || m.Host != "" || m.Session != "" {
			t.Fatalf("%q = %+v", subject, m)
		}
	}
	if m := webpush.MessageFor(store.AlertProjectDown, "vps / dev", ""); m.Host != "" || m.URL != "/#projects" {
		t.Fatalf("project alert = %+v", m)
	}
}
