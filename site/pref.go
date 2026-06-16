package site

import "net/http"

// prefMaxAge is how long a preference cookie lives — one year. Long enough
// that a per-browser choice (timezone, theme, …) sticks; the cookie is the
// single source for these low-stakes, non-secret settings.
const prefMaxAge = 365 * 24 * 3600

// SetPref writes a per-browser preference cookie: long-lived, path "/",
// SameSite=Lax, and NOT HttpOnly (so a client toggle can read/update it
// without a round-trip). An empty value deletes the cookie.
//
// It's the shared storage primitive behind TimezonePicker and ThemeToggle;
// apps that want a server-session or per-user-DB backing instead supply their
// own resolver (e.g. to TimezoneMiddleware) and persist however they like.
func SetPref(w http.ResponseWriter, name, value string) {
	c := &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		SameSite: http.SameSiteLaxMode,
	}
	if value == "" {
		c.MaxAge = -1 // delete
	} else {
		c.MaxAge = prefMaxAge
	}
	http.SetCookie(w, c)
}

// Pref reads a preference cookie set by SetPref, returning "" when absent.
func Pref(r *http.Request, name string) string {
	if c, err := r.Cookie(name); err == nil {
		return c.Value
	}
	return ""
}
