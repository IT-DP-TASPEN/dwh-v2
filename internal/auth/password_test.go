package auth

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

func TestPasswordHashAndVerify(t *testing.T) {
	password := "correct horse battery staple"
	first, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("expected random salts to produce different hashes")
	}

	allowed, err := VerifyPassword(password, first)
	if err != nil || !allowed {
		t.Fatalf("expected password verification, got allowed=%v err=%v", allowed, err)
	}
	allowed, err = VerifyPassword("incorrect password value", first)
	if err != nil || allowed {
		t.Fatalf("expected incorrect password rejection, got allowed=%v err=%v", allowed, err)
	}
}

func TestMalformedPasswordHash(t *testing.T) {
	if _, err := VerifyPassword("correct horse battery staple", "$argon2id$broken"); !errors.Is(err, ErrInvalidPasswordHash) {
		t.Fatalf("expected controlled malformed-hash error, got %v", err)
	}
}

func TestPasswordHashParameterParsing(t *testing.T) {
	salt := base64.RawStdEncoding.EncodeToString([]byte("0123456789abcdef"))
	digest := base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	encoded := fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=2$%s$%s", salt, digest)
	parameters, err := decodePasswordHash(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if parameters.memory != 65536 || parameters.iterations != 3 || parameters.parallelism != 2 {
		t.Fatalf("unexpected parameters: %+v", parameters)
	}
}

func TestValidatePassword(t *testing.T) {
	if err := ValidatePassword(strings.Repeat("a", MinPasswordCharacters-1)); err == nil {
		t.Fatal("expected short password error")
	}
	if err := ValidatePassword(strings.Repeat("a", MinPasswordCharacters)); err != nil {
		t.Fatalf("expected minimum length to pass: %v", err)
	}
	if err := ValidatePassword(strings.Repeat("a", MaxPasswordBytes+1)); err == nil {
		t.Fatal("expected oversized password error")
	}
}

func TestVerifyPasswordDoesNotApplyCreationMinimum(t *testing.T) {
	password := "01234"
	salt := []byte("0123456789abcdef")
	digest := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonParallelism, argonKeyLength)
	encoded := fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory,
		argonIterations,
		argonParallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(digest),
	)

	if err := ValidatePassword(password); err == nil {
		t.Fatal("expected creation policy to reject historical short password")
	}
	verified, err := VerifyPassword(password, encoded)
	if err != nil || !verified {
		t.Fatalf("expected historical hash verification, got verified=%v err=%v", verified, err)
	}
	if _, err := VerifyPassword(strings.Repeat("a", MaxPasswordBytes+1), encoded); err == nil {
		t.Fatal("expected verification input size bound")
	}
}
