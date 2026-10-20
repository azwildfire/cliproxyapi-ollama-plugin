package provider

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestGenerateDeviceKeyRoundTrip(t *testing.T) {
	key, errGenerate := generateDeviceKey("test")
	if errGenerate != nil {
		t.Fatalf("generateDeviceKey: %v", errGenerate)
	}
	if !strings.HasPrefix(key.publicKey, "ssh-ed25519 ") {
		t.Fatalf("unexpected public key %q", key.publicKey)
	}
	if !strings.HasPrefix(key.privatePEM, "-----BEGIN OPENSSH PRIVATE KEY-----") {
		t.Fatalf("private key is not OpenSSH PEM: %q", key.privatePEM[:40])
	}
	reparsed, errParse := parseDeviceKey(key.privatePEM)
	if errParse != nil {
		t.Fatalf("parseDeviceKey: %v", errParse)
	}
	if reparsed.publicKey != key.publicKey {
		t.Fatalf("public key changed after re-parse: %q vs %q", reparsed.publicKey, key.publicKey)
	}
}

func TestSignMatchesOllamaScheme(t *testing.T) {
	key, errGenerate := generateDeviceKey("test")
	if errGenerate != nil {
		t.Fatalf("generateDeviceKey: %v", errGenerate)
	}
	now := time.Unix(1700000000, 0)
	uri := signedRequestURI("/api/usage", url.Values{"range": {"24h"}}, now)
	if uri != "/api/usage?range=24h&ts=1700000000" {
		t.Fatalf("unexpected signed request URI %q", uri)
	}
	token, errSign := key.sign("GET", uri)
	if errSign != nil {
		t.Fatalf("sign: %v", errSign)
	}
	parts := strings.SplitN(token, ":", 2)
	if len(parts) != 2 || parts[0] != key.publicKeyBody() {
		t.Fatalf("token must be <pubkey>:<signature>, got %q", token)
	}
	blob, errDecode := base64.StdEncoding.DecodeString(parts[1])
	if errDecode != nil {
		t.Fatalf("signature is not base64: %v", errDecode)
	}
	signature := &ssh.Signature{Format: key.signer.PublicKey().Type(), Blob: blob}
	if errVerify := key.signer.PublicKey().Verify([]byte("GET,/api/usage?range=24h&ts=1700000000"), signature); errVerify != nil {
		t.Fatalf("signature does not verify over the challenge: %v", errVerify)
	}
	if errVerify := key.signer.PublicKey().Verify([]byte("GET,/api/usage?ts=1700000000"), signature); errVerify == nil {
		t.Fatalf("signature unexpectedly verifies over a different challenge")
	}
}

func TestConnectURL(t *testing.T) {
	key, errGenerate := generateDeviceKey("test")
	if errGenerate != nil {
		t.Fatalf("generateDeviceKey: %v", errGenerate)
	}
	connect := key.connectURL("https://ollama.com/", "my host")
	parsed, errParse := url.Parse(connect)
	if errParse != nil {
		t.Fatalf("connect URL is invalid: %v", errParse)
	}
	if parsed.Host != "ollama.com" || parsed.Path != "/connect" {
		t.Fatalf("unexpected connect URL %q", connect)
	}
	if parsed.Query().Get("name") != "my host" {
		t.Fatalf("device name not preserved: %q", parsed.Query().Get("name"))
	}
	decoded, errDecode := base64.RawURLEncoding.DecodeString(parsed.Query().Get("key"))
	if errDecode != nil || string(decoded) != key.publicKey {
		t.Fatalf("key parameter must be raw-url base64 of the authorized_keys line, got %q (%v)", parsed.Query().Get("key"), errDecode)
	}
}

func TestNormalizePEMRestoresFlattenedKey(t *testing.T) {
	key, errGenerate := generateDeviceKey("test")
	if errGenerate != nil {
		t.Fatalf("generateDeviceKey: %v", errGenerate)
	}
	flattened := strings.Join(strings.Fields(key.privatePEM), " ")
	restored := normalizePEM(flattened)
	if _, errParse := parseDeviceKey(restored); errParse != nil {
		t.Fatalf("restored PEM does not parse: %v\n%s", errParse, restored)
	}
	inline, errInline := decodeInlineKey(base64.StdEncoding.EncodeToString([]byte(key.privatePEM)))
	if errInline != nil {
		t.Fatalf("decodeInlineKey(base64): %v", errInline)
	}
	if _, errParse := parseDeviceKey(inline); errParse != nil {
		t.Fatalf("base64-decoded PEM does not parse: %v", errParse)
	}
}
