package webpush

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/heliowap/vpsdash/internal/store"
)

// ErrGone reports that the push service no longer knows the subscription
// (HTTP 404 or 410). The caller must forget it.
var ErrGone = errors.New("push subscription expired")

// ErrEndpoint rejects endpoints outside the known browser push services.
var ErrEndpoint = errors.New("push endpoint is not a known push service")

// ErrInvalidSubscription reports subscription keys or a payload that cannot
// be encrypted. Retrying does not help.
var ErrInvalidSubscription = errors.New("invalid push subscription")

// StatusError is a push service response other than success or gone.
type StatusError struct {
	Status     int
	RetryAfter time.Duration
}

func (e *StatusError) Error() string { return fmt.Sprintf("push service returned HTTP %d", e.Status) }

// Retryable reports whether the same request may succeed later.
func (e *StatusError) Retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// pushServiceHosts are the push services of current browsers. The server
// only posts to these hosts, so a stored subscription cannot aim requests at
// internal addresses.
var pushServiceHosts = []string{
	"fcm.googleapis.com",        // Chrome, Edge on Android, Android
	"jmt17.google.com",          // Chromium builds without Google API keys
	"push.services.mozilla.com", // Firefox
	"push.apple.com",            // Safari, iOS home-screen apps
	"notify.windows.com",        // Edge
}

// KnownPushService accepts HTTPS endpoints on the default port of a known
// browser push service or one of its subdomains.
func KnownPushService(endpoint *url.URL) bool {
	if endpoint.Scheme != "https" || endpoint.User != nil || endpoint.Port() != "" {
		return false
	}
	host := strings.ToLower(endpoint.Hostname())
	for _, allowed := range pushServiceHosts {
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return true
		}
	}
	return false
}

// Message is the JSON payload the service worker shows as a notification.
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
	Tag   string `json:"tag,omitempty"`
}

// MessageFor describes a queued alert in the dashboard's language and points
// the notification at the tab where the operator can act.
func MessageFor(kind, subject, body string) Message {
	m := Message{Body: body, Tag: kind + ":" + subject}
	switch kind {
	case store.AlertProjectDown:
		m.Title, m.URL = "Projeto falhou: "+subject, "/#projects"
	case store.AlertRunnerOffline:
		m.Title, m.URL = "Runner offline: "+subject, "/#fleet"
	case store.AlertCIFailed:
		m.Title, m.URL = "CI vermelho: "+subject, "/#fleet"
	case store.AlertAgentWaiting:
		m.Title, m.URL = "Agente esperando input: "+subject, "/#sessions"
	default:
		m.Title, m.URL = subject, "/#overview"
	}
	m.Title = truncate(m.Title, 200)
	m.Body = truncate(m.Body, 1000)
	return m
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit - len("…")
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + "…"
}

// TTL tells the push service how long to hold a message for an offline
// device. An agent prompt loses its point sooner than an incident.
func TTL(kind string) time.Duration {
	if kind == store.AlertAgentWaiting {
		return 30 * time.Minute
	}
	return 24 * time.Hour
}

const (
	maxAttempts  = 8
	firstBackoff = 30 * time.Second
	maxBackoff   = time.Hour
)

// Backoff returns the delay before retry number attempts (1-based).
func Backoff(attempts int) time.Duration {
	delay := firstBackoff
	for i := 1; i < attempts && delay < maxBackoff; i++ {
		delay *= 2
	}
	return min(delay, maxBackoff)
}

// Service signs, encrypts and delivers queued Web Push messages.
type Service struct {
	Keys  *Keys
	Store *store.Store
	HTTP  *http.Client
	// AllowEndpoint decides which endpoints may be stored and contacted.
	// It defaults to KnownPushService.
	AllowEndpoint func(*url.URL) bool
	Now           func() time.Time
}

func New(keys *Keys, st *store.Store) *Service {
	return &Service{Keys: keys, Store: st}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// ValidateEndpoint checks an endpoint before it is stored or contacted.
func (s *Service) ValidateEndpoint(raw string) error {
	endpoint, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || endpoint.Host == "" {
		return ErrEndpoint
	}
	allow := s.AllowEndpoint
	if allow == nil {
		allow = KnownPushService
	}
	if !allow(endpoint) {
		return ErrEndpoint
	}
	return nil
}

// ValidateKeys checks the browser's subscription keys.
func ValidateKeys(p256dh, auth string) error {
	point, err := decodeB64(p256dh)
	if err != nil {
		return errors.New("invalid p256dh key")
	}
	if _, err := ecdh.P256().NewPublicKey(point); err != nil {
		return errors.New("invalid p256dh key")
	}
	secret, err := decodeB64(auth)
	if err != nil || len(secret) != 16 {
		return errors.New("invalid auth secret")
	}
	return nil
}

// Send delivers one message to one subscription and classifies the result:
// nil on success, ErrGone for an expired subscription, *StatusError for other
// responses, or a transport error.
func (s *Service) Send(ctx context.Context, sub store.PushSubscription, message Message, ttl time.Duration, urgency string) error {
	if err := s.ValidateEndpoint(sub.Endpoint); err != nil {
		return err
	}
	if err := ValidateKeys(sub.P256DH, sub.Auth); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSubscription, err)
	}
	uaPublic, _ := decodeB64(sub.P256DH)
	authSecret, _ := decodeB64(sub.Auth)
	plaintext, err := json.Marshal(message)
	if err != nil {
		return err
	}
	body, err := Encrypt(uaPublic, authSecret, plaintext)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSubscription, err)
	}
	authorization, err := s.Keys.Authorization(sub.Endpoint, s.now().Add(12*time.Hour))
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", authorization)
	request.Header.Set("Content-Encoding", "aes128gcm")
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("TTL", strconv.Itoa(int(ttl.Seconds())))
	if urgency != "" {
		request.Header.Set("Urgency", urgency)
	}
	response, err := s.client().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		return nil
	case response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone:
		return ErrGone
	}
	statusErr := &StatusError{Status: response.StatusCode}
	if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil && seconds > 0 {
		statusErr.RetryAfter = time.Duration(seconds) * time.Second
	}
	return statusErr
}

// SendTest sends a test notification to one registered device right away.
// An expired subscription is forgotten and reported as ErrGone.
func (s *Service) SendTest(ctx context.Context, endpoint string) error {
	sub, err := s.Store.PushSubscriptionByEndpoint(ctx, endpoint)
	if err != nil {
		return err
	}
	message := Message{Title: "Teste do vpsdash", Body: "As notificações deste dispositivo estão ativas.", URL: "/#overview", Tag: "test"}
	err = s.Send(ctx, sub, message, 5*time.Minute, "normal")
	if errors.Is(err, ErrGone) {
		if deleteErr := s.Store.DeletePushSubscription(ctx, sub.ID, s.now()); deleteErr != nil {
			return errors.Join(err, deleteErr)
		}
	}
	return err
}

// DeliverDue attempts every delivery whose next attempt is due. Expired
// subscriptions are deleted; retryable failures back off exponentially up to
// maxAttempts; other failures drop the delivery.
func (s *Service) DeliverDue(ctx context.Context) error {
	now := s.now()
	due, err := s.Store.DuePushDeliveries(ctx, now, 50)
	if err != nil {
		return err
	}
	for _, delivery := range due {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		message := MessageFor(delivery.Kind, delivery.Subject, delivery.Body)
		sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		sendErr := s.Send(sendCtx, delivery.Subscription, message, TTL(delivery.Kind), "high")
		cancel()
		var statusErr *StatusError
		switch {
		case sendErr == nil:
			err = s.Store.FinishPushDelivery(ctx, delivery.ID, s.now())
		case errors.Is(sendErr, ErrGone):
			log.Printf("webpush: subscription %d expired; removing it", delivery.Subscription.ID)
			err = s.Store.DeletePushSubscription(ctx, delivery.Subscription.ID, s.now())
		case retryable(sendErr, &statusErr) && delivery.Attempts+1 < maxAttempts:
			delay := Backoff(delivery.Attempts + 1)
			if statusErr != nil && statusErr.RetryAfter > delay {
				delay = min(statusErr.RetryAfter, maxBackoff)
			}
			err = s.Store.RetryPushDelivery(ctx, delivery.ID, delivery.Attempts+1, s.now().Add(delay), sendErr.Error())
		default:
			log.Printf("webpush: dropping delivery %d after %d attempts: %v", delivery.ID, delivery.Attempts+1, sendErr)
			err = s.Store.FinishPushDelivery(ctx, delivery.ID, s.now())
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func retryable(err error, statusErr **StatusError) bool {
	if errors.As(err, statusErr) {
		return (*statusErr).Retryable()
	}
	// Endpoint, key and payload errors will not improve with time; transport
	// errors might.
	return !errors.Is(err, ErrEndpoint) && !errors.Is(err, ErrInvalidSubscription)
}

// Start delivers queued messages every ten seconds until ctx ends.
func (s *Service) Start(ctx context.Context) {
	if s == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			if err := s.DeliverDue(ctx); err != nil && ctx.Err() == nil {
				log.Printf("webpush: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
