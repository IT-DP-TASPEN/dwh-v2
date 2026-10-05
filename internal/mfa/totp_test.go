package mfa

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/secretcrypto"
)

func TestRFC6238SHA1Vectors(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	for _, v := range []struct {
		seconds int64
		want    string
	}{{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"}, {1234567890, "005924"}, {2000000000, "279037"}, {20000000000, "353130"}} {
		code, err := Code(secret, v.seconds/30)
		if err != nil || code != v.want {
			t.Fatalf("RFC vector %d: %s %v", v.seconds, code, err)
		}
	}
}
func TestSkewReplayAndFreshnessBoundaries(t *testing.T) {
	secret, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1800000000, 0)
	current := now.Unix() / 30
	for offset := int64(-2); offset <= 2; offset++ {
		code, _ := Code(secret, current+offset)
		counter, valid := Match(secret, code, now, -1)
		if valid != (offset >= -1 && offset <= 1) || valid && counter != current+offset {
			t.Fatalf("offset=%d counter=%d valid=%v", offset, counter, valid)
		}
		if valid {
			if _, replayed := Match(secret, code, now, counter); replayed {
				t.Fatal("replayed counter")
			}
		}
	}
	for _, age := range []time.Duration{Freshness - time.Nanosecond, Freshness, Freshness + time.Nanosecond} {
		if Recent(now.Add(-age), now) != (age <= Freshness) {
			t.Fatal("freshness boundary")
		}
	}
	if Recent(time.Time{}, now) || Recent(now.Add(time.Second), now) {
		t.Fatal("invalid freshness")
	}
}
func TestEncryptedSecretsBindIdentityAndPurpose(t *testing.T) {
	cipher := secretcrypto.New([32]byte{1})
	secret, _ := NewSecret()
	encrypted, err := cipher.Encrypt(secretcrypto.PurposeMFATOTPActive, 7, secret)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encrypted), secret) {
		t.Fatal("plaintext envelope")
	}
	plaintext, err := cipher.Decrypt(secretcrypto.PurposeMFATOTPActive, 7, encrypted)
	if err != nil || plaintext != secret {
		t.Fatal("roundtrip")
	}
	for _, v := range []struct {
		purpose secretcrypto.Purpose
		id      uint64
	}{{secretcrypto.PurposeMFATOTPActive, 8}, {secretcrypto.PurposeMFATOTPPending, 7}, {secretcrypto.PurposeReportingDatasourcePassword, 7}, {secretcrypto.PurposeFincloudAuthPassword, 7}} {
		if _, err = cipher.Decrypt(v.purpose, v.id, encrypted); err == nil {
			t.Fatal("ciphertext transplanted")
		}
	}
}
func TestRecoveryEntropyNormalizationAndIdentity(t *testing.T) {
	codes, err := NewRecoveryCodes()
	if err != nil || len(codes) != 10 {
		t.Fatal("code count", err)
	}
	seen := map[[32]byte]bool{}
	for _, code := range codes {
		raw := strings.ReplaceAll(code, "-", "")
		if len(raw) != 26 {
			t.Fatal("128-bit Base32 entropy")
		}
		hash := RecoveryHash(4, code)
		if seen[hash] {
			t.Fatal("duplicate")
		}
		seen[hash] = true
		if hash != RecoveryHash(4, strings.ToLower(raw)) || hash == RecoveryHash(5, code) {
			t.Fatal("normalization/binding")
		}
	}
}
