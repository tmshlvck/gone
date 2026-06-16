// Example: AuthGORM + SSO (OIDC + OAuth2) + CRUDAdmin.
//
// Builds on examples/auth_gorm but registers SSO providers from
// environment variables. The login page renders a "Sign in with X"
// button for every provider that has its env vars set; with no env
// vars the page is identical to auth_gorm.
//
// Supported providers (all optional):
//
//	GOOGLE_CLIENT_ID + GOOGLE_CLIENT_SECRET     — auth.GoogleProvider
//	GITHUB_CLIENT_ID + GITHUB_CLIENT_SECRET     — auth.GitHubProvider
//	OKTA_DOMAIN + OKTA_CLIENT_ID + OKTA_CLIENT_SECRET — auth.OktaProvider
//
// One more env var:
//
//	BASE_URL — public origin of this example (default
//	           "http://localhost:8080"). Used to compute each provider's
//	           redirect URL: {BASE_URL}/login/sso/{name}/callback. This
//	           MUST match the URL you register with the IdP, exactly.
//
// See README.md for OAuth-app registration steps on each provider.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
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
	// Always store time.Time in UTC, on any backend. Call once, before writes.
	if err := site.ForceUTC(db); err != nil {
		log.Fatalf("ForceUTC: %v", err)
	}
	ag, err := auth.NewAuthGORM(sm, db)
	if err != nil {
		log.Fatalf("NewAuthGORM: %v", err)
	}
	ag.AfterLogin = "/admin"
	ag.RPDisplayName = "gone auth_sso demo"
	ag.RPID = "localhost"
	ag.RPOrigins = []string{"http://localhost:8080"}

	if err := ag.GroupAdd("admin"); err != nil {
		log.Fatalf("GroupAdd: %v", err)
	}
	if err := ag.GroupAdd("users"); err != nil {
		log.Fatalf("GroupAdd users: %v", err)
	}
	if err := ag.UserAdd("admin", "admin@local", "admin"); err != nil {
		log.Fatalf("UserAdd: %v", err)
	}
	if err := ag.UserMod("admin", []string{"admin"}); err != nil {
		log.Fatalf("UserMod: %v", err)
	}

	// Each provider is registered only if its env vars are set; with none set
	// this behaves like auth_gorm.
	baseURL := os.Getenv("BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	registerSSOProviders(ag, baseURL)

	gate := auth.AuthzLoggedInReadAdminWrite{Auth: ag}

	userMM := crud.DeriveMetaModel[auth.UserGORM](crud.MetaModel[auth.UserGORM]{
		DisplayName: "Users",
		Fields: []crud.MetaField{
			// The L1 modal body id derives from the table's component path
			// ("/admin/users" → "admin-users-modal-l1-body").
			{Name: "ID", DisplayValue: func(mf crud.MetaField, value any) templ.Component {
				return userIDLink(fmt.Sprintf("%v", value), "admin-users-modal-l1-body")
			}},
			// Write-only password box (re-hashes a non-blank entry); TOTP +
			// passkey fields are hidden (managed from the account page).
			{Name: "PasswordHash", DisplayName: "Password", FormInputType: "password",
				DisplayValue:   crud.Redact,
				GenFormElement: crud.PasswordInput,
				BindStrings:    crud.HashWith(auth.HashPassword)},
			{Name: "TOTPSecret", Hidden: true},
			{Name: "WebAuthnHandle", Hidden: true},
			{Name: "Passkeys", Hidden: true},
			// SSO-Only flag + linked identities, with helpful admin copy.
			{Name: "SSOOnly", DisplayName: "SSO-Only",
				FormHelp: "When checked, this user can sign in only via a linked SSO identity (and optional TOTP). Password change and passkey enrolment are disabled. Cleared automatically when an admin un-checks this box."},
			{Name: "SSOIdentities", DisplayName: "Linked SSO identities",
				FormHelp: "Read-only. Users unlink their own identities from their account page; admins delete the user wholesale to remove all links at once.",
				ReadOnly: true},
		},
	})
	userTable := crud.NewTable(userMM, crud.GORMAccessor(userMM, db), site.DefaultSettings{}, gate)
	userTable.Segment = "users" // irregular plural of "UserGORM"

	groupMM := crud.DeriveMetaModel[auth.GroupGORM](crud.MetaModel[auth.GroupGORM]{DisplayName: "Groups"})
	groupTable := crud.NewTable(groupMM, crud.GORMAccessor(groupMM, db), site.DefaultSettings{}, gate)
	groupTable.Segment = "groups"

	admin := crud.DeriveAdmin([]crud.CRUDTableInterface{&userTable, &groupTable}, nil)

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

	handler := sm.LoadAndSave(auth.CSRFWrap(sm)(mux))
	log.Printf("auth_sso listening on :8080 — login admin / admin (or via SSO if configured), then open /admin")
	log.Fatal(http.ListenAndServe(":8080", handler))
}

// registerSSOProviders inspects environment variables and registers
// each provider whose required vars are set. Misconfigurations (e.g.
// only one half of a client-id/secret pair) are fatal — better to
// fail at startup than silently disable the button.
func registerSSOProviders(ag *auth.AuthGORM, baseURL string) {
	if id, secret := os.Getenv("GOOGLE_CLIENT_ID"), os.Getenv("GOOGLE_CLIENT_SECRET"); id != "" || secret != "" {
		mustHaveBoth("GOOGLE_CLIENT_ID", id, "GOOGLE_CLIENT_SECRET", secret)
		p := auth.GoogleProvider(id, secret, baseURL+"/login/sso/google/callback")
		// Google has no groups claim — assign every new sign-in to
		// the "users" group via DefaultGroups, then promote
		// individually as needed via the admin UI.
		p.DefaultGroups = []string{"users"}
		if err := ag.AddOIDCProvider(p); err != nil {
			log.Fatalf("AddOIDCProvider(google): %v", err)
		}
		log.Printf("SSO: Google configured")
	}
	if id, secret := os.Getenv("GITHUB_CLIENT_ID"), os.Getenv("GITHUB_CLIENT_SECRET"); id != "" || secret != "" {
		mustHaveBoth("GITHUB_CLIENT_ID", id, "GITHUB_CLIENT_SECRET", secret)
		p := auth.GitHubProvider(id, secret, baseURL+"/login/sso/github/callback")
		p.DefaultGroups = []string{"users"}
		if err := ag.AddOAuth2Provider(p); err != nil {
			log.Fatalf("AddOAuth2Provider(github): %v", err)
		}
		log.Printf("SSO: GitHub configured")
	}
	if domain, id, secret := os.Getenv("OKTA_DOMAIN"), os.Getenv("OKTA_CLIENT_ID"), os.Getenv("OKTA_CLIENT_SECRET"); domain != "" || id != "" || secret != "" {
		if domain == "" || id == "" || secret == "" {
			log.Fatalf("OKTA_DOMAIN + OKTA_CLIENT_ID + OKTA_CLIENT_SECRET must all be set together")
		}
		p := auth.OktaProvider(domain, id, secret, baseURL+"/login/sso/okta/callback")
		// Okta's typical setup: enable the groups claim in the
		// authorization server, then we read it here. CreateGroups
		// off — the admin controls what groups exist.
		p.GroupsClaim = "groups"
		p.DefaultGroups = []string{"users"}
		// Okta is typically a trusted IdP — auto-link by email so
		// users with pre-existing local accounts can adopt the SSO
		// flow without an admin step.
		p.AutoLinkByEmail = true
		if err := ag.AddOIDCProvider(p); err != nil {
			log.Fatalf("AddOIDCProvider(okta): %v", err)
		}
		log.Printf("SSO: Okta (%s) configured", domain)
	}
}

func mustHaveBoth(nameA, valA, nameB, valB string) {
	if valA == "" || valB == "" {
		log.Fatalf("%s and %s must both be set", nameA, nameB)
	}
}
