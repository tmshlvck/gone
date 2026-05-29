package crud

import (
	"errors"
	"net/http"
	"strings"

	"github.com/a-h/templ"
)

// Admin aggregates a set of CRUDTables under a single URL prefix with a
// sidebar that hx-boosts the active table into a working pane. Each
// sidebar click triggers a full-page swap (via HTMX hx-boost) — server
// re-renders the admin with the active highlight server-side, the
// browser updates the URL via hx-push-url, and back-button navigation
// works through HTMX's history cache.
//
// Whole-page swap (instead of "just the working area") keeps Admin
// minimal: no JS for the active highlight, no coordination with the
// child CRUDTables' own URL state (?page=, ?sort=, etc. live under
// /admin/{slug} and don't conflict with admin's navigation).
//
// Admin does NOT register the child tables' CRUD endpoints — the
// caller does that explicitly via each table's Route. Admin.Route only
// registers a redirect at GET prefix that lands the visitor on the
// first table. The per-slug page handler that wraps Admin.Render in
// the caller's page-shell is the caller's responsibility (the library
// has no <html> chrome).
type Admin struct {
	// Tables is the ordered list of CRUDTables the sidebar exposes,
	// top to bottom. The first one is the default landing on /admin.
	Tables []CRUDTableInterface

	// Authz gates Admin's own redirect endpoint. nil = AllowAll. Each
	// child table has its own Authz gating its own routes.
	Authz AuthzInterface

	// Slug is the path segment under which Admin is mounted. Default
	// "admin". Currently used only for documentation / future
	// multi-admin scenarios.
	Slug string

	// urlBase is the absolute prefix Admin was routed under (e.g.
	// "/admin"). Set by Route.
	urlBase string
}

// DeriveAdmin builds an Admin from a list of pre-derived CRUDTables.
// Tables can be derived from any backend (Map, GORM, future) — Admin
// works against the non-generic CRUDTableInterface.
//
// Relation wiring (MetaField.RelatedCRUD) is the caller's job in this
// variant — set it manually on each MetaField before passing the
// tables in. Use DeriveAdminAutoWire for the "auto-derive everything"
// shortcut.
func DeriveAdmin(tables []CRUDTableInterface, authz AuthzInterface) Admin {
	return Admin{
		Tables: tables,
		Authz:  authz,
		Slug:   "admin",
	}
}

// DeriveAdminAutoWire is like DeriveAdmin but additionally auto-wires
// every relation field's RelatedCRUD by matching the field's
// RelatedTypeName (the Go type name of the related struct) against
// each peer table's ModelName().
//
// The matching is purely name-based — passing two tables named "Hero"
// would produce ambiguous output (last write wins). In practice that
// doesn't happen because Go type names within one package are unique.
func DeriveAdminAutoWire(tables []CRUDTableInterface, authz AuthzInterface) Admin {
	for _, t := range tables {
		t.AutoWireRelations(tables)
	}
	return DeriveAdmin(tables, authz)
}

// Route registers GET prefix → 303 redirect to /{first table slug}.
// The caller's page handler at GET prefix/{slug} wraps Admin.Render in
// its page-shell; this redirect lands a visit to bare /admin on the
// first model so the sidebar isn't pointing at "no selection".
//
// If the table list is empty, Route is a no-op.
func (a *Admin) Route(mux Mux, prefix string) error {
	if mux == nil {
		return errors.New("nil mux")
	}
	a.urlBase = normalizePrefix(prefix)
	if len(a.Tables) == 0 {
		return nil
	}
	firstSlug := a.Tables[0].URLSlug()
	authz := authzOrAllow(a.Authz)
	// The pattern we register is the normalized base, or "/" if Admin
	// is mounted at root — ServeMux requires a non-empty path.
	pattern := a.urlBase
	if pattern == "" {
		pattern = "/"
	}
	mux.HandleFunc("GET "+pattern, func(w http.ResponseWriter, r *http.Request) {
		if !authz.CanList(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.Redirect(w, r, a.urlBase+"/"+firstSlug, http.StatusSeeOther)
	})
	return nil
}

// Render returns the Admin layout for the request URL. The active
// table is determined from r.URL.Path (the first segment after
// urlBase); its Render(r) output is embedded inline in the working
// area. The sidebar marks the matching entry as active.
//
// Sidebar links use hx-boost so clicks become fetch+body-swap
// (no full page reload), the URL updates via hx-push-url, and back-
// button works through HTMX's history cache.
func (a *Admin) Render(r *http.Request) (templ.Component, error) {
	activeSlug := a.activeSlug(r)
	entries := make([]AdminEntry, 0, len(a.Tables))
	var activeContent templ.Component
	for _, t := range a.Tables {
		active := t.URLSlug() == activeSlug
		entries = append(entries, AdminEntry{
			DisplayName: t.DisplayName(),
			URL:         a.urlBase + "/" + t.URLSlug(),
			Active:      active,
		})
		if active {
			c, err := t.Render(r)
			if err != nil {
				return nil, err
			}
			activeContent = c
		}
	}
	return AdminView(AdminViewData{
		Entries:       entries,
		ActiveContent: activeContent,
	}), nil
}

// activeSlug extracts the first path segment under urlBase from r.URL.
// Returns "" for the bare /admin URL or for any path that's not under
// urlBase (defensive — shouldn't happen if the caller routes correctly).
func (a *Admin) activeSlug(r *http.Request) string {
	p := strings.TrimPrefix(r.URL.Path, a.urlBase)
	p = strings.TrimPrefix(p, "/")
	if i := strings.Index(p, "/"); i >= 0 {
		p = p[:i]
	}
	return p
}

// AdminEntry is one row in the sidebar. Active is true on the entry
// whose slug matches the request URL — the templ adds menu-active to
// that link.
type AdminEntry struct {
	DisplayName string
	URL         string
	Active      bool
}

// AdminViewData carries the entries and the embedded active-table
// content into the AdminView templ.
type AdminViewData struct {
	Entries       []AdminEntry
	ActiveContent templ.Component // nil when no slug matches
}
