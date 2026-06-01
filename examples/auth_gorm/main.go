// Example: AuthGORM + CRUDAdmin over the User/Group tables.
//
// Demonstrates:
//   - auth.AuthGORM as a v2-style Auth impl backed by GORM
//   - GORM-derived CRUDTables for UserGORM and GroupGORM
//   - crud.Admin mounting both tables under /admin
//   - auth.AuthzLoggedInReadAdminWrite gating the tables: any
//     logged-in user reads; only the "admin" group writes.
//
// Seed: an `admin` group and an `admin / admin` user in it. Sign in
// with admin/admin to manage users and groups through the admin UI.
package main

import (
	"log"
	"net/http"
	"time"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2"
	"github.com/glebarez/sqlite"
	"github.com/tmshlvck/gone/auth"
	"github.com/tmshlvck/gone/crud"
	"gorm.io/gorm"
)

func main() {
	// ── Sessions ────────────────────────────────────────────────────
	sm := scs.New()
	sm.Lifetime = 24 * time.Hour
	sm.Cookie.HttpOnly = true
	sm.Cookie.SameSite = http.SameSiteLaxMode

	// ── DB + Auth ───────────────────────────────────────────────────
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		log.Fatalf("gorm: %v", err)
	}
	ag, err := auth.NewAuthGORM(sm, db) // auto-migrates UserGORM + GroupGORM
	if err != nil {
		log.Fatalf("NewAuthGORM: %v", err)
	}
	ag.AfterLogin = "/admin"

	// ── Seed admin / admin in admin group ───────────────────────────
	if err := ag.GroupAdd("admin"); err != nil {
		log.Fatalf("GroupAdd: %v", err)
	}
	if err := ag.UserAdd("admin", "admin@local", "admin"); err != nil {
		log.Fatalf("UserAdd: %v", err)
	}
	if err := ag.UserMod("admin", []string{"admin"}); err != nil {
		log.Fatalf("UserMod: %v", err)
	}

	// ── CRUD tables for User + Group ────────────────────────────────
	// Both tables use AuthzLoggedInReadAdminWrite: logged-in users
	// read, the admin group writes. Single-user demo so it behaves
	// like LoggedIn, but the gating works once you add non-admin
	// users via the UI.
	gate := auth.AuthzLoggedInReadAdminWrite{Auth: ag}

	userMM, err := crud.DeriveMetaModel[auth.UserGORM]()
	if err != nil {
		log.Fatalf("derive UserGORM: %v", err)
	}
	userMM.DisplayName = "Users"
	// PasswordHash is internal — don't render it in the admin (it
	// would leak the argon2id string + invite editing it directly).
	// Mark as Hidden so the table/form skip it; passwords are managed
	// through ag.Passwd(...) instead.
	userMM.MustFindField("PasswordHash").Hidden = true

	groupMM, err := crud.DeriveMetaModel[auth.GroupGORM]()
	if err != nil {
		log.Fatalf("derive GroupGORM: %v", err)
	}
	groupMM.DisplayName = "Groups"

	userTable := crud.DeriveGormCRUDTable[auth.UserGORM](userMM, gate, db)
	userTable.Slug = "users"
	groupTable := crud.DeriveGormCRUDTable[auth.GroupGORM](groupMM, gate, db)
	groupTable.Slug = "groups"

	// DeriveAdminAutoWire walks each table's relation fields and
	// matches their type name against peer ModelName() — wires up
	// the Groups picker on the User form, and the Users picker on
	// the Group form.
	//
	// Admin itself takes nil Authz: the index handler at /admin
	// just 303-redirects to the first child slug, and we want
	// anonymous users to hit the child handler (which goes through
	// pageShell and gets redirected to /login). With a strict gate
	// at Admin's index, anonymous users would see 403 instead.
	// The child CRUDTables keep their gates via `gate`.
	tables := []crud.CRUDTableInterface{&userTable, &groupTable}
	admin := crud.DeriveAdminAutoWire(tables, nil)

	// ── Page shell ──────────────────────────────────────────────────
	// One PageShellFunc serves /login + /admin. Anonymous requests
	// are redirected to /login unless they are the login page.
	loginPath := ag.LoginURL("")
	pageShell := func(w http.ResponseWriter, r *http.Request, title string, content templ.Component) {
		u := ag.CurrentUser(r)
		if u == nil && r.URL.Path != loginPath {
			http.Redirect(w, r, ag.LoginURL(r.URL.Path), http.StatusSeeOther)
			return
		}
		username := ""
		if u != nil {
			username = u.Username()
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		err := pageLayout(title, auth.CSRFToken(r.Context()), username, ag.LogoutURL(""), content).
			Render(r.Context(), w)
		if err != nil {
			log.Printf("render: %v", err)
		}
	}

	// ── Routing ─────────────────────────────────────────────────────
	mux := http.NewServeMux()
	if _, err := ag.Route(mux, "", pageShell); err != nil {
		log.Fatalf("auth route: %v", err)
	}
	adminURL, err := admin.Route(mux, "/", pageShell)
	if err != nil {
		log.Fatalf("admin route: %v", err)
	}
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, adminURL, http.StatusSeeOther)
	})

	// ── Middleware ──────────────────────────────────────────────────
	handler := sm.LoadAndSave(auth.CSRFWrap(sm)(mux))

	// ── Run ─────────────────────────────────────────────────────────
	addr := ":8080"
	log.Printf("auth_gorm listening on %s — login admin / admin, then open %s", addr, adminURL)
	log.Fatal(http.ListenAndServe(addr, handler))
}
