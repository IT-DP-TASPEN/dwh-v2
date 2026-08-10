package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type pendingFile struct {
	path string
	data []byte
	mode fs.FileMode
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("rename-module", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	target := flags.String("module", "", "new Go module path")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || *target == "" {
		return fmt.Errorf("usage: rename-module -module github.com/example/project")
	}
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("find project root: %w", err)
	}
	changed, err := renameModule(root, *target)
	if err != nil {
		return err
	}
	for _, path := range changed {
		_, _ = fmt.Fprintln(output, path)
	}
	return nil
}

func renameModule(root, target string) ([]string, error) {
	if err := validateModulePath(target); err != nil {
		return nil, err
	}
	moduleFile := filepath.Join(root, "go.mod")
	moduleData, err := os.ReadFile(moduleFile)
	if err != nil {
		return nil, fmt.Errorf("read go.mod: %w", err)
	}
	current, start, end, err := moduleDirective(moduleData)
	if err != nil {
		return nil, err
	}
	if current == target {
		return nil, nil
	}

	pending := make([]pendingFile, 0)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		slashPath := filepath.ToSlash(relative)
		if entry.IsDir() {
			if slashPath != "." && skippedDirectory(slashPath) {
				return filepath.SkipDir
			}
			return nil
		}

		var updated []byte
		switch {
		case slashPath == "go.mod":
			updated = replaceBytes(moduleData, start, end, []byte(target))
		case strings.HasSuffix(entry.Name(), ".go"):
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read %s: %w", slashPath, err)
			}
			updated, err = replaceGoImports(path, data, current, target)
			if err != nil {
				return err
			}
			if bytes.Equal(data, updated) {
				return nil
			}
		case strings.HasSuffix(entry.Name(), ".md") || strings.HasSuffix(entry.Name(), ".markdown"):
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read %s: %w", slashPath, err)
			}
			updated = replaceDocumentModule(data, current, target)
			if bytes.Equal(data, updated) {
				return nil
			}
		default:
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect %s: %w", slashPath, err)
		}
		pending = append(pending, pendingFile{path: path, data: updated, mode: info.Mode()})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("prepare module rename: %w", err)
	}

	changed := make([]string, 0, len(pending))
	for _, file := range pending {
		if err := writeAtomic(file.path, file.data, file.mode); err != nil {
			return nil, err
		}
		relative, _ := filepath.Rel(root, file.path)
		changed = append(changed, filepath.ToSlash(relative))
	}
	sort.Strings(changed)
	return changed, nil
}

func validateModulePath(path string) error {
	if path == "" || path != strings.TrimSpace(path) || len(path) > 255 || strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") || strings.Contains(path, "//") {
		return fmt.Errorf("invalid module path %q", path)
	}
	segments := strings.Split(path, "/")
	if len(segments) < 2 || !strings.Contains(segments[0], ".") {
		return fmt.Errorf("invalid module path %q: expected a dotted host and path", path)
	}
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." || strings.HasPrefix(segment, ".") || strings.HasSuffix(segment, ".") {
			return fmt.Errorf("invalid module path %q", path)
		}
		for _, character := range segment {
			if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || strings.ContainsRune("-._~", character) {
				continue
			}
			return fmt.Errorf("invalid module path %q: use lowercase ASCII letters, digits, hyphens, dots, underscores, or tildes", path)
		}
	}
	return nil
}

func moduleDirective(data []byte) (string, int, int, error) {
	offset := 0
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if bytes.HasPrefix(trimmed, []byte("module")) {
			fields := bytes.Fields(trimmed)
			if len(fields) != 2 || string(fields[0]) != "module" {
				return "", 0, 0, fmt.Errorf("go.mod has malformed module directive")
			}
			value := string(fields[1])
			index := bytes.Index(line, fields[1])
			return value, offset + index, offset + index + len(fields[1]), nil
		}
		offset += len(line)
	}
	return "", 0, 0, fmt.Errorf("go.mod has no module directive")
}

func replaceGoImports(filename string, data []byte, current, target string) ([]byte, error) {
	set := token.NewFileSet()
	parsed, err := parser.ParseFile(set, filename, data, parser.ImportsOnly)
	if err != nil {
		return nil, fmt.Errorf("parse Go imports in %s: %w", filename, err)
	}
	type edit struct {
		start int
		end   int
		value string
	}
	edits := make([]edit, 0)
	for _, declaration := range parsed.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.IMPORT {
			continue
		}
		for _, specification := range general.Specs {
			importSpec := specification.(*ast.ImportSpec)
			value, err := strconv.Unquote(importSpec.Path.Value)
			if err != nil {
				return nil, fmt.Errorf("decode import in %s: %w", filename, err)
			}
			if value != current && !strings.HasPrefix(value, current+"/") {
				continue
			}
			start := set.Position(importSpec.Path.Pos()).Offset
			end := set.Position(importSpec.Path.End()).Offset
			edits = append(edits, edit{start: start, end: end, value: strconv.Quote(target + strings.TrimPrefix(value, current))})
		}
	}
	updated := append([]byte(nil), data...)
	for index := len(edits) - 1; index >= 0; index-- {
		updated = replaceBytes(updated, edits[index].start, edits[index].end, []byte(edits[index].value))
	}
	return updated, nil
}

func replaceDocumentModule(data []byte, current, target string) []byte {
	var output bytes.Buffer
	for offset := 0; offset < len(data); {
		index := bytes.Index(data[offset:], []byte(current))
		if index < 0 {
			output.Write(data[offset:])
			break
		}
		index += offset
		end := index + len(current)
		beforeOK := index == 0 || !moduleCharacter(data[index-1])
		afterOK := end == len(data) || data[end] == '/' || !moduleCharacter(data[end])
		if beforeOK && afterOK {
			output.Write(data[offset:index])
			output.WriteString(target)
			offset = end
			continue
		}
		output.Write(data[offset : index+1])
		offset = index + 1
	}
	return output.Bytes()
}

func moduleCharacter(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("-._~!/", rune(character))
}

func skippedDirectory(path string) bool {
	if path == ".git" || path == "node_modules" || path == "bin" || path == "tmp" || path == "web/static" {
		return true
	}
	return strings.HasPrefix(path, ".git/") || strings.HasPrefix(path, "node_modules/") || strings.HasPrefix(path, "bin/") || strings.HasPrefix(path, "tmp/") || strings.HasPrefix(path, "web/static/")
}

func replaceBytes(data []byte, start, end int, replacement []byte) []byte {
	updated := make([]byte, 0, len(data)-(end-start)+len(replacement))
	updated = append(updated, data[:start]...)
	updated = append(updated, replacement...)
	updated = append(updated, data[end:]...)
	return updated
}

func writeAtomic(path string, data []byte, mode fs.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".rename-module-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", path, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode.Perm()); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("preserve mode for %s: %w", path, err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary file for %s: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary file for %s: %w", path, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
