# gone — Go Object Navigation Elements

A Go library for **HTMX-driven CRUD UIs and the auth stack** around
them. Describe a model once and get a working list page, create /
edit forms, and a per-row detail view — with validation, relation
pickers, an HTMX modal flow, and an Admin that bundles many models
behind a sidebar. Plug in sessions, CSRF, login (password + TOTP +
passkeys), and an authorization interface that gates everything
above.

```go
type Hero struct {
    ID    uint
    Name  string
    Power int
}

mm    := crud.DeriveMetaModel[Hero](crud.MetaModel[Hero]{DisplayName: "Heroes"})
data  := crud.MapAccessor(mm, store, &mu)
table := crud.NewTable(mm, data, 20, nil)

table.RegisterRoutes(root, "", "/heroes")
// "/heroes/…" now serves the CRUD fragments (the app owns the page route):
// /heroes/view         — list with search/sort/pagination
// /heroes/create       — create modal
// /heroes/{id}/edit    — edit modal
// /heroes/{id}/display — read-only detail
// /heroes/{id}/delete  — delete
```

## What you get

- **List page** with search, sortable columns, pagination, per-row
  edit / delete.
- **Create / edit modal** with per-field validators and a cross-field
  hook. Errors render inline next to the offending field plus a banner.
- **Relations** auto-detected from struct shape + GORM tags
  (belongs-to / many-to-many / has-many). Pickers know how to fetch
  their options and ship a "+ create new" button that opens a nested
  modal without losing the parent form's state.
- **Admin** bundles multiple `CRUDTable`s under one URL prefix with
  HTMX-boosted sidebar navigation, an active-link highlight, history
  cache, and auto-wired cross-table relations.
- **Backends**: in-memory map for tests / prototypes, GORM for
  production. New backends drop in by writing a constructor.
- **Time is UTC end-to-end** — display and form-bind are UTC, and
  `site.ForceUTC(db)` guarantees `time.Time` is stored in UTC on any
  backend (SQLite, Postgres), so SQL sort / range filters operate on
  the instant. Call it once after `gorm.Open`; see
  [`docs/CRUD.md`](docs/CRUD.md#time-fields-and-utc-storage).
- **Per-session display preferences** — a navbar timezone picker
  (UTC / browser-local / any IANA zone) renders *and* edits times in
  the viewer's zone while storage stays UTC, and a cookie-backed
  light/dark theme toggle. Both are small `site` helpers backed by
  `site.SetPref` cookies; page size is app-configurable too
  (`0` = no pagination).

The library emits **HTML fragments** — `<html>` / `<head>` /
`<body>` / theme are the caller's concern, supplied via a
`PageShellFunc`.

## Stack

- Go 1.24+ (generics + Go 1.22 `ServeMux` patterns).
- [templ](https://github.com/a-h/templ) for type-safe templates.
- [DaisyUI v5](https://daisyui.com) + Tailwind v4 + HTMX 2 in the
  caller's page shell (no static assets shipped by the library).
- GORM v2 (`gorm.io/gorm`) for the GORM backend, with pure-Go SQLite
  (`glebarez/sqlite`) in examples.
- [chi v5](https://github.com/go-chi/chi) router — tables register their
  fragment endpoints relative to a `chi.Router` (see [`docs/CRUD.md`](docs/CRUD.md)).

## Quick start

```go
package main

import (
    "log"
    "net/http"
    "sync"

    "github.com/a-h/templ"
    "github.com/go-chi/chi/v5"
    "github.com/tmshlvck/gone/crud"
)

type Hero struct {
    ID    uint
    Name  string
    Realm string
    Power int
}

func main() {
    store := map[uint]Hero{
        1: {1, "Aragorn", "Gondor", 90},
        2: {2, "Legolas", "Mirkwood", 85},
    }
    var mu sync.RWMutex

    // 1. Metadata: reflect Hero, overlay overrides (panics on a typo'd field).
    mm := crud.DeriveMetaModel[Hero](crud.MetaModel[Hero]{
        DisplayName: "Heroes",
        Fields: []crud.MetaField{
            {Name: "Name", FieldValidate: crud.All(crud.NotEmpty, crud.MaxLen(40))},
            {Name: "Power", FieldValidate: crud.IntRange(0, 100)},
        },
    })
    // 2. Data plane. 3. Table config (pageSize 20, no authz).
    table := crud.NewTable(mm, crud.MapAccessor(mm, store, &mu), 20, nil)

    mux := chi.NewRouter()
    const heroesPath = "/heroes"
    table.RegisterRoutes(mux, "", heroesPath) // the app owns the page route:
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

    log.Fatal(http.ListenAndServe(":8080", mux))
}

// pageShell wraps the library's fragment in the app's chrome.
// Free to redirect on auth failure, set headers, etc.
func pageShell(w http.ResponseWriter, r *http.Request, title string, content templ.Component) {
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    pageLayout(title, content).Render(r.Context(), w)
}
```

`pageLayout` is a small templ component you write — it owns
`<html>` / `<head>` (loading DaisyUI + Tailwind + HTMX) /
`<body>` chrome and renders `content` inside `<main>`.

## Admin

When you have several models, wrap them in an `Admin`:

```go
heroTable   := crud.NewTable(heroMM, crud.GORMAccessor(heroMM, db), 0, nil)
weaponTable := crud.NewTable(weaponMM, crud.GORMAccessor(weaponMM, db), 0, nil)
skillTable  := crud.NewTable(skillMM, crud.GORMAccessor(skillMM, db), 0, nil)

admin := crud.DeriveAdmin(
    []crud.CRUDTableInterface{&heroTable, &weaponTable, &skillTable},
    nil,
)

admin.RegisterRoutes(mux, "", "/admin", pageShell)
// "/admin" → 303 to /admin/heros
// "/admin/heros" — Hero table with sidebar, Hero active
// "/admin/weapons" — Weapon table with sidebar, Weapon active
// "/admin/skills"  — Skill table with sidebar, Skill active
// Plus every child's HTMX endpoints under /admin/{slug}/...
```

`DeriveAdmin` matches each relation field's Go type against peer tables'
model names and wires the relation pickers automatically when it registers
their routes — Hero's `Skills []Skill` widget knows about the Skill table
without any manual `WireRelations` call.

## Auth

`gone/auth` ships two `Auth` implementations:

- **`AuthSimple`** — in-memory users, argon2id at rest. For tests,
  prototypes, and one-admin setups.
- **`AuthGORM`** — GORM-backed users + groups + passkeys + TOTP.
  Multi-method login form (password / passkey / SSO-once-built).
  Self-service account page.

Both implement the same `auth.Auth` interface, so apps swap impls
by changing one constructor.

```go
sm := scs.New()
sa := auth.NewAuthSimple(sm)
sa.UserAdd("admin", "admin@local", "admin")

mux := chi.NewRouter()
sa.RegisterRoutes(mux, "", pageShell)

// Pipeline: scs.LoadAndSave → auth.CSRFWrap → app mux.
handler := sm.LoadAndSave(auth.CSRFWrap(sm)(mux))
```

For CRUDTable / Admin gating, drop in one of the stock `Authz`
helpers — `AuthzLoggedIn`, `AuthzLoggedInReadOnly`,
`AuthzLoggedInReadAdminWrite`, `AuthzAllowAll`, `AuthzDenyAll`:

```go
zoneTable := crud.NewTable(zoneMM, crud.GORMAccessor(zoneMM, db), 0,
    auth.AuthzLoggedInReadAdminWrite{Auth: a})
```

Or implement `auth.Authz` directly for per-resource policy. See
[`docs/AUTH.md`](docs/AUTH.md) for the full surface.

## Examples

| Path                          | Demonstrates                                                  |
|-------------------------------|---------------------------------------------------------------|
| `examples/form_mem`           | Single struct, manual handlers using `MetaModel.RenderForm` / `TryBindForm`. Shows the IPv4-or-IPv6 validator. |
| `examples/crud_mem`           | One `CRUDTable` over an in-memory map.                        |
| `examples/crud_gorm`          | Three `CRUDTable`s (Hero, Weapon, Skill) with 1:N and N:M relations. GORM backend. MPA-style — one model per page. |
| `examples/admin_gorm`         | Same schema as `crud_gorm`, wrapped in `Admin` (`DeriveAdmin`, relations auto-wired). Custom sidebar link demo. Zero per-field tweaking. Ships an app-owned `<style>` styling polish (the only example that does). |
| `examples/auth_simple`        | `AuthSimple` + a single CRUDTable behind a gated page shell. |
| `examples/auth_gorm`          | Full `AuthGORM`: User + Group CRUDTables under Admin; `AuthzLoggedInReadAdminWrite`; password / TOTP / passkey login; account modal. |
| `examples/auth_sso`           | `auth_gorm` + SSO providers (Google / GitHub / Okta) wired from env vars. README walks through OAuth-app registration on each. |

```sh
go run ./examples/admin_gorm
# open http://localhost:8080/admin

go run ./examples/auth_gorm
# open http://localhost:8080/login — login admin / admin
```

## Documentation

User-facing references (the practical "how do I…?" docs):

- [`docs/CRUD.md`](docs/CRUD.md) — full CRUD API reference, design
  notes, modal flow, validation pipeline, time/UTC storage,
  composition trade-offs.
- [`docs/AUTH.md`](docs/AUTH.md) — sessions / CSRF / login (password,
  TOTP, passkeys) / authz reference, with worked examples.
- [`docs/HOWTO-BEARER-TOKENS.md`](docs/HOWTO-BEARER-TOKENS.md) — per-user
  API keys for an app's JSON API, reusing gone's users + Authz.

Design rationale (the "why does it look like this?" doc):

- [`docs/DESIGN.md`](docs/DESIGN.md) — design decisions for both
  packages plus an open-questions / future-work log.

Operational:

- [`docs/TODO.md`](docs/TODO.md) — what's specced but not yet built
  (CSV import/export).
- [`AGENTS.md`](AGENTS.md) — short pointer for agents / new contributors.

## Status

Built and exercised by seven examples + 200+ unit/HTTP tests. Stable
enough to run in-house tools and small production apps:

- **`gone/crud`** — settled. CRUDTable + Admin + MetaModel +
  validators + relation pickers + configurable pagination.
- **`gone/auth`** — sessions, CSRF, AuthSimple, AuthGORM, TOTP,
  passkeys, SSO (OIDC + OAuth2), account page, authz interface.
- **`gone/site`** — page chrome + UTC-at-rest (`ForceUTC`), per-session
  timezone (picker + middleware + `TimeFormatter`), cookie theme toggle.

Planned (see [`docs/TODO.md`](docs/TODO.md)):

- **CSV import/export** — round-trip a CRUDTable's rows through CSV,
  driven by the existing MetaModel.

Bearer-token API keys are an app-side pattern today — see
[`docs/HOWTO-BEARER-TOKENS.md`](docs/HOWTO-BEARER-TOKENS.md).
