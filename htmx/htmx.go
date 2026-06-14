// Package htmx turns the stringly-typed HTMX wire protocol into named Go
// functions: request classification on the way in, and a small fluent
// builder for HX-* response directives on the way out.
//
// It depends only on net/http + encoding/json — no templ, no chi, no other
// gone packages — so any handler (library or application) can use it.
//
// The response builder accumulates directives and writes them in one Apply:
//
//	htmx.Reply().
//		Retarget("#table").
//		Reswap("innerHTML").
//		Trigger("crud-close-modal", nil).
//		Apply(w)
//
// Multiple Trigger calls coalesce into a single HX-Trigger header (a JSON
// object keyed by event name), which is how HTMX dispatches several
// client-side events from one response.
package htmx

import (
	"encoding/json"
	"net/http"
	"net/url"
)

// ──────────────────────────────────────────────────────────────────────────
// Request classification.
// ──────────────────────────────────────────────────────────────────────────

// IsRequest reports whether r originated from HTMX (HX-Request: true). When
// true a handler answers with a bare fragment; otherwise it serves a whole
// page or a redirect.
func IsRequest(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

// IsBoosted reports whether r came from an hx-boost link/form (HX-Boosted:
// true). gone does not use hx-boost, but the classifier is here for
// completeness and for apps that do.
func IsBoosted(r *http.Request) bool { return r.Header.Get("HX-Boosted") == "true" }

// Target returns the id of the element the request targets (HX-Target), or
// "" when absent (a non-HTMX request, or a swap without an explicit target).
func Target(r *http.Request) string { return r.Header.Get("HX-Target") }

// TriggerName returns the name attribute of the element that triggered the
// request (HX-Trigger-Name), or "".
func TriggerName(r *http.Request) string { return r.Header.Get("HX-Trigger-Name") }

// TriggerID returns the id of the element that triggered the request
// (HX-Trigger), or "".
func TriggerID(r *http.Request) string { return r.Header.Get("HX-Trigger") }

// CurrentURL returns the URL shown in the browser address bar at request
// time (HX-Current-URL), parsed. The bool is false when the header is absent
// or unparsable. Handlers use it to recover list state (page/sort/search)
// for a POST whose own URL points at a mutation endpoint.
func CurrentURL(r *http.Request) (*url.URL, bool) {
	cur := r.Header.Get("HX-Current-URL")
	if cur == "" {
		return nil, false
	}
	u, err := url.Parse(cur)
	if err != nil {
		return nil, false
	}
	return u, true
}

// ──────────────────────────────────────────────────────────────────────────
// Response directives.
// ──────────────────────────────────────────────────────────────────────────

// Resp accumulates HX-* response directives. Build one with Reply, chain
// setters, and write them all with Apply. The zero value is not usable —
// always start from Reply().
type Resp struct {
	retarget string
	reswap   string
	reselect string
	pushURL  string
	redirect string
	refresh  bool
	triggers map[string]any // event name → detail (nil detail allowed)
}

// Reply starts a response-directive builder.
func Reply() *Resp { return &Resp{} }

// Retarget overrides the element the response is swapped into (HX-Retarget),
// e.g. "#hero-table". sel is a CSS selector.
func (h *Resp) Retarget(sel string) *Resp { h.retarget = sel; return h }

// Reswap overrides the swap strategy (HX-Reswap), e.g. "innerHTML",
// "outerHTML", "none", "beforeend".
func (h *Resp) Reswap(spec string) *Resp { h.reswap = spec; return h }

// Reselect overrides which part of the response is selected for swapping
// (HX-Reselect). sel is a CSS selector applied to the response body.
func (h *Resp) Reselect(sel string) *Resp { h.reselect = sel; return h }

// PushURL pushes u into the browser history / address bar (HX-Push-URL) so
// the swapped state is bookmarkable and survives reload. Pass "false" to
// suppress a push HTMX would otherwise do.
func (h *Resp) PushURL(u string) *Resp { h.pushURL = u; return h }

// Redirect performs a client-side redirect to u (HX-Redirect): HTMX does a
// full browser navigation. Use for "you must log in" from inside a fragment
// response, where a 303 wouldn't be followed by the swap.
func (h *Resp) Redirect(u string) *Resp { h.redirect = u; return h }

// Refresh forces a full client-side page reload (HX-Refresh: true).
func (h *Resp) Refresh() *Resp { h.refresh = true; return h }

// Trigger schedules a client-side event named event after the swap settles
// (HX-Trigger). detail is JSON-encoded as the event payload; pass nil for a
// bare event. Repeated calls coalesce into one HX-Trigger header. Use it for
// any app- or component-specific event the client listens for (e.g. crud's
// "crud-close-modal" / "refresh-relation").
func (h *Resp) Trigger(event string, detail any) *Resp {
	if h.triggers == nil {
		h.triggers = map[string]any{}
	}
	h.triggers[event] = detail
	return h
}

// Apply writes the accumulated directives onto w's header. Call once, before
// writing the response body.
func (h *Resp) Apply(w http.ResponseWriter) {
	head := w.Header()
	if h.retarget != "" {
		head.Set("HX-Retarget", h.retarget)
	}
	if h.reswap != "" {
		head.Set("HX-Reswap", h.reswap)
	}
	if h.reselect != "" {
		head.Set("HX-Reselect", h.reselect)
	}
	if h.pushURL != "" {
		head.Set("HX-Push-Url", h.pushURL)
	}
	if h.redirect != "" {
		head.Set("HX-Redirect", h.redirect)
	}
	if h.refresh {
		head.Set("HX-Refresh", "true")
	}
	if len(h.triggers) > 0 {
		// HTMX accepts a JSON object mapping event name → detail. Encoding
		// errors are impossible for the values we put in (strings, bools,
		// nil, JSON-marshalable details), so ignore the error.
		if b, err := json.Marshal(h.triggers); err == nil {
			head.Set("HX-Trigger", string(b))
		}
	}
}
