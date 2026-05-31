package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2"
)

const (
	csrfSessionKey = "auth:csrf"
	csrfFormField  = "csrf_token"
	csrfHeaderName = "X-CSRF-Token"
)

// csrfManagerKey carries the *scs.SessionManager through r.Context()
// so CSRFToken / CSRFField / CSRFHeaders can pull the token out of
// the session without taking the manager as a separate argument.
type csrfManagerKey struct{}

// CSRFWrap returns the CSRF middleware bound to sm. It ensures every
// session has a token, validates it on mutating requests (anything
// other than GET / HEAD / OPTIONS), and stashes the session manager
// in r.Context() so CSRFToken / CSRFField / CSRFHeaders can find it.
//
// The token is checked from the X-CSRF-Token header first (so JSON
// bodies aren't parsed unnecessarily) and falls back to the
// csrf_token form field. Mismatches → 403.
//
// Token rotation on login is the AuthSimple.Login responsibility —
// CSRFWrap creates a token on first request but doesn't rotate it.
func CSRFWrap(sm *scs.SessionManager) func(http.Handler) http.Handler {
	if sm == nil {
		panic("auth.CSRFWrap: nil session manager")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			tok := sm.GetString(ctx, csrfSessionKey)
			if tok == "" {
				tok = newCSRFToken()
				sm.Put(ctx, csrfSessionKey, tok)
			}

			// Stash the manager in r.Context() so the templ helpers
			// below can resolve the token without a separate handle.
			ctx = context.WithValue(ctx, csrfManagerKey{}, sm)
			r = r.WithContext(ctx)

			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}

			got := r.Header.Get(csrfHeaderName)
			if got == "" {
				got = r.PostFormValue(csrfFormField)
			}
			if got == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(got)) != 1 {
				http.Error(w, "csrf check failed", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CSRFToken returns the current session's CSRF token, or "" if the
// CSRFWrap middleware didn't run (defensive — should never happen in
// practice, but the helpers below handle it gracefully).
func CSRFToken(ctx context.Context) string {
	sm, ok := ctx.Value(csrfManagerKey{}).(*scs.SessionManager)
	if !ok || sm == nil {
		return ""
	}
	return sm.GetString(ctx, csrfSessionKey)
}

// CSRFField renders a hidden <input> carrying the current CSRF token,
// ready to drop into any POST form: `@auth.CSRFField(ctx)` inside a
// templ.
func CSRFField(ctx context.Context) templ.Component {
	return csrfField(CSRFToken(ctx))
}

// CSRFHeaders returns the templ.Attributes carrying the X-CSRF-Token
// header for HTMX-driven mutations. Spread into the initiating element:
//
//	<button hx-post={ url } { auth.CSRFHeaders(ctx)... }>Save</button>
//
// (Spread syntax `{ x... }` requires templ ≥ v0.2; the rest of `gone`
// already targets templ v0.3.)
func CSRFHeaders(ctx context.Context) templ.Attributes {
	return templ.Attributes{
		"hx-headers": `{"` + csrfHeaderName + `":"` + CSRFToken(ctx) + `"}`,
	}
}

// rotateCSRF replaces the session's CSRF token. Called by AuthSimple
// (and any other Auth impl) on Login so the post-login session has a
// fresh token, decoupled from any token an attacker might have seen.
func rotateCSRF(ctx context.Context, sm *scs.SessionManager) {
	sm.Put(ctx, csrfSessionKey, newCSRFToken())
}

func newCSRFToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand.Read on Linux never fails in practice; if it
		// somehow does we'd rather panic than emit a predictable token.
		panic("auth: crypto/rand: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
