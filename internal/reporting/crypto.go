package reporting

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
)

const ciphertextVersion byte = 1

type Cipher struct{ key [32]byte }

func NewCipher(key [32]byte) *Cipher { return &Cipher{key: key} }

func (c *Cipher) Encrypt(datasourceID uint64, plaintext string) ([]byte, error) {
	aead, err := c.aead()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate password nonce: %w", err)
	}
	result := append([]byte{ciphertextVersion}, nonce...)
	return aead.Seal(result, nonce, []byte(plaintext), datasourceAAD(datasourceID)), nil
}

func (c *Cipher) Decrypt(datasourceID uint64, encoded []byte) (string, error) {
	aead, err := c.aead()
	if err != nil {
		return "", err
	}
	if len(encoded) < 1+aead.NonceSize()+aead.Overhead() || encoded[0] != ciphertextVersion {
		return "", fmt.Errorf("unsupported datasource credential envelope")
	}
	nonce := encoded[1 : 1+aead.NonceSize()]
	plaintext, err := aead.Open(nil, nonce, encoded[1+aead.NonceSize():], datasourceAAD(datasourceID))
	if err != nil {
		return "", fmt.Errorf("decrypt datasource credential: %w", err)
	}
	return string(plaintext), nil
}

func (c *Cipher) aead() (cipher.AEAD, error) {
	block, err := aes.NewCipher(c.key[:])
	if err != nil {
		return nil, fmt.Errorf("initialize datasource cipher: %w", err)
	}
	return cipher.NewGCM(block)
}

func datasourceAAD(id uint64) []byte {
	value := make([]byte, 8)
	binary.BigEndian.PutUint64(value, id)
	return value
}
