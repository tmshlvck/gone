package crud

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/a-h/templ"
)

// Admin aggregates a set of CRUDTables under a single URL prefix with a
// sidebar that HTMX-swaps the active table into a working pane. It does
// NOT register the child tables' CRUD endpoints — the caller does that
// explicitly via each table's Route, so data accessors stay off the
// Admin signature and tables can be mounted under arbitrary prefixes.
//
// Admin.Route only registers the sidebar HTMX swap endpoints
// (GET prefix/{slug}) and the index sidebar URL (GET prefix). The
// caller's page handler at /admin (or wherever) renders the page-shell
// around Admin.Render.
type Admin struct {
	// Tables is the ordered list of CRUDTables the sidebar exposes,
	// top to bottom. Slugs must be unique.
	Tables []CRUDTableInterface

	// Authz gates the index / sidebar-swap endpoints. nil = AllowAll.
	// (Each child table has its own Authz, gating its own routes.)
	Authz AuthzInterface

	// Slug is the path segment under which Admin is mounted, used for
	// the index id and the sidebar HTMX targets. Default "admin".
	Slug string

	// urlBase is the absolute prefix Admin was routed under (e.g.
	// "/admin"). Set by Route.
	urlBase string

	// workingAreaID is the id of the <div> Admin renders into which
	// sidebar links swap their target table's Render output. Derived
	// from Slug so multiple Admins can coexist on a page.
	workingAreaID string
}

// DeriveAdmin builds an Admin from a list of pre-derived CRUDTables.
// Tables can be derived from any backend (Map, GORM, future) — Admin
// works against the non-generic CRUDTableInterface.
func DeriveAdmin(tables []CRUDTableInterface, authz AuthzInterface) Admin {
	return Admin{
		Tables: tables,
		Authz:  authz,
		Slug:   "admin",
	}
}

// Route registers the sidebar HTMX swap endpoints under prefix.
// prefix + "" is reserved for the caller's index page (page-shell
// wraps Admin.Render). prefix + "/{slug}" returns the matching
// CRUDTable's Render output, for sidebar-link hx-get.
func (a *Admin) Route(mux Mux, prefix string) error {
	if mux == nil {
		return errors.New("nil mux")
	}
	a.urlBase = prefix
	a.workingAreaID = a.Slug + "-working-area"
	authz := authzOrAllow(a.Authz)

	for _, t := range a.Tables {
		t := t // capture
		slug := t.URLSlug()
		if slug == "" {
			return fmt.Errorf("Admin: table %q has empty URLSlug", t.DisplayName())
		}
		mux.HandleFunc("GET "+prefix+"/"+slug, func(w http.ResponseWriter, r *http.Request) {
			if !authz.CanList(r) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			comp, err := t.Render(r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeFragment(w, r, http.StatusOK, comp)
		})
	}
	return nil
}

// Render returns the Admin layout: sidebar + working-area placeholder.
// The caller wraps this in its own page-shell. Sidebar links hx-get the
// matching {slug} URL into the working area, so navigation between
// tables happens without a full page reload.
func (a *Admin) Render(r *http.Request) (templ.Component, error) {
	entries := make([]AdminEntry, 0, len(a.Tables))
	for _, t := range a.Tables {
		entries = append(entries, AdminEntry{
			DisplayName: t.DisplayName(),
			URL:         a.urlBase + "/" + t.URLSlug(),
		})
	}
	return AdminView(AdminViewData{
		Entries:       entries,
		WorkingAreaID: a.workingAreaID,
	}), nil
}

// AdminEntry is one row in the sidebar — the data the AdminView templ
// needs to render a single link. Built from the underlying table's
// DisplayName and URLSlug.
type AdminEntry struct {
	DisplayName string
	URL         string
}

// AdminViewData carries the entries and the working-area id into the
// AdminView templ.
type AdminViewData struct {
	Entries       []AdminEntry
	WorkingAreaID string
}
