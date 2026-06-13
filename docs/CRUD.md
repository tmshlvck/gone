# gone/crud — CRUD UI from a model

This document is the user-facing reference for `github.com/tmshlvck/gone/crud`.
For the design rationale — why the package is shaped this way, plus the
decision log — see [`DESIGN.md`](DESIGN.md). For the auth/CSRF/authz
reference see [`AUTH.md`](AUTH.md).

## What it does

You describe a Go struct once, in a declarative recipe. The library gives you:

- **A list page** (search + sort + pagination + per-row edit / delete).
- **A create form** and **an edit form** with per-field validators, a
  cross-field hook, and an HTMX modal flow.
- **A display fragment** for a single row.
- **Relation pickers** that link across `CRUDTable`s by URL (auto-resolved
  by Go type name) — including a nested "+ create new" modal that doesn't
  clobber the parent form.
- **An Admin** that bundles many `CRUDTable`s under one URL prefix with a
  sidebar.

Backends today: in-memory map (`NewMapTable`), GORM (`NewGormTable`). A new
backend is a new constructor over the same `Accessor` closures.

The library emits **HTML fragments** — no `<html>/<body>/<style>` — plus a
small JS bridge that wires HTMX modal events to DaisyUI dialogs. The page
chrome (head, navbar, theme, footer) and the *page routes* are the app's:
the library registers only the in-component fragment endpoints, and the app
embeds `table.Render(r)` in its own shell.

Navigation is multi-page — real `<a href>` links (Admin's sidebar included),
each a full page load; no `hx-boost`. Only in-component interactions (sort,
search, paginate, modal forms, delete) use targeted HTMX swaps. See
[`../REFACTOR-HTMX.md`](../REFACTOR-HTMX.md) for the why.

## Quick taste

```go
type Hero struct {
    ID    uint
    Name  string
    Power int
}

func main() {
    store := map[uint]Hero{1: {1, "Aragorn", 90}}
    var mu sync.RWMutex

    // Describe the whole table once.
    table := crud.NewMapTable(store, &mu, crud.Table[Hero]{
        Slug:  "heroes",
        Title: "Heroes",
        Fields: crud.Fields{
            "ID":    {ReadOnly: true},
            "Name":  {Validate: crud.All(crud.NotEmpty, crud.MaxLen(30))},
            "Power": {Help: "0–100", Validate: crud.IntRange(0, 100)},
        },
    })

    r := chi.NewRouter()
    table.RegisterRoutes(r, "", table.Slug) // fragment endpoints under /heroes/…
    r.Get("/"+table.Slug, func(w http.ResponseWriter, req *http.Request) {
        content, err := table.Render(req) // the app owns the page route
        if err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
            return
        }
        pageShell(w, req, "Heroes", content)
    })
    r.Get("/", func(w http.ResponseWriter, req *http.Request) {
        http.Redirect(w, req, table.URLBase(), http.StatusSeeOther)
    })

    log.Fatal(http.ListenAndServe(":8080", r))
}

func pageShell(w http.ResponseWriter, r *http.Request, title string, content templ.Component) {
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    appLayout(title, content).Render(r.Context(), w)
}
```

`appLayout` is a templ component the caller writes — DaisyUI + Tailwind +
HTMX loaded in its `<head>`; `content` rendered in `<main>`.

## Stack assumed

| Concern    | Choice                                                                     |
|------------|----------------------------------------------------------------------------|
| Language   | Go 1.24+ (generics)                                                        |
| Templating | [templ](https://github.com/a-h/templ)                                      |
| ORM        | GORM v2 (`gorm.io/gorm`) for the GORM backend                              |
| Router     | [chi v5](https://github.com/go-chi/chi) (`chi.Router`) — required          |
| Styling    | [DaisyUI v5](https://daisyui.com) + Tailwind v4 in the caller's page shell |
| HTMX       | `htmx.org@2` in the caller's page shell                                    |

The library bundles no CSS / JS / static assets. Examples load DaisyUI +
Tailwind + HTMX from jsDelivr/unpkg.

## Packages

- **`gone/crud`** — the recipe, the table/admin components, the metadata
  model, validators. What this document covers.
- **`gone/site`** — page-composition helpers shared with the app: the
  `Shell` function shape (the app's page chrome), a `Fragment` writer, and a
  `Respond` helper for a single URL that serves both a fragment and a full
  page. Depends on templ.
- **`gone/htmx`** — the HTMX wire protocol typed: request classification
  (`IsRequest`, `Target`, `CurrentURL`) and a fluent response-directive
  builder (`Reply().Retarget(…).Reswap(…).Trigger(…).Apply(w)`) with
  backend-driven modal control (`OpenModal`/`CloseModal`). Dependency-free.
  Apps reach for it in their own handlers; `crud` uses it internally.

## The recipe — `Table[T]`, `Field`, `Fields`

A model is described once, declaratively, and handed to a constructor. The
constructor reflects `T`, merges the overrides over the reflected defaults,
and returns a ready `CRUDTable[T]`.

```go
type Table[T any] struct {
    Slug     string     // URL slug (plural); empty = lowercase(TypeName)+"s"
    Title    string     // display name; empty = the Go type name
    PageSize int        // rows per page; 0 = library default (20)
    Authz    auth.Authz // nil = allow all

    ReadOnly         bool // disables create + edit + delete in one switch
    HideUnauthorized bool // omit disallowed mutation buttons (vs render disabled)

    Fields     Fields              // per-field overrides, keyed by Go field name
    Validate   func(instance T) error // optional model-level cross-field validator
    ShortLabel func(instance T) string // relation label override (see Relations)
}

// Field overrides one field's derived defaults. The zero value changes
// nothing — set only what you want. ReadOnly/Hidden are additive (true turns
// the flag on); every other field overrides when non-empty / non-nil.
type Field struct {
    Label     string    // -> DisplayName  (empty = keep derived = Go field name)
    Help      string    // -> FormHelp     (hint under the input)
    InputType string    // -> FormInputType ("email", "password", "date", …)
    ReadOnly  bool      // show in detail/list, omit from the form
    Hidden    bool      // omit entirely (list, detail, form)
    Validate  Validator // -> FieldValidate (per-field server validator)

    // The three generic per-field transforms; nil keeps the derived hook.
    // Compose them for bespoke fields without dropping to the low-level path.
    DisplayValue   func(mf MetaField, value any) templ.Component         // table/detail cell
    GenFormElement func(mf MetaField, value any) templ.Component         // whole form <input>
    BindStrings    func(mf MetaField, strs []string, instance any) error // parse form value(s) → struct
}

type Fields map[string]Field

func NewGormTable[T any](db *gorm.DB, cfg Table[T]) CRUDTable[T]
func NewMapTable[T any](store map[uint]T, mu *sync.RWMutex, cfg Table[T]) CRUDTable[T]
```

### Secret / password fields

There are no special "secret" flags — sensitive fields are just compositions
of the three generic hooks, with ready-made helpers for the common cases:

```go
func Redact(mf MetaField, value any) templ.Component        // DisplayValue: "-hidden-"/"-empty-"
func PasswordInput(mf MetaField, value any) templ.Component // GenFormElement: empty password box
func HashWith(hash func(string) (string, error)) func(MetaField, []string, any) error // BindStrings
```

- **Show a secret read-only** (a stored hash, a raw TOTP secret, an opaque
  `[]byte` handle) — redacted in the table, never sent to the form or bound:

  ```go
  "TOTPSecret": {Label: "TOTP", ReadOnly: true, DisplayValue: crud.Redact},
  ```

- **A write-only password field** — empty box in the form, redacted in the
  table; a non-blank entry is re-hashed and stored, a blank entry keeps the
  current value:

  ```go
  "PasswordHash": {Label: "Password", InputType: "password",
      DisplayValue:   crud.Redact,
      GenFormElement: crud.PasswordInput,
      BindStrings:    crud.HashWith(auth.HashPassword), // auth.HashPassword = argon2id
  },
  ```

`auth.HashPassword` hashes the same way the login path verifies. See
`examples/auth_gorm` for the User table using all of this.

The constructors **panic at startup** on a programming error — a non-struct
`T`, or a `Fields` key the type doesn't have (a typo / renamed field). This
is the `regexp.MustCompile` idiom: fail loudly at boot, not silently at first
render.

```go
heroes := crud.NewGormTable(db, crud.Table[Hero]{
    Slug: "heroes", Title: "Heroes", PageSize: 10,
    Fields: crud.Fields{
        "ID":      {ReadOnly: true},
        "Name":    {Help: "2–40 chars.", Validate: crud.All(crud.NotEmpty, crud.MaxLen(40))},
        "Weapons": {Label: "Weapons (read-only)", Help: "Edit via /weapons."},
    },
})
```

The returned `CRUDTable[T]` is a plain struct with public fields, so anything
the recipe doesn't cover — granular `CreateEnabled`, a one-off MetaField
hook — is still settable afterward. The low-level `DeriveMetaModel` +
`DeriveGormCRUDTable` + `MustFindField` path (see [Lower-level
API](#lower-level-api)) remains available for callers that need a custom
backend or hook.

## Routing surface

A table registers its in-component (fragment) endpoints **relative to the
router it is handed**, and renders absolute links from a base it is *told*:

```go
func (c *CRUDTable[T]) RegisterRoutes(r chi.Router, mountBase, slug string)
func (c *CRUDTable[T]) Render(r *http.Request) (templ.Component, error)
func (c *CRUDTable[T]) URLBase() string // absolute base, e.g. "/admin/heroes"
```

- **`slug`** is where the table sits relative to `r` (e.g. `"heroes"`). Empty
  falls back to the table's `Slug`, then a derived plural.
- **`mountBase`** is the absolute path at which `r` itself is served. The
  caller knows this; chi can't report it at registration time. The table's
  absolute base, used for every rendered `hx-get` / form action, is
  `mountBase + "/" + slug`.

The **app owns the page route** (`GET /{slug}`): it calls `Render(r)` and
wraps the result in its own shell. `RegisterRoutes` registers only the
fragments. For `slug="heroes"`, `mountBase="/admin"`:

| Method | Path                         | Returns                                   |
|--------|------------------------------|-------------------------------------------|
| GET    | `/admin/heroes/view`         | table fragment for sort/search/paginate   |
| GET    | `/admin/heroes/create`       | create form fragment                      |
| POST   | `/admin/heroes/create`       | create submit                             |
| GET    | `/admin/heroes/{id}/edit`    | edit form fragment                        |
| POST   | `/admin/heroes/{id}/edit`    | edit submit                               |
| POST   | `/admin/heroes/{id}/delete`  | delete (HTMX → rows fragment; else 303)   |
| GET    | `/admin/heroes/{id}/display` | per-row dump fragment                     |
| GET    | `/admin/heroes/options`      | relation-picker option list               |

Every handler gates on the table's `Authz` (nil = allow all).

### Mounting — the composition contract

One rule: **`mountBase` must equal the absolute prefix at which the passed
router is served.** Because routes register relative to `r`, the table
composes under stripping mounts (`chi.Route` / `chi.Mount`) and groups alike:

| caller wiring                         | call                                       | served at            |
|---------------------------------------|--------------------------------------------|----------------------|
| root                                  | `RegisterRoutes(root, "", "heroes")`       | `/heroes/…`          |
| `r.Route("/admin", …)` (strips)       | `RegisterRoutes(r, "/admin", "heroes")`    | `/admin/heroes/…`    |
| `r.Mount("/admin", sub)` (strips)     | `RegisterRoutes(sub, "/admin", "heroes")`  | `/admin/heroes/…`    |
| `r.Group(…)` at root (no strip)       | `RegisterRoutes(g, "", "admin/heroes")`    | `/admin/heroes/…`    |

Stripping mounts are first-class — the rendered HTML carries the right
absolute URLs because they're built from `mountBase`, never reverse-engineered
from the request.

## `Admin`

Bundles tables behind a sidebar. Navigation between tables is plain page
navigation (each sidebar entry is a real link to `/{mountBase}/{slug}`); the
server renders the whole page on each load and marks the active entry from
the request path — no JS for the active highlight.

```go
type Admin struct {
    Tables []CRUDTableInterface
    Authz  auth.Authz
    Slug   string // default "admin" (informational; mounting is the caller's)
}

func DeriveAdmin(tables []CRUDTableInterface, az auth.Authz) Admin

func (a *Admin) RegisterRoutes(r chi.Router, mountBase string, shell site.Shell) error
func (a *Admin) Render(r *http.Request) (templ.Component, error)
```

`Admin.RegisterRoutes` registers, on the router it is handed: every child
table's fragment endpoints, a `GET /{slug}` page handler that wraps the
active table in `shell`, a `GET` index redirect to the first table, and it
links the children's relation fields (see [Relations](#relations)). The
caller mounts it once, typically via a stripping `chi.Route`:

```go
r.Route("/admin", func(r chi.Router) {
    admin.RegisterRoutes(r, "/admin", pageShell)
})
```

Registered for `mountBase="/admin"`, tables `["heros","weapons","skills"]`:

| Method | Path                              | Returns                                  |
|--------|-----------------------------------|------------------------------------------|
| GET    | `/admin`                          | 303 redirect to `/admin/heros`           |
| GET    | `/admin/{slug}`                   | full page (sidebar + active table)       |
| GET    | `/admin/heros/view`, `/create`, … | each child table's fragment endpoints    |

`shell == nil` registers the index redirect and child fragments but no
per-slug page handler.

## Relations

Relation fields (`Owner Hero`, `Weapons []Weapon`, `Skills []Skill`) are
detected by reflection + gorm tags during derivation. Tables **link by URL,
not by an in-process pointer**: a relation `<select>` loads its `<option>`
list over HTTP from the *related* table's own `/options` endpoint, fired on
`load` (and again on `refresh-relation`, e.g. after a nested create). The
related table generates the `id → label` pairs — it already has the data.

The link is established by stamping each relation field's `RelatedURLBase`
from a Go-type-name → URL map, **after** the tables are routed:

```go
func WireRelations(tables ...CRUDTableInterface)
```

- **Admin** calls `WireRelations` for its managed tables automatically inside
  `RegisterRoutes` — Admin apps don't call it.
- **Standalone** tables call it themselves, once, after every
  `RegisterRoutes` (so the URLBases are set):

  ```go
  heroTable.RegisterRoutes(r, "", "heroes")
  weaponTable.RegisterRoutes(r, "", "weapons")
  crud.WireRelations(&heroTable, &weaponTable) // Owner select on /weapons → /heroes/options
  ```

A relation left unwired (no matching table) renders a degraded `<select>`
with no options endpoint — functional, not fatal.

### Relation labels

The text shown for one related row — in a `<select>` option and in a relation
cell — comes from `DefaultShortLabel(instance)`, which tries, in order:

1. a `Name` field (case-insensitive), if a non-empty string;
2. a `Label` field (case-insensitive);
3. a `Title` field (case-insensitive);
4. any string field whose name contains `name` (`Username`, `FullName`, …);
5. any string field whose name contains `title` (`Subtitle`, `JobTitle`, …);
6. an identifier — an `id` field, else a `…ID`/`…Id` foreign key — as `#<n>`;
7. a JSON dump of the row, as a last resort.

Stages 1–5 return the label alone (no `id:` prefix). To override it for a
model, set `ShortLabel` on its recipe — it drives both that table's `<select>`
options and the model's relation cells on other tables (propagated by
`WireRelations`):

```go
crud.Table[Hero]{
    ShortLabel: func(h Hero) string { return h.Name + " — " + h.Realm },
}
```

## Lower-level API

The recipe is sugar over these primitives. Reach for them to back a custom
storage type, to render a one-off form outside a table (see
`examples/form_mem`), or to install a bespoke MetaField hook.

### `MetaModel[T]` and `MetaField`

`MetaModel[T]` is pure metadata + render/bind helpers — no routing, no data,
no authz. `MetaField` describes one field.

```go
func DeriveMetaModel[T any]() (MetaModel[T], error) // reflect T → defaults
func (mm *MetaModel[T]) FindField(name string) (*MetaField, error)
func (mm *MetaModel[T]) MustFindField(name string) *MetaField // panics on miss

func (mm *MetaModel[T]) RenderDisplay(instance T) templ.Component
func (mm *MetaModel[T]) RenderForm(instance T, opts FormOpts) templ.Component
func (mm *MetaModel[T]) TryBindForm(r *http.Request, out *T) error // parse + bind + validate
```

`MetaField`'s relation metadata is filled by derivation; `RelatedURLBase`
(the related table's absolute URL) is left blank and filled later by
`WireRelations` / `Admin`:

```go
type MetaField struct {
    Name, DisplayName, FormInputType, FormHelp string
    Hidden, ReadOnly, Multiple, Sortable, Searchable bool

    RelationKind    RelationKind // NotRelation | RelationSingle | RelationMany2Many | RelationHasMany
    RelatedURLBase  string       // related table URL; blank until wired
    RelatedTypeName string       // Go type name of the related model
    FKFieldName     string       // RelationSingle: sibling FK uint
    FormFieldName   string       // POST key (defaults to Name)

    DisplayValue   func(mf MetaField, value any) templ.Component
    GenFormElement func(mf MetaField, value any) templ.Component
    BindStrings    func(mf MetaField, strs []string, instance any) error
    FieldValidate  Validator
}
```

`FormOpts` for `RenderForm`:

```go
type FormOpts struct {
    ActionURL   string
    HXTarget    string           // CSS selector for hx-target; empty = browser-only
    SubmitLabel string           // "Save", "Create"
    Title       string           // optional <h3> above the form
    Errors      ValidationErrors // raw error from TryBindForm — template splits it
    SuccessMsg  string           // optional green alert
}
```

### Backend constructors

The recipe constructors wrap these:

```go
func DeriveMapCRUDTable[T any](mm MetaModel[T], az auth.Authz, store map[uint]T, mu *sync.RWMutex) CRUDTable[T]
func DeriveGormCRUDTable[T any](mm MetaModel[T], az auth.Authz, db *gorm.DB) CRUDTable[T]
```

`NewMapTable(store, mu, cfg)` ≡ `DeriveMapCRUDTable(cfg→MetaModel, cfg.Authz,
store, mu)` + the table-level field assignments; same for GORM.

### Authz

```go
type Authz interface { // gone/auth
    CanList(r *http.Request) bool
    CanRead(r *http.Request) bool
    CanCreate(r *http.Request) bool
    CanUpdate(r *http.Request) bool
    CanDelete(r *http.Request) bool
}
```

`nil` = allow all; `auth.AuthzAllowAll{}` is the explicit no-op.
`auth.AuthzLoggedInReadAdminWrite{Auth: …}` is the stock "logged-in reads,
admin group writes" gate. See [`AUTH.md`](AUTH.md).

## Validation

Per-field validators run after the field is parsed; the model-level
`Validate` runs only once every field passed.

```go
type Validator = func(value any) error

// Built-ins:
func NotEmpty(value any) error
func MinLen(n int) Validator
func MaxLen(n int) Validator
func IntRange(min, max int64) Validator
func FloatRange(min, max float64) Validator
func Email(value any) error
func Pattern(re *regexp.Regexp, reason string) Validator
func IPv4Addr(value any) error
func IPv6Addr(value any) error
func IPv4Net(value any) error // CIDR
func IPv6Net(value any) error // CIDR

// Combinators:
func All(vs ...Validator) Validator // AND, first failure wins
func Any(vs ...Validator) Validator // OR, joins failures with " or "
```

Errors come back as a `ValidationErrors` map (field name → message; `""` for
model-level). Feed it straight into `FormOpts.Errors`; the template splits it
into the banner + per-field messages.

```go
type ValidationErrors map[string]string
const ModelLevelKey = "" // empty key for cross-field
func (e ValidationErrors) Error() string
func ValidationErrorsFromError(err error) ValidationErrors // non-validation errors → ModelLevelKey
```

## HTMX modals

Create / edit forms open in two stacked DaisyUI dialogs:

- **L1 — per-table** — `#{slug}-modal-l1` — emitted by each `Render()`.
- **L2 — shared singleton** — `#crud-modal-l2` — for the nested "+ create
  new" a relation picker opens. Auto-embedded by `Render`.

The flow is **backend-driven**: the form's HTMX attributes are static
(`hx-post=<action> hx-target=#…-body`); the server decides the outcome with
response directives, built via `gone/htmx`:

| Outcome          | Directives (`htmx.Reply()…Apply(w)`)                                   |
|------------------|------------------------------------------------------------------------|
| Validation error | `Retarget("#<body>").Reswap("innerHTML")`, body = form with errors     |
| L1 save success  | `CloseModal(<id>).Retarget("#<wrapper>").Reswap("innerHTML")`, body = refreshed table |
| L2 save success  | `CloseModal(<id>).Trigger("refresh-relation", true).Reswap("none")`    |

A small JS bridge (auto-embedded by the library) maps the `openModal` /
`closeModal` events to `dialog.showModal()` / `.close()`, and
`refresh-relation` makes every relation `<select>` re-fetch its options so a
freshly-created row appears — without disturbing any other field.

With JS off, mutation POSTs fall back to a `303` redirect to the list, so the
app still works as a plain MPA.

## Pagination

`PageSize` (default 20) controls rows per page; `?page=N` selects it
(1-indexed). The renderer emits a DaisyUI `join` button group with prev/next
+ numbers. Page / sort / search links are HTMX with `hx-push-url`, so the
state is a real, shareable URL — a reload re-renders the full page in that
state. Search and sort reset to page 1.

## Composition trade-offs

- **One stateful table per page.** Bookmarkable state (`hx-push-url`) lives in
  the table's own URL; multiple tables can't all own the address bar. Admin
  shows one table at a time, which sidesteps this.
- **Distinct slugs.** Two tables on one page (or in one Admin) MUST have
  distinct `Slug`s — the per-slug L1 modal IDs would otherwise collide. The
  default `lowercase(Name)+"s"` keeps distinct Go types apart; sharing a slug
  deliberately is a configuration bug.

## Testing

The primary lever is **HTTP end-to-end tests** via `net/http/httptest`
against a `chi.Router`: rows render, search filters, POST persists,
`HX-Request` flips the delete response from 303 to a fragment, the relation
`<select>` carries the right `/options` URL, …

Unit tests cover the reflection-heavy and config primitives that aren't
naturally observable through HTTP: `DeriveMetaModel`, `DefaultBindForm`, the
built-in validators (incl. IPv4/IPv6), `FindField` / `MustFindField`, and the
recipe (`NewMapTable` override application, unknown-field panic).

`go test ./...` — no external deps; SQLite is in-memory.

## Examples

| Path                  | Shows                                                                                          |
|-----------------------|------------------------------------------------------------------------------------------------|
| `examples/form_mem`   | Single struct + manual handlers using `RenderForm` / `TryBindForm`. IPv4-or-IPv6 via `Any(IPv4Addr, IPv6Addr)`. |
| `examples/crud_mem`   | One table via `NewMapTable`, in-memory map backend.                                            |
| `examples/crud_gorm`  | Three tables (Hero, Weapon, Skill) with 1:N and N:M relations via `NewGormTable` + `WireRelations`. GORM backend, MPA tab nav. |
| `examples/admin_gorm` | Same schema wrapped in `Admin` — zero per-field config (empty recipes, default slugs), relations auto-wired. |
| `examples/auth_gorm`  | `AuthGORM` + `Admin` over User/Group, with `Authz` and a `DisplayValue` override in the recipe. |

`go run ./examples/<name>` — each starts on `:8080`.
