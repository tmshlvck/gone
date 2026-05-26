package crud

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/a-h/templ"
)

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

// splitValidationErr separates a BindForm error into (perField, modelLevel).
// When err is ValidationErrors the entry under ModelLevelKey ("")
// becomes the model-level message and is removed from the per-field map
// (so it isn't rendered twice). Any other error type becomes a
// model-level message above the form.
func splitValidationErr(err error) (map[string]string, string) {
	if err == nil {
		return nil, ""
	}
	var verrs ValidationErrors
	if errors.As(err, &verrs) {
		modelErr := verrs[ModelLevelKey]
		fieldErrs := make(map[string]string, len(verrs))
		for k, v := range verrs {
			if k != ModelLevelKey {
				fieldErrs[k] = v
			}
		}
		return fieldErrs, modelErr
	}
	return nil, err.Error()
}
