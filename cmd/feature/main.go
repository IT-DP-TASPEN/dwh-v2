package main

import (
	"errors"
	"flag"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	name := flag.String("name", "", "lowercase feature name")
	root := flag.String("root", ".", "project root")
	flag.Parse()
	if err := scaffold(*root, *name); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("Created feature %q. Next: add it to internal/app/features.go, then run make fmt and make test.\n", *name)
}

func scaffold(root, name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	module, err := projectModule(root)
	if err != nil {
		return err
	}
	featureDir := filepath.Join(root, "internal", "features", name)
	templateDir := filepath.Join(root, "web", "templates", "features", name)
	for _, target := range []string{featureDir, templateDir} {
		if _, err := os.Stat(target); err == nil {
			return fmt.Errorf("target already exists: %s", target)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect target %s: %w", target, err)
		}
	}
	if err := os.MkdirAll(featureDir, 0o755); err != nil {
		return fmt.Errorf("create feature directory: %w", err)
	}
	if err := os.MkdirAll(templateDir, 0o755); err != nil {
		return fmt.Errorf("create template directory: %w", err)
	}
	files := map[string]string{
		filepath.Join(featureDir, "permissions.go"): permissionsSource(name, module),
		filepath.Join(featureDir, "navigation.go"):  navigationSource(name, module),
		filepath.Join(featureDir, "handler.go"):     handlerSource(name, module),
		filepath.Join(featureDir, "routes.go"):      routesSource(name),
		filepath.Join(templateDir, "index.html"):    templateSource(name),
	}
	for path, contents := range files {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return fmt.Errorf("create %s: %w", path, err)
		}
		if _, err := file.WriteString(contents); err != nil {
			file.Close()
			return fmt.Errorf("write %s: %w", path, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close %s: %w", path, err)
		}
	}
	return nil
}

func projectModule(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("go.mod has no module directive")
}

func validateName(name string) error {
	if name == "" || token.Lookup(name).IsKeyword() || name[0] < 'a' || name[0] > 'z' {
		return fmt.Errorf("name must match [a-z][a-z0-9]* and not be a Go keyword")
	}
	for _, character := range name[1:] {
		if character < 'a' || character > 'z' {
			if character < '0' || character > '9' {
				return fmt.Errorf("name must match [a-z][a-z0-9]* and not be a Go keyword")
			}
		}
	}
	return nil
}

func permissionsSource(name, module string) string {
	title := title(name)
	return fmt.Sprintf(`package %s

import %q

const PermissionView = %q

func PermissionDefinitions() []access.PermissionDefinition {
	return []access.PermissionDefinition{{Key: PermissionView, Name: %q, Group: %q, Description: %q}}
}
`, name, module+"/internal/access", name+".view", "View "+title, title, "View "+name+".")
}

func navigationSource(name, module string) string {
	return fmt.Sprintf(`package %s

import %q

func Navigation() navigation.Item {
	return navigation.Item{Key: %q, Label: %q, Path: %q, Permission: PermissionView, Match: navigation.MatchPrefix}
}
`, name, module+"/internal/platform/navigation", name, title(name), "/"+name)
}

func handlerSource(name, module string) string {
	return fmt.Sprintf(`package %s

import (
	"net/http"

	%q
)

type Handler struct{ admin *adminshell.Shell }

func NewHandler(admin *adminshell.Shell) *Handler { return &Handler{admin: admin} }

func (handler *Handler) Index(writer http.ResponseWriter, request *http.Request) {
	handler.admin.RenderPage(writer, request, http.StatusOK, %q, %q, nil)
}
`, name, module+"/internal/platform/adminshell", "features/"+name+"/index", title(name))
}

func routesSource(name string) string {
	return fmt.Sprintf(`package %s

import "github.com/go-chi/chi/v5"

func (handler *Handler) RegisterRoutes(router chi.Router) {
	router.With(handler.admin.RequirePermission(PermissionView)).Get(%q, handler.Index)
}
`, name, "/"+name)
}

func templateSource(name string) string {
	nameTitle := title(name)
	return fmt.Sprintf(`{{define "content"}}
<section>
    <div class="mb-6 flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div>
            <p class="text-sm font-medium text-emerald-600 dark:text-emerald-400">%s</p>
            <h1 class="mt-1 text-3xl font-bold tracking-tight text-slate-950 dark:text-white">%s</h1>
            <p class="mt-2 text-slate-600 dark:text-slate-400">Description</p>
        </div>
    </div>
</section>
{{end}}`,
		nameTitle,
		nameTitle,
	)
}

func title(name string) string {
	return strings.ToUpper(name[:1]) + name[1:]
}
