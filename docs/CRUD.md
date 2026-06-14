# gone/crud — CRUD UI from a model

This document is the user-facing reference for `github.com/tmshlvck/gone/crud`.
For the design rationale — why the package is shaped this way, plus the
decision log — see [`DESIGN.md`](DESIGN.md). For the auth/CSRF/authz
reference see [`AUTH.md`](AUTH.md).

## What it does

You describe a Go struct once as a `MetaModel`, pair it with a data
`Accessor`, and wrap the two in a `CRUDTable`. The library gives you:

- **A list page** (search + sort + pagination + per-row edit / delete).
- **A create form** and **an edit form** with per-field validators, a
  cross-field hook, and an HTMX modal flow.
- **A display fragment** for a single row.
- **Relation pickers** that link across `CRUDTable`s by URL (auto-resolved
  by Go type name) — including a nested "+ create new" modal that doesn't
  clobber the parent form.
- **An Admin** that bundles many `CRUDTable`s under one URL prefix with a
  sidebar.

Backends today: in-memory map (`MapAccessor`), GORM (`GORMAccessor`). A new
backend is a new type implementing the five-method `Accessor[T]` interface.

The library emits **HTML fragments** — no `<html>/<body>/<style>` — plus a
small JS bridge that wires HTMX modal events to DaisyUI dialogs. The page
chrome (head, navbar, theme, footer) and the *page routes* are the app's:
the library registers only the in-component fragment endpoints, and the app
embeds `table.Render(r)` in its own shell.

Navigation is multi-page — real `<a href>` links (Admin's sidebar included),
each a full page load; no `hx-boost`. Only in-component interactions (sort,
search, paginate, modal forms, delete) use targeted HTMX swaps. See
[`DESIGN.md`](DESIGN.md) for the rationale.

## The three steps

Construction separates **what** from **where**. You build a table from three
pieces — metadata, data, config — and only then say where it lives:

```go
mm    := crud.DeriveMetaModel[Hero](crud.MetaModel[Hero]{ /* overrides */ }) // WHAT to render/bind
data  := crud.GORMAccessor[Hero](mm, db)                                     // the data plane
table := crud.NewTable(mm, data, 10, az)                                     // table config (pageSize, authz)
table.RegisterRoutes(root, "", "/admin/heroes")                             // WHERE it lives
```

- `DeriveMetaModel` reflects `Hero`, then overlays your overrides.
- `GORMAccessor` / `MapAccessor` build the data plane from the **same** `mm`
  (they read it to learn which fields are searchable / sortable / relations).
- `NewTable` pairs the two and adds table-level config (`pageSize`, `authz`,
  the mutation toggles).
- `RegisterRoutes` is the only place a path appears — the table is otherwise
  path-agnostic, so the same table can be mounted standalone or under an
  Admin without rebuilding it.

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

    // 1. Metadata: reflect Hero, overlay per-field overrides.
    mm := crud.DeriveMetaModel[Hero](crud.MetaModel[Hero]{
        DisplayName: "Heroes",
        Fields: []crud.MetaField{
            {Name: "ID", ReadOnly: true},
            {Name: "Name", FieldValidate: crud.All(crud.NotEmpty, crud.MaxLen(30))},
            {Name: "Power", FormHelp: "0–100", FieldValidate: crud.IntRange(0, 100)},
        },
    })
    // 2. Data plane over the caller-owned map + mutex.
    data := crud.MapAccessor(mm, store, &mu)
    // 3. Table config: pageSize 0 (= default 20), no authz.
    table := crud.NewTable(mm, data, 0, nil)

    r := chi.NewRouter()
    const heroesPath = "/heroes"
    table.RegisterRoutes(r, "", heroesPath) // fragment endpoints under /heroes/…
    r.Get(heroesPath, func(w http.ResponseWriter, req *http.Request) {
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

- **`gone/crud`** — the metadata model, the table/admin components, the data
  accessors, validators. What this document covers.
- **`gone/site`** — page-composition helpers shared with the app: the
  `Shell` function shape (the app's page chrome), a `Fragment` writer, and a
  `Respond` helper for a single URL that serves both a fragment and a full
  page. Depends on templ.
- **`gone/htmx`** — the HTMX wire protocol typed: request classification
  (`IsRequest`, `Target`, `CurrentURL`) and a fluent response-directive
  builder (`Reply().Retarget(…).Reswap(…).Trigger(…).Apply(w)`).
  Dependency-free. Apps reach for it in their own handlers; `crud` uses it
  internally (e.g. `Trigger("crud-close-modal", nil)` for the modal bridge).

## Metadata — `DeriveMetaModel` and `MetaField`

`MetaModel[T]` is pure metadata + render/bind helpers — no routing, no data,
no authz. `DeriveMetaModel` reflects `T` into defaults, then overlays a
*preset* — a partial `MetaModel[T]` carrying only the overrides you want:

```go
func DeriveMetaModel[T any](preset MetaModel[T]) MetaModel[T]
```

```go
mm := crud.DeriveMetaModel[Hero](crud.MetaModel[Hero]{
    DisplayName: "Heroes",                          // empty = the Go type name
    Validate:    func(h Hero) error { … },          // optional cross-field rule
    Fields: []crud.MetaField{                        // matched to derived fields by Name
        {Name: "ID",    ReadOnly: true},
        {Name: "Name",  FormHelp: "2–40 chars.", FieldValidate: crud.All(crud.NotEmpty, crud.MaxLen(40))},
        {Name: "Power", FormInputType: "number",  FieldValidate: crud.IntRange(0, 100)},
    },
})
```

Merge rules: the preset's non-empty `DisplayName` / `Validate` win; each
preset field (matched by `Name`) overlays its non-empty strings, non-nil
hooks, and **additive** `ReadOnly`/`Hidden`/`Sortable`/`Searchable` (a `true`
turns the flag on; forcing one *off* means editing the returned `mm`
directly). Relation metadata (`RelationKind`, `FKFieldName`, the relation
hooks) stays derive-authoritative.

`DeriveMetaModel` **panics at startup** on a programming error — a non-struct
`T`, or a preset field `Name` the type doesn't have (a typo / renamed field).
This is the `regexp.MustCompile` idiom: fail loudly at boot, not silently at
first render. Pass the zero `crud.MetaModel[T]{}` for pure defaults.

`MetaField` describes one field. Its relation metadata is filled by
derivation; `RelatedURLBase` (the related table's absolute URL) is left blank
and filled later by `WireRelations` / `Admin`:

```go
type MetaField struct {
    Name, DisplayName, FormInputType, FormHelp string
    Hidden, ReadOnly, Multiple, Sortable, Searchable bool

    RelationKind    RelationKind // NotRelation | RelationSingle | RelationMany2Many | RelationHasMany
    RelatedURLBase  string       // related table URL; blank until wired
    RelatedTypeName string       // Go type name of the related model
    FKFieldName     string       // RelationSingle: sibling FK uint
    FormFieldName   string       // POST key (defaults to Name)

    // The three generic per-field transforms; nil keeps the derived hook.
    DisplayValue   func(mf MetaField, value any) templ.Component         // table/detail cell
    GenFormElement func(mf MetaField, value any) templ.Component         // whole form <input>
    BindStrings    func(mf MetaField, strs []string, instance any) error // parse form value(s) → struct
    FieldValidate  Validator                                             // per-field server validator
}
```

The preset is the usual way to set per-field metadata. For the rare
post-construction tweak, `FindField` returns a mutable pointer into the model:

```go
func (mm *MetaModel[T]) FindField(name string) (*MetaField, error)
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
  {Name: "TOTPSecret", ReadOnly: true, DisplayValue: crud.Redact},
  ```

- **A write-only password field** — empty box in the form, redacted in the
  table; a non-blank entry is re-hashed and stored, a blank entry keeps the
  current value:

  ```go
  {Name: "PasswordHash", DisplayName: "Password", FormInputType: "password",
      DisplayValue:   crud.Redact,
      GenFormElement: crud.PasswordInput,
      BindStrings:    crud.HashWith(auth.HashPassword)}, // auth.HashPassword = argon2id
  ```

`auth.HashPassword` hashes the same way the login path verifies. See
`examples/auth_gorm` for the User table using all of this.

## The data plane — `Accessor[T]`

A `CRUDTable` is backend-blind: every read and write goes through an
`Accessor[T]`. The library ships two; a third backend is just a third
implementation of this interface.

```go
type Accessor[T any] interface {
    Get(ctx context.Context, id uint) (T, error)
    List(ctx context.Context, search, sortBy string, sortDesc bool, offset, limit int) ([]CRUDSearchResult[T], int64, error)
    Create(ctx context.Context, in T) (id uint, out T, err error)
    Update(ctx context.Context, id uint, in T) (out T, err error)
    Delete(ctx context.Context, id uint) error
}

func GORMAccessor[T any](mm MetaModel[T], db *gorm.DB) Accessor[T]
func MapAccessor[T any](mm MetaModel[T], store map[uint]T, mu *sync.RWMutex) Accessor[T]
```

Both constructors take the `mm` so they can resolve searchable/sortable
fields to columns (GORM) or reflection lookups (map) once, up front. Build
the accessor from the **same** `MetaModel` you hand to `NewTable`.

`MapAccessor` searches with a case-insensitive substring match over every
`Searchable` field and sorts by reflection; the map and mutex stay the
caller's. `GORMAccessor` searches Searchable columns with `LIKE`, preloads
associations on reads, and replays many-to-many selections on update.

## Building the table — `NewTable`

```go
func NewTable[T any](mm MetaModel[T], data Accessor[T], pageSize int, authz auth.Authz) CRUDTable[T]
```

`pageSize` is rows per page (0 = library default, 20); `authz` gates every
route (nil = allow all). Create / edit / delete are enabled by default. The
returned `CRUDTable[T]` is a plain struct with public fields, so anything the
constructor doesn't cover is set afterward:

```go
table := crud.NewTable(mm, data, 10, gate)
table.CreateEnabled = false           // granular mutation toggles
table.HideUnauthorized = true         // omit disallowed buttons (vs render disabled)
table.Segment = "heroes"              // path segment override (irregular plurals)
table.ShortLabel = func(h Hero) string { return h.Name } // relation label (see Relations)
```

## Routing surface

A table registers its in-component (fragment) endpoints **relative to the
router it is handed**, and renders absolute links from a base it is *told*:

```go
func (c *CRUDTable[T]) RegisterRoutes(r chi.Router, routerPrefix, componentPath string)
func (c *CRUDTable[T]) Render(r *http.Request) (templ.Component, error)
func (c *CRUDTable[T]) URLBase() string // absolute base, e.g. "/admin/heroes"
```

- **`componentPath`** is where the table sits relative to `r` — one or more
  segments, e.g. `"/heroes"` or `"/admin/heroes"`. Empty falls back to the
  table's `Segment`, then a derived plural of the model name.
- **`routerPrefix`** is the absolute path at which `r` itself is served (`""`
  when `r` is the root mux). The caller knows this; chi can't report it at
  registration time. The table's absolute base, used for every rendered
  `hx-get` / form action, is `routerPrefix + componentPath`.

The **app owns the page route**: it calls `Render(r)` and wraps the result in
its own shell. `RegisterRoutes` registers only the fragments. For
`componentPath="/admin/heroes"`, `routerPrefix=""`:

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

One rule: **`routerPrefix` must equal the absolute prefix at which the passed
router is served.** Because routes register relative to `r`, the table
composes on the root mux without a stripping `chi.Route`, and under one too:

| caller wiring                         | call                                              | served at         |
|---------------------------------------|---------------------------------------------------|-------------------|
| root mux                              | `RegisterRoutes(root, "", "/heroes")`             | `/heroes/…`       |
| root mux, multi-segment path          | `RegisterRoutes(root, "", "/admin/heroes")`       | `/admin/heroes/…` |
| `r.Route("/app", …)` (strips)         | `RegisterRoutes(r, "/app", "/heroes")`            | `/app/heroes/…`   |
| `r.Mount("/app", sub)` (strips)       | `RegisterRoutes(sub, "/app", "/heroes")`          | `/app/heroes/…`   |

Multi-segment component paths and stripping mounts both work — the rendered
HTML carries the right absolute URLs because they're built from
`routerPrefix + componentPath`, never reverse-engineered from the request.

## `Admin`

Bundles tables behind a sidebar. Navigation between tables is plain page
navigation (each sidebar entry is a real link); the server renders the whole
page on each load and marks the active entry from the request path — no JS
for the active highlight.

```go
type Admin struct {
    Tables []CRUDTableInterface
    Authz  auth.Authz
    SidebarTop, SidebarBottom []SidebarLink // optional app-defined links
}

func DeriveAdmin(tables []CRUDTableInterface, az auth.Authz) Admin

func (a *Admin) RegisterRoutes(r chi.Router, routerPrefix, componentPath string, shell site.Shell) error
func (a *Admin) Render(r *http.Request) (templ.Component, error)
```

`Admin.RegisterRoutes` composes every path on the router it is handed — **no
stripping `chi.Route` needed**, just the root mux. Each child table is
mounted at `componentPath + "/" + its URLSlug` (a lowercased plural of the
model name, or its `Segment` override). It also registers an index redirect
to the first table, a per-slug page handler wrapping the active table in
`shell`, and links the children's relation fields (see [Relations](#relations)):

```go
admin.RegisterRoutes(root, "", "/admin", pageShell)
```

Registered for `componentPath="/admin"`, tables `["heroes","weapons","skills"]`:

| Method | Path                               | Returns                                  |
|--------|------------------------------------|------------------------------------------|
| GET    | `/admin`                           | 303 redirect to `/admin/heroes`          |
| GET    | `/admin/{slug}`                    | full page (sidebar + active table)       |
| GET    | `/admin/heroes/view`, `/create`, … | each child table's fragment endpoints    |

`shell == nil` registers the index redirect and child fragments but no
per-slug page handler.

Children's URL segments come from `URLSlug()` — a lowercased plural of the Go
type name. For an irregular plural (or a model named `UserGORM` you want at
`/admin/users`), set `Segment` on the child table before building the Admin:

```go
userTable.Segment = "users" // else /admin/usergorms
```

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
  heroTable.RegisterRoutes(r, "", "/heroes")
  weaponTable.RegisterRoutes(r, "", "/weapons")
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
model, set `ShortLabel` on its table — it drives both that table's `<select>`
options and the model's relation cells on other tables (propagated by
`WireRelations`):

```go
heroTable.ShortLabel = func(h Hero) string { return h.Name + " — " + h.Realm }
```

## Single-instance primitives

`MetaModel` also exposes the render/bind primitives the table composes
internally. Reach for them to render a one-off form / dump outside a table,
owning the routing and data yourself (see `examples/form_mem`):

```go
func (mm *MetaModel[T]) RenderDisplay(instance T) templ.Component
func (mm *MetaModel[T]) RenderForm(instance T, opts FormOpts) templ.Component
func (mm *MetaModel[T]) TryBindForm(r *http.Request, out *T) error // ParseForm + bind + validate
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

## Authz

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

- **L1 — per-table** — `#{key}-modal-l1` — emitted by each `Render()`, where
  `{key}` is the table's component path sanitized to a DOM id
  (`/admin/heroes` → `admin-heroes`).
- **L2 — shared singleton** — `#crud-modal-l2` — for the nested "+ create
  new" a relation picker opens. Auto-embedded by `Render`.

Open and close are **client-driven** — the server carries no modal ids. A
small JS bridge (auto-embedded by `Render` via `PageModals`) keeps a push/pop
stack of open dialogs and does two things:

- **Auto-open**: when a *GET* fetches a form into a `.crud-modal-body` (the
  body of an L1 or L2 dialog), it opens that dialog and pushes it. The GET
  guard means a POST submit — whose `Reswap:none` still fires `afterSwap` —
  never re-opens the modal it just closed.
- **Generic close**: on the `crud-close-modal` event (emitted on a successful
  mutation) it closes the *topmost* dialog, so a nested "+ new" create closes
  itself and leaves its parent form open. An Esc / backdrop close keeps the
  stack in sync.

The server only decides the *outcome* of a submit, via `gone/htmx`
directives — it never names a dialog:

| Outcome          | Directives (`htmx.Reply()…Apply(w)`)                                   |
|------------------|------------------------------------------------------------------------|
| Validation error | *(none)* — the form re-renders into its own `hx-target` (the modal body), modal stays open |
| L1 save success  | `Trigger("crud-close-modal", nil).Retarget("#<list>").Reswap("innerHTML")`, body = refreshed table |
| L2 save success  | `Trigger("crud-close-modal", nil).Trigger("refresh-relation", true).Reswap("none")` |

`refresh-relation` makes every relation `<select>` re-fetch its options so a
freshly-created row appears — without disturbing any other field.

With JS off, mutation POSTs fall back to a `303` redirect to the list, so the
app still works as a plain MPA.

## Pagination

`pageSize` (default 20) controls rows per page; `?page=N` selects it
(1-indexed). The renderer emits a DaisyUI `join` button group with prev/next
+ numbers. Page / sort / search links are HTMX with `hx-push-url`, so the
state is a real, shareable URL — a reload re-renders the full page in that
state. Search and sort reset to page 1.

## Composition trade-offs

- **One stateful table per page.** Bookmarkable state (`hx-push-url`) lives in
  the table's own URL; multiple tables can't all own the address bar. Admin
  shows one table at a time, which sidesteps this.
- **Distinct component paths.** Two tables on one page (or in one Admin) MUST
  be mounted at distinct paths — the per-table L1 modal IDs derive from the
  component path, so a shared path would collide. The default
  `lowercase(Name)+"s"` segment keeps distinct Go types apart; sharing a path
  deliberately is a configuration bug.

## Testing

The primary lever is **HTTP end-to-end tests** via `net/http/httptest`
against a `chi.Router`: rows render, search filters, POST persists,
`HX-Request` flips the delete response from 303 to a fragment, the relation
`<select>` carries the right `/options` URL, …

Unit tests cover the reflection-heavy primitives that aren't naturally
observable through HTTP: `DeriveMetaModel` (preset merge, unknown-field
panic), `DefaultBindForm`, the built-in validators (incl. IPv4/IPv6),
`FindField`, and the secret-field helpers.

`go test ./...` — no external deps; SQLite is in-memory.

## Examples

| Path                  | Shows                                                                                          |
|-----------------------|------------------------------------------------------------------------------------------------|
| `examples/form_mem`   | Single struct + manual handlers using `RenderForm` / `TryBindForm`. IPv4-or-IPv6 via `Any(IPv4Addr, IPv6Addr)`. |
| `examples/crud_mem`   | One table via `DeriveMetaModel` + `MapAccessor` + `NewTable`, in-memory map backend.           |
| `examples/crud_gorm`  | Three tables (Hero, Weapon, Skill) with 1:N and N:M relations via `GORMAccessor` + `WireRelations`. GORM backend, MPA tab nav. |
| `examples/admin_gorm` | Same schema wrapped in `Admin` — zero per-field config (empty presets, default slugs), relations auto-wired. |
| `examples/auth_gorm`  | `AuthGORM` + `Admin` over User/Group, with `Authz`, a write-only password field, and a `DisplayValue` override. |

`go run ./examples/<name>` — each starts on `:8080`.
