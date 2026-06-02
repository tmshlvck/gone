package auth

import (
	"context"
	"net/http"

	"github.com/a-h/templ"
)

// Mux is the small surface a route-registering component (CRUDTable,
// Admin, AuthSimple, AuthGORM) needs to register HTTP handlers. Both
// *http.ServeMux and chi.Router satisfy it; the library never asks
// for the concrete type, so callers wire whichever router they
// already use.
//
// Defined here (rather than in gone/crud/) so AuthSimple.Route and
// CRUDTable.Route share a single interface, breaking what would
// otherwise be an import cycle (crud → auth for Authz, auth → crud
// for Mux). gone/crud re-exports it as crud.Mux = auth.Mux for
// callers that learned the type by its old name.
type Mux interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

// PageShellFunc wraps a library component's output in the app's page
// chrome. It receives the HTTP writer and request directly — not a
// templ.Component to return — so the caller can write redirects,
// custom headers, or "anonymous → /login" responses from inside the
// shell.
//
// title is supplied by the component (CRUDTable's PageTitle, Admin's
// active-table DisplayName, AuthSimple's "Sign in") and is typically
// what the shell writes into <title> and any heading.
//
// content is the component-rendered body the shell embeds.
//
// nil shell on Route() means "don't register a page handler" —
// useful for tests and for fragment-only callers.
type PageShellFunc func(w http.ResponseWriter, r *http.Request, title string, content templ.Component)

// Auth is what every page handler and authz helper interacts with.
// AuthSimple (v1) and AuthGORM (v2) both implement it. Apps depend on
// the Auth interface, not the concrete impl — swapping happens by
// changing one constructor call.
//
// Notably absent: there is no "LoadUser" middleware (CurrentUser does
// the session lookup on demand) and no "Require" middleware. The page
// handler (or page shell) calls CurrentUser itself and decides:
// redirect to login, render an access-denied page, or render a
// redacted anonymous view. Auth exposes LoginURL(returnTo) so the
// handler can build the redirect target.
type Auth interface {
	// Route mounts the impl's login + logout handlers under baseUrl.
	// Each impl ships its own templates (simple form vs. multi-method
	// when AuthGORM lands). Returns the absolute urlBase the routes
	// were mounted under. shell wraps the login form in the app's
	// chrome; nil is allowed for tests / fragment-only callers.
	Route(mux Mux, baseUrl string, shell PageShellFunc) (string, error)

	// CurrentUser returns the user the session points to, or nil for
	// anonymous. Page handlers call this and decide their response.
	// Cheap enough to call multiple times per request — scs caches
	// the session payload in r.Context().
	CurrentUser(r *http.Request) User

	// LoginURL / LogoutURL build the URL to the respective endpoint,
	// encoding `next` as the "?next=..." query parameter. Empty next
	// returns just the path.
	//
	//   http.Redirect(w, r, a.LoginURL(r.URL.Path), http.StatusSeeOther)
	LoginURL(next string) string
	LogoutURL(next string) string

	// IsAuthPath reports whether path is one of the auth-managed
	// pages that must remain accessible to anonymous (or
	// partially-authenticated) users: the password page, any
	// staged-login step (e.g. /login/totp), etc. Page shells use
	// this to skip their "redirect anonymous to /login" guard so
	// the login flow itself isn't trapped by it.
	IsAuthPath(path string) bool

	// Login writes u into the session as the current user and rotates
	// the session ID (session-fixation defense). Logout destroys the
	// session. Useful for programmatic flows (tests, post-signup
	// auto-login).
	Login(ctx context.Context, u User) error
	Logout(ctx context.Context) error
}

// User exposes the subset of user state page handlers and authz
// helpers consult. Per-impl extras (PasswordHash for AuthSimple,
// credential IDs for AuthGORM passkeys) stay on the concrete impl's
// user type; callers type-assert when they need them.
//
// Passkey-only users may not have a meaningful Username/Email —
// Username() can return "" in those cases.
type User interface {
	Username() string
	Email() string
	Groups() []Group
	HasGroup(name string) bool
}

// Group is the named collection that AuthzLoggedInReadAdminWrite (and
// app-level authz impls) consult.
type Group interface {
	Name() string
}
