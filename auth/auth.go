package auth

import (
	"context"

	"github.com/go-chi/chi/v5"
	"github.com/tmshlvck/gone/site"
)

// Relative auth route patterns. RegisterRoutes registers these on the
// router the caller hands it, so auth composes under any mount (root, a
// stripping chi.Route/Mount, or a group) exactly like crud's tables. The
// absolute form — recorded in the impl's *Path fields and used for links,
// redirects, LoginURL and IsAuthPath — is mountBase + the pattern.
const (
	pathLogin          = "/login"
	pathLogout         = "/logout"
	pathTOTPLogin      = "/login/totp"
	pathPasskeyOptions = "/login/passkey/options"
	pathPasskeyFinish  = "/login/passkey/finish"
	pathSSO            = "/login/sso"
)

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
	// RegisterRoutes mounts the impl's login + logout (and, for AuthGORM,
	// TOTP / passkey / SSO / account) handlers on r, at paths RELATIVE to
	// r. mountBase is the absolute prefix at which r is served (the caller
	// knows it; chi can't report it at registration time) — recorded so
	// LoginURL, redirects, and rendered form actions resolve absolutely,
	// even behind a stripping mount. shell wraps the login form in the
	// app's chrome; nil is allowed for tests / fragment-only callers.
	RegisterRoutes(r chi.Router, mountBase string, shell site.Shell) error

	// CurrentUser returns the user the session points to, or nil for
	// anonymous. Page handlers call this (CurrentUser(r.Context())) and
	// decide their response. Takes only the ctx — the session payload
	// rides in it (scs caches it there), so no *http.Request is needed.
	// Cheap enough to call multiple times per request.
	CurrentUser(ctx context.Context) User

	// CurrentUsername returns the username the session points to, or ""
	// for anonymous (or a ctx with no loaded session — e.g. a background
	// job). Unlike CurrentUser it does NO user lookup — it's the session
	// string verbatim — so it's the right tool for code that only needs
	// an identity label, such as an Accessor audit hook: c.Data sees
	// r.Context(), and the session payload rides along in it. CurrentUser
	// is implemented on top of it.
	CurrentUsername(ctx context.Context) string

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
