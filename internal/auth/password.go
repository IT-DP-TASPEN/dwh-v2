package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	MinPasswordCharacters = 8
	MaxPasswordBytes      = 1024

	argonMemory      = 64 * 1024
	argonIterations  = 3
	argonParallelism = 2
	argonSaltLength  = 16
	argonKeyLength   = 32
)

var ErrInvalidPasswordHash = errors.New("invalid password hash")

type argonParameters struct {
	memory      uint32
	iterations  uint32
	parallelism uint8
	salt        []byte
	hash        []byte
}

func ValidatePassword(password string) error {
	if len(password) > MaxPasswordBytes {
		return fmt.Errorf("password must be at most %d bytes", MaxPasswordBytes)
	}
	if !utf8.ValidString(password) {
		return fmt.Errorf("password must be valid UTF-8")
	}
	if utf8.RuneCountInString(password) < MinPasswordCharacters {
		return fmt.Errorf("password must be at least %d characters", MinPasswordCharacters)
	}
	return nil
}

func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}

	salt := make([]byte, argonSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonParallelism, argonKeyLength)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argonMemory,
		argonIterations,
		argonParallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

func VerifyPassword(password, encodedHash string) (bool, error) {
	if len(password) > MaxPasswordBytes {
		return false, fmt.Errorf("password must be at most %d bytes", MaxPasswordBytes)
	}
	parameters, err := decodePasswordHash(encodedHash)
	if err != nil {
		return false, err
	}
	actual := argon2.IDKey(
		[]byte(password),
		parameters.salt,
		parameters.iterations,
		parameters.memory,
		parameters.parallelism,
		uint32(len(parameters.hash)),
	)
	return subtle.ConstantTimeCompare(actual, parameters.hash) == 1, nil
}

func decodePasswordHash(encoded string) (argonParameters, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return argonParameters{}, invalidHash("format")
	}
	version, err := parsePrefixedUint(parts[2], "v=", 32)
	if err != nil || version != argon2.Version {
		return argonParameters{}, invalidHash("version")
	}

	values := strings.Split(parts[3], ",")
	if len(values) != 3 {
		return argonParameters{}, invalidHash("parameters")
	}
	memory, err := parsePrefixedUint(values[0], "m=", 32)
	if err != nil || memory == 0 || memory > 256*1024 {
		return argonParameters{}, invalidHash("memory")
	}
	iterations, err := parsePrefixedUint(values[1], "t=", 32)
	if err != nil || iterations == 0 || iterations > 10 {
		return argonParameters{}, invalidHash("iterations")
	}
	parallelism, err := parsePrefixedUint(values[2], "p=", 8)
	if err != nil || parallelism == 0 || parallelism > 16 {
		return argonParameters{}, invalidHash("parallelism")
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return argonParameters{}, invalidHash("salt")
	}
	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(hash) < 16 || len(hash) > 64 {
		return argonParameters{}, invalidHash("digest")
	}

	return argonParameters{
		memory:      uint32(memory),
		iterations:  uint32(iterations),
		parallelism: uint8(parallelism),
		salt:        salt,
		hash:        hash,
	}, nil
}

func parsePrefixedUint(value, prefix string, bits int) (uint64, error) {
	if !strings.HasPrefix(value, prefix) || len(value) == len(prefix) {
		return 0, ErrInvalidPasswordHash
	}
	return strconv.ParseUint(strings.TrimPrefix(value, prefix), 10, bits)
}

func invalidHash(part string) error {
	return fmt.Errorf("%w: invalid %s", ErrInvalidPasswordHash, part)
}
