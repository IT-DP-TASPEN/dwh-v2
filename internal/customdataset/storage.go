package customdataset

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

type Storage struct{ root string }

func NewStorage(root string) (*Storage, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("custom dataset storage directory is required")
	}
	root = filepath.Clean(root)
	for _, directory := range []string{root, filepath.Join(root, ".tmp"), filepath.Join(root, "uploads")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("create custom dataset storage: %w", err)
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return nil, fmt.Errorf("secure custom dataset storage: %w", err)
		}
	}
	return &Storage{root: root}, nil
}

func (storage *Storage) Save(ctx context.Context, originalName string, source io.Reader) (StoredFile, error) {
	return storage.save(ctx, originalName, source, MaxUploadBytes)
}

func (storage *Storage) save(ctx context.Context, originalName string, source io.Reader, maximum int64) (StoredFile, error) {
	originalName = filepath.Base(originalName)
	if !utf8.ValidString(originalName) || utf8.RuneCountInString(originalName) > 255 {
		return StoredFile{}, fmt.Errorf("%w: CSV filename is invalid", ErrInvalid)
	}
	if !strings.EqualFold(filepath.Ext(originalName), ".csv") {
		return StoredFile{}, fmt.Errorf("%w: only .csv files are supported", ErrInvalid)
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return StoredFile{}, err
	}
	key := hex.EncodeToString(random) + ".csv"
	temporary := filepath.Join(storage.root, ".tmp", key)
	destination := filepath.Join(storage.root, "uploads", key)
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return StoredFile{}, fmt.Errorf("create custom dataset upload: %w", err)
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(temporary)
			_ = os.Remove(destination)
		}
	}()
	hash := sha256.New()
	validator := utf8Validator{}
	buffer := make([]byte, 64<<10)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return StoredFile{}, err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			size += int64(read)
			if size > maximum {
				return StoredFile{}, fmt.Errorf("%w: CSV exceeds %d bytes", ErrInvalid, maximum)
			}
			if !validator.Write(buffer[:read]) {
				return StoredFile{}, fmt.Errorf("%w: CSV contains invalid UTF-8", ErrInvalid)
			}
			if _, err := hash.Write(buffer[:read]); err != nil {
				return StoredFile{}, err
			}
			if _, err := file.Write(buffer[:read]); err != nil {
				return StoredFile{}, fmt.Errorf("write custom dataset upload: %w", err)
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return StoredFile{}, readErr
			}
			break
		}
		if read == 0 {
			return StoredFile{}, io.ErrNoProgress
		}
	}
	if !validator.Complete() {
		return StoredFile{}, fmt.Errorf("%w: CSV contains incomplete UTF-8", ErrInvalid)
	}
	if size == 0 {
		return StoredFile{}, fmt.Errorf("%w: CSV is empty", ErrInvalid)
	}
	if err := file.Sync(); err != nil {
		return StoredFile{}, err
	}
	if err := file.Close(); err != nil {
		return StoredFile{}, err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return StoredFile{}, err
	}
	if err := os.Chmod(destination, 0o600); err != nil {
		return StoredFile{}, err
	}
	remove = false
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return StoredFile{Key: key, OriginalName: originalName, Size: size, SHA256: digest}, nil
}

func (storage *Storage) Open(key string) (*os.File, error) {
	if filepath.Base(key) != key || !strings.HasSuffix(key, ".csv") {
		return nil, fmt.Errorf("invalid custom dataset storage key")
	}
	path := filepath.Join(storage.root, "uploads", key)
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func (storage *Storage) Remove(key string) error {
	if filepath.Base(key) != key {
		return fmt.Errorf("invalid custom dataset storage key")
	}
	return os.Remove(filepath.Join(storage.root, "uploads", key))
}

func (storage *Storage) Reconcile(referenced map[string]struct{}, cutoff time.Time) error {
	if err := storage.reconcileDirectory(filepath.Join(storage.root, "uploads"), referenced, cutoff); err != nil {
		return err
	}
	return storage.reconcileDirectory(filepath.Join(storage.root, ".tmp"), nil, cutoff)
}

func (storage *Storage) reconcileDirectory(directory string, referenced map[string]struct{}, cutoff time.Time) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if _, found := referenced[entry.Name()]; found {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

type utf8Validator struct{ pending []byte }

func (validator *utf8Validator) Write(chunk []byte) bool {
	data := make([]byte, 0, len(validator.pending)+len(chunk))
	data = append(data, validator.pending...)
	data = append(data, chunk...)
	validator.pending = validator.pending[:0]
	for len(data) > 0 {
		if !utf8.FullRune(data) {
			validator.pending = append(validator.pending, data...)
			return len(validator.pending) <= utf8.UTFMax-1
		}
		value, size := utf8.DecodeRune(data)
		if value == utf8.RuneError && size == 1 {
			return false
		}
		data = data[size:]
	}
	return true
}

func (validator *utf8Validator) Complete() bool { return len(validator.pending) == 0 }
