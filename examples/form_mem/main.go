// Example: edit a single in-memory ExampleConfig struct via an auto-derived
// MetaModel. Demonstrates the simplified §6.1–6.2 surface plus shared
// gone/crud view components on a mix of scalar types.
package main

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/a-h/templ"

	"github.com/tmshlvck/gone/crud"
)

type ExampleConfig struct {
	Hostname    string
	BindAddress string // IP kept as plain string (same as a GORM column)
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

func renderPage(w http.ResponseWriter, r *http.Request, status int, title string, frag templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := pageShell(title, frag).Render(r.Context(), w); err != nil {
		log.Printf("render: %v", err)
	}
}

func main() {
	mm, err := crud.DeriveMetaModel[ExampleConfig]()
	if err != nil {
		log.Fatalf("derive: %v", err)
	}
	mm.DisplayName = "Server configuration"

	// Per-field overrides after derivation.
	for i := range mm.Fields {
		switch mm.Fields[i].Name {
		case "AdminEmail":
			mm.Fields[i].FormInputType = "email"
			mm.Fields[i].DisplayName = "Admin email"
		case "BindAddress":
			mm.Fields[i].DisplayName = "Bind address"
		case "EnableTLS":
			mm.Fields[i].DisplayName = "TLS enabled"
		case "MaxRequests":
			mm.Fields[i].DisplayName = "Max requests"
		case "StartTime":
			mm.Fields[i].DisplayName = "Start time"
		}
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		cells := mm.DisplayValues(mm, cfg)
		mu.RUnlock()
		renderPage(w, r, http.StatusOK, mm.DisplayName, crud.DumpView(crud.DumpViewData{
			DisplayName: mm.DisplayName,
			EditURL:     "/edit",
			Fields:      mm.Fields,
			Cells:       cells,
		}))
	})

	mux.HandleFunc("GET /edit", func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		inputs := mm.GenFormElements(mm, cfg)
		mu.RUnlock()
		renderPage(w, r, http.StatusOK, "Edit "+mm.DisplayName, crud.FormView(crud.FormViewData{
			DisplayName: "Edit " + mm.DisplayName,
			ActionURL:   "/edit",
			BackURL:     "/",
			SubmitText:  "Save",
			Fields:      mm.Fields,
			Inputs:      inputs,
		}))
	})

	mux.HandleFunc("POST /edit", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.RLock()
		next := cfg
		mu.RUnlock()

		if err := mm.BindForm(mm, r.PostForm, &next); err != nil {
			inputs := mm.GenFormElements(mm, next)
			renderPage(w, r, http.StatusBadRequest, "Edit "+mm.DisplayName, crud.FormView(crud.FormViewData{
				DisplayName: "Edit " + mm.DisplayName,
				ActionURL:   "/edit",
				BackURL:     "/",
				SubmitText:  "Save",
				Fields:      mm.Fields,
				Inputs:      inputs,
				ErrMsg:      err.Error(),
			}))
			return
		}

		mu.Lock()
		cfg = next
		mu.Unlock()
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})

	addr := ":8080"
	log.Printf("form_mem listening on %s — open / and /edit", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
