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
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2"
	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/tmshlvck/gone/auth"
	"github.com/tmshlvck/gone/crud"
	"github.com/tmshlvck/gone/site"
	"gorm.io/gorm"
)

func main() {
	sm := scs.New()
	sm.Lifetime = 24 * time.Hour
	sm.Cookie.HttpOnly = true
	sm.Cookie.SameSite = http.SameSiteLaxMode

	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		log.Fatalf("gorm: %v", err)
	}
	// Always store time.Time in UTC, on any backend. Call once, before any
	// writes (incl. the user/group seeding below).
	if err := site.ForceUTC(db); err != nil {
		log.Fatalf("ForceUTC: %v", err)
	}
	ag, err := auth.NewAuthGORM(sm, db) // auto-migrates UserGORM + GroupGORM
	if err != nil {
		log.Fatalf("NewAuthGORM: %v", err)
	}
	ag.AfterLogin = "/admin"
	// WebAuthn relying-party config (passkeys). RPID is the bare host; origin
	// must match the URL bar exactly. Set both to your real hostname in prod.
	ag.RPDisplayName = "gone auth_gorm demo"
	ag.RPID = "localhost"
	ag.RPOrigins = []string{"http://localhost:8080"}

	// Seed admin / admin in the admin group.
	if err := ag.GroupAdd("admin"); err != nil {
		log.Fatalf("GroupAdd: %v", err)
	}
	if err := ag.UserAdd("admin", "admin@local", "admin"); err != nil {
		log.Fatalf("UserAdd: %v", err)
	}
	if err := ag.UserMod("admin", []string{"admin"}); err != nil {
		log.Fatalf("UserMod: %v", err)
	}

	// Both tables: logged-in users read, the admin group writes.
	gate := auth.AuthzLoggedInReadAdminWrite{Auth: ag}

	userMM := crud.DeriveMetaModel[auth.UserGORM](crud.MetaModel[auth.UserGORM]{
		DisplayName: "Users",
		Fields: []crud.MetaField{
			// Clickable ID column: a button that GETs /account/{id} into the
			// table's L1 modal body (id derived from the component path,
			// "/admin/users" → "admin-users-modal-l1-body") to change a password.
			{Name: "ID", DisplayValue: func(mf crud.MetaField, value any) templ.Component {
				return userIDLink(fmt.Sprintf("%v", value), "admin-users-modal-l1-body")
			}},
			// Write-only password box: a non-blank entry is re-hashed, a blank
			// one keeps the current hash.
			{Name: "PasswordHash", DisplayName: "Password", FormInputType: "password",
				DisplayValue:   crud.Redact,
				GenFormElement: crud.PasswordInput,
				BindStrings:    crud.HashWith(auth.HashPassword)},
			// TOTP + passkeys are managed from the account page — hide here.
			{Name: "TOTPSecret", Hidden: true},
			{Name: "WebAuthnHandle", Hidden: true},
			{Name: "Passkeys", Hidden: true},
		},
	})
	userTable := crud.NewTable(userMM, crud.GORMAccessor(userMM, db), 0, gate)
	userTable.Segment = "users" // irregular plural of "UserGORM"

	groupMM := crud.DeriveMetaModel[auth.GroupGORM](crud.MetaModel[auth.GroupGORM]{DisplayName: "Groups"})
	groupTable := crud.NewTable(groupMM, crud.GORMAccessor(groupMM, db), 0, gate)
	groupTable.Segment = "groups"

	// Admin takes nil Authz so its /admin index just 303s anonymous users to
	// the first child, which then redirects to /login via pageShell. The child
	// tables keep their own `gate`. Relations are auto-wired at RegisterRoutes.
	admin := crud.DeriveAdmin([]crud.CRUDTableInterface{&userTable, &groupTable}, nil)

	// One shell serves /login, /login/totp, and /admin. Anonymous requests
	// redirect to /login unless already on an auth page (IsAuthPath) — else
	// the staged TOTP login would bounce itself.
	pageShell := func(w http.ResponseWriter, r *http.Request, title string, content templ.Component) {
		u := ag.CurrentUser(r)
		if u == nil && !ag.IsAuthPath(r.URL.Path) {
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

	mux := chi.NewRouter()
	mux.Use(middleware.Logger)
	if err := ag.RegisterRoutes(mux, "", pageShell); err != nil {
		log.Fatalf("auth route: %v", err)
	}
	if err := admin.RegisterRoutes(mux, "", "/admin", pageShell); err != nil {
		log.Fatalf("admin route: %v", err)
	}
	mux.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	})

	// App-owned preferences page embedding the library's account-security
	// cards via auth.AccountSection. No "{ref}" param on this route, so the
	// section resolves to the signed-in user (self-service). The library
	// mounts the password/TOTP/passkey/SSO endpoints the cards point at; we
	// only own the page chrome and any app-specific cards.
	mux.Get("/preferences", func(w http.ResponseWriter, r *http.Request) {
		cards, target, res := ag.AccountSection(r)
		switch res {
		case auth.AccountAnonymous:
			http.Redirect(w, r, ag.LoginURL(r.URL.Path), http.StatusSeeOther)
			return
		case auth.AccountForbidden:
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		case auth.AccountNotFound:
			http.NotFound(w, r)
			return
		}
		pageShell(w, r, "Preferences", preferencesPage(target.Username, cards))
	})

	handler := sm.LoadAndSave(auth.CSRFWrap(sm)(mux))
	log.Printf("auth_gorm listening on :8080 — login admin / admin, then open /admin")
	log.Fatal(http.ListenAndServe(":8080", handler))
}
