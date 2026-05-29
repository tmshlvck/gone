// Example: chi + alexedwards/scs/v2 + a small hand-rolled CSRF middleware
// that mirrors PRD §5.2 — token-in-session, validated as X-CSRF-Token
// header or form field on mutating requests, rotated on login, cleared on
// logout. No gin, no gorilla.
package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"log"
	"net/http"
	"time"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2"
	"github.com/go-chi/chi/v5"
)

const (
	csrfSessionKey = "csrf_token"
	userSessionKey = "username"
	formField      = "csrf_token"
	headerName     = "X-CSRF-Token"
)

// csrfMiddleware ensures every session has a token and validates it on
// mutating requests. Read-only requests (GET / HEAD / OPTIONS) are bypassed.
// Header is checked before form so JSON request bodies aren't parsed
// unnecessarily.
func csrfMiddleware(sm *scs.SessionManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			tok := sm.GetString(ctx, csrfSessionKey)
			if tok == "" {
				tok = newToken()
				sm.Put(ctx, csrfSessionKey, tok)
			}

			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}

			got := r.Header.Get(headerName)
			if got == "" {
				got = r.PostFormValue(formField)
			}
			if tok == "" || got == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(got)) != 1 {
				http.Error(w, "csrf check failed", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func currentCSRF(r *http.Request, sm *scs.SessionManager) string {
	return sm.GetString(r.Context(), csrfSessionKey)
}

func newToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// renderHTML writes a templ component as the response body with the given
// status code. Three lines of glue, no extra package.
func renderHTML(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := c.Render(r.Context(), w); err != nil {
		log.Printf("render: %v", err)
	}
}

func main() {
	sm := scs.New()
	sm.Lifetime = 24 * time.Hour
	sm.Cookie.HttpOnly = true
	sm.Cookie.SameSite = http.SameSiteLaxMode

	r := chi.NewRouter()
	r.Use(sm.LoadAndSave)
	r.Use(csrfMiddleware(sm))

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		if u := sm.GetString(r.Context(), userSessionKey); u != "" {
			http.Redirect(w, r, "/protected", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})

	r.Get("/login", func(w http.ResponseWriter, r *http.Request) {
		renderHTML(w, r, http.StatusOK, loginPage(currentCSRF(r, sm), ""))
	})

	r.Post("/login", func(w http.ResponseWriter, r *http.Request) {
		username := r.PostFormValue("username")
		password := r.PostFormValue("password")
		if username != "admin" || password != "hunter2" {
			renderHTML(w, r, http.StatusUnauthorized, loginPage(currentCSRF(r, sm), "invalid credentials"))
			return
		}
		// Session-fixation defense: rotate the session ID before storing
		// the now-authenticated identity.
		if err := sm.RenewToken(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		sm.Put(r.Context(), userSessionKey, username)
		sm.Put(r.Context(), csrfSessionKey, newToken())
		http.Redirect(w, r, "/protected", http.StatusSeeOther)
	})

	r.Post("/logout", func(w http.ResponseWriter, r *http.Request) {
		_ = sm.Destroy(r.Context())
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})

	r.Get("/protected", func(w http.ResponseWriter, r *http.Request) {
		u := sm.GetString(r.Context(), userSessionKey)
		if u == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		renderHTML(w, r, http.StatusOK, protectedPage(u, currentCSRF(r, sm)))
	})

	addr := ":8080"
	log.Printf("sessions example listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, r))
}
