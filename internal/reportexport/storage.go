package reportexport

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Storage struct{ root string }

func NewStorage(root string) (*Storage, error) {
	root = filepath.Clean(root)
	if root == "." || root == string(filepath.Separator) {
		return nil, fmt.Errorf("dedicated report export directory is required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, ".work"), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, "final"), 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Join(root, ".work"), 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Join(root, "final"), 0o700); err != nil {
		return nil, err
	}
	return &Storage{root: root}, nil
}

func (storage *Storage) Workspace(jobID uint64, attempt uint32) (string, error) {
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return "", err
	}
	path := filepath.Join(storage.root, ".work", strconv.FormatUint(jobID, 10), strconv.FormatUint(uint64(attempt), 10), hex.EncodeToString(token))
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", err
	}
	return path, nil
}

func (storage *Storage) Publish(jobID uint64, attempt uint32, workspaceArtifact string) (string, uint64, error) {
	workspaceArtifact = filepath.Clean(workspaceArtifact)
	if !within(filepath.Join(storage.root, ".work"), workspaceArtifact) {
		return "", 0, fmt.Errorf("artifact is outside export workspace")
	}
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return "", 0, err
	}
	directory := filepath.Join(storage.root, "final", strconv.FormatUint(jobID, 10), strconv.FormatUint(uint64(attempt), 10), hex.EncodeToString(token))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", 0, err
	}
	destination := filepath.Join(directory, filepath.Base(workspaceArtifact))
	if err := os.Rename(workspaceArtifact, destination); err != nil {
		return "", 0, err
	}
	if err := os.Chmod(destination, 0o600); err != nil {
		return "", 0, err
	}
	info, err := os.Stat(destination)
	if err != nil {
		return "", 0, err
	}
	relative, err := filepath.Rel(storage.root, destination)
	if err != nil {
		return "", 0, err
	}
	return filepath.ToSlash(relative), uint64(info.Size()), nil
}

func (storage *Storage) Open(relative string) (*os.File, error) {
	path, err := storage.path(relative)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

func (storage *Storage) Remove(relative string) error {
	path, err := storage.path(relative)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	removeEmptyParents(filepath.Dir(path), filepath.Join(storage.root, "final"))
	return nil
}

func (storage *Storage) RemoveWorkspace(path string) error {
	path = filepath.Clean(path)
	if !within(filepath.Join(storage.root, ".work"), path) {
		return fmt.Errorf("workspace is outside export storage")
	}
	return os.RemoveAll(path)
}

func (storage *Storage) ReconcileFinal(referenced map[string]struct{}, olderThan time.Time) error {
	root := filepath.Join(storage.root, "final")
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(storage.root, path)
		if err != nil {
			return err
		}
		if _, found := referenced[filepath.ToSlash(relative)]; !found && info.ModTime().Before(olderThan) {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return nil
	})
}

func (storage *Storage) CleanupWorkspaces(olderThan time.Time) error {
	root := filepath.Join(storage.root, ".work")
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() || path == root {
			return nil
		}
		relative, _ := filepath.Rel(root, path)
		if len(strings.Split(relative, string(filepath.Separator))) != 3 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().Before(olderThan) {
			if err := os.RemoveAll(path); err != nil {
				return err
			}
			return filepath.SkipDir
		}
		return nil
	})
}

func (storage *Storage) path(relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", fmt.Errorf("invalid artifact path")
	}
	path := filepath.Join(storage.root, filepath.FromSlash(relative))
	if !within(filepath.Join(storage.root, "final"), path) {
		return "", fmt.Errorf("artifact path escapes export storage")
	}
	return path, nil
}

func within(root, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func removeEmptyParents(path, stop string) {
	for within(stop, path) && path != stop {
		if err := os.Remove(path); err != nil {
			return
		}
		path = filepath.Dir(path)
	}
}
