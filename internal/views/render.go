package views

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"mini-aigw/internal/models"
	"mini-aigw/webassets"
)

type Renderer struct {
	fs        fs.FS
	funcMap   template.FuncMap
	templates map[string]*template.Template
}

var GlobalRenderer *Renderer

func InitRenderer() (*Renderer, error) {
	tfs := webassets.TemplateFS()

	funcMap := template.FuncMap{
		"hasRole": func(u *models.User, role string) bool {
			if u == nil {
				return false
			}
			return u.HasRole(role)
		},
		"hasGroup": func(u *models.User, group string) bool {
			if u == nil {
				return false
			}
			for _, g := range u.GetGroups() {
				if g == group {
					return true
				}
			}
			return false
		},
		"formatTime": func(v interface{}) string {
			if v == nil {
				return "-"
			}
			switch t := v.(type) {
			case time.Time:
				if t.IsZero() {
					return "-"
				}
				return t.In(time.Local).Format("2006-01-02 15:04:05")
			case *time.Time:
				if t == nil || t.IsZero() {
					return "-"
				}
				return t.In(time.Local).Format("2006-01-02 15:04:05")
			}
			return fmt.Sprintf("%v", v)
		},
		"formatDate": func(v interface{}) string {
			if v == nil {
				return "-"
			}
			switch t := v.(type) {
			case time.Time:
				if t.IsZero() {
					return "-"
				}
				return t.In(time.Local).Format("2006-01-02")
			case *time.Time:
				if t == nil || t.IsZero() {
					return "-"
				}
				return t.In(time.Local).Format("2006-01-02")
			}
			return fmt.Sprintf("%v", v)
		},
		"formatSize": func(v interface{}) string {
			var b int64
			switch val := v.(type) {
			case int:
				b = int64(val)
			case int64:
				b = val
			default:
				return "0 B"
			}
			const unit = 1024
			if b < unit {
				return fmt.Sprintf("%d B", b)
			}
			div, exp := int64(unit), 0
			for n := b / unit; n >= unit; n /= unit {
				div *= unit
				exp++
			}
			return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
		},
		"formatScore": func(s float64) string {
			return fmt.Sprintf("%.2f", s)
		},
		"sliceContains": func(slice []string, val string) bool {
			for _, s := range slice {
				if s == val {
					return true
				}
			}
			return false
		},
		"safeHTML": func(s string) template.HTML {
			return template.HTML(s)
		},
		"toJSON": func(v interface{}) string {
			b, _ := json.Marshal(v)
			return string(b)
		},
		"splitTags": func(tagsStr *string) []string {
			if tagsStr == nil || *tagsStr == "" {
				return nil
			}
			var tags []string
			if err := json.Unmarshal([]byte(*tagsStr), &tags); err == nil {
				return tags
			}
			parts := strings.Split(*tagsStr, ",")
			var clean []string
			for _, p := range parts {
				trimmed := strings.TrimSpace(p)
				if trimmed != "" {
					clean = append(clean, trimmed)
				}
			}
			return clean
		},
	}

	r := &Renderer{
		fs:        tfs,
		funcMap:   funcMap,
		templates: make(map[string]*template.Template),
	}

	// Pre-compile templates
	pages := []string{
		"dashboard.html",
		"knowledge.html",
		"rag_test.html",
		"api_keys.html",
		"models.html",
		"users.html",
		"login.html",
	}

	for _, page := range pages {
		tmpl, err := template.New("base").Funcs(funcMap).ParseFS(tfs, "base.html", page, "partials/*.html")
		if err != nil {
			return nil, fmt.Errorf("failed to parse template %s: %w", page, err)
		}
		r.templates[page] = tmpl
	}

	// Compile partials standalone
	partials, err := fs.Glob(tfs, "partials/*.html")
	if err == nil {
		for _, p := range partials {
			baseName := strings.TrimPrefix(p, "partials/")
			tmpl, err := template.New(baseName).Funcs(funcMap).ParseFS(tfs, p)
			if err == nil {
				r.templates[p] = tmpl
				r.templates[baseName] = tmpl
			}
		}
	}

	GlobalRenderer = r
	return r, nil
}

func (r *Renderer) Render(w http.ResponseWriter, pageName string, data interface{}) error {
	tmpl, ok := r.templates[pageName]
	if !ok {
		return fmt.Errorf("template %s not found", pageName)
	}

	var buf bytes.Buffer
	// Check if this is a full page with base layout or a standalone partial
	executeName := "base"
	if strings.HasPrefix(pageName, "partials/") || !strings.Contains(pageName, ".html") || pageName == "login.html" {
		executeName = ""
	}

	var err error
	if executeName != "" {
		err = tmpl.ExecuteTemplate(&buf, executeName, data)
	} else {
		err = tmpl.Execute(&buf, data)
	}

	if err != nil {
		return fmt.Errorf("render error for %s: %w", pageName, err)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err = io.Copy(w, &buf)
	return err
}

func (r *Renderer) RenderPartial(w http.ResponseWriter, partialName string, data interface{}) error {
	tmpl, ok := r.templates[partialName]
	if !ok {
		// try with partials/ prefix
		tmpl, ok = r.templates["partials/"+partialName]
		if !ok {
			return fmt.Errorf("partial %s not found", partialName)
		}
	}

	var buf bytes.Buffer
	var err error
	baseName := partialName
	if strings.Contains(baseName, "/") {
		baseName = baseName[strings.LastIndex(baseName, "/")+1:]
	}
	err = tmpl.ExecuteTemplate(&buf, baseName, data)
	if err != nil {
		err = tmpl.Execute(&buf, data)
	}
	if err != nil {
		return fmt.Errorf("render partial error for %s: %w", partialName, err)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err = io.Copy(w, &buf)
	return err
}
