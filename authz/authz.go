// Package authz defines the access-control interface every gone
// component consults before touching data, plus a couple of no-op
// implementations (AllowAll, DenyAll) and a nil-safe coercion helper.
//
// authz is intentionally a leaf-of-import-tree package: it depends
// only on net/http. gone/crud and (future) gone/auth and
// gone/jsonapi all import authz; authz imports none of them. RBAC
// types (Permission / Role / Grant / Resolver) will live in this
// package too — see PRD-AUTH §6.
package authz

import "net/http"

// Interface lets the application short-circuit access to gated
// routes. Each method receives *http.Request so the implementation
// can read user info from headers, cookies, or context — keeping
// the surface router-agnostic.
//
// Return true to permit, false to deny (the consumer responds with
// 403). Methods are named for CRUD operations because that's the
// dominant consumer today (gone/crud); they map naturally to JSON
// API verbs (GET list / GET item / POST / PUT / DELETE) as well.
//
// nil is treated as AllowAll by every consumer — wrap with OrAllow
// at the boundary if you want to call methods directly.
type Interface interface {
	CanList(r *http.Request) bool
	CanRead(r *http.Request) bool
	CanCreate(r *http.Request) bool
	CanUpdate(r *http.Request) bool
	CanDelete(r *http.Request) bool
}

// AllowAll is the no-op authz used when none is configured. Every
// check returns true. Exported as a named value so apps can be
// explicit ("Authz: authz.AllowAll{}") instead of relying on the
// nil convention.
type AllowAll struct{}

func (AllowAll) CanList(*http.Request) bool   { return true }
func (AllowAll) CanRead(*http.Request) bool   { return true }
func (AllowAll) CanCreate(*http.Request) bool { return true }
func (AllowAll) CanUpdate(*http.Request) bool { return true }
func (AllowAll) CanDelete(*http.Request) bool { return true }

// DenyAll is the symmetric helper — every check returns false.
// Useful for read-only views, tests, and "lock it down by default
// until I configure authz".
type DenyAll struct{}

func (DenyAll) CanList(*http.Request) bool   { return false }
func (DenyAll) CanRead(*http.Request) bool   { return false }
func (DenyAll) CanCreate(*http.Request) bool { return false }
func (DenyAll) CanUpdate(*http.Request) bool { return false }
func (DenyAll) CanDelete(*http.Request) bool { return false }

// OrAllow returns a non-nil Interface — either the supplied one or
// AllowAll. Consumers call this before invoking interface methods so
// the dispatch loop doesn't double-check nil.
//
//	authz.OrAllow(c.Authz).CanRead(r)
func OrAllow(a Interface) Interface {
	if a == nil {
		return AllowAll{}
	}
	return a
}
