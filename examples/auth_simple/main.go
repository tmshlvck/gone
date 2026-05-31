// Example: crud_mem + AuthSimple. The /heroes CRUD table is gated by
// a page shell — anonymous visitors are redirected to /login.
// Login: admin / admin.
package main

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2"
	"github.com/tmshlvck/gone/auth"
	"github.com/tmshlvck/gone/crud"
)

type Hero struct {
	ID     uint
	Name   string
	Realm  string
	Power  int
	Active bool
}

// seedHeroes returns a small fixed roster so pagination has something
// to chew on. Trimmed from crud_mem's set — the auth wrapper is the
// point here.
func seedHeroes() map[uint]Hero {
	rows := []Hero{
		{Name: "Aragorn", Realm: "Gondor", Power: 90, Active: true},
		{Name: "Legolas", Realm: "Mirkwood", Power: 85, Active: true},
		{Name: "Gandalf", Realm: "Middle-earth", Power: 99, Active: true},
		{Name: "Frodo", Realm: "Shire", Power: 40, Active: true},
		{Name: "Samwise", Realm: "Shire", Power: 55, Active: true},
		{Name: "Gimli", Realm: "Erebor", Power: 75, Active: true},
		{Name: "Galadriel", Realm: "Lothlórien", Power: 95, Active: true},
		{Name: "Elrond", Realm: "Rivendell", Power: 92, Active: true},
		{Name: "Éowyn", Realm: "Rohan", Power: 80, Active: true},
		{Name: "Faramir", Realm: "Gondor", Power: 72, Active: true},
		{Name: "Treebeard", Realm: "Fangorn", Power: 88, Active: true},
		{Name: "Tom Bombadil", Realm: "Old Forest", Power: 97, Active: true},
	}
	store := make(map[uint]Hero, len(rows))
	for i, h := range rows {
		id := uint(i + 1)
		h.ID = id
		store[id] = h
	}
	return store
}

// deriveHeroesTable builds the configured CRUDTable[Hero]. Pulled out
// so the test reuses one source of truth for the table shape.
func deriveHeroesTable(store map[uint]Hero, mu *sync.RWMutex, az auth.Authz) crud.CRUDTable[Hero] {
	mm, err := crud.DeriveMetaModel[Hero]()
	if err != nil {
		log.Fatalf("derive: %v", err)
	}
	mm.DisplayName = "Heroes"
	{
		f := mm.MustFindField("ID")
		f.ReadOnly = true
		f.Sortable = true
	}
	{
		f := mm.MustFindField("Name")
		f.FormHelp = "Display name, 2–30 characters."
		f.FieldValidate = crud.All(crud.NotEmpty, crud.MinLen(2), crud.MaxLen(30))
	}
	{
		f := mm.MustFindField("Realm")
		f.FormHelp = "Origin (e.g. Gondor, Mirkwood)."
		f.FieldValidate = crud.All(crud.NotEmpty, crud.MaxLen(40))
	}
	{
		f := mm.MustFindField("Power")
		f.FormHelp = "Power level, 0–100."
		f.FieldValidate = crud.IntRange(0, 100)
	}
	table := crud.DeriveMapCRUDTable[Hero](mm, az, store, mu)
	table.Slug = "heroes"
	table.PageSize = 10
	return table
}

// protectedShell wraps the library's component output in app chrome
// and redirects anonymous requests to /login. The closure captures sa
// so the user badge + logout form can render the right CSRF token /
// logout URL.
func protectedShell(sa *auth.AuthSimple) auth.PageShellFunc {
	return func(w http.ResponseWriter, r *http.Request, title string, content templ.Component) {
		u := sa.CurrentUser(r)
		if u == nil {
			http.Redirect(w, r, sa.LoginURL(r.URL.Path), http.StatusSeeOther)
			return
		}
		badge := &userBadge{
			Username:   u.Username(),
			LogoutPath: sa.LogoutURL(""),
			CSRFToken:  auth.CSRFToken(r.Context()),
		}
		renderPage(w, r, title, badge, content)
	}
}

// loginShell skips the auth gate (the login page IS the anonymous
// landing) but still emits page chrome with the CSRF meta so any
// post-login HTMX requests work without an extra round-trip.
func loginShell(w http.ResponseWriter, r *http.Request, title string, content templ.Component) {
	renderPage(w, r, title, nil, content)
}

// renderPage emits the body. CSRF token is pulled from r.Context()
// where auth.CSRFWrap stashed it.
func renderPage(w http.ResponseWriter, r *http.Request, title string, badge *userBadge, content templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageLayout(title, auth.CSRFToken(r.Context()), badge, content).Render(r.Context(), w); err != nil {
		log.Printf("render: %v", err)
	}
}

func main() {
	// ── Seed ────────────────────────────────────────────────────────
	store := seedHeroes()
	var mu sync.RWMutex

	// ── Sessions ────────────────────────────────────────────────────
	sm := scs.New()
	sm.Lifetime = 24 * time.Hour
	sm.Cookie.HttpOnly = true
	sm.Cookie.SameSite = http.SameSiteLaxMode

	// ── Auth ────────────────────────────────────────────────────────
	sa := auth.NewAuthSimple(sm)
	sa.AfterLogin = "/heroes"
	if err := sa.UserAdd("admin", "admin@local", "admin"); err != nil {
		log.Fatalf("UserAdd: %v", err)
	}

	// ── CRUD table ──────────────────────────────────────────────────
	// AuthzLoggedInReadAdminWrite: any logged-in user reads, admin
	// group writes. Every AuthSimple user is implicitly in "admin",
	// so for this single-user demo the writer-side check is effectively
	// "you must be logged in"; the helper is there to demonstrate the
	// admin-write convention with real teeth.
	table := deriveHeroesTable(store, &mu, auth.AuthzLoggedInReadAdminWrite{Auth: sa})

	// ── Routing ─────────────────────────────────────────────────────
	mux := http.NewServeMux()
	if _, err := sa.Route(mux, "", loginShell); err != nil {
		log.Fatalf("auth route: %v", err)
	}
	heroesURL, err := table.Route(mux, "", protectedShell(sa))
	if err != nil {
		log.Fatalf("table route: %v", err)
	}
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, heroesURL, http.StatusSeeOther)
	})

	// ── Middleware ──────────────────────────────────────────────────
	// scs LoadAndSave is the outermost wrapper; auth.CSRFWrap runs
	// inside it (CSRF reads/writes the session that LoadAndSave
	// provides).
	handler := sm.LoadAndSave(auth.CSRFWrap(sm)(mux))

	// ── Run ─────────────────────────────────────────────────────────
	addr := ":8080"
	log.Printf("auth_simple listening on %s — login admin / admin", addr)
	log.Fatal(http.ListenAndServe(addr, handler))
}
