package auth

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestGenerateAndHashToken(t *testing.T) {
	first, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{first, second} {
		decoded, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			t.Fatalf("decode token: %v", err)
		}
		if len(decoded) != TokenBytes {
			t.Fatalf("expected %d bytes, got %d", TokenBytes, len(decoded))
		}
		if strings.ContainsAny(token, "+/=") {
			t.Fatalf("token is not cookie-safe: %q", token)
		}
	}

	firstHash := HashToken(first)
	if firstHash != HashToken(first) {
		t.Fatal("expected deterministic token hash")
	}
	if firstHash == HashToken(second) {
		t.Fatal("expected different token hashes")
	}
}
