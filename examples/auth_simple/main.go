// Example: crud_mem + AuthSimple. The /heroes CRUD table is gated by
// the page shell — anonymous visitors are redirected to /login.
// Login: admin / admin.
package main

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2"
	"github.com/go-chi/chi/v5"
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

// deriveHeroesTable builds the configured CRUDTable[Hero] in three steps:
// metadata, data plane, table config. az gates every route.
func deriveHeroesTable(store map[uint]Hero, mu *sync.RWMutex, az auth.Authz) crud.CRUDTable[Hero] {
	mm := crud.DeriveMetaModel[Hero](crud.MetaModel[Hero]{
		DisplayName: "Heroes",
		Fields: []crud.MetaField{
			{Name: "ID", ReadOnly: true},
			{Name: "Name", FormHelp: "Display name, 2–30 characters.", FieldValidate: crud.All(crud.NotEmpty, crud.MinLen(2), crud.MaxLen(30))},
			{Name: "Realm", FormHelp: "Origin (e.g. Gondor, Mirkwood).", FieldValidate: crud.All(crud.NotEmpty, crud.MaxLen(40))},
			{Name: "Power", FormHelp: "Power level, 0–100.", FieldValidate: crud.IntRange(0, 100)},
		},
	})
	return crud.NewTable(mm, crud.MapAccessor(mm, store, mu), 10, az)
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
	// AuthzLoggedInReadAdminWrite: logged-in users read, admin group
	// writes. Every AuthSimple user is implicitly in "admin", so this
	// behaves like AuthzLoggedIn for this single-user demo.
	table := deriveHeroesTable(store, &mu, auth.AuthzLoggedInReadAdminWrite{Auth: sa})

	// ── Page shell ──────────────────────────────────────────────────
	// One PageShellFunc serves both the login page and the protected
	// /heroes routes. Anonymous requests are redirected to /login,
	// except when they're already on an auth-managed page (login,
	// or any future staged-login step). sa.IsAuthPath knows which
	// ones to skip so the login flow doesn't bounce itself.
	pageShell := func(w http.ResponseWriter, r *http.Request, title string, content templ.Component) {
		u := sa.CurrentUser(r)
		if u == nil && !sa.IsAuthPath(r.URL.Path) {
			http.Redirect(w, r, sa.LoginURL(r.URL.Path), http.StatusSeeOther)
			return
		}
		username := ""
		if u != nil {
			username = u.Username()
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		err := pageLayout(title, auth.CSRFToken(r.Context()), username, sa.LogoutURL(""), content).
			Render(r.Context(), w)
		if err != nil {
			log.Printf("render: %v", err)
		}
	}

	// ── Routing ─────────────────────────────────────────────────────
	mux := chi.NewRouter()
	if err := sa.RegisterRoutes(mux, "", pageShell); err != nil {
		log.Fatalf("auth route: %v", err)
	}
	// The library registers the table's fragment endpoints; the app owns
	// the page route, embedding table.Render(r) in pageShell.
	const heroesPath = "/heroes"
	table.RegisterRoutes(mux, "", heroesPath)
	mux.Get(heroesPath, func(w http.ResponseWriter, r *http.Request) {
		content, err := table.Render(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		pageShell(w, r, "Heroes", content)
	})
	mux.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, table.URLBase(), http.StatusSeeOther)
	})

	// ── Middleware ──────────────────────────────────────────────────
	// scs LoadAndSave is the outermost wrapper; auth.CSRFWrap runs
	// inside it.
	handler := sm.LoadAndSave(auth.CSRFWrap(sm)(mux))

	// ── Run ─────────────────────────────────────────────────────────
	addr := ":8080"
	log.Printf("auth_simple listening on %s — login admin / admin", addr)
	log.Fatal(http.ListenAndServe(addr, handler))
}
