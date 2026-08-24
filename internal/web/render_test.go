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

func TestRendererWritesRequestedStatus(t *testing.T) {
	templates := template.Must(template.New("pages").Parse(`{{define "page"}}ready{{end}}`))
	renderer := &Renderer{templates: templates}
	response := httptest.NewRecorder()

	renderer.RenderStatus(response, http.StatusTooManyRequests, "page", nil)

	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusTooManyRequests)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q", contentType)
	}
	if cacheControl := response.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Fatalf("Cache-Control = %q", cacheControl)
	}
}

func TestRendererNotFoundProvidesRecoveryLink(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()

	renderer.NotFound(response, NotFoundPage{
		Title:       "Word not found",
		Heading:     "Word not found",
		Message:     "This word may have been removed.",
		ReturnURL:   "/vocabulary",
		ReturnLabel: "Back to vocabulary",
	})

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
	body := response.Body.String()
	for _, expected := range []string{"Word not found", `href="/vocabulary"`, "Back to vocabulary", `class="site-header"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("not-found page does not contain %q: %s", expected, body)
		}
	}
}
