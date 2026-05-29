# gone/crud — CRUD UI from a model

This document is the user-facing reference for `github.com/tmshlvck/gone/crud`.
For the design rationale see [`PRD-CRUD.md`](../PRD-CRUD.md). For the
parallel auth/CSRF/RBAC design see [`PRD-AUTH.md`](../PRD-AUTH.md).

## What it does

You describe a Go struct once. The library gives you:

- **A list page** (search + sort + pagination + per-row edit / delete).
- **A create form** and **an edit form** with per-field validators, a
  cross-field hook, and HTMX modal flow.
- **A display fragment** for a single row.
- **Relation pickers** that auto-wire across `CRUDTable`s by Go type
  name — including a nested "+ create new" modal that doesn't clobber
  the parent form.
- **An Admin** that bundles many `CRUDTable`s under one URL prefix with
  an HTMX-boosted sidebar.

Backends today: in-memory map (`DeriveMapCRUDTable`), GORM
(`DeriveGormCRUDTable`). New backends drop in by writing a constructor.

The library emits **HTML fragments** — no `<html>/<body>/<style>` —
plus the JS bridge that wires HTMX modal events to DaisyUI dialogs.
Page chrome (head, navbar, theme, auth-aware footer) is supplied by
the caller via a `PageShellFunc`.

## Quick taste

```go
type Hero struct {
    ID    uint
    Name  string
    Power int
}

func main() {
    store := map[uint]Hero{1: {1, "Aragorn", 90}}
    mm, _ := crud.DeriveMetaModel[Hero]()
    mm.MustFindField("Name").FieldValidate = crud.All(crud.NotEmpty, crud.MaxLen(30))
    mm.MustFindField("Power").FieldValidate = crud.IntRange(0, 100)

    table := crud.DeriveMapCRUDTable[Hero](mm, nil, store, &sync.RWMutex{})
    table.Slug = "heroes"

    mux := http.NewServeMux()
    url, err := table.Route(mux, "/", pageShell)
    if err != nil { log.Fatal(err) }
    mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
        http.Redirect(w, r, url, http.StatusSeeOther)
    })

    log.Fatal(http.ListenAndServe(":8080", mux))
}

func pageShell(w http.ResponseWriter, r *http.Request, title string, content templ.Component) {
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    appLayout(title, content).Render(r.Context(), w)
}
```

`appLayout` is a templ component the caller writes — DaisyUI + Tailwind
+ HTMX loaded in its `<head>`; `content` rendered in `<main>`.

## Stack assumed

| Concern        | Choice                                                       |
|----------------|--------------------------------------------------------------|
| Language       | Go 1.24+ (generics + 1.22 `ServeMux`)                        |
| Templating     | [templ](https://github.com/a-h/templ)                        |
| ORM            | GORM v2 (`gorm.io/gorm`) for the GORM backend                |
| Router         | stdlib `net/http`; `chi` works (use `chi.Group` for middleware) |
| Styling        | [DaisyUI v5](https://daisyui.com) + Tailwind v4 in the caller's page shell |
| HTMX           | `htmx.org@2` in the caller's page shell                      |

The library bundles no CSS / JS / static assets. Examples load
DaisyUI + Tailwind + HTMX from jsDelivr/unpkg.

## Core types

### `MetaField`

One field of a model. Describes how to render, bind, validate.

```go
type MetaField struct {
    Name          string
    DivID         string
    DisplayName   string
    FormInputType string   // "text" | "number" | "checkbox" | "datetime-local" | "email" | …
    FormHelp      string
    Hidden        bool     // hide everywhere
    ReadOnly      bool     // visible but not in the form
    Multiple      bool
    Sortable      bool     // column header is a sort link
    Searchable    bool     // included in case-insensitive search

    RelationKind    RelationKind   // NotRelation | RelationSingle | RelationMany2Many | RelationHasMany
    RelatedCRUD     CRUDTableInterface
    RelatedTypeName string         // Go type name; used by AutoWireRelations
    FKFieldName     string         // for RelationSingle: sibling FK uint
    FormFieldName   string         // POST key (defaults to Name; relation single uses FKFieldName)

    DisplayValue   func(mf MetaField, value any) templ.Component
    GenFormElement func(mf MetaField, value any) templ.Component
    FromStrings    func(mf MetaField, strs []string, instance any) error
    FieldValidate  Validator
}
```

Defaults are installed by `DeriveMetaModel` — caller post-mutates fields
to override.

### `MetaModel[T any]`

Pure metadata + render / bind helpers. No routing state, no data
accessors, no authz.

```go
type MetaModel[T any] struct {
    Fields      []MetaField
    Name        string
    Slug        string                              // url-safe singular; default lowercase Name
    DisplayName string
    Validate    func(instance T) error              // cross-field validator (optional)
}

func DeriveMetaModel[T any]() (MetaModel[T], error)
func (mm *MetaModel[T]) FindField(name string) (*MetaField, error)
func (mm *MetaModel[T]) MustFindField(name string) *MetaField

func (mm *MetaModel[T]) RenderDisplay(instance T) templ.Component
func (mm *MetaModel[T]) RenderForm(instance T, opts FormOpts) templ.Component

// Parse the request form and bind it into out. Returns ValidationErrors
// (which implements error) on validation failure.
func (mm *MetaModel[T]) TryBindForm(r *http.Request, out *T) error
```

`FormOpts` for `RenderForm`:

```go
type FormOpts struct {
    ActionURL   string
    HXTarget    string             // CSS selector for hx-target; empty = browser-only
    SubmitLabel string             // "Save", "Create"
    Title       string             // optional <h3> above the form
    Errors      ValidationErrors   // raw error from TryBindForm — template splits it
    SuccessMsg  string             // optional green alert
}
```

### `CRUDTable[T any]`

Wraps a MetaModel with backend closures + authz + routing.

```go
type CRUDTable[T any] struct {
    MetaData      MetaModel[T]
    Authz         AuthzInterface       // nil = AllowAll
    Slug          string               // url-safe plural; default lowercase(Name)+"s"
    PageSize      int                  // 0 = library default (20)
    CreateEnabled, EditEnabled, DeleteEnabled bool
    PageTitle     string               // shell title; default MetaData.DisplayName
    // Internal: urlBase, ListID, Get/List/Create/Update/Delete closures
}

// Backends.
func DeriveMapCRUDTable[T any](mm MetaModel[T], authz AuthzInterface, store map[uint]T, mu *sync.RWMutex) CRUDTable[T]
func DeriveGormCRUDTable[T any](mm MetaModel[T], authz AuthzInterface, db *gorm.DB) CRUDTable[T]

func (c *CRUDTable[T]) Route(mux Mux, baseUrl string, shell PageShellFunc) (string, error)
func (c *CRUDTable[T]) Render(r *http.Request) (templ.Component, error)

func (c *CRUDTable[T]) URLBase() string         // absolute path; set by Route
func (c *CRUDTable[T]) HTMXTableURL() string    // URLBase + "/view"
func (c *CRUDTable[T]) HTMXCreateURL() string   // URLBase + "/create"
```

Routes registered, for `baseUrl="/admin"` and `Slug="heroes"`:

| Method | Path                              | Returns                                          |
|--------|-----------------------------------|--------------------------------------------------|
| GET    | `/admin/heroes`                   | main page (only when `shell != nil`)             |
| GET    | `/admin/heroes/view`              | table fragment for HTMX swap                     |
| GET    | `/admin/heroes/create`            | create form fragment                             |
| POST   | `/admin/heroes/create`            | create submit                                    |
| GET    | `/admin/heroes/{id}/edit`         | edit form fragment                               |
| POST   | `/admin/heroes/{id}/edit`         | edit submit                                      |
| POST   | `/admin/heroes/{id}/delete`       | delete (HTMX → rows fragment; else 303)          |
| GET    | `/admin/heroes/{id}/display`      | per-row dump fragment                            |
| GET    | `/admin/heroes/options`           | relation-picker option list                      |

Every handler gates on `c.Authz`.

### `Admin`

Bundles tables behind a sidebar.

```go
type Admin struct {
    Tables []CRUDTableInterface
    Authz  AuthzInterface
    Slug   string  // default "admin"
}

func DeriveAdmin(tables []CRUDTableInterface, authz AuthzInterface) Admin
func DeriveAdminAutoWire(tables []CRUDTableInterface, authz AuthzInterface) Admin

func (a *Admin) Route(mux Mux, baseUrl string, shell PageShellFunc) (string, error)
func (a *Admin) Render(r *http.Request) (templ.Component, error)
```

`Admin.Route(mux, "/", shell)` mounts Admin at `baseUrl + "/" + Slug`
(default `/admin`) and **auto-routes every child table** under it.
The caller doesn't call `table.Route()` separately.

Registered for `Admin.Slug = "admin"` and tables `["heros", "weapons", "skills"]`:

| Method | Path                                  | Returns                                              |
|--------|---------------------------------------|------------------------------------------------------|
| GET    | `/admin`                              | 303 redirect to `/admin/heros`                       |
| GET    | `/admin/{slug}`                       | full page (sidebar + active table) via `shell`       |
| GET    | `/admin/heros/view`, `/create`, …     | child table's HTMX endpoints                         |
| GET    | `/admin/weapons/view`, …              | child table's HTMX endpoints                         |
| GET    | `/admin/skills/view`, …               | child table's HTMX endpoints                         |

`DeriveAdminAutoWire` additionally calls each table's
`AutoWireRelations` so `RelatedCRUD` pointers between tables fill
themselves in by matching Go type names — no manual wiring needed.

To mount Admin at the URL root, set `Admin.Slug = ""` before `Route`.

### `PageShellFunc`

The only chrome boundary between library and app.

```go
type PageShellFunc func(w http.ResponseWriter, r *http.Request, title string, content templ.Component)
```

- Receives the HTTP writer and request directly — can redirect on auth
  failure, set headers, etc.
- `title` is supplied by the component (table's `PageTitle`, or the
  active table's `DisplayName` for Admin per-slug pages).
- `content` is the fragment to embed in the page chrome.
- `nil` on Route → library doesn't register the main page handler.

Typical implementation:

```go
func pageShell(w http.ResponseWriter, r *http.Request, title string, content templ.Component) {
    user, _ := r.Context().Value(userKey{}).(*User)
    if user == nil {
        http.Redirect(w, r, "/login", http.StatusSeeOther)
        return
    }
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    appLayout(title, user, content).Render(r.Context(), w)
}
```

### `AuthzInterface`

```go
type AuthzInterface interface {
    CanList(r *http.Request) bool
    CanRead(r *http.Request) bool
    CanCreate(r *http.Request) bool
    CanUpdate(r *http.Request) bool
    CanDelete(r *http.Request) bool
}
```

`nil = AllowAll`. `crud.AllowAll{}` is also available as a named no-op.

### `Mux`

```go
type Mux interface {
    HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}
```

`*http.ServeMux` and `chi.Router` both satisfy it. The library only
needs `HandleFunc`.

For middleware layering with chi: use `chi.Group` (preserves prefix).
**Don't** use `chi.Mount` / `chi.Route` — they prefix-strip, which
breaks the absolute URLs the library renders in HTML.

## Validation

Per-field validators run after `FromStrings` populates the field;
model-level `Validate` runs only when every field passed.

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
func IPv4Net(value any) error           // CIDR
func IPv6Net(value any) error           // CIDR

// Combinators:
func All(vs ...Validator) Validator     // AND, first failure wins
func Any(vs ...Validator) Validator     // OR, joins failures with " or "
```

Errors come back as a `ValidationErrors` map (field name → message;
`""` for model-level). Feed it directly into `FormOpts.Errors` — the
template splits it for the banner + per-field display.

```go
type ValidationErrors map[string]string

const ModelLevelKey = ""   // empty key for cross-field

func (e ValidationErrors) Error() string

// Wraps any error into a ValidationErrors map; non-validation errors
// land under ModelLevelKey.
func ValidationErrorsFromError(err error) ValidationErrors
```

## HTMX modals

Create / edit forms open in two stacked DaisyUI dialogs.

- **L1 — per-table** — `#{slug}-modal-l1` — emitted by each
  `CRUDTable.Render()`.
- **L2 — shared singleton** — `#crud-modal-l2` — for nested "+ create
  new" opened by a relation picker. Auto-embedded inside `CRUDTable.Render`
  so callers don't need a separate include.

Each dialog has an X close button + backdrop click.

**Signaling outcomes.** The form's HTMX attributes are static:
`hx-post=<action> hx-target=#{slug}-modal-l*-body`. The server uses
response headers to decide what happens:

| Outcome              | Response                                                                      |
|----------------------|-------------------------------------------------------------------------------|
| Validation error     | `HX-Retarget: #<bodyID>`, `HX-Reswap: innerHTML`, body = form with errors      |
| L1 save success      | `HX-Retarget: #<wrapper>`, body = refreshed table fragment + `HX-Trigger: closeModal:<modalID>` |
| L2 save success      | `HX-Reswap: none`, `HX-Trigger: {closeModal:<id>, refresh-relation: true}`     |

The page-shell JS bridge (auto-embedded by the library) listens for
`closeModal` / `refresh-relation` events on the body and acts accordingly.

**Relation widget refresh.** After an L2 save, every relation `<select>`
on the page re-fetches its options via `GET {relatedBase}/options` so
the freshly-created row appears in the dropdown — without disturbing
any other form field.

**Modal open timing.** Click → fetch → response → swap → open. On slow
networks the page sits still until the form arrives. Acceptable
MPA-feel; a "spinner-then-swap" upgrade is small if needed later.

## Pagination

`CRUDTable.PageSize` (default 20) controls rows per page. `?page=N`
selects the page (1-indexed). The renderer emits a DaisyUI `join`
button group with prev/next + clickable numbers. All page links are
HTMX with `hx-push-url`, so bookmarking / back-button work. Search and
sort changes reset to page 1.

## Composition trade-off

CRUDTable bookmarkable state (via `hx-push-url`) lives in the table's
own URL. Multiple CRUDTables on one page can't all push their state
to the URL bar — pick **at most one** stateful table per page. Admin
handles this naturally by showing one table at a time in the working
area; for "two tables side by side", treat that as a future extension
that disables URL state on the secondary table.

## Slug uniqueness

Two CRUDTables on the same page (or inside the same Admin) **MUST**
have distinct `Slug` values — per-slug L1 modal IDs would otherwise
collide. Default `lowercase(Name) + "s"` heuristic avoids accidental
collisions for distinct Go types; deliberately sharing a slug is a
configuration bug.

## chi sub-router caveat

The library renders absolute URLs. Mounting it under a prefix-stripping
sub-router (`http.StripPrefix`, `chi.Mount`, `chi.Route`) breaks the
emitted URLs because the prefix is invisible to the handlers.

For middleware layering with chi, use `chi.Group` (preserves prefix)
and pass the full external URL to `Route`.

## Testing

Primary lever is **HTTP end-to-end tests** via `net/http/httptest`. The
package's exported behavior is the routes registered by `Route`; most
of what's worth verifying is observable through them — rows render,
search filters, POST persists, HX-Request flips the delete response
from 303 to fragment, …

Unit tests cover the reflection-heavy primitives that aren't naturally
observable through HTTP: `DeriveMetaModel`, `DefaultBindForm`, the
built-in validators (incl. the IPv4/IPv6 ones), `FindField` /
`MustFindField`, the normalize-prefix helper.

`go test ./...` — no external deps; SQLite is in-memory.

## Examples

| Path                          | Shows                                                        |
|-------------------------------|--------------------------------------------------------------|
| `examples/form_mem`           | Single struct + manual handlers using `RenderForm` / `TryBindForm`. IPv4-or-IPv6 validator via `Any(IPv4Addr, IPv6Addr)`. |
| `examples/crud_mem`           | One `CRUDTable`, in-memory map backend.                       |
| `examples/crud_gorm`          | Three `CRUDTable`s (Hero, Weapon, Skill) with 1:N and N:M relations. GORM backend. MPA-style with tab nav. |
| `examples/admin_gorm`         | Same schema as `crud_gorm`, but wrapped in `Admin` with `DeriveAdminAutoWire`. Zero per-field tweaking — defaults all the way. |

`go run ./examples/<name>` — each starts on `:8080`.
