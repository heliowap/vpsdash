package webpush

import (
	"bytes"
	"crypto/ecdh"
	"strings"
	"testing"
)

// Values from RFC 8291 Section 5 and Appendix A.
const (
	rfcPlaintext  = "When I grow up, I want to be a watermelon"
	rfcASPrivate  = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	rfcASPublic   = "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"
	rfcUAPrivate  = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
	rfcUAPublic   = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	rfcSalt       = "DGv6ra1nlYgDCS1FRnbzlw"
	rfcAuthSecret = "BTBZMqHH6r4Tts7J_aSIgg"
	rfcCEK        = "oIhVW04MRdy2XN9CiKLxTg"
	rfcNonce      = "4h_95klXJ5E_qnoN"
	rfcMessage    = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
)

func mustB64(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := decodeB64(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRFC8291AppendixAKeyDerivation(t *testing.T) {
	uaPrivate, err := ecdh.P256().NewPrivateKey(mustB64(t, rfcUAPrivate))
	if err != nil {
		t.Fatal(err)
	}
	asPublic, err := ecdh.P256().NewPublicKey(mustB64(t, rfcASPublic))
	if err != nil {
		t.Fatal(err)
	}
	secret, err := uaPrivate.ECDH(asPublic)
	if err != nil {
		t.Fatal(err)
	}
	if got := b64.EncodeToString(secret); got != "kyrL1jIIOHEzg3sM2ZWRHDRB62YACZhhSlknJ672kSs" {
		t.Fatalf("ecdh_secret = %s", got)
	}
	cek, nonce, err := deriveKeys(secret, mustB64(t, rfcAuthSecret), mustB64(t, rfcSalt), mustB64(t, rfcUAPublic), mustB64(t, rfcASPublic))
	if err != nil {
		t.Fatal(err)
	}
	if b64.EncodeToString(cek) != rfcCEK || b64.EncodeToString(nonce) != rfcNonce {
		t.Fatalf("CEK = %s, NONCE = %s", b64.EncodeToString(cek), b64.EncodeToString(nonce))
	}
}

func TestRFC8291AppendixAEncryptionMatchesExample(t *testing.T) {
	asPrivate, err := ecdh.P256().NewPrivateKey(mustB64(t, rfcASPrivate))
	if err != nil {
		t.Fatal(err)
	}
	got, err := encrypt(mustB64(t, rfcUAPublic), mustB64(t, rfcAuthSecret), []byte(rfcPlaintext), mustB64(t, rfcSalt), asPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, mustB64(t, rfcMessage)) {
		t.Fatalf("message =\n%s\nwant\n%s", b64.EncodeToString(got), rfcMessage)
	}
	if len(got) < 86 || !strings.HasPrefix(b64.EncodeToString(got[:86]), "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z") {
		t.Fatalf("unexpected 86-octet header: %x", got[:86])
	}
}

func TestRFC8291AppendixADecryptsExample(t *testing.T) {
	uaPrivate, err := ecdh.P256().NewPrivateKey(mustB64(t, rfcUAPrivate))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Decrypt(uaPrivate, mustB64(t, rfcAuthSecret), mustB64(t, rfcMessage))
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != rfcPlaintext {
		t.Fatalf("plaintext = %q", plain)
	}
}
