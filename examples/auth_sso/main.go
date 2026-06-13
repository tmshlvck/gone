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
	ag, err := auth.NewAuthGORM(sm, db)
	if err != nil {
		log.Fatalf("NewAuthGORM: %v", err)
	}
	ag.AfterLogin = "/admin"
	ag.RPDisplayName = "gone auth_sso demo"
	ag.RPID = "localhost"
	ag.RPOrigins = []string{"http://localhost:8080"}

	// ── Seed admin / admin in admin group ───────────────────────────
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

	// ── SSO providers (env-var driven) ──────────────────────────────
	// Each registration is conditional on the relevant env vars. With
	// none set the example behaves identically to auth_gorm — useful
	// for showing the "zero-config" baseline.
	baseURL := os.Getenv("BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	registerSSOProviders(ag, baseURL)

	// ── CRUD tables for User + Group ────────────────────────────────
	gate := auth.AuthzLoggedInReadAdminWrite{Auth: ag}

	userTable := crud.NewGormTable(db, crud.Table[auth.UserGORM]{
		Slug: "users", Title: "Users", Authz: gate,
		Fields: crud.Fields{
			"ID": {DisplayValue: func(mf crud.MetaField, value any) templ.Component {
				return userIDLink(fmt.Sprintf("%v", value), "users-modal-l1-body")
			}},
			// Secrets: write-only password box (re-hashes a non-blank entry),
			// and set/unset-only display for TOTP + the opaque WebAuthn handle.
			"PasswordHash": {
				Label:          "Password",
				InputType:      "password",
				DisplayValue:   crud.Redact,
				GenFormElement: crud.PasswordInput,
				BindStrings:    crud.HashWith(auth.HashPassword),
			},
			"TOTPSecret":     {Label: "TOTP", ReadOnly: true, DisplayValue: crud.Redact},
			"WebAuthnHandle": {ReadOnly: true, DisplayValue: crud.Redact},
			// SSO-Only flag + linked identities, with helpful admin copy.
			"SSOOnly": {
				Label: "SSO-Only",
				Help:  "When checked, this user can sign in only via a linked SSO identity (and optional TOTP). Password change and passkey enrolment are disabled. Cleared automatically when an admin un-checks this box.",
			},
			"SSOIdentities": {
				Label:    "Linked SSO identities",
				Help:     "Read-only. Users unlink their own identities from their account page; admins delete the user wholesale to remove all links at once.",
				ReadOnly: true,
			},
		},
	})
	groupTable := crud.NewGormTable(db, crud.Table[auth.GroupGORM]{
		Slug: "groups", Title: "Groups", Authz: gate,
	})

	tables := []crud.CRUDTableInterface{&userTable, &groupTable}
	admin := crud.DeriveAdmin(tables, nil)

	// ── Page shell ──────────────────────────────────────────────────
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

	// ── Routing ─────────────────────────────────────────────────────
	mux := chi.NewRouter()
	if err := ag.RegisterRoutes(mux, "", pageShell); err != nil {
		log.Fatalf("auth route: %v", err)
	}
	mux.Route("/admin", func(r chi.Router) {
		if err := admin.RegisterRoutes(r, "/admin", pageShell); err != nil {
			log.Fatalf("admin route: %v", err)
		}
	})
	mux.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	})

	// ── Middleware ──────────────────────────────────────────────────
	handler := sm.LoadAndSave(auth.CSRFWrap(sm)(mux))

	// ── Run ─────────────────────────────────────────────────────────
	addr := ":8080"
	log.Printf("auth_sso listening on %s — login admin / admin, or via SSO if configured", addr)
	log.Printf("admin URL: /admin")
	log.Fatal(http.ListenAndServe(addr, handler))
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
