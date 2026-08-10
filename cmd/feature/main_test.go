package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScaffold(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/project\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(unrelated, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := scaffold(root, "customers"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"routes.go", "handler.go", "permissions.go", "navigation.go"} {
		contents, err := os.ReadFile(filepath.Join(root, "internal", "features", "customers", name))
		if err != nil || !strings.Contains(string(contents), "package customers") {
			t.Fatalf("bad generated %s: %v", name, err)
		}
		if strings.Contains(string(contents), "github.com/IT-DP-TASPEN/goment") {
			t.Fatalf("generated %s ignored current module", name)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), name, contents, parser.PackageClauseOnly)
		if err != nil {
			t.Fatalf("invalid generated %s: %v", name, err)
		}
		if parsed.Name.Name != "customers" {
			t.Fatalf("generated %s package=%s", name, parsed.Name.Name)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "web", "templates", "features", "customers", "index.html")); err != nil {
		t.Fatal(err)
	}
	if err := scaffold(root, "customers"); err == nil {
		t.Fatal("existing targets were overwritten")
	}
	if contents, _ := os.ReadFile(unrelated); string(contents) != "keep" {
		t.Fatal("unrelated file changed")
	}
}

func TestValidateName(t *testing.T) {
	for _, name := range []string{"", "Customers", "customer-items", "../bad", "type", "café"} {
		if validateName(name) == nil {
			t.Fatalf("accepted invalid name %q", name)
		}
	}
	for _, name := range []string{"customers", "report2"} {
		if err := validateName(name); err != nil {
			t.Fatalf("rejected %q: %v", name, err)
		}
	}
}
