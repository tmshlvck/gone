package crud

import "net/http"

// AuthzInterface lets the application short-circuit access to CRUD
// routes (both CRUDTable's per-action endpoints and MetaModel's
// single-instance form). nil = AllowAll. See PRD §6.5.
//
// Each method receives *http.Request so authz can look at headers,
// cookies, or context — keeping the surface router-agnostic. Return
// true to permit, false to deny (the handler returns 403).
type AuthzInterface interface {
	CanList(r *http.Request) bool
	CanRead(r *http.Request) bool
	CanCreate(r *http.Request) bool
	CanUpdate(r *http.Request) bool
	CanDelete(r *http.Request) bool
}

// AllowAll is the no-op authz used when none is configured. Every check
// returns true. Provided as a named value so apps can be explicit.
type AllowAll struct{}

func (AllowAll) CanList(*http.Request) bool   { return true }
func (AllowAll) CanRead(*http.Request) bool   { return true }
func (AllowAll) CanCreate(*http.Request) bool { return true }
func (AllowAll) CanUpdate(*http.Request) bool { return true }
func (AllowAll) CanDelete(*http.Request) bool { return true }

// authzOrAllow returns a non-nil AuthzInterface — either the supplied
// one or AllowAll. Used by handlers so the dispatch loop doesn't
// double-check nil.
func authzOrAllow(a AuthzInterface) AuthzInterface {
	if a == nil {
		return AllowAll{}
	}
	return a
}
