package web

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewRendererParsesTemplates(t *testing.T) {
	if _, err := NewRenderer(); err != nil {
		t.Fatalf("parse templates: %v", err)
	}
}

func TestRendererDoesNotWritePartialTemplateErrors(t *testing.T) {
	templates := template.Must(template.New("pages").Parse(`{{define "broken"}}partial output{{.Missing}}{{end}}`))
	renderer := &Renderer{templates: templates}
	response := httptest.NewRecorder()

	renderer.Render(response, "broken", 1)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if body := response.Body.String(); strings.Contains(body, "partial output") {
		t.Fatalf("response contains partial template output: %q", body)
	}
}
