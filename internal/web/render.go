package web

import (
	"bytes"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"

	"github.com/justinas/nosurf"

	appweb "github.com/tomasmik/goi/web"
)

type Renderer struct {
	templates *template.Template
}

type NotFoundPage struct {
	Title       string
	Heading     string
	Message     string
	ReturnURL   string
	ReturnLabel string
}

func percentage(part, total int) int {
	if total <= 0 {
		return 0
	}
	return part * 100 / total
}

func NewRenderer() (*Renderer, error) {
	templates, err := template.New("pages").Funcs(template.FuncMap{
		"add":     func(left, right int) int { return left + right },
		"percent": percentage,
	}).ParseFS(appweb.TemplateFiles, "templates/layouts/*.html", "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return &Renderer{templates: templates}, nil
}

func CSRFToken(r *http.Request) string {
	return nosurf.Token(r)
}

func (r *Renderer) Render(w http.ResponseWriter, name string, data any) {
	r.RenderStatus(w, http.StatusOK, name, data)
}

func (r *Renderer) NotFound(w http.ResponseWriter, page NotFoundPage) {
	r.RenderStatus(w, http.StatusNotFound, "not-found.html", page)
}

func (r *Renderer) RenderStatus(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("Cache-Control", "no-store")
	var page bytes.Buffer
	if err := r.templates.ExecuteTemplate(&page, name, data); err != nil {
		slog.Error("render template", "template", name, "error", err)
		http.Error(w, "could not display this page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	if _, err := page.WriteTo(w); err != nil {
		slog.Error("write rendered template", "template", name, "error", err)
	}
}
