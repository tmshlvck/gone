# How-to — bearer-token API keys for an app's JSON API

This guide shows how to give an app **per-user API keys** that
authenticate requests to its own JSON API (e.g. endpoints served with
[huma](https://github.com/danielgtaylor/huma)) via
`Authorization: Bearer <key>`, while reusing `gone/auth`'s existing
users, groups, and `Authz` policy — and how to surface key management
on the user's preferences page next to the library's account-security
cards.

> **Status.** Everything below is **app-side**: it works today against
> the current `gone/auth` API without any library changes. A future
> library feature (see [`TODO.md`](TODO.md) §1, *API keys (AuthGORM)*)
> will move the model, storage, verification, and the account-page card
> into `gone/auth` behind an `EnableAPIKeys` flag. Until then, the app
> owns those pieces; the seams this guide uses
> (`auth.UserGORMAdapter`, `AuthGORM.DB`, `AuthGORM.AccountSection`)
> are stable. When the library feature lands, the app-owned model +
> card collapse into a flag and a built-in card; the wiring in
> [step 2](#step-2--hook-the-key-back-into-authz) stays the same.

## What we are (and aren't) doing

- **In scope:** bearer auth for the app's *own* REST/JSON endpoints.
- **Out of scope:** the `crud/` and `auth/` routes. Those are
  browser, cookie-session surfaces; they neither need nor accept API
  keys. Keep keys off them entirely.

The design in one line: **a bearer key resolves to the owning
`auth.User`, which we drop into the request context so the *same*
`Authz` checks the rest of gone uses apply unchanged.** No second
permission model.

```
Authorization: Bearer gone_a1b2c3d4_…   ──▶  validate (hash → lookup)
                                          ──▶  owning auth.User into ctx
                                          ──▶  AuthzLoggedInReadAdminWrite
                                              (sees the user, allows/denies)
```

## Step 1 — the key model + verification (app-side)

A bearer key is high-entropy, so a fast hash (SHA-256) is sufficient —
unlike a password, there's nothing to brute-force. Store only the hash;
show the raw key to the user **once**, at creation.

```go
// apikey.go (in your app)
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/tmshlvck/gone/auth"
	"gorm.io/gorm"
)

type APIKey struct {
	ID         uint   `gorm:"primarykey"`
	UserID     uint   `gorm:"index;not null"` // effective principal
	HashedKey  string `gorm:"uniqueIndex"`    // sha256(raw), hex; never the raw key
	Prefix     string `gorm:"index;size:12"`  // shown in the UI to identify a key
	Name       string // user-supplied label
	LastUsedAt *time.Time
	ExpiresAt  *time.Time // nil = no expiry
	Disabled   bool
	CreatedAt  time.Time
}

var ErrInvalidAPIKey = errors.New("invalid api key")

// KeyStore wraps the same *gorm.DB AuthGORM uses (ag.DB), so keys live
// alongside users in one database.
type KeyStore struct{ DB *gorm.DB }

func NewKeyStore(db *gorm.DB) (*KeyStore, error) {
	if err := db.AutoMigrate(&APIKey{}); err != nil {
		return nil, err
	}
	return &KeyStore{DB: db}, nil
}

// Issue mints a new key for userID and returns the RAW key — the only
// time it's ever available. Persist nothing but its hash.
func (s *KeyStore) Issue(userID uint, name string, expires *time.Time) (raw string, err error) {
	buf := make([]byte, 24)
	if _, err = rand.Read(buf); err != nil {
		return "", err
	}
	secret := base64.RawURLEncoding.EncodeToString(buf)
	prefix := secret[:8]
	raw = "gone_" + prefix + "_" + secret[8:]

	sum := sha256.Sum256([]byte(raw))
	return raw, s.DB.Create(&APIKey{
		UserID:    userID,
		HashedKey: hex.EncodeToString(sum[:]),
		Prefix:    prefix,
		Name:      name,
		ExpiresAt: expires,
	}).Error
}

// Validate hashes the presented key, finds the matching non-disabled,
// non-expired row, bumps LastUsedAt, and returns the OWNING user as an
// auth.User — so the app's existing Authz checks apply unchanged.
func (s *KeyStore) Validate(raw string) (auth.User, error) {
	if !strings.HasPrefix(raw, "gone_") {
		return nil, ErrInvalidAPIKey
	}
	sum := sha256.Sum256([]byte(raw))
	hashed := hex.EncodeToString(sum[:])

	var k APIKey
	if err := s.DB.Where("hashed_key = ? AND disabled = ?", hashed, false).
		First(&k).Error; err != nil {
		return nil, ErrInvalidAPIKey
	}
	if k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt) {
		return nil, ErrInvalidAPIKey
	}

	// The owning user — Preload("Groups") so HasGroup works, mirror
	// AuthGORM.CurrentUser's disabled-user check.
	var u auth.UserGORM
	if err := s.DB.Preload("Groups").First(&u, k.UserID).Error; err != nil {
		return nil, ErrInvalidAPIKey
	}
	if u.Disabled {
		return nil, ErrInvalidAPIKey
	}

	now := time.Now()
	s.DB.Model(&k).Update("last_used_at", &now) // best-effort
	return auth.UserGORMAdapter{U: &u}, nil
}
```

`auth.UserGORMAdapter{U: &u}` is the exact `auth.User` value
`AuthGORM.CurrentUser` returns, so a key-authenticated user is
indistinguishable from a session-authenticated one to everything
downstream.

## Step 2 — hook the key back into Authz

The stock `Authz` impls (`AuthzLoggedIn`, `AuthzLoggedInReadAdminWrite`,
…) reach the user through one call: `Auth.CurrentUser(r)`, which reads
the **session**. A bearer request has no session, so we make the user
reachable two ways:

1. A middleware validates the bearer header and stuffs the resolved
   user into the request context.
2. A thin `Auth` wrapper whose `CurrentUser` checks that context first,
   then falls back to the real session lookup.

```go
// bearer.go (in your app)
package main

import (
	"context"
	"net/http"
	"strings"

	"github.com/tmshlvck/gone/auth"
)

type apiUserKey struct{}

func withAPIUser(ctx context.Context, u auth.User) context.Context {
	return context.WithValue(ctx, apiUserKey{}, u)
}

func apiUserFrom(ctx context.Context) auth.User {
	u, _ := ctx.Value(apiUserKey{}).(auth.User)
	return u
}

// BearerAuth validates "Authorization: Bearer <key>" and, on success,
// puts the owning user in the context. A missing / bad key is NOT an
// error here — it just leaves the context userless so Authz denies the
// request the same way it denies an anonymous browser. (Return 401
// eagerly instead if you prefer; either works.)
func BearerAuth(ks *KeyStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			const p = "Bearer "
			if h := r.Header.Get("Authorization"); strings.HasPrefix(h, p) {
				if u, err := ks.Validate(strings.TrimPrefix(h, p)); err == nil {
					r = r.WithContext(withAPIUser(r.Context(), u))
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// apiAuth is an Auth whose CurrentUser prefers a bearer-resolved user
// from the context, falling back to AuthGORM's session lookup. Pass it
// to the stock Authz impls so they "just work" for both transports.
type apiAuth struct{ *auth.AuthGORM }

func (a apiAuth) CurrentUser(r *http.Request) auth.User {
	if u := apiUserFrom(r.Context()); u != nil {
		return u
	}
	return a.AuthGORM.CurrentUser(r)
}
```

Now the same policy object gates both the browser CRUD UI and the JSON
API:

```go
ks, _ := NewKeyStore(ag.DB)

// Browser side (cookie session): unchanged.
gate := auth.AuthzLoggedInReadAdminWrite{Auth: ag}

// API side: identical policy, but resolves the user from a bearer key
// (via apiAuth) when there's no session.
apiGate := auth.AuthzLoggedInReadAdminWrite{Auth: apiAuth{ag}}
```

Because the key resolves to the owning user, **all read/write decisions
reuse that user's groups** — an admin's key can write, a regular user's
key can only read, exactly as in the browser.

## Step 3 — mount the JSON API (outside CSRF)

Two wiring rules matter:

1. **CSRF.** `auth.CSRFWrap` rejects every mutating request (POST/PUT/…)
   that lacks a valid token. A bearer request carries no cookie to
   forge against, so CSRF doesn't apply — but `CSRFWrap` doesn't know
   that and would 403 it. **Mount the JSON API on a router that is not
   wrapped by `CSRFWrap`.** Keep the browser routes wrapped.
2. **Authz.** Call `apiGate.CanRead(r)` / `CanCreate(r)` / … inside
   your handlers (or a per-operation middleware) just like CRUDTable
   does, and 403 on `false`.

```go
func main() {
	// ... AuthGORM setup (ag), CRUD admin, pageShell as in examples/auth_gorm ...

	ks, _ := NewKeyStore(ag.DB)
	apiGate := auth.AuthzLoggedInReadAdminWrite{Auth: apiAuth{ag}}

	// Browser routes — CSRF-wrapped, cookie sessions.
	web := chi.NewRouter()
	ag.RegisterRoutes(web, "", pageShell)
	admin.RegisterRoutes(web, "", "/admin", pageShell)

	// JSON API — bearer auth, NO CSRF.
	api := chi.NewRouter()
	api.Use(BearerAuth(ks))
	// Mount your huma API here (humachi adapter) and gate operations
	// with apiGate. Sketch of a hand-written handler:
	api.Get("/api/heroes", func(w http.ResponseWriter, r *http.Request) {
		if !apiGate.CanList(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// ... serve JSON ...
	})

	// Top-level: /api/* bypasses CSRF; everything else goes through it.
	root := chi.NewRouter()
	root.Mount("/api", api)
	root.Mount("/", auth.CSRFWrap(sm)(web))

	handler := sm.LoadAndSave(root) // scs still wraps both, harmless for the API
	http.ListenAndServe(":8080", handler)
}
```

### With huma

huma mounts onto a router via an adapter (`humachi.New(api, …)` for
chi). Put `BearerAuth` on the chi router that hosts the huma API, then
read authorization in each operation. The resolved principal is on the
context:

```go
huma.Get(humaAPI, "/api/heroes", func(ctx context.Context, _ *struct{}) (*HeroesOut, error) {
	if u := apiUserFrom(ctx); u == nil {
		return nil, huma.Error401Unauthorized("api key required")
	}
	// ... or call apiGate against the *http.Request if you carry it ...
})
```

huma can also declare a `bearer` security scheme so the key requirement
shows up in the generated OpenAPI — that's purely a documentation
concern and orthogonal to the auth above. See the huma docs for the
current security-scheme API.

## Step 4 — manage keys on the preferences page

The user manages their own keys on the same page that hosts the
library's account-security cards. Use `AuthGORM.AccountSection` for the
security block (password / TOTP / passkeys / SSO) and render an
app-owned **API keys** card beneath it — the
["bundled auth block + app block"](AUTH.md#account-page) embedding
pattern.

```go
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
	// Load the signed-in user's keys for the app card.
	var keys []APIKey
	ks.DB.Where("user_id = ?", target.ID).Order("created_at DESC").Find(&keys)
	pageShell(w, r, "Preferences", preferencesPage(target.Username, cards, keys))
})

// Issue / revoke routes — these ARE browser POSTs, so keep them on the
// CSRF-wrapped router and include auth.CSRFField(ctx) in the forms.
mux.Post("/preferences/apikey", issueKeyHandler(ag, ks))             // create
mux.Post("/preferences/apikey/{id}/revoke", revokeKeyHandler(ag, ks)) // disable
```

The preferences template stacks the library cards above the app's own
card group:

```templ
templ preferencesPage(username string, accountCards templ.Component, keys []APIKey) {
	<div class="max-w-6xl mx-auto flex flex-col gap-8">
		<h1 class="text-2xl font-semibold">Preferences — { username }</h1>
		<section class="flex flex-col gap-3">
			<h2 class="text-lg font-medium opacity-80">Account security</h2>
			@accountCards
		</section>
		<section class="flex flex-col gap-3">
			<h2 class="text-lg font-medium opacity-80">API keys</h2>
			@apiKeysCard(keys) // your card: list (prefix + name + last-used +
			                   // expiry + Revoke) and a "New key" form
		</section>
	</div>
}
```

The `apiKeysCard` shows each key by `Prefix` + `Name` + `LastUsedAt` +
`ExpiresAt` with a per-row Revoke button, and a "New key" form. On
create, show the raw key returned by `KeyStore.Issue` **once** (a
copy-to-clipboard banner) and never again.

Notes:

- **Issue/revoke are browser actions** under the cookie session, so
  they go through CSRF like any other form — only the *consuming* JSON
  API bypasses CSRF.
- **Admin-edits-other:** because `AccountSection` returns `target`
  (which honors a `{ref}` route param with admin rights), you can mount
  the same page at `/preferences/{ref}` and list/manage that user's
  keys in an admin modal too — but think about whether admins *should*
  mint keys for other users before exposing it.
- API keys are independent of the login method, so they stay available
  to SSO-only users (who have no password or passkey to manage).

## Security checklist

- [ ] Store only the **hash** of the key; show the raw value once.
- [ ] JSON API mounted **outside** `CSRFWrap` (no cookie ⇒ no CSRF).
- [ ] Authz gated with an `apiAuth`-backed policy so groups still
      decide read vs. write.
- [ ] `Validate` checks `!Disabled` **and** `ExpiresAt` **and** the
      owning user's `Disabled` flag.
- [ ] Keys never accepted on `crud/` or `auth/` routes.
- [ ] Serve over TLS — a bearer key is a plaintext credential on the
      wire.

## See also

- [`AUTH.md`](AUTH.md) — `Auth` / `Authz` reference; the
  [Account page](AUTH.md#account-page) section documents
  `AccountSection` and the embedding pattern.
- [`TODO.md`](TODO.md) §1 — the planned in-library `EnableAPIKeys`
  feature this guide anticipates.
- [`examples/auth_gorm`](../examples/auth_gorm) — the preferences page
  embedding `AccountSection` (without the API-keys card; add it per
  step 4).
