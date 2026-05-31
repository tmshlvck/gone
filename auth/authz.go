// Package auth holds the authentication interface (Auth) and its
// concrete implementations (AuthSimple, future AuthGORM), the
// authorization interface (Authz) plus stock implementations
// (AuthzAllowAll, AuthzDenyAll, AuthzLoggedIn, …), and the CSRF
// helpers. Everything security-related in one flat package.
//
// This file holds the Authz half — what gone/crud and friends
// consult before touching data. The Auth interface, User/Group
// interfaces, and AuthSimple live in other files of the same package.
package auth

import "net/http"

// Authz lets the application short-circuit access to gated routes.
// Each method receives *http.Request so the implementation can read
// user info from headers, cookies, or context — keeping the surface
// router-agnostic.
//
// Return true to permit, false to deny (the consumer responds with
// 403). Methods are named for CRUD operations because that's the
// dominant consumer today (gone/crud); they map naturally to JSON
// API verbs (GET list / GET item / POST / PUT / DELETE) as well.
//
// nil is treated as AuthzAllowAll by every consumer — wrap with
// AuthzOrAllow at the boundary if you want to call methods directly.
type Authz interface {
	CanList(r *http.Request) bool
	CanRead(r *http.Request) bool
	CanCreate(r *http.Request) bool
	CanUpdate(r *http.Request) bool
	CanDelete(r *http.Request) bool
}

// AuthzAllowAll is the no-op authz used when none is configured.
// Every check returns true. Exported as a named value so apps can
// be explicit ("Authz: auth.AuthzAllowAll{}") instead of relying on
// the nil convention.
type AuthzAllowAll struct{}

func (AuthzAllowAll) CanList(*http.Request) bool   { return true }
func (AuthzAllowAll) CanRead(*http.Request) bool   { return true }
func (AuthzAllowAll) CanCreate(*http.Request) bool { return true }
func (AuthzAllowAll) CanUpdate(*http.Request) bool { return true }
func (AuthzAllowAll) CanDelete(*http.Request) bool { return true }

// AuthzDenyAll is the symmetric helper — every check returns false.
// Useful for read-only views, tests, and "lock it down by default
// until I configure authz".
type AuthzDenyAll struct{}

func (AuthzDenyAll) CanList(*http.Request) bool   { return false }
func (AuthzDenyAll) CanRead(*http.Request) bool   { return false }
func (AuthzDenyAll) CanCreate(*http.Request) bool { return false }
func (AuthzDenyAll) CanUpdate(*http.Request) bool { return false }
func (AuthzDenyAll) CanDelete(*http.Request) bool { return false }

// AuthzOrAllow returns a non-nil Authz — either the supplied one or
// AuthzAllowAll. Consumers call this before invoking interface
// methods so the dispatch loop doesn't double-check nil.
//
//	auth.AuthzOrAllow(c.Authz).CanRead(r)
func AuthzOrAllow(a Authz) Authz {
	if a == nil {
		return AuthzAllowAll{}
	}
	return a
}

// AuthzLoggedIn permits every action iff the request bears an
// authenticated user. Anonymous requests are denied uniformly.
type AuthzLoggedIn struct {
	Auth Auth // any concrete impl (AuthSimple, future AuthGORM)
}

func (a AuthzLoggedIn) check(r *http.Request) bool {
	return a.Auth != nil && a.Auth.CurrentUser(r) != nil
}

func (a AuthzLoggedIn) CanList(r *http.Request) bool   { return a.check(r) }
func (a AuthzLoggedIn) CanRead(r *http.Request) bool   { return a.check(r) }
func (a AuthzLoggedIn) CanCreate(r *http.Request) bool { return a.check(r) }
func (a AuthzLoggedIn) CanUpdate(r *http.Request) bool { return a.check(r) }
func (a AuthzLoggedIn) CanDelete(r *http.Request) bool { return a.check(r) }

// AuthzLoggedInReadOnly: reads (CanList / CanRead) require login;
// writes (CanCreate / CanUpdate / CanDelete) always denied even for
// logged-in users. Use to expose data read-only behind login.
type AuthzLoggedInReadOnly struct {
	Auth Auth
}

func (a AuthzLoggedInReadOnly) check(r *http.Request) bool {
	return a.Auth != nil && a.Auth.CurrentUser(r) != nil
}

func (a AuthzLoggedInReadOnly) CanList(r *http.Request) bool { return a.check(r) }
func (a AuthzLoggedInReadOnly) CanRead(r *http.Request) bool { return a.check(r) }
func (AuthzLoggedInReadOnly) CanCreate(*http.Request) bool   { return false }
func (AuthzLoggedInReadOnly) CanUpdate(*http.Request) bool   { return false }
func (AuthzLoggedInReadOnly) CanDelete(*http.Request) bool   { return false }

// AuthzLoggedInReadAdminWrite: any logged-in user may read; only
// members of AdminGroup may write. AdminGroup defaults to "admin"
// when empty.
type AuthzLoggedInReadAdminWrite struct {
	Auth       Auth
	AdminGroup string // empty → "admin"
}

func (a AuthzLoggedInReadAdminWrite) user(r *http.Request) User {
	if a.Auth == nil {
		return nil
	}
	return a.Auth.CurrentUser(r)
}

func (a AuthzLoggedInReadAdminWrite) admin(r *http.Request) bool {
	u := a.user(r)
	if u == nil {
		return false
	}
	name := a.AdminGroup
	if name == "" {
		name = "admin"
	}
	return u.HasGroup(name)
}

func (a AuthzLoggedInReadAdminWrite) CanList(r *http.Request) bool   { return a.user(r) != nil }
func (a AuthzLoggedInReadAdminWrite) CanRead(r *http.Request) bool   { return a.user(r) != nil }
func (a AuthzLoggedInReadAdminWrite) CanCreate(r *http.Request) bool { return a.admin(r) }
func (a AuthzLoggedInReadAdminWrite) CanUpdate(r *http.Request) bool { return a.admin(r) }
func (a AuthzLoggedInReadAdminWrite) CanDelete(r *http.Request) bool { return a.admin(r) }
