package secretcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"io"
	"testing"
)

func TestPurposeBoundEnvelope(t *testing.T) {
	var key [32]byte
	key[0] = 7
	cipher := New(key)
	encoded, err := cipher.Encrypt(PurposeFincloudAuthPassword, 42, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if encoded[0] != versionPurposeBound {
		t.Fatalf("version=%d", encoded[0])
	}
	if got, err := cipher.Decrypt(PurposeFincloudAuthPassword, 42, encoded); err != nil || got != "secret" {
		t.Fatalf("decrypt=%q error=%v", got, err)
	}
	for _, attempt := range []struct {
		purpose Purpose
		id      uint64
	}{{PurposeReportingDatasourcePassword, 42}, {PurposeFincloudAuthPassword, 43}} {
		if _, err := cipher.Decrypt(attempt.purpose, attempt.id, encoded); err == nil {
			t.Fatalf("ciphertext accepted for purpose=%q id=%d", attempt.purpose, attempt.id)
		}
	}
}

func TestLegacyEnvelopeIsReportingOnly(t *testing.T) {
	var key [32]byte
	key[0] = 9
	encoded := legacyEnvelope(t, key, 11, "old-secret")
	cipher := New(key)
	if got, err := cipher.Decrypt(PurposeReportingDatasourcePassword, 11, encoded); err != nil || got != "old-secret" {
		t.Fatalf("decrypt=%q error=%v", got, err)
	}
	if _, err := cipher.Decrypt(PurposeFincloudAuthPassword, 11, encoded); err == nil {
		t.Fatal("Fincloud purpose accepted legacy reporting envelope")
	}
}

func legacyEnvelope(t *testing.T, key [32]byte, id uint64, plaintext string) []byte {
	t.Helper()
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatal(err)
	}
	aad := make([]byte, 8)
	binary.BigEndian.PutUint64(aad, id)
	return aead.Seal(append([]byte{versionLegacyReporting}, nonce...), nonce, []byte(plaintext), aad)
}
