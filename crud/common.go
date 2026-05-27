package crud

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/a-h/templ"
)

// Mux is the small surface a CRUDTable / Admin needs to register HTTP
// handlers. Both *http.ServeMux and chi.Router satisfy it; the library
// never asks for the concrete type, so callers wire whichever router
// they already use.
//
// For chi-based callers that want to layer middleware over the library's
// routes: use chi.Group (which stacks middleware without changing the
// prefix). chi.Route prefixes-mounts and would double the absolute
// paths the library registers.
type Mux interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

// ──────────────────────────────────────────────────────────────────────────
// HTTP / HTMX helpers shared by single.go and table.go.
//
// These were previously duplicated (writeFragment in single.go,
// makeFragmentHandler inline in table.go) or one-sided (isHTMXRequest
// only on the table). Consolidated here so both code paths use the
// same surface.
// ──────────────────────────────────────────────────────────────────────────

// writeFragment writes a templ.Component as the entire response body
// (no <html>/<body> chrome). Sets Content-Type and the supplied status.
// Used by every endpoint that returns an HTMX-friendly fragment.
func writeFragment(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := c.Render(r.Context(), w); err != nil {
		log.Printf("render: %v", err)
	}
}

// isHTMXRequest reports whether r came from HTMX. When true, handlers
// respond with a partial fragment + HX-* headers; otherwise they
// redirect (303) or send a full response.
func isHTMXRequest(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// modalIDsFromHeader returns (modalID, bodyID) based on the originating
// HX-Target header. The library renders the L1 "+ Create" / "edit"
// buttons with hx-target=#ModalL1BodyID and the relation widget's "+"
// button with hx-target=#ModalL2BodyID, so the level is unambiguous.
// Defaults to L1 (handles browser fallback when no HX-Target is set).
func modalIDsFromHeader(r *http.Request) (modalID, bodyID string) {
	switch r.Header.Get("HX-Target") {
	case ModalL2BodyID:
		return ModalL2ID, ModalL2BodyID
	default:
		return ModalL1ID, ModalL1BodyID
	}
}

// parseID extracts the {id} path value from r and parses it to uint.
// Returns (0, false) on any parse failure — handler converts that to
// HTTP 400.
func parseID(r *http.Request) (uint, bool) {
	n, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, false
	}
	return uint(n), true
}

// ValidationErrorsFromError lifts any error into a ValidationErrors map
// so callers can feed it straight into FormOpts.Errors without separate
// split-then-rejoin steps.
//
// If err is already ValidationErrors (possibly wrapped), it's returned
// as-is. Any other error becomes a single-entry map with the message
// under ModelLevelKey ("") — rendered as the alert banner above the
// form. nil → nil.
func ValidationErrorsFromError(err error) ValidationErrors {
	if err == nil {
		return nil
	}
	var verrs ValidationErrors
	if errors.As(err, &verrs) {
		return verrs
	}
	return ValidationErrors{ModelLevelKey: err.Error()}
}
