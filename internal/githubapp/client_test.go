package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := New(42, pemKey, map[string]int64{"heliowap": 7}, server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSetVariableUsesInstallationTokenAndActionsVariableEndpoint(t *testing.T) {
	var tokenRequests, updates int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/installations/7/access_tokens":
			tokenRequests++
			if r.Method != "POST" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				t.Errorf("token request = %s %s", r.Method, r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"installation-token","expires_at":"2030-01-01T00:00:00Z"}`))
		case "/repos/heliowap/vpsdash/actions/variables/AGENT_RUNNER":
			updates++
			if r.Method != "PATCH" || r.Header.Get("Authorization") != "Bearer installation-token" {
				t.Errorf("update auth/method = %s %s", r.Method, r.Header.Get("Authorization"))
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	for i := 0; i < 2; i++ {
		if err := c.SetVariable(context.Background(), "heliowap/vpsdash", "AGENT_RUNNER", "ubuntu-latest"); err != nil {
			t.Fatal(err)
		}
	}
	if tokenRequests != 1 || updates != 2 {
		t.Fatalf("token requests=%d updates=%d", tokenRequests, updates)
	}
}

func TestSetVariableRejectsUnknownLabel(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("network should not be called") })
	if err := c.SetVariable(context.Background(), "heliowap/vpsdash", "AGENT_RUNNER", "my-random-host"); err == nil {
		t.Fatal("unknown label accepted")
	}
}
