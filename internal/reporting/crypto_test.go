package reporting

import "testing"

func TestDatasourceCipherBindsCiphertextToDatasource(t *testing.T) {
	var key [32]byte
	key[0] = 7
	cipher := NewCipher(key)
	encoded, err := cipher.Encrypt(42, "secret")
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := cipher.Decrypt(42, encoded)
	if err != nil || plaintext != "secret" {
		t.Fatalf("plaintext=%q error=%v", plaintext, err)
	}
	if _, err := cipher.Decrypt(43, encoded); err == nil {
		t.Fatal("ciphertext accepted for another datasource")
	}
}
