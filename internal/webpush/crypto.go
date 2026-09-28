// Package webpush sends Web Push messages (RFC 8030) with VAPID
// authentication (RFC 8292) and aes128gcm payload encryption (RFC 8291),
// using only the Go standard library.
package webpush

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"strings"
	"time"
)

// recordSize is the aes128gcm record size advertised in the header. Push
// payloads are limited to 4096 octets, so every message is a single record.
const recordSize = 4096

// MaxPlaintext is the largest payload that fits in one 4096-octet record
// after the 86-octet header, the padding delimiter, and the GCM tag.
const MaxPlaintext = recordSize - 86 - 1 - 16

// Keys is the VAPID key pair that identifies this application server.
type Keys struct {
	private *ecdsa.PrivateKey
	// Public is the uncompressed P-256 point, base64url without padding. The
	// browser receives it as applicationServerKey.
	Public  string
	Subject string
}

// GenerateKeys creates a new VAPID key pair and returns the base64url
// encodings of the raw private scalar and the uncompressed public point.
func GenerateKeys() (privateKey, publicKey string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	raw, err := key.Bytes()
	if err != nil {
		return "", "", err
	}
	public, err := key.PublicKey.Bytes()
	if err != nil {
		return "", "", err
	}
	return b64.EncodeToString(raw), b64.EncodeToString(public), nil
}

var b64 = base64.RawURLEncoding

func decodeB64(value string) ([]byte, error) {
	return b64.DecodeString(strings.TrimRight(strings.TrimSpace(value), "="))
}

// ParseKeys validates a VAPID key pair and subject. The subject must be a
// mailto: or https: URI so push services can contact the operator.
func ParseKeys(privateKey, publicKey, subject string) (*Keys, error) {
	raw, err := decodeB64(privateKey)
	if err != nil {
		return nil, errors.New("VAPID_PRIVATE_KEY is not base64url")
	}
	key, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
	if err != nil {
		return nil, fmt.Errorf("VAPID_PRIVATE_KEY: %w", err)
	}
	public, err := key.PublicKey.Bytes()
	if err != nil {
		return nil, err
	}
	declared, err := decodeB64(publicKey)
	if err != nil || string(declared) != string(public) {
		return nil, errors.New("VAPID_PUBLIC_KEY does not match VAPID_PRIVATE_KEY")
	}
	parsed, err := url.Parse(subject)
	if err != nil || (parsed.Scheme != "mailto" && parsed.Scheme != "https") || (parsed.Opaque == "" && parsed.Host == "") {
		return nil, errors.New("VAPID_SUBJECT must be a mailto: or https: URI")
	}
	return &Keys{private: key, Public: b64.EncodeToString(public), Subject: subject}, nil
}

// FromEnv reads VAPID_PUBLIC_KEY, VAPID_PRIVATE_KEY and VAPID_SUBJECT.
func FromEnv(values map[string]string) (*Keys, error) {
	if values["VAPID_PRIVATE_KEY"] == "" || values["VAPID_PUBLIC_KEY"] == "" || values["VAPID_SUBJECT"] == "" {
		return nil, errors.New("VAPID_PUBLIC_KEY, VAPID_PRIVATE_KEY and VAPID_SUBJECT are required")
	}
	return ParseKeys(values["VAPID_PRIVATE_KEY"], values["VAPID_PUBLIC_KEY"], values["VAPID_SUBJECT"])
}

// Authorization returns the RFC 8292 "vapid" Authorization header value for
// a push endpoint. The JWT audience is the endpoint origin.
func (k *Keys) Authorization(endpoint string, expires time.Time) (string, error) {
	target, err := url.Parse(endpoint)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return "", errors.New("invalid push endpoint")
	}
	header := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, err := json.Marshal(map[string]any{"aud": target.Scheme + "://" + target.Host, "exp": expires.Unix(), "sub": k.Subject})
	if err != nil {
		return "", err
	}
	signingInput := header + "." + b64.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, k.private, digest[:])
	if err != nil {
		return "", err
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return "vapid t=" + signingInput + "." + b64.EncodeToString(signature) + ", k=" + k.Public, nil
}

// VerifyAuthorization checks a vapid Authorization header against the given
// public key and returns its JWT claims. Push services perform this check;
// it is exported so tests and diagnostics can use the same parser.
func VerifyAuthorization(header string) (claims map[string]any, publicKey string, err error) {
	rest, ok := strings.CutPrefix(header, "vapid ")
	if !ok {
		return nil, "", errors.New("missing vapid scheme")
	}
	var token string
	for part := range strings.SplitSeq(rest, ",") {
		key, value, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch key {
		case "t":
			token = value
		case "k":
			publicKey = value
		}
	}
	pieces := strings.Split(token, ".")
	if len(pieces) != 3 || publicKey == "" {
		return nil, "", errors.New("malformed vapid credentials")
	}
	point, err := decodeB64(publicKey)
	if err != nil {
		return nil, "", err
	}
	public, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), point)
	if err != nil {
		return nil, "", err
	}
	signature, err := decodeB64(pieces[2])
	if err != nil || len(signature) != 64 {
		return nil, "", errors.New("malformed signature")
	}
	digest := sha256.Sum256([]byte(pieces[0] + "." + pieces[1]))
	if !ecdsa.Verify(public, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
		return nil, "", errors.New("invalid signature")
	}
	var head map[string]any
	if raw, err := decodeB64(pieces[0]); err != nil || json.Unmarshal(raw, &head) != nil || head["alg"] != "ES256" {
		return nil, "", errors.New("unexpected JWT header")
	}
	raw, err := decodeB64(pieces[1])
	if err != nil {
		return nil, "", err
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, "", err
	}
	return claims, publicKey, nil
}

// Encrypt encrypts plaintext for a subscription with the aes128gcm content
// coding of RFC 8291, using a fresh ephemeral key and salt.
func Encrypt(uaPublic, authSecret, plaintext []byte) ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	ephemeral, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return encrypt(uaPublic, authSecret, plaintext, salt, ephemeral)
}

func encrypt(uaPublic, authSecret, plaintext, salt []byte, asPrivate *ecdh.PrivateKey) ([]byte, error) {
	if len(plaintext) > MaxPlaintext {
		return nil, fmt.Errorf("push payload exceeds %d bytes", MaxPlaintext)
	}
	if len(authSecret) != 16 {
		return nil, errors.New("auth secret must be 16 bytes")
	}
	if len(salt) != 16 {
		return nil, errors.New("salt must be 16 bytes")
	}
	ua, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		return nil, fmt.Errorf("subscription key: %w", err)
	}
	secret, err := asPrivate.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPublic := asPrivate.PublicKey().Bytes()
	cek, nonce, err := deriveKeys(secret, authSecret, salt, uaPublic, asPublic)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	header := make([]byte, 0, 21+len(asPublic))
	header = append(header, salt...)
	header = binary.BigEndian.AppendUint32(header, recordSize)
	header = append(header, byte(len(asPublic)))
	header = append(header, asPublic...)
	record := append(append(make([]byte, 0, len(plaintext)+1), plaintext...), 0x02)
	return gcm.Seal(header, nonce, record, nil), nil
}

// deriveKeys implements the key derivation of RFC 8291 Section 3.4 and
// RFC 8188 Section 2.2.
func deriveKeys(ecdhSecret, authSecret, salt, uaPublic, asPublic []byte) (cek, nonce []byte, err error) {
	prkKey, err := hkdf.Extract(sha256.New, ecdhSecret, authSecret)
	if err != nil {
		return nil, nil, err
	}
	keyInfo := "WebPush: info\x00" + string(uaPublic) + string(asPublic)
	ikm, err := hkdf.Expand(sha256.New, prkKey, keyInfo, 32)
	if err != nil {
		return nil, nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, nil, err
	}
	if cek, err = hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16); err != nil {
		return nil, nil, err
	}
	if nonce, err = hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12); err != nil {
		return nil, nil, err
	}
	return cek, nonce, nil
}

// Decrypt reverses Encrypt with the user agent's private key. The server
// never holds subscriber private keys; this exists for tests and fakes that
// play the browser's role.
func Decrypt(uaPrivate *ecdh.PrivateKey, authSecret, body []byte) ([]byte, error) {
	if len(body) < 21 {
		return nil, errors.New("short aes128gcm body")
	}
	salt := body[:16]
	idLen := int(body[20])
	if len(body) < 21+idLen {
		return nil, errors.New("short aes128gcm header")
	}
	asKey, err := ecdh.P256().NewPublicKey(body[21 : 21+idLen])
	if err != nil {
		return nil, err
	}
	secret, err := uaPrivate.ECDH(asKey)
	if err != nil {
		return nil, err
	}
	cek, nonce, err := deriveKeys(secret, authSecret, salt, uaPrivate.PublicKey().Bytes(), asKey.Bytes())
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, body[21+idLen:], nil)
	if err != nil {
		return nil, err
	}
	end := len(plain) - 1
	for end >= 0 && plain[end] == 0 {
		end--
	}
	if end < 0 || plain[end] != 0x02 {
		return nil, errors.New("missing last-record delimiter")
	}
	return plain[:end], nil
}
