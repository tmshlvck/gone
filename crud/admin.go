package crud

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/tmshlvck/gone/auth"
	"github.com/tmshlvck/gone/site"
)

// Admin aggregates a set of CRUDTables under a single URL prefix with a
// sidebar. Navigation between tables is plain page navigation (each sidebar
// entry is a real link to /{mountBase}/{slug}); the server renders the whole
// page on each load and marks the active entry from the request path, so no
// JS or active-state coordination is needed.
//
// Admin.RegisterRoutes registers, on the router it is handed: every child
// table's fragment endpoints, a per-slug page handler that wraps the active
// table in the app's shell, and a GET-index redirect to the first table. It
// also links the children's relation fields (see WireRelations). The app
// supplies the page shell; the library has no <html> chrome.
type Admin struct {
	// Tables is the ordered list of CRUDTables the sidebar exposes,
	// top to bottom. The first one is the default landing on /admin.
	Tables []CRUDTableInterface

	// Authz gates Admin's own redirect endpoint. nil = AllowAll. Each
	// child table has its own Authz gating its own routes.
	Authz auth.Authz

	// SidebarTop / SidebarBottom are optional app-defined links that
	// render above / below the table entries. Clicking one HTMX-swaps
	// the response into the working area (#crud-admin-main) instead
	// of the whole admin root, so the response should be a *fragment*
	// of the content area (the app's handler returns whatever should
	// occupy the right pane).
	//
	// The app is responsible for handling the URLs. The library doesn't
	// auto-route them — they can point at any path the mux already
	// covers.
	SidebarTop    []SidebarLink
	SidebarBottom []SidebarLink

	// urlBase is the absolute prefix Admin was routed under (e.g.
	// "/admin"). Set by Route.
	urlBase string
}

// SidebarLink is one custom entry in Admin's sidebar — a static link
// at the top or bottom of the menu. Clicking it HTMX-fetches URL and
// swaps the response into the working area; the address bar updates
// via hx-push-url, so reloading the page or navigating directly to
// URL also works (the app's handler decides whether to return a
// fragment or a full page based on HX-Request).
type SidebarLink struct {
	DisplayName string
	URL         string
	// Separator: render a thin divider above this link. Use for
	// grouping ("Custom" section after the models, etc.) or as a
	// dummy entry on its own (DisplayName / URL both empty) for a
	// pure visual break.
	Separator bool
}

// DeriveAdmin builds an Admin from a list of pre-derived CRUDTables.
// Tables can be derived from any backend (Map, GORM, future) — Admin
// works against the non-generic CRUDTableInterface.
//
// Cross-table relation links are wired automatically at RegisterRoutes time
// (Admin calls WireRelations once every child has its URLBase) by matching
// each relation field's RelatedTypeName against the managed tables' Go type
// names — no manual wiring. The matching is name-based; two tables of the
// same Go type would be ambiguous (last write wins), which doesn't happen
// for distinct types in one package.
func DeriveAdmin(tables []CRUDTableInterface, az auth.Authz) Admin {
	return Admin{
		Tables: tables,
		Authz:  az,
	}
}

// RegisterRoutes mounts Admin on r, composing every path relative to r — no
// stripping chi.Route needed, just the root mux:
//
//	admin.RegisterRoutes(root, "", "/admin", pageShell)
//
// routerPrefix is the absolute path at which r is served ("" for the root
// mux); componentPath is where Admin sits relative to r (e.g. "/admin"). Each
// child table is mounted at componentPath + "/" + its URLSlug (a lowercased
// plural of the model name, or its Segment override).
//
// Registers, for componentPath="/admin", tables ["heroes","weapons","skills"]:
//
//	GET  /admin                        → 303 to /admin/heroes (index redirect)
//	GET  /admin/heroes                 → page (shell wrapping sidebar + heroes table)
//	GET  /admin/weapons | /skills      → page (active table)
//	GET  /admin/heroes/view, /create…  → each child table's fragment endpoints
//
// shell == nil → no per-slug page handler is registered (the index redirect
// and child fragments still are). Child relation fields are linked via
// WireRelations once all children are routed.
func (a *Admin) RegisterRoutes(r chi.Router, routerPrefix, componentPath string, shell site.Shell) error {
	base := "/" + strings.Trim(componentPath, "/")
	a.urlBase = normalizePrefix(routerPrefix) + base
	if len(a.Tables) == 0 {
		return nil
	}
	// Mount each child under {base}/{slug}, relative to r. Children's page
	// handlers are not registered — Admin owns the page route.
	for _, t := range a.Tables {
		if t.URLSlug() == "" {
			return fmt.Errorf("Admin: table %q has empty URLSlug", t.DisplayName())
		}
		t.RegisterRoutes(r, routerPrefix, base+"/"+t.URLSlug())
	}
	// Every child now has its URLBase set — link relation fields across
	// them (Go type name → URLBase) so relation pickers fetch options from
	// the right sibling table.
	WireRelations(a.Tables...)
	az := auth.AuthzOrAllow(a.Authz)
	firstSlug := a.Tables[0].URLSlug()
	// Index redirect: GET {base} → first table.
	r.Get(base, func(w http.ResponseWriter, req *http.Request) {
		if !az.CanList(req) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.Redirect(w, req, a.urlBase+"/"+firstSlug, http.StatusSeeOther)
	})
	// Per-slug page handler — registered only when the caller supplied a
	// shell. Wraps Admin.Render in the shell with the active table's
	// DisplayName as the page title.
	if shell != nil {
		r.Get(base+"/{slug}", func(w http.ResponseWriter, req *http.Request) {
			if !az.CanList(req) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			activeSlug := chi.URLParam(req, "slug")
			title := "Admin"
			for _, t := range a.Tables {
				if t.URLSlug() == activeSlug {
					title = t.DisplayName()
					break
				}
			}
			body, err := a.Render(req)
			if failInternal(w, err) {
				return
			}
			shell(w, req, title, body)
		})
	}
	return nil
}

// Render returns the Admin layout for the request URL. The active
// table is determined from r.URL.Path (the first segment after
// urlBase); its Render(r) output is embedded inline in the working
// area. The sidebar marks the matching entry as active.
//
// Sidebar entries are plain links (real MPA navigation); the server
// re-renders the whole page on each load and marks the active entry
// from the request path.
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
		TopLinks:      a.SidebarTop,
		BottomLinks:   a.SidebarBottom,
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
	TopLinks      []SidebarLink   // rendered above Entries
	BottomLinks   []SidebarLink   // rendered below Entries
}
