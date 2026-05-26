// Example: edit a single in-memory ExampleConfig struct via an
// auto-derived MetaModel. The library exposes only barebone fragment
// endpoints — this example owns its page shell and wraps both the
// display and form fragments in its own card + title + Edit button.
// Demonstrates per-field validators, FormHelp, and a cross-field
// MetaModel.Validate (MaxRequests must exceed Port).
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
	// URLs and HTMX target live on the MetaModel; RouteForm picks them
	// up, and RenderFormComponent embeds them into the form's
	// action / hx-post / hx-target attributes.
	mm.FormURL = formURL
	mm.HXTarget = hxTarget

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
		f.FieldValidate = crud.NotEmpty
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

	getter := func() (ExampleConfig, error) {
		mu.RLock()
		defer mu.RUnlock()
		return cfg, nil
	}
	setter := func(next ExampleConfig) error {
		mu.Lock()
		defer mu.Unlock()
		cfg = next
		return nil
	}

	mux := http.NewServeMux()

	// Library registers ONLY barebone fragment endpoints — no page shell,
	// no card, no Edit button.
	if err := mm.RouteForm(mux, getter, setter); err != nil {
		log.Fatalf("RouteForm: %v", err)
	}

	// App owns the main page. It wraps the barebone DisplayComponent in
	// its own card + title + Edit button; the swap container
	// (#main-content) lives inside the card so HTMX swaps land in the
	// data area without disturbing the chrome.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		instance, _ := getter()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := pageShell(mm.DisplayName, formURL, hxTarget,
			mm.RenderDisplayComponent(r, instance),
		).Render(r.Context(), w); err != nil {
			log.Printf("render: %v", err)
		}
	})

	addr := ":8080"
	log.Printf("form_mem listening on %s — open /", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

