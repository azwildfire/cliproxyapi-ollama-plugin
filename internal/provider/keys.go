package provider

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// deviceKey is an Ollama device identity: an ed25519 SSH key whose public half is linked to an
// ollama.com account through https://ollama.com/connect. Requests are authenticated by signing
// "METHOD,/path?query&ts=UNIX" and sending "<public-key>:<signature>" as the Authorization value,
// exactly like the ollama CLI and server do.
type deviceKey struct {
	signer     ssh.Signer
	privatePEM string
	publicKey  string
}

func generateDeviceKey(comment string) (*deviceKey, error) {
	_, private, errGenerate := ed25519.GenerateKey(rand.Reader)
	if errGenerate != nil {
		return nil, fmt.Errorf("generate ed25519 key: %w", errGenerate)
	}
	block, errMarshal := ssh.MarshalPrivateKey(private, comment)
	if errMarshal != nil {
		return nil, fmt.Errorf("encode private key: %w", errMarshal)
	}
	return parseDeviceKey(string(pem.EncodeToMemory(block)))
}

func parseDeviceKey(pemText string) (*deviceKey, error) {
	pemText = strings.TrimSpace(pemText)
	if pemText == "" {
		return nil, errors.New("private key is empty")
	}
	signer, errParse := ssh.ParsePrivateKey([]byte(pemText))
	if errParse != nil {
		return nil, fmt.Errorf("parse private key: %w", errParse)
	}
	publicKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	if len(strings.Fields(publicKey)) < 2 {
		return nil, errors.New("malformed public key")
	}
	return &deviceKey{signer: signer, privatePEM: pemText + "\n", publicKey: publicKey}, nil
}

// publicKeyBody returns the base64 key material without the "ssh-ed25519" type prefix.
func (k *deviceKey) publicKeyBody() string {
	return strings.Fields(k.publicKey)[1]
}

func (k *deviceKey) fingerprint() string {
	return ssh.FingerprintSHA256(k.signer.PublicKey())
}

// connectURL is the page where the account owner approves this device key.
func (k *deviceKey) connectURL(baseURL, deviceName string) string {
	encoded := base64.RawURLEncoding.EncodeToString([]byte(k.publicKey))
	return strings.TrimRight(baseURL, "/") + "/connect?name=" + url.PathEscape(deviceName) + "&key=" + encoded
}

// sign produces the Authorization value for a request whose request URI already carries ts.
func (k *deviceKey) sign(method, requestURI string) (string, error) {
	challenge := method + "," + requestURI
	signature, errSign := k.signer.Sign(rand.Reader, []byte(challenge))
	if errSign != nil {
		return "", fmt.Errorf("sign request: %w", errSign)
	}
	return k.publicKeyBody() + ":" + base64.StdEncoding.EncodeToString(signature.Blob), nil
}

// signedRequestURI adds the timestamp and encodes the query the same way ollama does
// (url.Values.Encode sorts keys), because the server verifies the signature over this exact string.
func signedRequestURI(path string, query url.Values, now time.Time) string {
	values := url.Values{}
	for key, list := range query {
		values[key] = append([]string(nil), list...)
	}
	values.Set("ts", strconv.FormatInt(now.Unix(), 10))
	return path + "?" + values.Encode()
}
