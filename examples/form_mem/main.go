// Example: edit a single in-memory struct with MetaModel's render/bind
// primitives — RenderDisplay, RenderForm, TryBindForm — and app-owned
// routes. Shows per-field validators, a cross-field MetaModel.Validate
// (MaxRequests must exceed Port), and the HTMX swap between dump and form.
package main

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/tmshlvck/gone/crud"
)

type ExampleConfig struct {
	Hostname    string
	BindAddress string
	Port        int
	EnableTLS   bool
	MaxRequests uint64
	Threshold   float64
	StartTime   time.Time
	AdminEmail  string
}

var (
	cfg = ExampleConfig{
		Hostname:    "server01.example.com",
		BindAddress: "10.0.0.7",
		Port:        8443,
		EnableTLS:   true,
		MaxRequests: 100_000,
		Threshold:   0.85,
		StartTime:   time.Date(2026, 5, 25, 9, 30, 0, 0, time.UTC),
		AdminEmail:  "admin@example.com",
	}
	mu sync.RWMutex
)

const (
	formURL  = "/edit"
	hxTarget = "#main-content"
)

func main() {
	// All per-field metadata is declared once in the preset; DeriveMetaModel
	// reflects ExampleConfig and overlays it (panicking on a typo'd field).
	mm := crud.DeriveMetaModel[ExampleConfig](crud.MetaModel[ExampleConfig]{
		DisplayName: "Server configuration",
		Fields: []crud.MetaField{
			{Name: "Hostname", FormHelp: "FQDN or short host, 1–253 chars.", FieldValidate: crud.All(crud.NotEmpty, crud.MaxLen(253))},
			{Name: "BindAddress", DisplayName: "Bind address", FormHelp: "IPv4 or IPv6 the server listens on.", FieldValidate: crud.All(crud.NotEmpty, crud.Any(crud.IPv4Addr, crud.IPv6Addr))},
			{Name: "Port", FormHelp: "TCP port, 1–65535.", FieldValidate: crud.IntRange(1, 65535)},
			{Name: "EnableTLS", DisplayName: "TLS enabled"},
			{Name: "MaxRequests", DisplayName: "Max requests", FormHelp: "Concurrent request cap, must exceed the port number.", FieldValidate: crud.IntRange(1, 10_000_000)},
			{Name: "Threshold", FormHelp: "CPU load shed threshold, 0.0–1.0.", FieldValidate: crud.FloatRange(0.0, 1.0)},
			{Name: "StartTime", DisplayName: "Start time"},
			{Name: "AdminEmail", DisplayName: "Admin email", FormInputType: "email", FieldValidate: crud.All(crud.NotEmpty, crud.Email)},
		},
		// Cross-field rule: MaxRequests must exceed Port (arbitrary, easy to
		// violate in the demo).
		Validate: func(c ExampleConfig) error {
			if c.MaxRequests <= uint64(c.Port) {
				return fmt.Errorf("Max requests (%d) must be greater than Port (%d)", c.MaxRequests, c.Port)
			}
			return nil
		},
	})

	get := func() ExampleConfig { mu.RLock(); defer mu.RUnlock(); return cfg }
	set := func(next ExampleConfig) { mu.Lock(); defer mu.Unlock(); cfg = next }

	formOpts := func(errs crud.ValidationErrors) crud.FormOpts {
		return crud.FormOpts{ActionURL: formURL, HXTarget: hxTarget, SubmitLabel: "Save", Title: "Edit " + mm.DisplayName, Errors: errs}
	}

	r := chi.NewRouter()
	r.Use(middleware.Logger)

	// GET /      — full page with the read-only dump.
	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := pageLayout(mm.DisplayName, formURL, hxTarget, mm.RenderDisplay(get())).Render(req.Context(), w); err != nil {
			log.Printf("render: %v", err)
		}
	})
	// GET /edit  — form fragment swapped into #main-content.
	r.Get(formURL, func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = mm.RenderForm(get(), formOpts(nil)).Render(req.Context(), w)
	})
	// POST /edit — bind + validate; re-render the form with errors, or swap
	// back to the dump on success.
	r.Post(formURL, func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		instance := get()
		if err := mm.TryBindForm(req, &instance); err != nil {
			_ = mm.RenderForm(instance, formOpts(crud.ValidationErrorsFromError(err))).Render(req.Context(), w)
			return
		}
		set(instance)
		_ = mm.RenderDisplay(get()).Render(req.Context(), w)
	})

	log.Printf("form_mem listening on :8080 — open /")
	log.Fatal(http.ListenAndServe(":8080", r))
}
