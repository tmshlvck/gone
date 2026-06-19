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
// sidebar. The sidebar is an ordered list of elements — CRUDTables, custom
// Links, Headers and Separators in any order (see SidebarElementInterface).
// Navigation is plain page navigation (every clickable entry is a real <a
// href>): the server re-renders the whole page on each load and marks the
// active entry from the request path, so there's no JS or active-state
// coordination.
//
// Admin.RegisterRoutes registers, on the router it is handed: every child
// table's fragment endpoints, a page handler that wraps the active table in
// the app's shell, and a GET-index redirect to the first table. It also links
// the children's relation fields (see WireRelations). The app supplies the
// page shell; the library has no <html> chrome. Only CRUDTables are routed;
// Links/Headers/Separators are render-only (the app owns the Links' URLs).
type Admin struct {
	// Elements is the ordered sidebar, top to bottom: CRUDTables, custom
	// Links, Headers and Separators interleaved in any order. Only the
	// CRUDTables are routed (and relation-wired); the rest are render-only.
	// The first CRUDTable is the default landing on the {base} index.
	Elements []SidebarElementInterface

	// Authz gates Admin's own redirect and page endpoints. nil = AllowAll.
	// Each child table has its own Authz gating its own routes.
	Authz auth.Authz

	// urlBase is the absolute prefix Admin was routed under (e.g.
	// "/admin"). Set by RegisterRoutes.
	urlBase string
}

// SidebarElementInterface is the surface Admin's sidebar renders each entry
// from: a display label and an absolute URL. The (DisplayName, URLBase) pair
// encodes the entry kind:
//
//	URLBase != ""                       → a link (a CRUDTable or a custom Link)
//	URLBase == "" && DisplayName != ""  → a header / group label (not clickable)
//	both ""                             → a separator (<hr>)
//
// *CRUDTable[T] satisfies it (CRUDTableInterface embeds it); Link, Header and
// Separator build the lightweight non-table elements.
type SidebarElementInterface interface {
	DisplayName() string
	URLBase() string
}

// sidebarElement is the concrete non-table sidebar entry. Build one with
// Link, Header or Separator rather than setting the fields directly.
type sidebarElement struct {
	name string
	url  string
}

func (e sidebarElement) DisplayName() string { return e.name }
func (e sidebarElement) URLBase() string     { return e.url }

// Link is a custom sidebar entry pointing at an app-owned URL. Clicking it is
// plain page navigation (a real <a href>): the app's handler at url renders
// the destination — either a full page that replaces the Admin view (or an
// external site), or, to keep the Admin frame, by calling Admin.Render(r,
// content) with its own working-area content. In the latter case the sidebar
// highlights this Link, matching url against the request path.
func Link(displayName, url string) SidebarElementInterface {
	return sidebarElement{name: displayName, url: url}
}

// Header is a non-clickable group label rendered in the sidebar.
func Header(displayName string) SidebarElementInterface {
	return sidebarElement{name: displayName}
}

// Separator is a thin horizontal divider (<hr>) in the sidebar.
func Separator() SidebarElementInterface {
	return sidebarElement{}
}

// DeriveAdmin builds an Admin from an ordered list of sidebar elements —
// CRUDTables (derived from any backend: Map, GORM, future), custom Links,
// Headers and Separators, in any order. Tables are matched against the
// non-generic CRUDTableInterface.
//
// Cross-table relation links are wired automatically at RegisterRoutes time
// (Admin calls WireRelations once every child has its URLBase) by matching
// each relation field's RelatedTypeName against the managed tables' Go type
// names — no manual wiring. The matching is name-based; two tables of the
// same Go type would be ambiguous (last write wins), which doesn't happen
// for distinct types in one package.
func DeriveAdmin(elements []SidebarElementInterface, az auth.Authz) Admin {
	return Admin{
		Elements: elements,
		Authz:    az,
	}
}

// RegisterRoutes mounts Admin on r, composing every path relative to r — no
// stripping chi.Route needed, just the root mux:
//
//	admin.RegisterRoutes(root, "", "/admin", pageShell)
//
// routerPrefix is the absolute path at which r is served ("" for the root
// mux); componentPath is where Admin sits relative to r (e.g. "/admin"). Each
// child table is mounted at componentPath + "/" + a lowercased plural of the
// model name (Hero→"heros"). Non-table elements (Links/Headers/Separators)
// are not routed — the app owns the Links' URLs.
//
// Registers, for componentPath="/admin", tables ["heros","weapons","skills"]:
//
//	GET  /admin                        → 303 to /admin/heros (index redirect)
//	GET  /admin/heros                  → page (shell wrapping sidebar + heros table)
//	GET  /admin/weapons | /skills      → page (active table)
//	GET  /admin/heros/view, /create…   → each child table's fragment endpoints
//
// shell == nil → no page handler is registered (the index redirect and child
// fragments still are). Child relation fields are linked via WireRelations
// once all children are routed.
func (a *Admin) RegisterRoutes(r chi.Router, routerPrefix, componentPath string, shell site.Shell) error {
	base := "/" + strings.Trim(componentPath, "/")
	a.urlBase = normalizePrefix(routerPrefix) + base
	// Mount each CRUDTable under {base}/{slug}, relative to r, where slug is a
	// derived plural of the model name (Hero→"heros"). Children's page
	// handlers are not registered — Admin owns the page route. Non-table
	// elements are skipped.
	var tables []CRUDTableInterface
	for _, el := range a.Elements {
		t, ok := el.(CRUDTableInterface)
		if !ok {
			continue
		}
		if t.ModelName() == "" {
			return fmt.Errorf("Admin: table %q has empty ModelName", t.DisplayName())
		}
		t.RegisterRoutes(r, routerPrefix, base+"/"+defaultSlug(t.ModelName()))
		tables = append(tables, t)
	}
	if len(tables) == 0 {
		return nil
	}
	// Every child now has its URLBase set — link relation fields across them
	// (Go type name → URLBase) so relation pickers fetch options from the
	// right sibling table.
	WireRelations(tables...)
	az := auth.AuthzOrAllow(a.Authz)
	// Captured after the mount loop, so URLBase is set. The first table is the
	// default landing for the bare {base} index.
	firstURL := tables[0].URLBase()
	// Index redirect: GET {base} → first table.
	r.Get(base, func(w http.ResponseWriter, req *http.Request) {
		if !az.CanList(req) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.Redirect(w, req, firstURL, http.StatusSeeOther)
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
			title := "Admin"
			if t := a.activeTable(req); t != nil {
				title = t.DisplayName()
			}
			body, err := a.Render(req, nil)
			if failInternal(w, err) {
				return
			}
			shell(w, req, title, body)
		})
	}
	return nil
}

// Render returns the Admin layout for the request URL. The sidebar lists every
// element in order, marking the one whose URLBase matches r.URL.Path active.
// The working area shows content when non-nil (a custom-link module supplying
// its own content); otherwise the active CRUDTable renders itself. Every
// sidebar URL is the element's own URLBase — Admin treats it as authoritative.
//
// Entries are plain links (real MPA navigation); the server re-renders the
// whole page on each load and marks the active entry from the request path.
func (a *Admin) Render(r *http.Request, content templ.Component) (templ.Component, error) {
	activeURL := a.activeURL(r)
	entries := make([]AdminEntry, 0, len(a.Elements))
	for _, el := range a.Elements {
		url := el.URLBase()
		entries = append(entries, AdminEntry{
			DisplayName: el.DisplayName(),
			URL:         url,
			Active:      url != "" && url == activeURL,
		})
	}
	// Caller-supplied content (a custom-link module) wins; otherwise the
	// active table renders itself into the working area.
	activeContent := content
	if activeContent == nil {
		if t := a.activeTable(r); t != nil {
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

// activeURL returns the URLBase of the element that owns r.URL.Path — the
// clickable entry mounted at, or owning a sub-path of, the request URL. Match
// is by longest URLBase prefix, with a "/" boundary so /admin/users doesn't
// swallow a sibling at /admin/users-roles. "" when nothing matches (e.g. the
// bare {base} index before its redirect fires).
func (a *Admin) activeURL(r *http.Request) string {
	best := ""
	for _, el := range a.Elements {
		b := el.URLBase()
		if b == "" {
			continue
		}
		if r.URL.Path == b || strings.HasPrefix(r.URL.Path, b+"/") {
			if len(b) > len(best) {
				best = b
			}
		}
	}
	return best
}

// activeTable returns the child table that owns r.URL.Path, by the same
// longest-URLBase-prefix match as activeURL but restricted to CRUDTables. nil
// when no table matches (the bare index, or a custom-link page).
func (a *Admin) activeTable(r *http.Request) CRUDTableInterface {
	var best CRUDTableInterface
	bestLen := -1
	for _, el := range a.Elements {
		t, ok := el.(CRUDTableInterface)
		if !ok {
			continue
		}
		b := t.URLBase()
		if r.URL.Path == b || strings.HasPrefix(r.URL.Path, b+"/") {
			if len(b) > bestLen {
				best, bestLen = t, len(b)
			}
		}
	}
	return best
}

// AdminEntry is one row in the sidebar. The (DisplayName, URL) pair encodes
// the kind (link / header / separator — see SidebarElementInterface); Active
// is true on the entry whose URL matches the request, so the templ adds
// menu-active to that link.
type AdminEntry struct {
	DisplayName string
	URL         string
	Active      bool
}

// AdminViewData carries the ordered sidebar entries and the embedded
// working-area content into the AdminView templ.
type AdminViewData struct {
	Entries       []AdminEntry
	ActiveContent templ.Component // nil when nothing matches
}
