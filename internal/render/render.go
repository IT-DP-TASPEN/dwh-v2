package render

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

type PageData struct {
	Title   string
	AppName string
	Notice  *Notice
	Data    any
}

type Renderer struct {
	files  fs.FS
	reload bool
	pages  map[string]*template.Template
}

func New(files fs.FS, reload bool) (*Renderer, error) {
	renderer := &Renderer{
		files:  files,
		reload: reload,
		pages:  make(map[string]*template.Template),
	}

	pageNames := make([]string, 0)
	for _, root := range []string{"templates/pages", "templates/features"} {
		err := fs.WalkDir(files, root, func(filePath string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || path.Ext(filePath) != ".html" || strings.HasPrefix(path.Base(filePath), "_") {
				return nil
			}
			name := strings.TrimSuffix(strings.TrimPrefix(filePath, "templates/"), ".html")
			name = strings.TrimPrefix(name, "pages/")
			pageNames = append(pageNames, name)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("find page templates in %q: %w", root, err)
		}
	}
	if len(pageNames) == 0 {
		return nil, fmt.Errorf("find page templates: no pages found")
	}

	for _, name := range pageNames {
		parsed, err := renderer.parsePage(name)
		if err != nil {
			return nil, err
		}
		if !reload {
			renderer.pages[name] = parsed
		}
	}

	return renderer, nil
}

func (r *Renderer) RenderPage(writer http.ResponseWriter, status int, page string, data any) error {
	return r.render(writer, status, page, "admin", data)
}

func (r *Renderer) RenderPageWithLayout(writer http.ResponseWriter, status int, page, layout string, data any) error {
	if layout == "" || path.Base(layout) != layout {
		return fmt.Errorf("invalid layout template %q", layout)
	}
	return r.render(writer, status, page, layout, data)
}

func (r *Renderer) RenderPartial(writer http.ResponseWriter, status int, page, name string, data any) error {
	return r.render(writer, status, page, name, data)
}

func (r *Renderer) render(writer http.ResponseWriter, status int, page, name string, data any) error {
	parsed, err := r.template(page)
	if err != nil {
		return err
	}

	var buffer bytes.Buffer
	if err := parsed.ExecuteTemplate(&buffer, name, data); err != nil {
		return fmt.Errorf("execute template %q: %w", name, err)
	}

	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(status)
	if _, err := buffer.WriteTo(writer); err != nil {
		return fmt.Errorf("write template response: %w", err)
	}
	return nil
}

func (r *Renderer) template(name string) (*template.Template, error) {
	if name == "" || !fs.ValidPath(name) || path.Ext(name) != "" {
		return nil, fmt.Errorf("invalid page template %q", name)
	}
	if !r.reload {
		parsed, ok := r.pages[name]
		if !ok {
			return nil, fmt.Errorf("unknown page template %q", name)
		}
		return parsed, nil
	}
	return r.parsePage(name)
}

func (r *Renderer) parsePage(name string) (*template.Template, error) {
	files, err := fs.Glob(r.files, "templates/layouts/*.html")
	if err != nil {
		return nil, fmt.Errorf("find layout templates: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("find layout templates: no layouts found")
	}

	for _, pattern := range []string{"templates/components/*.html", "templates/partials/*.html"} {
		matches, err := fs.Glob(r.files, pattern)
		if err != nil {
			return nil, fmt.Errorf("find templates matching %q: %w", pattern, err)
		}
		files = append(files, matches...)
	}

	pageFile := "templates/pages/" + name + ".html"
	if strings.Contains(name, "/") {
		pageFile = "templates/" + name + ".html"
	}
	if _, err := fs.Stat(r.files, pageFile); err != nil {
		return nil, fmt.Errorf("find page template %q: %w", name, err)
	}
	featurePartials, err := fs.Glob(r.files, path.Dir(pageFile)+"/_*.html")
	if err != nil {
		return nil, fmt.Errorf("find feature partials for %q: %w", name, err)
	}
	files = append(files, featurePartials...)
	files = append(files, pageFile)

	parsed, err := template.New(name).ParseFS(r.files, files...)
	if err != nil {
		return nil, fmt.Errorf("parse page template %q: %w", name, err)
	}
	return parsed, nil
}
