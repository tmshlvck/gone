// Example: edit a single in-memory ExampleConfig struct using
// MetaModel's render/bind primitives directly — no library-managed
// routes. The app writes its own HTTP handlers around
// MetaModel.RenderDisplay, MetaModel.RenderForm, and
// MetaModel.TryBindForm, picks its own URLs, and supplies its own
// page shell.
//
// Demonstrates per-field validators, FormHelp, a cross-field
// MetaModel.Validate (MaxRequests must exceed Port), and the green
// "Saved." banner above the form after a successful POST.
package main

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

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
	mm, err := crud.DeriveMetaModel[ExampleConfig]()
	if err != nil {
		log.Fatalf("derive: %v", err)
	}
	mm.DisplayName = "Server configuration"

	// Per-field metadata: display names, help text, and field validators
	// — each tweak reaches its field via MetaModel.MustFindField, which
	// panics on a typo so a renamed model surfaces immediately.
	{
		f := mm.MustFindField("Hostname")
		f.FormHelp = "FQDN or short host, 1–253 chars."
		f.FieldValidate = crud.All(crud.NotEmpty, crud.MaxLen(253))
	}
	{
		f := mm.MustFindField("BindAddress")
		f.DisplayName = "Bind address"
		f.FormHelp = "IPv4 or IPv6 the server listens on."
		// Either v4 OR v6 acceptable — Any joins their messages with
		// " or " so a user typing "garbage" sees both alternatives.
		f.FieldValidate = crud.All(crud.NotEmpty, crud.Any(crud.IPv4Addr, crud.IPv6Addr))
	}
	{
		f := mm.MustFindField("Port")
		f.FormHelp = "TCP port, 1–65535."
		f.FieldValidate = crud.IntRange(1, 65535)
	}
	mm.MustFindField("EnableTLS").DisplayName = "TLS enabled"
	{
		f := mm.MustFindField("MaxRequests")
		f.DisplayName = "Max requests"
		f.FormHelp = "Concurrent request cap, must exceed the port number."
		f.FieldValidate = crud.IntRange(1, 10_000_000)
	}
	{
		f := mm.MustFindField("Threshold")
		f.FormHelp = "CPU load shed threshold, 0.0–1.0."
		f.FieldValidate = crud.FloatRange(0.0, 1.0)
	}
	mm.MustFindField("StartTime").DisplayName = "Start time"
	{
		f := mm.MustFindField("AdminEmail")
		f.DisplayName = "Admin email"
		f.FormInputType = "email"
		f.FieldValidate = crud.All(crud.NotEmpty, crud.Email)
	}

	// Cross-field rule: MaxRequests must be strictly larger than Port.
	// This is intentionally arbitrary so it's easy to violate in the demo
	// (Port=8443 and MaxRequests=100 fails; MaxRequests=100_000 passes).
	mm.Validate = func(instance ExampleConfig) error {
		if instance.MaxRequests <= uint64(instance.Port) {
			return fmt.Errorf("Max requests (%d) must be greater than Port (%d)",
				instance.MaxRequests, instance.Port)
		}
		return nil
	}

	get := func() ExampleConfig {
		mu.RLock()
		defer mu.RUnlock()
		return cfg
	}
	set := func(next ExampleConfig) {
		mu.Lock()
		defer mu.Unlock()
		cfg = next
	}

	mux := http.NewServeMux()

	// GET /             — full page with the dump in the working area.
	// GET /edit         — form fragment (HTMX-swap into #main-content).
	// POST /edit        — bind + validate + set. Returns the dump on success
	//                     (with HX-Trigger: form-saved so the page shows
	//                     "Saved." next time the form opens) or the form
	//                     with errors on failure.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := pageLayout(mm.DisplayName, formURL, hxTarget,
			mm.RenderDisplay(get()),
		).Render(r.Context(), w); err != nil {
			log.Printf("render: %v", err)
		}
	})

	formOpts := func(errs crud.ValidationErrors) crud.FormOpts {
		return crud.FormOpts{
			ActionURL:   formURL,
			HXTarget:    hxTarget,
			SubmitLabel: "Save",
			Title:       "Edit " + mm.DisplayName,
			Errors:      errs,
		}
	}

	mux.HandleFunc("GET "+formURL, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = mm.RenderForm(get(), formOpts(nil)).Render(r.Context(), w)
	})

	mux.HandleFunc("POST "+formURL, func(w http.ResponseWriter, r *http.Request) {
		instance := get()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Validation failure: re-render the form with the bound values
		// and inline errors. Status 200 — HTMX only swaps 2xx bodies.
		if err := mm.TryBindForm(r, &instance); err != nil {
			_ = mm.RenderForm(instance, formOpts(crud.ValidationErrorsFromError(err))).Render(r.Context(), w)
			return
		}
		set(instance)
		// Success: swap back to the dump fragment.
		_ = mm.RenderDisplay(get()).Render(r.Context(), w)
	})

	addr := ":8080"
	log.Printf("form_mem listening on %s — open /", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
