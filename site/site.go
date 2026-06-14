// Package site holds the small page-composition helpers shared by gone's
// HTMX components and the applications that embed them.
//
// Scope is deliberately minimal. gone is a library, not a framework: it does
// not own the page. An application supplies its own chrome (head, theme,
// navigation) as a Shell function and decides how its pages are laid out and
// which nav item is active. site only provides:
//
//   - Shell, the function shape an app's page-chrome takes;
//   - Fragment, which writes a templ component as a bare HTML response;
//   - Respond, an optional convenience for a single URL that serves both a
//     fragment (to HTMX) and a full page (to a browser navigation);
//   - StylesheetCSS / StyleTag, an OPTIONAL polish layer over DaisyUI v5
//     (see style.go). The library never injects it; an app opts in.
//
// A richer Shell/Nav model (with library-computed active-nav) may land here
// later; it is intentionally absent for now.
package site

import (
	"log"
	"net/http"

	"github.com/a-h/templ"
	"github.com/tmshlvck/gone/htmx"
)

// Shell is an application's page-chrome function: wrap content in the app's
// full HTML document (html/head/theme/nav) under the given title and write
// it to w. The library never defines one — the app owns its look.
//
// A Shell may also write headers or redirect (e.g. bounce an anonymous user
// to /login) before rendering; it is a normal handler tail, not a pure
// renderer.
type Shell func(w http.ResponseWriter, r *http.Request, title string, content templ.Component)

// Fragment writes c as a bare HTML fragment — Content-Type set, no
// html/body/chrome. This is what every in-component HTMX handler returns;
// the surrounding page (the app's Shell, or whatever embeds the component)
// already provides the document.
func Fragment(w http.ResponseWriter, r *http.Request, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := c.Render(r.Context(), w); err != nil {
		log.Printf("site.Fragment render: %v", err)
	}
}

// Respond serves content as a bare fragment when r is an HTMX request, or
// wraps it in shell for a full page otherwise. It is a convenience for an
// app route that wants ONE URL to answer both — most gone apps keep page
// routes and fragment routes separate (the page route uses shell directly,
// the fragment routes use Fragment) and don't need this.
func Respond(w http.ResponseWriter, r *http.Request, shell Shell, title string, content templ.Component) {
	if htmx.IsRequest(r) {
		Fragment(w, r, content)
		return
	}
	shell(w, r, title, content)
}
