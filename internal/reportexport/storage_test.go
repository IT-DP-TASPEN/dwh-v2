package reportexport

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoragePublishesOpaqueAttemptPathsAndRemovesOrphans(t *testing.T) {
	storage, err := NewStorage(filepath.Join(t.TempDir(), "exports"))
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := storage.Workspace(7, 2)
	if err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(workspace, "report.xlsx")
	if err := os.WriteFile(artifact, []byte("xlsx"), 0o600); err != nil {
		t.Fatal(err)
	}
	relative, size, err := storage.Publish(7, 2, artifact)
	if err != nil {
		t.Fatal(err)
	}
	if size != 4 {
		t.Fatalf("size=%d", size)
	}
	secondWorkspace, err := storage.Workspace(7, 2)
	if err != nil {
		t.Fatal(err)
	}
	secondArtifact := filepath.Join(secondWorkspace, "report.xlsx")
	if err := os.WriteFile(secondArtifact, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	secondRelative, _, err := storage.Publish(7, 2, secondArtifact)
	if err != nil {
		t.Fatal(err)
	}
	if secondRelative == relative {
		t.Fatal("attempt artifact path was not opaque")
	}
	path, err := storage.path(relative)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := storage.ReconcileFinal(map[string]struct{}{relative: {}}, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("referenced artifact removed: %v", err)
	}
	if err := storage.ReconcileFinal(map[string]struct{}{}, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("orphan artifact remains: %v", err)
	}
}
