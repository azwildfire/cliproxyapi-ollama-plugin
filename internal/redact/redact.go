// Package redact bounds upstream error text and strips credential material from it.
package redact

import (
	"regexp"
	"strings"
)

const maxErrorBytes = 512

var authorizationPattern = regexp.MustCompile(`(?i)(authorization["']?\s*[:=]\s*["']?)(bearer\s+)?[^\s"',]+`)

// ErrorBody returns a bounded, redacted copy of an upstream error body.
func ErrorBody(body []byte, secrets ...string) string {
	return Text(string(body), secrets...)
}

// Text redacts known secrets and authorization values from free text and bounds its length.
func Text(text string, secrets ...string) string {
	text = strings.TrimSpace(text)
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if len(secret) < 8 {
			continue
		}
		text = strings.ReplaceAll(text, secret, "[redacted]")
	}
	text = authorizationPattern.ReplaceAllString(text, "${1}[redacted]")
	if len(text) > maxErrorBytes {
		text = text[:maxErrorBytes] + "..."
	}
	return text
}
