// Example: edit a single in-memory ExampleConfig struct via an auto-derived
// MetaModel using crud.MetaModel.RouteForm + DisplayComponent — the
// library exposes only fragment endpoints; this example owns its page
// shell and embeds the dump component in /. Demonstrates per-field
// validators, FormHelp, and a cross-field MetaModel.Validate
// (MaxRequests must exceed Port).
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

	// Per-field metadata: display names, help text, and field validators.
	for i := range mm.Fields {
		switch mm.Fields[i].Name {
		case "Hostname":
			mm.Fields[i].FormHelp = "FQDN or short host, 1–253 chars."
			mm.Fields[i].FieldValidate = crud.All(crud.NotEmpty, crud.MaxLen(253))
		case "BindAddress":
			mm.Fields[i].DisplayName = "Bind address"
			mm.Fields[i].FormHelp = "IPv4 or IPv6 the server listens on."
			mm.Fields[i].FieldValidate = crud.NotEmpty
		case "Port":
			mm.Fields[i].FormHelp = "TCP port, 1–65535."
			mm.Fields[i].FieldValidate = crud.IntRange(1, 65535)
		case "EnableTLS":
			mm.Fields[i].DisplayName = "TLS enabled"
		case "MaxRequests":
			mm.Fields[i].DisplayName = "Max requests"
			mm.Fields[i].FormHelp = "Concurrent request cap, must exceed the port number."
			mm.Fields[i].FieldValidate = crud.IntRange(1, 10_000_000)
		case "Threshold":
			mm.Fields[i].FormHelp = "CPU load shed threshold, 0.0–1.0."
			mm.Fields[i].FieldValidate = crud.FloatRange(0.0, 1.0)
		case "StartTime":
			mm.Fields[i].DisplayName = "Start time"
		case "AdminEmail":
			mm.Fields[i].DisplayName = "Admin email"
			mm.Fields[i].FormInputType = "email"
			mm.Fields[i].FieldValidate = crud.All(crud.NotEmpty, crud.Email)
		}
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

	// Library registers ONLY partial endpoints — no page shell.
	if err := mm.RouteForm(mux, formURL, hxTarget, getter, setter); err != nil {
		log.Fatalf("RouteForm: %v", err)
	}

	// App owns the main page route. It embeds DisplayComponent inside
	// its own page shell and wraps the result with the #main-content
	// container so form swaps land in the right place.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		instance, _ := getter()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := pageShell(mm.DisplayName,
			mm.DisplayComponent(instance, formURL, hxTarget),
		).Render(r.Context(), w); err != nil {
			log.Printf("render: %v", err)
		}
	})

	addr := ":8080"
	log.Printf("form_mem listening on %s — open /", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
