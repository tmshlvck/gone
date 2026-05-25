// Package openapi builds an OpenAPI 3 spec from Go structs and exposes
// handlers that serve it (plus a Swagger UI page).
//
// The package does not wrap route registration — callers register their
// HTTP handlers as usual on whatever router they use, and describe each
// operation separately via Spec.Add. The spec is served via JSONHandler()
// / DocsHandler() (router-agnostic) or via Mount (Go 1.22+ ServeMux
// convenience).
package openapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	oapi "github.com/swaggest/openapi-go"
	"github.com/swaggest/openapi-go/openapi3"
)

// Op describes one operation. Req/Resp use swaggest/openapi-go struct tags
// (path/query/header/json/...) to derive parameters and schemas.
type Op struct {
	Summary     string
	Description string
	Tags        []string
	Req         any
	Resp        any
}

// Spec is a thin wrapper around swaggest/openapi-go's reflector.
type Spec struct {
	refl *openapi3.Reflector
	mu   sync.Mutex
}

// NewSpec returns an empty OpenAPI 3.0.3 spec with the given title and version.
func NewSpec(title, version string) *Spec {
	r := &openapi3.Reflector{}
	r.Spec = &openapi3.Spec{Openapi: "3.0.3"}
	r.Spec.Info.WithTitle(title).WithVersion(version)
	return &Spec{refl: r}
}

// Add records one operation. path accepts either OpenAPI syntax ("/x/{id}")
// or chi/gin-style ":id"/"*splat" — both end up as {id} in the spec.
func (s *Spec) Add(method, path string, op Op) {
	s.mu.Lock()
	defer s.mu.Unlock()

	octx, err := s.refl.NewOperationContext(method, normalizePath(path))
	if err != nil {
		panic(fmt.Errorf("openapi.Add(%s %s): %w", method, path, err))
	}
	if op.Summary != "" {
		octx.SetSummary(op.Summary)
	}
	if op.Description != "" {
		octx.SetDescription(op.Description)
	}
	if len(op.Tags) > 0 {
		octx.SetTags(op.Tags...)
	}
	if op.Req != nil {
		octx.AddReqStructure(op.Req)
	}
	if op.Resp != nil {
		octx.AddRespStructure(op.Resp, func(cu *oapi.ContentUnit) { cu.HTTPStatus = 200 })
	}
	if err := s.refl.AddOperation(octx); err != nil {
		panic(fmt.Errorf("openapi.Add(%s %s): %w", method, path, err))
	}
}

// JSONHandler returns an http.Handler that serves the spec as
// application/json. Mount it however your router wants.
func (s *Spec) JSONHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		body, err := json.MarshalIndent(s.refl.Spec, "", "  ")
		s.mu.Unlock()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
}

// DocsHandler returns an http.Handler that serves a Swagger UI HTML page
// pointing at specURL (the URL where JSONHandler is mounted).
func (s *Spec) DocsHandler(specURL string) http.Handler {
	return http.HandlerFunc(swaggerUIHandler(specURL))
}

// Mount attaches the JSON spec and Swagger UI to a *http.ServeMux using
// Go 1.22 method+pattern syntax. Defaults: /openapi.json and /docs.
func (s *Spec) Mount(mux *http.ServeMux, specPath, docsPath string) {
	if specPath == "" {
		specPath = "/openapi.json"
	}
	if docsPath == "" {
		docsPath = "/docs"
	}
	mux.Handle("GET "+specPath, s.JSONHandler())
	mux.Handle("GET "+docsPath, s.DocsHandler(specPath))
}

// normalizePath converts chi/gin :name / *splat segments into OpenAPI {name}.
// OpenAPI segments and stdlib ServeMux {name} segments pass through.
func normalizePath(p string) string {
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		if len(seg) > 1 && (seg[0] == ':' || seg[0] == '*') {
			parts[i] = "{" + seg[1:] + "}"
		}
	}
	return strings.Join(parts, "/")
}

func swaggerUIHandler(specURL string) http.HandlerFunc {
	url, _ := json.Marshal(specURL)
	page := []byte(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8"/>
  <title>API docs</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css"/>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>SwaggerUIBundle({url: ` + string(url) + `, dom_id: '#swagger-ui'});</script>
</body>
</html>`)
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
	}
}
