package auth

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/a-h/templ"
	"github.com/alexedwards/argon2id"
	"github.com/alexedwards/scs/v2"
)

// adminGroupName is the single group every AuthSimple user is implicitly
// a member of. AuthzLoggedInReadAdminWrite (default AdminGroup="admin")
// permits writes for any logged-in AuthSimple user.
const adminGroupName = "admin"

const userSessionKey = "auth:username"

// AuthSimple is the v1 Auth implementation. Users live in memory,
// configured by code at startup via UserAdd / UserDel / Passwd.
// Passwords are argon2id-hashed at rest (PHC-encoded — same string
// format AuthGORM will use, so the hash representation doesn't have
// to migrate when the GORM backend lands).
//
// AuthSimple is a quick-and-dirty fixture for prototypes and tests —
// no password change UI, no email verification, no account
// management page. Reach for AuthGORM (when it lands) once you need
// real user management.
type AuthSimple struct {
	// Sessions is the scs manager used to read/write the session
	// payload. Required.
	Sessions *scs.SessionManager

	// AfterLogin is where POST /login redirects when the form's
	// "next" hidden field is empty or unsafe. Default "/".
	AfterLogin string

	mu    sync.RWMutex
	users map[string]*UserSimple

	// Populated by Route().
	urlBase    string
	loginPath  string
	logoutPath string
}

// NewAuthSimple builds an AuthSimple bound to sm. Add users via
// UserAdd before mounting it on a router.
func NewAuthSimple(sm *scs.SessionManager) *AuthSimple {
	if sm == nil {
		panic("auth.NewAuthSimple: nil session manager")
	}
	return &AuthSimple{
		Sessions:   sm,
		AfterLogin: "/",
		users:      make(map[string]*UserSimple),
	}
}

// ──────────────────────────────────────────────────────────────────
// Concrete configuration methods — NOT on the Auth interface.

// ErrUserExists is returned by UserAdd when the username is already
// registered.
var ErrUserExists = errors.New("auth: user already exists")

// ErrUserNotFound is returned by UserDel / Passwd when the named user
// doesn't exist.
var ErrUserNotFound = errors.New("auth: user not found")

// ErrEmptyUsername is returned when an empty username is passed to a
// mutating method. Empty usernames would break session lookup.
var ErrEmptyUsername = errors.New("auth: empty username")

// ErrInvalidPassword is returned by Authenticate when the username
// exists but the supplied password doesn't match. Kept distinct from
// ErrUserNotFound so callers can log / instrument the two cases
// separately, but HTTP handlers should map both to the same generic
// "invalid credentials" response to prevent username enumeration.
var ErrInvalidPassword = errors.New("auth: invalid password")

// UserAdd creates a user with the given email and password. The
// password is argon2id-hashed before storage (PHC-encoded). Every
// AuthSimple user is implicitly a member of the "admin" group.
//
// Returns ErrUserExists if a user with the same username is already
// registered, or ErrEmptyUsername if username == "".
func (s *AuthSimple) UserAdd(username, email, password string) error {
	if username == "" {
		return ErrEmptyUsername
	}
	hash, err := argon2id.CreateHash(password, argon2id.DefaultParams)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.users[username]; exists {
		return ErrUserExists
	}
	s.users[username] = &UserSimple{
		username: username,
		email:    email,
		hash:     hash,
	}
	return nil
}

// UserDel removes the named user. Returns ErrUserNotFound if absent.
// Active sessions for the removed user are not destroyed
// automatically — CurrentUser will return nil for them on next
// request (the username lookup fails).
func (s *AuthSimple) UserDel(username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.users[username]; !exists {
		return ErrUserNotFound
	}
	delete(s.users, username)
	return nil
}

// Passwd replaces the named user's password. The new password is
// re-hashed. Returns ErrUserNotFound if absent.
func (s *AuthSimple) Passwd(username, password string) error {
	hash, err := argon2id.CreateHash(password, argon2id.DefaultParams)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, exists := s.users[username]
	if !exists {
		return ErrUserNotFound
	}
	u.hash = hash
	return nil
}

// ──────────────────────────────────────────────────────────────────
// Auth interface methods.

// CurrentUser reads the username out of the session and looks the
// user up. Returns nil for anonymous requests AND for sessions whose
// user has since been deleted via UserDel.
//
// Returning the interface (auth.User) is what makes Auth pluggable.
// Returning a typed *UserSimple as a nil-interface would be a footgun
// for callers that compare against nil — guarded explicitly here.
func (s *AuthSimple) CurrentUser(r *http.Request) User {
	username := s.Sessions.GetString(r.Context(), userSessionKey)
	if username == "" {
		return nil
	}
	s.mu.RLock()
	u := s.users[username]
	s.mu.RUnlock()
	if u == nil {
		return nil
	}
	return u
}

// LoginURL returns the login path with `next` encoded as ?next=…
// query parameter. An empty next returns just the path. Falls back
// to "/login" if Route hasn't been called yet (handy for tests).
func (s *AuthSimple) LoginURL(next string) string {
	path := s.loginPath
	if path == "" {
		path = "/login"
	}
	if next == "" {
		return path
	}
	return path + "?next=" + url.QueryEscape(next)
}

// IsAuthPath reports whether path is the AuthSimple login endpoint.
// AuthSimple has no staged-login step, so only loginPath qualifies.
func (s *AuthSimple) IsAuthPath(path string) bool {
	if s.loginPath != "" {
		return path == s.loginPath
	}
	return path == "/login"
}

// LogoutURL is the symmetric helper for the logout endpoint.
func (s *AuthSimple) LogoutURL(next string) string {
	path := s.logoutPath
	if path == "" {
		path = "/logout"
	}
	if next == "" {
		return path
	}
	return path + "?next=" + url.QueryEscape(next)
}

// Login rotates the session ID (defeats session-fixation), writes
// the username into the session, and rotates the CSRF token. Callers
// pass any User whose Username() matches a registered AuthSimple
// user — typically the value returned by Authenticate inside the
// POST /login handler, or a UserSimple constructed by hand in tests.
func (s *AuthSimple) Login(ctx context.Context, u User) error {
	if u == nil || u.Username() == "" {
		return ErrEmptyUsername
	}
	if err := s.Sessions.RenewToken(ctx); err != nil {
		return err
	}
	s.Sessions.Put(ctx, userSessionKey, u.Username())
	rotateCSRF(ctx, s.Sessions)
	return nil
}

// Logout destroys the session. Returns the underlying scs error if
// the session can't be torn down (rare; typically a misconfigured
// store).
func (s *AuthSimple) Logout(ctx context.Context) error {
	return s.Sessions.Destroy(ctx)
}

// Authenticate checks username + password against the registered
// users. Returns ErrUserNotFound for an unknown username and
// ErrInvalidPassword for a wrong password. The two branches keep
// distinct error classes for telemetry / logging but HTTP handlers
// should map both to the same generic "invalid credentials" response
// to prevent username enumeration.
//
// Exported so apps can drive login programmatically (e.g. an API
// endpoint) without going through the form handler.
func (s *AuthSimple) Authenticate(username, password string) (User, error) {
	s.mu.RLock()
	u := s.users[username]
	s.mu.RUnlock()
	if u == nil {
		return nil, ErrUserNotFound
	}
	match, err := argon2id.ComparePasswordAndHash(password, u.hash)
	if err != nil {
		return nil, err
	}
	if !match {
		return nil, ErrInvalidPassword
	}
	return u, nil
}

// Route mounts GET/POST /login + POST /logout under baseUrl. shell
// wraps the GET /login form in the app's chrome — when nil, the form
// renders as a bare fragment (useful for tests).
//
// Registered patterns (baseUrl="" / "/"):
//
//	GET    /login   render login form (reads ?next=… for the redirect target)
//	POST   /login   authenticate + Login + redirect to next or AfterLogin
//	POST   /logout  Logout + redirect to LoginURL("")
//
// Returns the absolute urlBase the routes were mounted under.
func (s *AuthSimple) Route(mux Mux, baseUrl string, shell PageShellFunc) (string, error) {
	if mux == nil {
		return "", errors.New("auth.AuthSimple.Route: nil mux")
	}
	base := normalizeAuthPrefix(baseUrl)
	s.urlBase = base
	s.loginPath = base + "/login"
	s.logoutPath = base + "/logout"
	mountPasswordLogin(mux, passwordLoginOpts{
		LoginPath:    s.loginPath,
		LogoutPath:   s.logoutPath,
		AfterLogin:   func() string { return s.AfterLogin },
		Authenticate: s.Authenticate,
		// AuthSimple has no TOTP stage: just complete the login.
		Login: func(ctx context.Context, u User, _ string) (string, error) {
			return "", s.Login(ctx, u)
		},
		Logout:   s.Logout,
		LoginURL: s.LoginURL,
		Shell:    shell,
		// AuthSimple has no passkeys — leaving these empty keeps the
		// "Use passkey" button out of the login form.
	})
	return s.urlBase, nil
}

// passwordLoginOpts is the data + closures the shared
// mountPasswordLogin helper needs. Both AuthSimple.Route and
// AuthGORM.Route fill one of these and delegate — the login form
// and POST handling are identical for v1 (single-method password).
// AuthGORM will likely fork its own Route once passkeys / SSO land.
//
// AfterLogin is a getter, not a string, so the caller's field can
// still be mutated after Route() and the next login picks up the
// new value.
type passwordLoginOpts struct {
	LoginPath, LogoutPath string
	AfterLogin            func() string
	Authenticate          func(username, password string) (User, error)
	// Login completes (or stages) the sign-in. The returned override
	// is a redirect URL the helper uses INSTEAD of the user-supplied
	// next / AfterLogin; "" means "use the default redirect". AuthGORM
	// uses the override to inject a TOTP-step page between password
	// and full sign-in; AuthSimple wraps its Login and returns "".
	Login    func(ctx context.Context, u User, formNext string) (override string, err error)
	Logout   func(ctx context.Context) error
	LoginURL func(next string) string
	Shell    PageShellFunc

	// PasskeyOptionsPath + PasskeyFinishPath wire the "Use passkey"
	// button into the rendered login form. When both are empty the
	// button + the conditional-UI script are omitted entirely.
	// AuthSimple leaves them empty (no passkeys); AuthGORM fills
	// them only when RP fields are configured.
	PasskeyOptionsPath string
	PasskeyFinishPath  string
}

func mountPasswordLogin(mux Mux, o passwordLoginOpts) {
	mux.HandleFunc("GET "+o.LoginPath, func(w http.ResponseWriter, r *http.Request) {
		next := safeNext(r.URL.Query().Get("next"))
		body := loginForm(loginFormData{
			Action:          o.LoginPath,
			Next:            next,
			CSRFToken:       CSRFToken(r.Context()),
			PasskeysEnabled: o.PasskeyOptionsPath != "" && o.PasskeyFinishPath != "",
			PasskeyOptions:  o.PasskeyOptionsPath,
			PasskeyFinish:   o.PasskeyFinishPath,
		})
		writeShell(w, r, "Sign in", body, o.Shell)
	})

	mux.HandleFunc("POST "+o.LoginPath, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		username := r.PostFormValue("username")
		password := r.PostFormValue("password")
		next := safeNext(r.PostFormValue("next"))

		u, err := o.Authenticate(username, password)
		if err != nil {
			body := loginForm(loginFormData{
				Action:          o.LoginPath,
				Next:            next,
				CSRFToken:       CSRFToken(r.Context()),
				Error:           "Invalid username or password.",
				Username:        username,
				PasskeysEnabled: o.PasskeyOptionsPath != "" && o.PasskeyFinishPath != "",
				PasskeyOptions:  o.PasskeyOptionsPath,
				PasskeyFinish:   o.PasskeyFinishPath,
			})
			w.WriteHeader(http.StatusUnauthorized)
			writeShell(w, r, "Sign in", body, o.Shell)
			return
		}
		override, err := o.Login(r.Context(), u, next)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		dest := override
		if dest == "" {
			dest = next
		}
		if dest == "" {
			dest = o.AfterLogin()
		}
		http.Redirect(w, r, dest, http.StatusSeeOther)
	})

	mux.HandleFunc("POST "+o.LogoutPath, func(w http.ResponseWriter, r *http.Request) {
		if err := o.Logout(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		next := safeNext(r.URL.Query().Get("next"))
		if next == "" {
			next = safeNext(r.PostFormValue("next"))
		}
		if next == "" {
			next = o.LoginURL("")
		}
		http.Redirect(w, r, next, http.StatusSeeOther)
	})
}

// writeShell renders body through shell, or as a bare fragment when
// shell is nil. The fragment path is for tests / non-page callers.
func writeShell(w http.ResponseWriter, r *http.Request, title string, body templ.Component, shell PageShellFunc) {
	if shell != nil {
		shell(w, r, title, body)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := body.Render(r.Context(), w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ──────────────────────────────────────────────────────────────────
// UserSimple + GroupSimple — exported concrete types that satisfy
// auth.User and auth.Group. Fields are unexported so the bcrypt
// hash stays internal and external code goes through the methods.

// UserSimple is AuthSimple's User implementation. hash is the
// PHC-encoded argon2id string produced by argon2id.CreateHash.
type UserSimple struct {
	username string
	email    string
	hash     string
}

// Username returns the user's username.
func (u *UserSimple) Username() string { return u.username }

// Email returns the user's email (empty if not set).
func (u *UserSimple) Email() string { return u.email }

// Groups returns the single hardcoded "admin" group every AuthSimple
// user belongs to.
func (u *UserSimple) Groups() []Group {
	return []Group{adminGroup}
}

// HasGroup reports whether u is a member of the named group. For
// AuthSimple, only "admin" returns true.
func (u *UserSimple) HasGroup(name string) bool {
	return name == adminGroupName
}

// GroupSimple is AuthSimple's Group implementation. Single instance
// (the "admin" group); GroupSimple values are not meant to be
// constructed by callers.
type GroupSimple struct {
	name string
}

// Name returns the group's name.
func (g *GroupSimple) Name() string { return g.name }

var adminGroup = &GroupSimple{name: adminGroupName}

// ──────────────────────────────────────────────────────────────────
// Local helpers.

// normalizeAuthPrefix is the auth-package twin of crud.normalizePrefix
// (unexported there). Same rules: "" / "/" → "" (root); strip trailing
// slash; otherwise return as-is.
func normalizeAuthPrefix(p string) string {
	if p == "/" {
		return ""
	}
	return strings.TrimRight(p, "/")
}

// safeNext validates the redirect target. Open-redirect risk: if POST
// /login redirects to an attacker-supplied next, we'd be an OAuth-style
// stepping stone. Only allow same-origin paths: must start with "/" and
// not "//" (which would be host-relative).
func safeNext(next string) string {
	if next == "" {
		return ""
	}
	if !strings.HasPrefix(next, "/") {
		return ""
	}
	if strings.HasPrefix(next, "//") {
		return ""
	}
	return next
}
