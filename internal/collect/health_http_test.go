package collect

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/heliowap/vpsdash/internal/config"
)

func TestHealthURLPolicyAllowsInventoryAndPublicDestinations(t *testing.T) {
	p := newHealthPolicy([]config.Host{{TailnetName: "vps.tailnet.ts.net"}})
	p.lookup = func(_ context.Context, host string) ([]net.IPAddr, error) {
		addresses := map[string]string{
			"vps.tailnet.ts.net": "100.101.102.103",
			"public.example.com": "8.8.8.8",
			"rebinding.example":  "127.0.0.1",
		}
		return []net.IPAddr{{IP: net.ParseIP(addresses[host])}}, nil
	}
	for _, raw := range []string{"http://vps.tailnet.ts.net:8080/health", "https://public.example.com/health"} {
		if err := p.validate(context.Background(), raw); err != nil {
			t.Fatalf("rejected %s: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"http://127.0.0.1:8484/healthz",
		"http://169.254.169.254/latest/meta-data/",
		"http://100.101.102.103/health",
		"http://rebinding.example/health",
		"http://user:pass@public.example.com/health",
		"file:///etc/passwd",
	} {
		if err := p.validate(context.Background(), raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestHealthProbeBlocksRedirectToOtherHost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := newHealthHTTPClient([]config.Host{{TailnetName: "127.0.0.1"}})
	if ok, detail, err := checkHTTP(context.Background(), client, server.URL+"/health"); err != nil || !ok || detail != "healthy" {
		t.Fatalf("health check = %t, %q, %v", ok, detail, err)
	}
	if _, _, err := checkHTTP(context.Background(), client, server.URL+"/redirect"); !errors.Is(err, errHealthURLDenied) {
		t.Fatalf("redirect was not denied: %v", err)
	}
	if err := ValidateHealthURL(context.Background(), nil, server.URL); err == nil || !strings.Contains(err.Error(), "unapproved") {
		t.Fatalf("loopback URL was accepted at save: %v", err)
	}
}

func TestUnresolvedHealthHostCanBeSavedButDoesNotRecordOutage(t *testing.T) {
	p := newHealthPolicy(nil)
	p.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return nil, errors.New("temporary DNS outage")
	}
	if err := p.validateForSave(context.Background(), "https://pending.example.com/health"); err != nil {
		t.Fatalf("well-formed unresolved URL rejected at save: %v", err)
	}
	client := newHealthHTTPClientWithPolicy(p)
	if _, _, err := checkHTTP(context.Background(), client, "https://pending.example.com/health"); !errors.Is(err, errHealthDNSUnavailable) {
		t.Fatalf("DNS outage was recorded as a project failure: %v", err)
	}
}
