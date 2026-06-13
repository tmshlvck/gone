package crud

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/tmshlvck/gone/htmx"
)

// ──────────────────────────────────────────────────────────────────────────
// HTTP / HTMX helpers shared by single.go and table.go.
//
// HTMX request classification and HX-* response directives live in
// gone/htmx; fragment writing in gone/site. The helpers here are the
// crud-specific glue (id parsing, modal-id derivation, error shaping).
// ──────────────────────────────────────────────────────────────────────────

// modalIDsFromHeader returns (modalID, bodyID, isL2) for the originating
// modal, derived from the HX-Target request header.
//
//   - bodyID = the value of HX-Target (e.g. "hero-modal-l1-body" or
//     "crud-modal-l2-body").
//   - modalID = bodyID with the "-body" suffix stripped
//     ("hero-modal-l1" / "crud-modal-l2").
//   - isL2 = true iff bodyID is the shared L2 body ID.
//
// Browser (non-HTMX) callers have no HX-Target — returns empty strings
// and isL2=false. Handlers that branch on level use isL2 as the test.
func modalIDsFromHeader(r *http.Request) (modalID, bodyID string, isL2 bool) {
	bodyID = htmx.Target(r)
	if bodyID == "" {
		return "", "", false
	}
	modalID = strings.TrimSuffix(bodyID, "-body")
	isL2 = bodyID == ModalL2BodyID
	return
}

// parseID extracts the {id} path value from r and parses it to uint.
// Returns (0, false) on any parse failure — handler converts that to
// HTTP 400.
func parseID(r *http.Request) (uint, bool) {
	n, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		return 0, false
	}
	return uint(n), true
}

// failInternal writes err as a 500 and returns true. Handler should
// short-circuit when this returns true:
//
//	if failInternal(w, err) { return }
//
// nil error is the no-op (returns false).
func failInternal(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
	return true
}

// normalizePrefix converts any user-supplied URL prefix to a canonical
// form: empty or "/" → "" (root mount); "/x/" → "/x"; "/x" → "/x".
// Used by Route() to make Route(mux, "") and Route(mux, "/") behave
// the same, and to silently swallow trailing slashes the caller might
// have appended.
func normalizePrefix(prefix string) string {
	if prefix == "/" {
		return ""
	}
	return strings.TrimRight(prefix, "/")
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
