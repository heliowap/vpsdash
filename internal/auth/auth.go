package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

const CookieName = "vpsdash_session"
const sessionLifetime = 12 * time.Hour

type Authenticator struct {
	passwordHash string
	key          []byte
}

func HashPassword(password string) (string, error) {
	if len(password) < 12 {
		return "", errors.New("password must contain at least 12 characters")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=2$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var mem, iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &iterations, &parallelism); err != nil {
		return false
	}
	if mem < 8192 || mem > 131072 || iterations < 1 || iterations > 10 || parallelism < 1 || parallelism > 8 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) != 32 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, iterations, mem, parallelism, uint32(len(want)))
	return subtle.ConstantTimeCompare(want, got) == 1
}

func New(passwordHash string, key []byte) (*Authenticator, error) {
	if len(key) < 32 {
		return nil, errors.New("session key must be at least 32 bytes")
	}
	if !strings.HasPrefix(passwordHash, "$argon2id$") {
		return nil, errors.New("invalid password hash")
	}
	return &Authenticator{passwordHash: passwordHash, key: append([]byte(nil), key...)}, nil
}

func (a *Authenticator) VerifyPassword(password string) bool {
	return VerifyPassword(a.passwordHash, password)
}

func (a *Authenticator) sign(payload string) []byte {
	mac := hmac.New(sha256.New, a.key)
	_, _ = mac.Write([]byte(payload))
	return mac.Sum(nil)
}

func (a *Authenticator) Issue(w http.ResponseWriter, now time.Time) string {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return ""
	}
	payload := strconv.FormatInt(now.Add(sessionLifetime).Unix(), 10) + ":" + base64.RawURLEncoding.EncodeToString(nonce)
	value := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(a.sign(payload))
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: value, Path: "/", MaxAge: int(sessionLifetime.Seconds()), HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	return a.csrf(value)
}

func (a *Authenticator) Validate(r *http.Request, now time.Time) (string, bool) {
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return "", false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return "", false
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	payload := string(payloadBytes)
	if !hmac.Equal(sig, a.sign(payload)) {
		return "", false
	}
	expRaw, _, ok := strings.Cut(payload, ":")
	if !ok {
		return "", false
	}
	expires, err := strconv.ParseInt(expRaw, 10, 64)
	if err != nil || now.Unix() >= expires || expires > now.Add(sessionLifetime).Unix() {
		return "", false
	}
	return a.csrf(cookie.Value), true
}

// SessionID returns a stable identifier for a valid session cookie, derived
// with the session key so it cannot be computed from the cookie alone.
// Server-side state such as step-up re-authentication is bound to it.
func (a *Authenticator) SessionID(r *http.Request, now time.Time) (string, bool) {
	if _, ok := a.Validate(r, now); !ok {
		return "", false
	}
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return "", false
	}
	return base64.RawURLEncoding.EncodeToString(a.sign("sid:" + cookie.Value)), true
}

func (a *Authenticator) csrf(cookieValue string) string {
	return base64.RawURLEncoding.EncodeToString(a.sign("csrf:" + cookieValue))
}

func ValidCSRF(expected, received string) bool {
	return expected != "" && subtle.ConstantTimeCompare([]byte(expected), []byte(received)) == 1
}

func Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}
