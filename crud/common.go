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

// Modal wiring constants. The library uses two stacked DaisyUI dialogs for
// create/edit forms; open/close is driven by the client-side bridge in
// PageModals (see views.templ), so handlers carry no per-modal id bookkeeping.
const (
	// modalFormTarget is the hx-target a create/edit form re-renders itself
	// into on a validation error. "closest .crud-modal-body" resolves to the
	// body of whichever modal (L1 or the shared L2) the form is shown in, so
	// the same form markup works at either level — no per-modal id threading.
	modalFormTarget = "closest .crud-modal-body"

	// crudCloseModalEvent asks the client to close the topmost open modal (the
	// dialog the just-submitted form lived in). Emitted on a successful
	// mutation; the PageModals bridge JS listens for it by this exact name.
	crudCloseModalEvent = "crud-close-modal"

	// refreshRelationEvent tells every relation <select> to reload its option
	// list — fired after a nested ("+ new") create adds a row. Relation
	// pickers subscribe via hx-trigger="… refresh-relation from:body" (see
	// relation.go); the name must stay in sync.
	refreshRelationEvent = "refresh-relation"
)

// isNestedModal reports whether the request was submitted from inside the
// shared L2 ("+ create new") modal — its resolved hx-target is the L2 body.
// A nested create refreshes the parent form's relation pickers instead of the
// table (whose list area isn't even on the current page). Browser (non-HTMX)
// callers have no HX-Target and read as not-nested.
func isNestedModal(r *http.Request) bool {
	return htmx.Target(r) == ModalL2BodyID
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
