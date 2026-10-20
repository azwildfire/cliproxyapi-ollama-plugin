package redact

import (
	"strings"
	"testing"
)

func TestTextRedactsSecretsAndBounds(t *testing.T) {
	out := Text(`{"error":"bad key sk-abcdefghijkl","authorization":"Bearer tokenvalue"}`, "sk-abcdefghijkl")
	if strings.Contains(out, "sk-abcdefghijkl") || strings.Contains(out, "tokenvalue") {
		t.Fatalf("secret leaked: %s", out)
	}
	long := strings.Repeat("x", 2000)
	if got := Text(long); len(got) > 520 {
		t.Fatalf("output not bounded: %d", len(got))
	}
	if Text("short", "abc") != "short" {
		t.Fatalf("short secrets must be ignored")
	}
}
