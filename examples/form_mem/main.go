// Example: edit a single in-memory ExampleConfig struct via an auto-derived
// MetaModel. Demonstrates the same gone/crud view components as crud_mem
// (DumpView, FormView) but with inline HTMX swaps into #main-content
// instead of a modal — no CRUDTable involved.
package main

import (
	"errors"
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

// renderPage wraps frag in the page shell. Used for browser navigation /
// initial loads. HTMX requests skip the shell and write the fragment
// directly via writeFragment.
func renderPage(w http.ResponseWriter, r *http.Request, status int, title string, frag templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := pageShell(title, frag).Render(r.Context(), w); err != nil {
		log.Printf("render: %v", err)
	}
}

func writeFragment(w http.ResponseWriter, r *http.Request, status int, frag templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := frag.Render(r.Context(), w); err != nil {
		log.Printf("render: %v", err)
	}
}

func isHX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

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

	dumpFragment := func() templ.Component {
		mu.RLock()
		cells := mm.DisplayValues(mm, cfg)
		mu.RUnlock()
		return crud.DumpView(crud.DumpViewData{
			DisplayName:  mm.DisplayName,
			EditURL:      "/edit",
			EditHXTarget: "#main-content",
			Fields:       mm.Fields,
			Cells:        cells,
		})
	}

	formFragment := func(modelErr string, fieldErrors map[string]string, instance ExampleConfig) templ.Component {
		inputs := mm.GenFormElements(mm, instance)
		// crud.FormView emits no card wrapper (so it composes inside a
		// modal). For inline use we add one explicitly so the page
		// styling matches the dump view.
		return inCard(crud.FormView(crud.FormViewData{
			DisplayName: "Edit " + mm.DisplayName,
			ActionURL:   "/edit",
			BackURL:     "/",
			SubmitText:  "Save",
			Fields:      mm.Fields,
			Inputs:      inputs,
			HXTarget:    "#main-content",
			ErrMsg:      modelErr,
			FieldErrors: fieldErrors,
		}))
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		renderPage(w, r, http.StatusOK, mm.DisplayName, dumpFragment())
	})

	mux.HandleFunc("GET /edit", func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		snapshot := cfg
		mu.RUnlock()
		frag := formFragment("", nil, snapshot)
		if isHX(r) {
			writeFragment(w, r, http.StatusOK, frag)
		} else {
			renderPage(w, r, http.StatusOK, "Edit "+mm.DisplayName, frag)
		}
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
			var verrs crud.ValidationErrors
			var fieldErrs map[string]string
			var modelErr string
			if errors.As(err, &verrs) {
				fieldErrs = verrs
			} else {
				modelErr = err.Error()
			}
			frag := formFragment(modelErr, fieldErrs, next)
			if isHX(r) {
				writeFragment(w, r, http.StatusBadRequest, frag)
			} else {
				renderPage(w, r, http.StatusBadRequest, "Edit "+mm.DisplayName, frag)
			}
			return
		}

		mu.Lock()
		cfg = next
		mu.Unlock()

		if isHX(r) {
			// Replace the inline form with the freshly rendered dump.
			writeFragment(w, r, http.StatusOK, dumpFragment())
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})

	addr := ":8080"
	log.Printf("form_mem listening on %s — open / and /edit", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
