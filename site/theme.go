package site

import "net/http"

// ThemeCookie is the cookie name ThemeToggle writes and Theme reads.
const ThemeCookie = "gone_theme"

// Theme returns the DaisyUI theme name the viewer chose (the ThemeCookie
// value), or fallback when none is set. The app reads it when rendering its
// shell so the initial <html data-theme="…"> is correct server-side — no
// flash-of-wrong-theme on load:
//
//	<html data-theme={ site.Theme(r, "light") }>
//
// ThemeToggle then flips data-theme and updates the cookie client-side, so the
// toggle stays instant and the next load is already correct.
func Theme(r *http.Request, fallback string) string {
	if v := Pref(r, ThemeCookie); v != "" {
		return v
	}
	return fallback
}
