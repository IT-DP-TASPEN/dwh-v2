package reporting

import "github.com/ibldzn/go-admin/internal/secretcrypto"

type Cipher struct{ cipher *secretcrypto.Cipher }

func NewCipher(key [32]byte) *Cipher { return &Cipher{cipher: secretcrypto.New(key)} }

func (c *Cipher) Encrypt(datasourceID uint64, plaintext string) ([]byte, error) {
	return c.cipher.Encrypt(secretcrypto.PurposeReportingDatasourcePassword, datasourceID, plaintext)
}

func (c *Cipher) Decrypt(datasourceID uint64, encoded []byte) (string, error) {
	return c.cipher.Decrypt(secretcrypto.PurposeReportingDatasourcePassword, datasourceID, encoded)
}
