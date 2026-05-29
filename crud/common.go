package crud

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

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

// PageShellFunc wraps the library's component output in the app's page
// chrome. It receives the HTTP writer and request directly — not a
// templ.Component to return — so the caller can write redirects,
// custom headers, or auth failures from inside the shell.
//
// title is supplied by the component (CRUDTable's PageTitle field,
// Admin's active-table DisplayName) and is typically what the shell
// writes into <title> and any heading.
//
// content is the component-rendered body the shell should embed
// inside its chrome.
//
// A typical implementation:
//
//	func pageShell(w http.ResponseWriter, r *http.Request, title string, content templ.Component) {
//	    user, _ := r.Context().Value(userKey{}).(*User)
//	    if user == nil {
//	        http.Redirect(w, r, "/login", http.StatusSeeOther)
//	        return
//	    }
//	    w.Header().Set("Content-Type", "text/html; charset=utf-8")
//	    appPageTemplate(title, user, content).Render(r.Context(), w)
//	}
//
// nil shell on Route means "don't register a page handler" — useful
// for tests and for fragment-only callers.
type PageShellFunc func(w http.ResponseWriter, r *http.Request, title string, content templ.Component)

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
	bodyID = r.Header.Get("HX-Target")
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
	n, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, false
	}
	return uint(n), true
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
