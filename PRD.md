# gone — Go Object Navigation Elements — PRD

## 1. Purpose

`gone` is a Go library that renders **HTMX-driven CRUD UIs** from a model's
metadata, support for **JSON API endpoints** defined in the program, with a
batteries-included surface for **auth, authorization, and observability**.
The intent is "describe a model once and render three ways (table, form, dump),
and slot the result into a session-authenticated app behind a reverse proxy,
creating both the app components for simple CRUD part and an admin interface for
the models.

Most consumers will be **HTMX multi-page apps**. A subset of routes will be
**JSON APIs** for programmatic clients (with API keys that inherit the user's
authorizations).

## 2. Goals

1. **Describe a model once.** A `MetaModel` drives table, form, dump, CRUD
   module and optional JSON endpoints renderings of the same entity.
2. **Auto-generated metadata.** Given a Go struct (typically a GORM model),
   `gone` reflects field names, types, and relations to produce a default
   `MetaModel`. The caller may then override individual fields, wire custom
   validators / transforms, or supply the whole `MetaModel` explicitly.
3. **Generic accessors → backend agnosticism.** Field accessors are
   reflection-derived getters/setters by default. This means the same
   `MetaModel` can be backed by GORM, an in-memory map, or a single struct
   wrapper. GORM is first-class; the others are nice to have.
4. **Pluggable validators + transforms.** Per-field hooks for validating
   input and transforming values between the wire (forms/JSON) and the model.
5. **Safe HTML by default.** All interpolated values escape; an explicit
   `templ.SafeString`-style escape hatch exists for callers that want to
   inject raw HTML.
6. **Session-cookie + CSRF baseline.** Every mutating route goes through a
   CSRF check from day one. Anonymous CSRF works (no login required).
7. **Authorization model.** A reusable `User` / `Group` / `APIKey` model
   The DNS-zone walk-through in §10 is the reference scenario.
8. **Observability defaults.** Structured logs (`log/slog`), Prometheus
   metrics, request IDs.
9. **Deploy behind a proxy.** Trust list for `X-Forwarded-*`, optional
    PROXY-protocol listener.
10. **Library, not framework.** Components return `http.Handler`-shaped
    values. Callers wire them onto whatever router they already use
    (subject to §4's open question).

## 3. Non-goals (this PRD)

- **Multi-DB user federation.** A single `User` table with optional
  external-auth links (OIDC `sub`, TOTP secrets) is enough.
- **Field-level audit logging.** Out of scope; can be added per-model via
  GORM hooks.
- **GraphQL.** Out of scope.
- **Background job queue.** Out of scope; bring your own.

## 4. Stack

### 4.1 Decided

| Concern              | Choice                                                                    |
|----------------------|---------------------------------------------------------------------------|
| Language             | Go 1.24+ (need generics + 1.22 `ServeMux`)                                 |
| ORM                  | **GORM v2** with the pure-Go `glebarez/sqlite` driver in examples         |
| Templating           | **templ** (`a-h/templ`) — type-safe, compiled, active                      |
| Router / framework   | stdlib `net/http` semantics + **chi** in examples                          |
| Password hashing     | `golang.org/x/crypto/bcrypt` (or argon2 via `argon2id` — TBD)              |
| TOTP                 | `pquerna/otp` (active 2025-08)                                            |
| OIDC client          | `coreos/go-oidc` + `golang.org/x/oauth2`                                  |
| Metrics              | `prometheus/client_golang`                                                |
| Structured logging   | `log/slog` (stdlib)                                                       |
| HTTP compression     | Defer to reverse proxy (Caddy / nginx) by default                          |
| Session middleware   | `alexedwards/scs/v2` in examples, no dependency in library                 |
| CSRF                 | hand-rolled                                                                |
| CORS                 | hand-rolled                                                                |

## 5. Security

### 5.1 Escaping

`templ` escapes all interpolated values by default. Raw HTML requires
`templ.Raw(string)` (the equivalent of Python's `Markup`). Per-field display
overrides in `MetaField.TableDisplay` may return either a plain string
(escaped) or `templ.Component` (already-rendered, trusted).

### 5.2 CSRF

A single CSRF token lives in the session. Every mutating route (POST /
PUT / DELETE / PATCH) is validated against either:

- form field `csrf_token`, **or**
- header `X-CSRF-Token` (HTMX path).

The token is **rotated on login** (session-fixation defense) and **cleared
on logout**. Anonymous CSRF works — the token is created on first form
render. Read-only routes (GET / HEAD / OPTIONS) bypass CSRF entirely.

`Crud` and `Form` components emit:

- `auth.CSRFField(ctx)` → `<input type="hidden" name="csrf_token" value="…">`
- `auth.CSRFHeaders(ctx)` → JSON for HTMX `hx-headers=` on delete buttons

### 5.3 Passwords

Argon2id by default (pure Go, no cgo). (Allow bcrypt accepted as legacy). Cost
parameters configurable; sensible defaults.

### 5.4 API keys

API keys need to be supported for selected endpoints. They authenticate via either:
- `Authorization: Bearer <key>` header, or
- query string for short polls where allowed by route policy.

The authorization model needs to support the key creation and validation.
Keys are hashed at rest. **API key requests bypass CSRF** (header-only
auth, no session involved) but still pass authorization checks. The
effective principal for an API-key request is the owning user — all
`read_authz` / `write_authz` decisions use that user.

## 6. Core abstractions

### 6.1 `MetaField`

Non-generic. Hooks take `value any` (the already-extracted Go-typed field
value) and `instance any` (the whole struct). `Derive*` installs the
default hooks; callers post-mutate fields to override.

```go
type MetaField struct {
    Name          string  // Go struct field name
    DivID         string  // id attribute on generated HTML wrapper
    DisplayName   string  // table + form label; default: title-case of Name
    FormInputType string  // <input type=...>: "text" | "number" | "checkbox" |
                          // "datetime-local" | "email" | "select" | …
    FormHelp      string  // help text; default empty
    Hidden        bool    // hide from table + form + dump
    ReadOnly      bool    // displayed but not editable
    Multiple      bool    // array / multi-value (e.g. <select multiple>)
    Sortable      bool    // table column header is a sort link (default true for comparable kinds)
    Searchable    bool    // included in case-insensitive substring search (default true for string kinds)

    // Render hooks return templ.Component so they compose with surrounding
    // markup. Defaults: scalar → text; slice/array → <ul> and recurse;
    // struct (relation row) → DefaultShortValue.
    DisplayValue   func(mf MetaField, value any) templ.Component
    GenFormElement func(mf MetaField, value any) templ.Component

    // FromStrings parses wire form values into the field's Go type and
    // writes them into instance via reflection. strs is form[mf.Name];
    // an empty slice means the field was absent (unchecked checkbox).
    FromStrings func(mf MetaField, strs []string, instance any) error

    // FieldValidate runs after FromStrings (validation pipeline §6.7).
    FieldValidate func(mf MetaField, instance any) error

    // Relation-only — wired by the caller after derivation. Used by
    // GenFormElement (option lists) and DisplayValue (short-name lookup).
    RelatedCRUD CRUDTableInterface
}

// Standard helpers; installed as defaults by DeriveMetaModel and exposed
// so callers can layer custom hooks on top.
func DefaultDisplayValue(mf MetaField, value any) templ.Component
func DefaultGenFormElement(mf MetaField, value any) templ.Component
func DefaultFromStrings(mf MetaField, strs []string, instance any) error
func DefaultShortValue(instance any) string  // "Id : Name" if both fields present
```

**Implementation status (2026-05-25):** `MetaField`, the four `Default*`
helpers, and `Sortable` / `Searchable` derivation are working in
`gone/crud/meta.go`. `RelatedCRUD` and `FieldValidate` are reserved
fields; their default hooks are unset until §6.7 lands.

### 6.2 `MetaModel[T any]`

Generic over the model type for the hooks that produce / consume `T`.
Fields are non-generic (see §6.1). Hooks take `mm` as their first
argument so a caller's post-mutation of `mm.Fields` is observed at
call time (avoiding closure-captures-value pitfalls).

```go
type MetaModel[T any] struct {
    Fields []MetaField

    Name        string           // type name (e.g. "Hero")
    DisplayName string           // table + form label; default: type name
    FormBanner  templ.Component  // optional HTML banner above the form

    // Model-level walks. Default implementations iterate Fields and call
    // each field's hook with the field's reflected value.
    DisplayValues   func(mm MetaModel[T], instance T) []templ.Component
    GenFormElements func(mm MetaModel[T], instance T) []templ.Component

    // BindForm parses a posted form into out, mutating it in place.
    // Default impl walks Fields and calls each MetaField.FromStrings.
    BindForm func(mm MetaModel[T], form map[string][]string, out *T) error

    // Cross-field validator. Nil = no model-level rule. See §6.7.
    Validate func(ctx context.Context, mm MetaModel[T], instance T) error

    // Short label for use inside other models' relation pickers.
    // Default: DefaultShortValue (Name field, else ID, else %v).
    ShortValue func(instance T) string
}

// DeriveMetaModel builds a MetaModel[T] by reflecting T and installing
// default hooks. Callers post-mutate to override individual fields
// (DisplayName, FormInputType, ReadOnly, …) or the model-level hooks.
func DeriveMetaModel[T any]() (MetaModel[T], error)

// DefaultDisplayValues / DefaultGenFormElements / DefaultBindForm are
// the installed walks. Available as named functions so callers can call
// them from custom hooks (e.g. validate, then call DefaultBindForm).
func DefaultDisplayValues[T any](mm MetaModel[T], instance T) []templ.Component
func DefaultGenFormElements[T any](mm MetaModel[T], instance T) []templ.Component
func DefaultBindForm[T any](mm MetaModel[T], form map[string][]string, out *T) error
```

**Implementation status (2026-05-25):** `MetaModel[T]`, `DeriveMetaModel[T]`,
and the three `Default*` walks ship in `gone/crud/meta.go`. Supports
`string`, signed/unsigned ints, floats, `bool`, `time.Time`. Validation
hooks (`MetaModel.Validate`, `MetaField.FieldValidate`) and
`ShortValue` defaults are reserved fields; concrete defaults land with
§6.7.

### 6.3 `CRUDTable[T any]`

Generic over `T` for the typed data hooks; non-generic methods on
`*CRUDTable[T]` satisfy the companion `CRUDTableInterface` so `Admin`
and `MetaField.RelatedCRUD` can hold a heterogeneous slice.

```go
// CRUDSearchResult bundles a row with the ID the backend assigned to it,
// so handlers don't have to dig into the model to discover the ID.
type CRUDSearchResult[T any] struct {
    ID  uint
    Row T
}

type CRUDTable[T any] struct {
    URLBase       string          // default: "/" + lowercase MetaData.Name
    MetaData      MetaModel[T]
    CreateEnabled bool            // default true
    EditEnabled   bool            // default true
    DeleteEnabled bool            // default true
    Authz         AuthzInterface  // nil = AllowAll

    // Data accessors. Derive* populates these with backend-specific closures.
    Get    func(ctx context.Context, id uint) (T, error)
    List   func(ctx context.Context, search, sortBy string, sortDesc bool, offset, limit int) ([]CRUDSearchResult[T], int64, error)
    Create func(ctx context.Context, data T) (uint, T, error)  // returns assigned ID
    Update func(ctx context.Context, id uint, data T) (T, error)
    Delete func(ctx context.Context, id uint) error
}

// Backend-specific constructors. Each installs closures appropriate to
// its storage; the rest of the library treats them uniformly.
func DeriveMapCRUDTable[T any](store map[uint]T, mu *sync.RWMutex, mm MetaModel[T]) CRUDTable[T]
func DeriveGormCRUDTable[T any](db *gorm.DB, mm MetaModel[T]) CRUDTable[T]
func DeriveStructCRUDTable[T any](v *T, mu *sync.RWMutex, mm MetaModel[T]) CRUDTable[T]

// PageShell lets callers wrap each rendered fragment in their own site
// chrome. nil = serve fragments raw (useful for HTMX partial swaps).
type PageShell func(title string, content templ.Component) templ.Component

// Route registers list/create/edit/delete handlers under c.URLBase using
// Go 1.22 method+pattern syntax. Routes registered:
//   GET    {base}              — list (?q=, ?sort=, ?desc=1)
//   GET    {base}/create       — create form    (if CreateEnabled)
//   POST   {base}/create       — submit create  (if CreateEnabled)
//   GET    {base}/{id}/edit    — edit form      (if EditEnabled)
//   POST   {base}/{id}/edit    — submit update  (if EditEnabled)
//   POST   {base}/{id}/delete  — delete         (if DeleteEnabled)
func (c *CRUDTable[T]) Route(mux *http.ServeMux, shell PageShell) error

// CRUDRelationOption is the type-erased row used in cross-model relation
// pickers (e.g. a <select> on Hero pulling options from a Skill CRUD).
type CRUDRelationOption struct {
    ID        uint
    Instance  any
    ShortName string  // from RelatedCRUD's MetaModel.ShortValue
}

// CRUDTableInterface is the non-generic surface. Admin and
// MetaField.RelatedCRUD hold this; *CRUDTable[T] satisfies it.
type CRUDTableInterface interface {
    DisplayName() string
    HTMXTableURL() string                                                    // for Admin's index
    HTMXCreateURL() string                                                   // for the "+" button on a relation widget
    SearchOptions(ctx context.Context, search string) ([]CRUDRelationOption, int64, error)
    GetOptionsByID(ctx context.Context, ids []uint) ([]CRUDRelationOption, error)
}
```

**Default views.** `gone/crud` ships three templ components driven by
`*ViewData` structs:

- `DumpView(DumpViewData)` — single-instance key/value rendering. Used
  by `form_mem` and as the "show" page in a future Dump component.
- `TableView(TableViewData)` — multi-row list with search input,
  sortable headers, inline edit/delete buttons.
- `FormView(FormViewData)` — create or edit form (caller picks
  ActionURL + SubmitText).

The components emit page **fragments** — no `<html>/<body>`. Callers
supply page chrome via `PageShell` (per-app styling, navigation,
auth-aware menus). HTMX partial responses pass `shell = nil`.

**Backends:**

- **`DeriveMapCRUDTable[T]`** — `map[uint]T` with auto-assigned IDs.
  Reflection-based search (case-insensitive substring across
  `Searchable` fields) and sort (any comparable kind + `time.Time`).
  The mutex protects the whole map. Keeps a struct's `ID` field in
  sync with the map key if present.
- **`DeriveGormCRUDTable[T]`** *(TBD)* — reads/writes via `*gorm.DB`.
  Preloads top-level relations via `clause.Associations` on detail
  reads; flat on list reads. Search compiles to `db.Where("col LIKE ?
  OR col2 LIKE ?", q, q)` against `Searchable` fields enumerated from
  the schema at Derive time.
- **`DeriveStructCRUDTable[T]`** *(TBD)* — wraps a single caller-owned
  `*T`. `Get` returns the wrapped struct; `Update` mutates in place.
  `List` returns a single-element slice; `Create` / `Delete` return
  errors. This is the "edit one live instance" mode that
  `form_mem` currently demonstrates without going through a
  `CRUDTable` at all (it talks to `MetaModel` directly).

Per-operation hooks (Create / Update / Delete) fire **after** the
operation succeeds and run synchronously so errors surface to the
HTTP response.

**Implementation status (2026-05-25):** `CRUDTable[T]`, `Route`,
`PageShell`, `DumpView` / `TableView` / `FormView`, and
`DeriveMapCRUDTable[T]` ship and are exercised by `examples/crud_mem`.
`CRUDTableInterface`'s `SearchOptions` / `GetOptionsByID` are deferred
until the first relation example needs them. `DeriveGormCRUDTable` /
`DeriveStructCRUDTable` are the next backend implementations.

**Open: URLBase plurality.** Default is `"/" + strings.ToLower(mm.Name)`
which gives `/hero` for `Hero`. Apps almost always want `/heroes`. The
current pattern is post-Derive override. Could add a `Pluralize` option
or read a tag. Not blocking; defer.

### 6.4 `Admin`

```go
type Admin struct {
    Components []CRUDTableInterface
    Authz      AuthzInterface
}

func (a *Admin) Route(mux *http.ServeMux) error
```

### 6.5 `AuthzInterface`

```go
type AuthzInterface interface {
    CanList(r *http.Request) bool
    CanRead(r *http.Request) bool
    CanCreate(r *http.Request) bool
    CanUpdate(r *http.Request) bool
    CanDelete(r *http.Request) bool
}
```

Taking `*http.Request` keeps the surface router-agnostic — chi handlers
still receive `*http.Request`. A trivial `AllowAll` implementation ships
for tests and the single-struct backend's typical use cases.

### 6.6 Auto-derivation

`DeriveMetaModel[T]()` and `DeriveMapCRUDTable[T]()` ship today in
`gone/crud/`. `DeriveGormCRUDTable[T]` and `DeriveStructCRUDTable[T]`
are the next concrete backends; they reuse the same `MetaModel[T]`
shape so the rendering layer is unchanged.

### 6.7 Validation

**Per field, in this order:**

1. **Bind.** The MetaForm parses the wire value into the field's Go
   type. Failure here (`"abc"` into an `int`, missing required field on
   a JSON body) emits a `ValidationError{Code: "bind"}` and **skips
   layers 2–3 for that field** — there is no Go value to feed them.

2. **Per-field hook.** `MetaField.Validate(ctx, value) error` — runs
   only if layers 1 and 2 passed for this field. This is the "main
   place" for app-specific rules that tags can't express: uniqueness
   checks against the DB, cross-system lookups, business invariants on
   a single value.

**Across the whole submission, after all fields are done:**

3. **Model-level hook.** `MetaModel.Validate(ctx, T) error` — runs only
   if every per-field layer passed. For cross-field constraints
   (`StartDate < EndDate`, `Country == "US" → ZIP required`, …). The
   instance passed in is fully populated and per-field-valid; cross-
   field checks can rely on that.

All errors accumulate within a single submission rather than
short-circuiting on the first failure — the user sees every problem at
once, Pydantic-style. The single exception is the skip rule in (1):
fields with a bind error don't produce additional same-field errors
from layers 2–3.

```go
type ValidationError struct {
    Field   string         // MetaField.Name; "" for model-level
    Code    string         // stable identifier: "required", "min", "bind", "unique", …
    Message string         // human-readable English (default) or app-supplied
    Params  map[string]any // values referenced in Message (e.g. {"min": 2})
}

type ValidationErrors []ValidationError
```

The library ships sensible default English messages for the standard
validator tags. Callers can override per field via `MetaField.Validate`,
or globally via a hook on `MetaModel`. `Code` stays stable so test
assertions and structured logs don't break when wording changes.

`Form` and `Crud` collect the full `ValidationErrors` slice, render
each next to the offending field, and re-fill the form with the user's
input.

### 6.8 Form binding

`MetaModel.BindForm` walks `Fields` and calls each `MetaField.FromStrings`
to coerce the wire value into the field's Go type via reflection.
`DefaultFromStrings` handles `string`, signed/unsigned ints, floats,
`bool` (with the hidden-input/checkbox pairing trick), and `time.Time`
(HTML `datetime-local` format). Custom binders override per `MetaModel`.

### 6.9 Styling

`gone/crud` emits markup with **DaisyUI** + **TailwindCSS** classes
(`input input-bordered`, `btn btn-primary`, `table table-zebra`,
`form-control`, `alert alert-error`, …) and **assumes both are loaded
by the caller's page shell**. The library bundles no CSS and serves no
static assets — keeps gone a true library, lets the caller pick the
DaisyUI theme, integrates with the host app's existing Tailwind build.

The example shells (`examples/*/page.templ`) load **DaisyUI v5** plus
its **themes.css** bundle and the **Tailwind v4 browser CDN**:

```html
<link href="https://cdn.jsdelivr.net/npm/daisyui@5" rel="stylesheet" type="text/css"/>
<link href="https://cdn.jsdelivr.net/npm/daisyui@5/themes.css" rel="stylesheet" type="text/css"/>
<script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
```

DaisyUI v5 dropped the `-bordered` modifiers (`input-bordered` etc.) —
the base `input` / `select` / `textarea` classes now include borders by
default. Library views use the unsuffixed class names. Production apps
should still run their own Tailwind build with DaisyUI as a plugin; the
CDN is dev-only.

Buttons in the library are `btn-outline` by default (outlined border,
transparent fill) — DaisyUI's hover state automatically fills them. The
active pagination page is the one filled button per UI; everything else
shows outline-default / fill-on-hover.

Per-instance DOM IDs: `DeriveMetaModel` and `DeriveMapCRUDTable` mint
short random suffixes so multiple CRUDTables can share a page without
ID collisions. Format is `<role>_<rand>` (e.g. `table_zhk6dthk`,
`modal-content_4u2yznow`, `field_hostname_a1b2c3d4`). The HTTP handler
sends the modal id back through `HX-Trigger` JSON payloads
(`{"openModal":"<id>"}`) so the page-shell JS knows which dialog to
open or close.

The page shells in the examples include a light/dark theme toggle
(DaisyUI's `swap swap-rotate` with `data-theme-toggle` checkbox), pure
client-side: `prefers-color-scheme` on first load, persisted to
`localStorage`, applied as `data-theme` on `<html>`. No library code
involved — drop the same snippet into any consumer's page shell.

Component fragments emit **no `<html>`/`<body>`/`<style>`** — they're
fragments wrapped by the caller's `PageShell`. Direct full-page browser
navigation goes through the shell; HTMX requests (`HX-Request: true`)
get the raw fragment so it can be swapped into an already-rendered page.

### 6.10 HTMX partials

The whole `CRUDTable` UI is HTMX-driven. The library ships:

- A `<div id="crud-list">` wrapper around the table + count + pagination.
  All in-page list updates target it via `hx-target="#crud-list"`.
- `TableContent(d)` templ component returns the `<table>` + footer
  fragment — served by the **`GET {base}/rows`** partial endpoint
  registered by `Route`. Replaces `#crud-list` on every list-update
  HTMX swap.
- `hx-get` / `hx-target` / `hx-push-url` attributes on the search
  input, sortable column headers, pagination buttons, and the row
  delete buttons. Fallback `href` values keep the page functional with
  HTMX disabled.
- HX-Request detection on delete — HTMX delete returns the refreshed
  list fragment; browser delete returns a 303 redirect.

**Create / edit forms open in a DaisyUI modal.** The page contains a
hidden `<dialog id="crud-modal">` with `#modal-content` inside it. The
"+ Create" and per-row "edit" buttons `hx-get` the form into
`#modal-content`; the response carries `HX-Trigger: openModal` so the
client opens the dialog. On successful POST the server returns the
list fragment + `HX-Trigger: closeModal`; HTMX swaps `#crud-list` *and*
the modal closes. On validation error the server sets
`HX-Retarget: #modal-content` so the re-rendered form lands back in
the modal with the error visible.

For inline use (no modal), `FormView.HXTarget` carries any element ID
the caller chooses. `form_mem`'s example sets it to `#main-content`
and swaps the dump for the form (and back to dump on save) — same
`FormView` partial, different placement.

### 6.11 Pagination

`CRUDTable.PageSize` (default 20) controls rows per page. `?page=N`
selects the page (1-indexed). The renderer emits a DaisyUI `join`
button group with prev/next + clickable numbers when there's more
than one page. All page links are HTMX with `hx-target="#crud-list"`
plus `hx-push-url` so the browser URL updates and bookmarking /
back-button work. Search and sort changes reset to page 1
automatically (search input is `hx-include="this"` only; sort URLs
strip the page param).

The library assumes HTMX is loaded by the caller's page shell.
Examples load `https://unpkg.com/htmx.org@2` from CDN. They also
include a small `DOMContentLoaded` script that bridges the
`openModal` / `closeModal` HX-Trigger events to the dialog's
`showModal()` / `close()`.

### 6.12 Testing

**Primary lever: HTTP end-to-end tests** using `net/http/httptest`. The
package's exported API is the routes registered by `CRUDTable.Route`,
and most behavior worth verifying is observable through them: did the
list page render the right rows, did search filter, did sort change
order, did a POST persist, does a checked checkbox round-trip, does
the `/rows` partial omit `<table>` chrome, does HX-Request flip the
delete response from 303-redirect to fragment-return.

`crud/table_test.go` ships ~14 such tests against an in-memory `item`
model and a `*http.ServeMux`. They're cheap (sub-millisecond), don't
need an external process, and exercise rendering + handlers in one
pass.

**Unit tests** cover the reflection-heavy primitives that aren't
naturally observable through HTTP: `DeriveMetaModel` returning the
right `FormInputType` per Go kind, `DefaultBindForm` round-tripping
each scalar type, the unchecked-checkbox bind quirk. Targeted, small.

What we **don't** unit-test exhaustively: templ rendering output
byte-for-byte (brittle and templ-generate is already a source of
correctness), default rendering of every Go scalar (covered by the
e2e tests through observable HTML).

Run with `go test ./...` — no external dependencies, no DB, no
browser. Future GORM-backed tests will use an in-memory SQLite
(`glebarez/sqlite`'s `:memory:` DSN) so they stay self-contained.






### 7.6 `jsonapi.JSONAPI`
**New, Go-specific.** Wraps the same `MetaModel` + `Backend` and exposes:

- `GET    {base}` → list (with `?search`, `?sort_by`, `?offset`, `?limit`)
- `GET    {base}/{id}` → one entity with top-level relations preloaded
- `POST   {base}` → create (JSON body matches the create-derived payload)
- `PUT    {base}/{id}` → update
- `DELETE {base}/{id}` → delete
- `GET    {base}/openapi.json` → spec generated from the `MetaModel` via
  the patterns already prototyped in `openapi/openapi.go`.

The same authz / API-key / CSRF-bypass rules apply.

## 8. Auth surface

```go
type Auth struct {
    // Loaded user resolver. Returns *User or nil for anonymous.
    UserLoader  func(ctx context.Context, r *http.Request) (*User, error)

    // CSRF helpers (see §5.2).
    CSRFField   func(ctx context.Context) templ.Component
    CSRFHeaders func(ctx context.Context) templ.Component

    // Required-user / optional-user middleware.
    RequireUser http.Handler          // 401 → redirect to login (or HX-Redirect)
    OptionalUser http.Handler         // injects *User|nil into context
}
```

Pluggable login mechanisms:

- `login.Password` — username/password against the `User` table (argon2id).
- `login.TOTP`     — second-factor extension over `login.Password`.
- `login.OIDC`     — federated login; creates / links `User` rows on
                      first sign-in via `sub` claim.

All three call back into the same `Auth.Login(ctx, *User)` to write the
session cookie. Session storage is via `scs` (or open-question
alternative); the session opaquely holds `{user_id, login_at, csrf}`.

## 9. JSON API + HTMX coexistence

A single mounted `Crud` can additionally surface a `JSONAPI` for the same
metadata. The JSONAPI honors:

- **Authentication**: session cookie, API key, or anonymous (depending
  on the route's `read_authz`).
- **Authorization**: identical `read_authz` / `write_authz` callbacks. API
  keys resolve to their owning user before authz runs.
- **CSRF**: skipped for header-authenticated requests, enforced for
  session-cookie requests (defense against XSRF from a browser session
  abusing the JSON endpoint).
- **Content negotiation**: optional. By default JSON endpoints live at a
  separate path (`/heroes` HTML, `/api/heroes` JSON) rather than
  content-negotiating one URL.

## 10. Authorization model

A reusable RBAC + per-resource ACL model. Components import what they need
from `gone/authz`. The DNS-zone scenario in the user request is the design
target:

```
                            Roles
                              │
        ┌─────────────────────┼─────────────────────┐
        │                     │                     │
    superadmin            zone-admin              user
    │                     │                       │
    create_zones          per Zone:               none implicit
    create_users          - manage all Records
    everything            - grant rights to users
                          - delegate sub-perms

                            Grants
                              │
        ┌─────────────────────┼─────────────────────┐
        │                     │                     │
   Grant(user, role)     Grant(user, zone, perm)  Grant(user, record, perm)
   (global role)         (per-zone scope)         (per-record scope)
```

### 10.1 Schema sketch

```go
type User struct {
    ID            uint
    Username      string
    Email         string
    PasswordHash  string
    TOTPSecret    string             // optional, encrypted at rest
    OIDCSubject   string             // optional, for federated logins
    Disabled      bool
}

type Group struct {
    ID    uint
    Name  string
    Users []User `gorm:"many2many:user_groups"`
}

type Role struct {
    ID          uint
    Name        string                // "superadmin", "zone-admin", "user"
    Permissions []Permission `gorm:"many2many:role_permissions"`
}

type Permission struct {
    ID     uint
    Code   string                     // "zone.create", "record.update", …
}

// Grant binds a principal (user OR group) to a Role, optionally scoped
// to a resource (zone, record, etc.). ResourceType is the Go type name;
// ResourceID is the stringified primary key.
type Grant struct {
    ID            uint
    UserID        *uint
    GroupID       *uint
    RoleID        uint
    ResourceType  string             // "" for global, else e.g. "Zone"
    ResourceID    string             // "" for global, else "42"
    ExpiresAt     *time.Time
}

type APIKey struct {
    ID         uint
    UserID     uint                  // inherits this user's grants
    HashedKey  string                // bcrypt of the raw key
    Name       string                // user-supplied label
    LastUsedAt *time.Time
    ExpiresAt  *time.Time
    Disabled   bool
}
```
