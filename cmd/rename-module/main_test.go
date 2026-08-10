package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRenameModuleUpdatesExactSourcesOnly(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "go.mod", "module github.com/old/project\n\ngo 1.26\n")
	writeTestFile(t, root, "main.go", "package main\nimport (\n\t\"github.com/old/project/internal/app\"\n\tother \"github.com/old/projectish/pkg\"\n)\n")
	writeTestFile(t, root, "README.md", "Use `github.com/old/project` or github.com/old/project/cmd/app. Keep github.com/old/projectish.\n")
	writeTestFile(t, root, "web/static/js/app.js", "github.com/old/project")
	writeTestFile(t, root, "node_modules/pkg/file.go", "package pkg\nimport \"github.com/old/project/internal/app\"\n")
	writeTestFile(t, root, ".git/config", "github.com/old/project")
	writeTestFile(t, root, "data.bin", "\x00github.com/old/project\x00")

	changed, err := renameModule(root, "example.com/new/starter")
	if err != nil {
		t.Fatal(err)
	}
	wantChanged := []string{"README.md", "go.mod", "main.go"}
	if !reflect.DeepEqual(changed, wantChanged) {
		t.Fatalf("changed files=%v, want %v", changed, wantChanged)
	}
	assertTestFile(t, root, "go.mod", "module example.com/new/starter\n\ngo 1.26\n")
	mainSource := readTestFile(t, root, "main.go")
	if !strings.Contains(mainSource, `"example.com/new/starter/internal/app"`) || !strings.Contains(mainSource, `"github.com/old/projectish/pkg"`) {
		t.Fatalf("imports were not replaced exactly:\n%s", mainSource)
	}
	readme := readTestFile(t, root, "README.md")
	if strings.Count(readme, "example.com/new/starter") != 2 || !strings.Contains(readme, "github.com/old/projectish") {
		t.Fatalf("documentation replacement was not boundary-safe: %s", readme)
	}
	for _, path := range []string{"web/static/js/app.js", "node_modules/pkg/file.go", ".git/config", "data.bin"} {
		if !strings.Contains(readTestFile(t, root, path), "github.com/old/project") {
			t.Fatalf("excluded file %s changed", path)
		}
	}
}

func TestRenameModuleRejectsMalformedTargetWithoutChanges(t *testing.T) {
	for _, target := range []string{"", "local", "Example.com/project", "example.com//project", "example.com/project@v1", " example.com/project", "example.com/../project"} {
		t.Run(strings.ReplaceAll(target, "/", "_"), func(t *testing.T) {
			root := t.TempDir()
			original := "module github.com/old/project\n"
			writeTestFile(t, root, "go.mod", original)
			if _, err := renameModule(root, target); err == nil {
				t.Fatalf("malformed target %q accepted", target)
			}
			assertTestFile(t, root, "go.mod", original)
		})
	}
}

func TestRenameModuleNoop(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "go.mod", "module example.com/project\n")
	changed, err := renameModule(root, "example.com/project")
	if err != nil || len(changed) != 0 {
		t.Fatalf("same-module rename: changed=%v err=%v", changed, err)
	}
}

func writeTestFile(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o640); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, root, relative string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertTestFile(t *testing.T, root, relative, want string) {
	t.Helper()
	if got := readTestFile(t, root, relative); got != want {
		t.Fatalf("%s=%q, want %q", relative, got, want)
	}
}
