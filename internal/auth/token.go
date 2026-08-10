package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

const TokenBytes = 32

func GenerateToken() (string, error) {
	random := make([]byte, TokenBytes)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(random), nil
}

func HashToken(token string) [sha256.Size]byte {
	return sha256.Sum256([]byte(token))
}
