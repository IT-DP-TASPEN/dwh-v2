package customdataset

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorageRetainsExactImmutableBytes(t *testing.T) {
	storage, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	content := "\ufeffname,value\r\na,1\r\n"
	stored, err := storage.Save(context.Background(), "report.CSV", strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if stored.Size != int64(len(content)) || stored.SHA256 != sha256.Sum256([]byte(content)) {
		t.Fatalf("metadata=%+v", stored)
	}
	file, err := storage.Open(stored.Key)
	if err != nil {
		t.Fatal(err)
	}
	bytes, _ := os.ReadFile(file.Name())
	_ = file.Close()
	if string(bytes) != content {
		t.Fatal("retained bytes changed")
	}
	info, _ := os.Stat(storage.root)
	fileInfo, _ := os.Stat(file.Name())
	if info.Mode().Perm() != 0o700 || fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("modes root=%o file=%o", info.Mode().Perm(), fileInfo.Mode().Perm())
	}
}

func TestStorageLimitsUTF8AndPaths(t *testing.T) {
	if _, err := NewStorage(""); err == nil {
		t.Fatal("empty directory accepted")
	}
	storage, _ := NewStorage(t.TempDir())
	if _, err := storage.Save(context.Background(), "data.txt", strings.NewReader("x")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("extension error=%v", err)
	}
	if _, err := storage.Save(context.Background(), strings.Repeat("a", 252)+".csv", strings.NewReader("x")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("filename error=%v", err)
	}
	if _, err := storage.save(context.Background(), "data.csv", strings.NewReader("12345"), 4); !errors.Is(err, ErrInvalid) {
		t.Fatalf("limit error=%v", err)
	}
	if _, err := storage.Save(context.Background(), "data.csv", strings.NewReader(string([]byte{0xe2, 0x82}))); !errors.Is(err, ErrInvalid) {
		t.Fatalf("split rune error=%v", err)
	}
	if _, err := storage.Open("../secret.csv"); err == nil {
		t.Fatal("path traversal accepted")
	}
	validator := utf8Validator{}
	if !validator.Write([]byte{0xe2}) || !validator.Write([]byte{0x82, 0xac}) || !validator.Complete() {
		t.Fatal("valid split rune rejected")
	}
}

func TestStorageReconcileKeepsReferencedUploads(t *testing.T) {
	storage, _ := NewStorage(t.TempDir())
	kept, _ := storage.Save(context.Background(), "kept.csv", strings.NewReader("a\n"))
	orphan, _ := storage.Save(context.Background(), "orphan.csv", strings.NewReader("b\n"))
	temporary := filepath.Join(storage.root, ".tmp", "interrupted.csv")
	if err := os.WriteFile(temporary, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(filepath.Join(storage.root, "uploads", orphan.Key), old, old)
	_ = os.Chtimes(temporary, old, old)
	if err := storage.Reconcile(map[string]struct{}{kept.Key: {}}, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Open(kept.Key); err != nil {
		t.Fatal("referenced upload removed")
	}
	if _, err := storage.Open(orphan.Key); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan error=%v", err)
	}
	if _, err := os.Stat(temporary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary error=%v", err)
	}
}
