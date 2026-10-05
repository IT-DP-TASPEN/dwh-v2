// Package mfa implements mandatory browser MFA with durable, one-time challenges.
package mfa

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const ChallengeLifetime = 5 * time.Minute
const Freshness = 10 * time.Minute
const MaxFailures = 5

func Recent(verified, now time.Time) bool {
	return !verified.IsZero() && !verified.After(now) && now.Sub(verified) <= Freshness
}

func NewSecret() (string, error) {
	var b [20]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]), nil
}

// Code implements RFC 6238, SHA-1, six digits, and a 30-second period.
func Code(secret string, counter int64) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		return "", err
	}
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(counter))
	mac := hmac.New(sha1.New, key)
	mac.Write(b[:])
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 15
	value := binary.BigEndian.Uint32(digest[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1000000), nil
}

func Match(secret, input string, now time.Time, last int64) (int64, bool) {
	if len(input) != 6 {
		return 0, false
	}
	for _, ch := range input {
		if ch < '0' || ch > '9' {
			return 0, false
		}
	}
	current := now.Unix() / 30
	// Match before applying replay checks. A colliding older code never bypasses replay.
	for _, counter := range []int64{current, current - 1, current + 1} {
		expected, err := Code(secret, counter)
		if err == nil && subtle.ConstantTimeCompare([]byte(input), []byte(expected)) == 1 {
			return counter, counter > last
		}
	}
	return 0, false
}

func ProvisioningURI(issuer, username, secret string) string {
	return "otpauth://totp/" + url.PathEscape(issuer+":"+username) + "?" + url.Values{"secret": {secret}, "issuer": {issuer}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}.Encode()
}

func NewRecoveryCodes() ([]string, error) {
	codes := make([]string, 10)
	for i := range codes {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, err
		}
		s := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
		codes[i] = s[:5] + "-" + s[5:10] + "-" + s[10:15] + "-" + s[15:20] + "-" + s[20:]
	}
	return codes, nil
}

func RecoveryHash(userID uint64, input string) [32]byte {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(input), "-", ""))
	return sha256.Sum256([]byte(fmt.Sprintf("mfa-recovery:%d:%s", userID, normalized)))
}
