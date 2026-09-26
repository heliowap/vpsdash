package auth

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPasswordAndSignedSession(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "correct horse battery staple") || VerifyPassword(hash, "wrong") {
		t.Fatal("password verification failed")
	}
	a, err := New(hash, []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.Issue(w, time.Unix(1000, 0))
	cookie := w.Result().Cookies()[0]
	r := httptest.NewRequest("GET", "https://vps.tailnet.ts.net/api/session", nil)
	r.AddCookie(cookie)
	csrf, ok := a.Validate(r, time.Unix(1001, 0))
	if !ok || csrf == "" {
		t.Fatal("valid cookie rejected")
	}
	if _, ok := a.Validate(r, time.Unix(1000+13*3600, 0)); ok {
		t.Fatal("expired cookie accepted")
	}
	cookie.Value += "tamper"
	r = httptest.NewRequest("GET", "https://vps.tailnet.ts.net/api/session", nil)
	r.AddCookie(cookie)
	if _, ok := a.Validate(r, time.Unix(1001, 0)); ok {
		t.Fatal("tampered cookie accepted")
	}
}
