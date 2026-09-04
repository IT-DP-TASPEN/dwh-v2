package secretcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
)

type Purpose string

const (
	PurposeReportingDatasourcePassword Purpose = "reporting-datasource-password"
	PurposeFincloudAuthPassword        Purpose = "fincloud-auth-profile-password"
	versionLegacyReporting             byte    = 1
	versionPurposeBound                byte    = 2
)

type Cipher struct{ key [32]byte }

func New(key [32]byte) *Cipher { return &Cipher{key: key} }

func (c *Cipher) Encrypt(purpose Purpose, recordID uint64, plaintext string) ([]byte, error) {
	if !validPurpose(purpose) || recordID == 0 {
		return nil, fmt.Errorf("secret purpose and record ID are required")
	}
	aead, err := c.aead()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate secret nonce: %w", err)
	}
	result := append([]byte{versionPurposeBound}, nonce...)
	return aead.Seal(result, nonce, []byte(plaintext), purposeAAD(purpose, recordID)), nil
}

func (c *Cipher) Decrypt(purpose Purpose, recordID uint64, encoded []byte) (string, error) {
	if !validPurpose(purpose) || recordID == 0 {
		return "", fmt.Errorf("secret purpose and record ID are required")
	}
	aead, err := c.aead()
	if err != nil {
		return "", err
	}
	if len(encoded) < 1+aead.NonceSize()+aead.Overhead() {
		return "", fmt.Errorf("unsupported secret envelope")
	}
	var aad []byte
	switch encoded[0] {
	case versionPurposeBound:
		aad = purposeAAD(purpose, recordID)
	case versionLegacyReporting:
		if purpose != PurposeReportingDatasourcePassword {
			return "", fmt.Errorf("unsupported secret envelope")
		}
		aad = legacyReportingAAD(recordID)
	default:
		return "", fmt.Errorf("unsupported secret envelope")
	}
	nonce := encoded[1 : 1+aead.NonceSize()]
	plaintext, err := aead.Open(nil, nonce, encoded[1+aead.NonceSize():], aad)
	if err != nil {
		return "", fmt.Errorf("decrypt secret: %w", err)
	}
	return string(plaintext), nil
}

func (c *Cipher) aead() (cipher.AEAD, error) {
	block, err := aes.NewCipher(c.key[:])
	if err != nil {
		return nil, fmt.Errorf("initialize secret cipher: %w", err)
	}
	return cipher.NewGCM(block)
}

func validPurpose(purpose Purpose) bool {
	return purpose == PurposeReportingDatasourcePassword || purpose == PurposeFincloudAuthPassword
}

func purposeAAD(purpose Purpose, recordID uint64) []byte {
	const domain = "new-dwh-secret\x00"
	value := make([]byte, len(domain)+len(purpose)+1+8)
	offset := copy(value, domain)
	offset += copy(value[offset:], purpose)
	offset++
	binary.BigEndian.PutUint64(value[offset:], recordID)
	return value
}

func legacyReportingAAD(recordID uint64) []byte {
	value := make([]byte, 8)
	binary.BigEndian.PutUint64(value, recordID)
	return value
}
