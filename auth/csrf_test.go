package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/alexedwards/scs/v2"
)

// newCSRFHarness builds the minimal stack needed to exercise
// CSRFWrap end-to-end: scs.LoadAndSave + CSRFWrap + a recording stub
// handler that records whether the inner handler ran. Tests inspect
// hit to assert "request reached the inner handler" without needing
// CRUDTable or any other consumer.
type csrfHarness struct {
	sm      *scs.SessionManager
	handler http.Handler
	hit     bool
}

func newCSRFHarness() *csrfHarness {
	h := &csrfHarness{sm: scs.New()}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.hit = true
		w.WriteHeader(http.StatusOK)
	})
	h.handler = h.sm.LoadAndSave(CSRFWrap(h.sm)(inner))
	return h
}

// prime runs a GET to obtain a session cookie + CSRF token (extracted
// from the session directly via the manager — the inner handler
// doesn't render anything that contains it). Returns both.
func (h *csrfHarness) prime(t *testing.T) (token string, cookie *http.Cookie) {
	t.Helper()
	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	h.handler.ServeHTTP(rr, req)
	for _, c := range rr.Result().Cookies() {
		if c.Name == "session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie set on GET /")
	}
	// Re-issue a GET through the harness with the cookie so the token
	// is reachable via the manager's Get under the same session id.
	req = httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)
	rr = httptest.NewRecorder()
	// Pull the token by wrapping a quick inspector inside LoadAndSave.
	h.sm.LoadAndSave(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token = h.sm.GetString(r.Context(), csrfSessionKey)
	})).ServeHTTP(rr, req)
	if token == "" {
		t.Fatal("CSRFWrap did not seed a session token on GET /")
	}
	return token, cookie
}

func TestCSRFWrapGETPassesWithoutToken(t *testing.T) {
	h := newCSRFHarness()
	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	h.handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !h.hit {
		t.Errorf("GET status = %d, hit = %v, want 200 + hit", rr.Code, h.hit)
	}
}

func TestCSRFWrapHEADAndOPTIONSPass(t *testing.T) {
	for _, method := range []string{"HEAD", "OPTIONS"} {
		h := newCSRFHarness()
		req := httptest.NewRequest(method, "/", nil)
		rr := httptest.NewRecorder()
		h.handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("%s status = %d, want 200", method, rr.Code)
		}
		if !h.hit {
			t.Errorf("%s did not reach inner handler", method)
		}
	}
}

func TestCSRFWrapPOSTWithoutTokenRejected(t *testing.T) {
	h := newCSRFHarness()
	_, cookie := h.prime(t)
	h.hit = false

	req := httptest.NewRequest("POST", "/", strings.NewReader(""))
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	h.handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
	if h.hit {
		t.Error("inner handler reached despite missing CSRF token")
	}
}

func TestCSRFWrapPOSTWithWrongTokenRejected(t *testing.T) {
	h := newCSRFHarness()
	_, cookie := h.prime(t)
	h.hit = false

	body := url.Values{"csrf_token": {"bogus"}}.Encode()
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	h.handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
	if h.hit {
		t.Error("inner handler reached despite wrong CSRF token")
	}
}

func TestCSRFWrapPOSTWithFormTokenAccepted(t *testing.T) {
	h := newCSRFHarness()
	tok, cookie := h.prime(t)
	h.hit = false

	body := url.Values{"csrf_token": {tok}}.Encode()
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	h.handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	if !h.hit {
		t.Error("inner handler not reached despite valid form CSRF token")
	}
}

// TestCSRFWrapPOSTWithHeaderAccepted is the HTMX path — htmx attaches
// the CSRF token via X-CSRF-Token header (configRequest hook in the
// app's page chrome).
func TestCSRFWrapPOSTWithHeaderAccepted(t *testing.T) {
	h := newCSRFHarness()
	tok, cookie := h.prime(t)
	h.hit = false

	req := httptest.NewRequest("POST", "/", strings.NewReader(""))
	req.Header.Set("X-CSRF-Token", tok)
	req.Header.Set("HX-Request", "true")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	h.handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	if !h.hit {
		t.Error("inner handler not reached despite valid X-CSRF-Token header")
	}
}

// TestCSRFWrapHeaderTakesPrecedence makes sure the header is checked
// even when a wrong form field is also present (htmx requests can
// theoretically carry both; the header is the canonical source).
func TestCSRFWrapHeaderTakesPrecedence(t *testing.T) {
	h := newCSRFHarness()
	tok, cookie := h.prime(t)
	h.hit = false

	body := url.Values{"csrf_token": {"wrong"}}.Encode()
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", tok)
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	h.handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK || !h.hit {
		t.Errorf("status = %d hit = %v, want 200 + hit", rr.Code, h.hit)
	}
}

func TestCSRFTokenOutsideWrapReturnsEmpty(t *testing.T) {
	// No CSRFWrap in the chain → no manager stashed in context → "".
	req := httptest.NewRequest("GET", "/", nil)
	if got := CSRFToken(req.Context()); got != "" {
		t.Errorf("CSRFToken outside CSRFWrap = %q, want empty", got)
	}
}

func TestCSRFHeadersBuildsHeader(t *testing.T) {
	// Build a context that carries a session with a known token.
	sm := scs.New()
	var attrs map[string]any
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CSRFWrap sets the token if missing; pull it back out.
		tok := sm.GetString(r.Context(), csrfSessionKey)
		got := CSRFHeaders(r.Context())
		attrs = got
		// Sanity-check the value matches the stored token.
		want := `{"X-CSRF-Token":"` + tok + `"}`
		if got["hx-headers"] != want {
			t.Errorf("hx-headers = %v, want %s", got["hx-headers"], want)
		}
	})
	handler := sm.LoadAndSave(CSRFWrap(sm)(inner))
	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if attrs == nil {
		t.Fatal("inner handler did not run")
	}
}

func TestCSRFWrapNilManagerPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("CSRFWrap(nil) should panic")
		}
	}()
	_ = CSRFWrap(nil)
}
