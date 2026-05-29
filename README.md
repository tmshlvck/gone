# gone — Go Object Navigation Elements

A Go library that renders **HTMX-driven CRUD UIs** from a struct's
metadata. Describe a model once and get a working list page, create /
edit forms, and a per-row detail view — with validation, relation
pickers, an HTMX modal flow, and an Admin that bundles many models
behind a sidebar.

```go
type Hero struct {
    ID    uint
    Name  string
    Power int
}

mm, _ := crud.DeriveMetaModel[Hero]()
table := crud.DeriveMapCRUDTable[Hero](mm, nil, store, &mu)
table.Slug = "heroes"

url, _ := table.Route(mux, "/", pageShell)
// "/heroes" now serves a full CRUD UI:
// /heroes              — list with search/sort/pagination + create modal
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
- stdlib `net/http`; works with `chi` (use `chi.Group` for middleware,
  not `chi.Mount` — see [`docs/CRUD.md`](docs/CRUD.md)).

## Quick start

```go
package main

import (
    "log"
    "net/http"
    "sync"

    "github.com/a-h/templ"
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

    mm, err := crud.DeriveMetaModel[Hero]()
    if err != nil { log.Fatal(err) }
    mm.DisplayName = "Heroes"
    mm.MustFindField("Name").FieldValidate = crud.All(crud.NotEmpty, crud.MaxLen(40))
    mm.MustFindField("Power").FieldValidate = crud.IntRange(0, 100)

    table := crud.DeriveMapCRUDTable[Hero](mm, nil, store, &mu)
    table.Slug = "heroes"
    table.PageSize = 20

    mux := http.NewServeMux()
    url, err := table.Route(mux, "/", pageShell)
    if err != nil { log.Fatal(err) }
    mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
        http.Redirect(w, r, url, http.StatusSeeOther)
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
heroTable   := crud.DeriveGormCRUDTable[Hero](heroMM, nil, db)
weaponTable := crud.DeriveGormCRUDTable[Weapon](weaponMM, nil, db)
skillTable  := crud.DeriveGormCRUDTable[Skill](skillMM, nil, db)

admin := crud.DeriveAdminAutoWire(
    []crud.CRUDTableInterface{&heroTable, &weaponTable, &skillTable},
    nil,
)

url, _ := admin.Route(mux, "/", pageShell)
// "/admin" → 303 to /admin/heroes
// "/admin/heroes" — Hero table with sidebar, Hero active
// "/admin/weapons" — Weapon table with sidebar, Weapon active
// "/admin/skills"  — Skill table with sidebar, Skill active
// Plus every child's HTMX endpoints under /admin/{slug}/...
```

`DeriveAdminAutoWire` matches each relation field's Go type against
peer tables' model names and fills `RelatedCRUD` automatically — Hero's
`Skills []Skill` widget knows about the Skill table without any manual
wiring.

## Examples

| Path                          | Demonstrates                                                  |
|-------------------------------|---------------------------------------------------------------|
| `examples/form_mem`           | Single struct, manual handlers using `MetaModel.RenderForm` / `TryBindForm`. Shows the IPv4-or-IPv6 validator. |
| `examples/crud_mem`           | One `CRUDTable` over an in-memory map.                         |
| `examples/crud_gorm`          | Three `CRUDTable`s (Hero, Weapon, Skill) with 1:N and N:M relations. GORM backend. MPA-style — one model per page. |
| `examples/admin_gorm`         | Same schema as `crud_gorm`, wrapped in `Admin` with `DeriveAdminAutoWire`. Zero per-field tweaking. |

```sh
go run ./examples/admin_gorm
# open http://localhost:8080/admin
```

## Documentation

- [`docs/CRUD.md`](docs/CRUD.md) — full API reference, design notes,
  modal flow, validation pipeline, composition trade-offs.
- [`PRD-CRUD.md`](PRD-CRUD.md) — design document for the CRUD package
  (target API + rationale).
- [`PRD-AUTH.md`](PRD-AUTH.md) — design document for the auth /
  CSRF / RBAC packages (work in progress).
- [`TODO.md`](TODO.md) — sketched-but-unbuilt features outside the
  auth scope (JSON API, observability, etc.).

## Status

Built and exercised by the four examples + ~50 unit/HTTP tests.
Stable enough to use for in-house tools; the API for `CRUDTable` /
`Admin` / `MetaModel` is settled. Auth / CSRF / API key work is
planned (see TODO).
