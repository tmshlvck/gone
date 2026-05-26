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
    // Receives only the field's parsed value — no MetaField, no struct.
    FieldValidate Validator  // == func(value any) error

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
    DivID       string           // "model_<lcname>_<rand>"
    DisplayName string           // table + form label; default: type name

    // URLs + HTMX swap target. RouteForm / RouteDisplay register
    // handlers at these URLs; Render*Component embeds them into the
    // rendered fragment (form action, hx-post, hx-target). All three
    // may be empty for a MetaModel embedded into a CRUDTable — the
    // table has its own per-row URLs.
    FormURL    string  // POST target; GET also serves the form fragment
    DisplayURL string  // GET serves the display fragment
    HXTarget   string  // HTMX swap container id (e.g. "#main-content")

    // Authz gates every handler RouteForm / RouteDisplay register.
    // nil = AllowAll. See §6.5.
    Authz AuthzInterface

    // Model-level walks. Default implementations iterate Fields and call
    // each field's hook with the field's reflected value.
    DisplayValues   func(mm MetaModel[T], instance T) []templ.Component
    GenFormElements func(mm MetaModel[T], instance T) []templ.Component

    // BindForm parses a posted form into out, mutating it in place,
    // then runs FieldValidate per field and finally Validate (cross-field).
    // Returns ValidationErrors (which implements error) on failure.
    BindForm func(mm MetaModel[T], form map[string][]string, out *T) error

    // Validate is the user-defined cross-field validator. Receives
    // only the populated instance — no MetaModel, no extra context.
    // nil = no model-level rule. Runs after every per-field validator
    // passes; a non-nil error is stored under ValidationErrors[""]
    // and rejects the form. See §6.7.
    Validate func(instance T) error
}

// DeriveMetaModel builds a MetaModel[T] by reflecting T and installing
// default hooks. Callers post-mutate to override individual fields
// (DisplayName, FormInputType, ReadOnly, FieldValidate, FormHelp) or the
// model-level hooks (Validate, URLs, HXTarget, Authz).
func DeriveMetaModel[T any]() (MetaModel[T], error)

// FindField returns a pointer to the named MetaField (so the caller can
// mutate FormHelp / FieldValidate / RelatedCRUD / … in place) or an
// error if no field matches. Examples pair it with a one-line `must`
// helper for compact per-field setup:
//
//   f := must(mm.FindField("Name"))
//   f.FormHelp = "Display name, 2–30 characters."
//   f.FieldValidate = crud.All(crud.NotEmpty, crud.MinLen(2), crud.MaxLen(30))
//
// A typo or rename surfaces as a clean log.Fatal at startup rather than
// a silently-skipped switch case at form-render time.
func (mm *MetaModel[T]) FindField(name string) (*MetaField, error)

// DefaultDisplayValues / DefaultGenFormElements / DefaultBindForm are
// the installed walks. Available as named functions so callers can call
// them from custom hooks (e.g. validate, then call DefaultBindForm).
func DefaultDisplayValues[T any](mm MetaModel[T], instance T) []templ.Component
func DefaultGenFormElements[T any](mm MetaModel[T], instance T) []templ.Component
func DefaultBindForm[T any](mm MetaModel[T], form map[string][]string, out *T) error
```

**Component renderers** — embed the model in the app's own page templ.
Both renderers return **barebone** fragments: just the data table /
form fields, no card wrap, no Edit/Cancel/Back chrome, no page title
in the dump. The caller's pageShell (or CRUDTable's modal-box) supplies
chrome. URLs and the HTMX swap target come from the MetaModel struct,
not per-call ViewData — that way the declarative model carries enough
to render itself without per-render boilerplate.

```go
// RenderDisplayComponent returns the barebone display fragment for an
// instance — just the field/value table. r is reserved for future
// authz-driven rendering decisions (locale, hide-on-deny, …).
func (mm *MetaModel[T]) RenderDisplayComponent(r *http.Request, instance T) templ.Component

// RenderFormComponent returns the barebone form fragment. The form's
// title is "Edit <DisplayName>" (intrinsic to the action); action URL
// and hx-target come from mm.FormURL / mm.HXTarget. fieldErrors /
// modelErr drive the validation feedback (nil/"" = fresh form).
func (mm *MetaModel[T]) RenderFormComponent(
    r *http.Request,
    instance T,
    fieldErrors map[string]string,
    modelErr string,
) templ.Component
```

**Partial-endpoint mounters** — register fragment-only HTTP handlers at
`mm.DisplayURL` / `mm.FormURL`. No PageShell, no full-page wrapping;
the app owns the page route(s) that embed the components above. Every
handler runs an authz check (`mm.Authz` — nil = AllowAll) before
touching data: GET ~ CanRead; POST ~ CanUpdate when getter ≠ nil, or
CanCreate when getter == nil ("create new" flow).

```go
// Mounts GET mm.DisplayURL → barebone dump fragment. No Edit button —
// the caller's chrome (or a downstream wrapper templ) supplies it if
// needed.
func (mm *MetaModel[T]) RouteDisplay(
    mux *http.ServeMux,
    getter func() (T, error),
) error

// Mounts GET mm.FormURL → form fragment, POST mm.FormURL → bind +
// validate + setter; on success returns the dump fragment (so the
// swap container flips back to the dump); on validation failure
// returns the form with per-field errors and the model-level alert.
func (mm *MetaModel[T]) RouteForm(
    mux *http.ServeMux,
    getter func() (T, error),
    setter func(data T) error,
) error
```

**End-to-end pattern**, top to bottom:

1. *Derive the metadata.* `mm := DeriveMetaModel[T]()`; set per-field
   `FormHelp` and `FieldValidate`; set model-level `mm.Validate` for
   cross-field rules; set `mm.FormURL`, `mm.HXTarget`, `mm.Authz`.
2. *Bind data to routes.* `mm.RouteForm(mux, getter, setter)`
   registers the partial endpoints at `mm.FormURL` (GET + POST).
3. *Include the component renderer.* The app's own handler for
   `GET /` renders its page shell — which supplies the card, the
   page title, and the Edit button hx-getting to `mm.FormURL` — and
   embeds `mm.RenderDisplayComponent(r, instance)` inside it.

The library never emits `<html>`/`<body>`/`<style>` chrome. Page
composition is the application's job — see `examples/form_mem/main.go`
for the worked example, including a cross-field `Validate` that
requires `MaxRequests` to exceed `Port`.

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
    PageSize      int             // rows per page; 0 = library default (20)
    Authz         AuthzInterface  // nil = AllowAll

    // Per-instance DOM IDs (set by Derive*) so multiple CRUDTables can
    // share one page without collisions.
    ListID         string // "table_<rand>"; HTMX swap target
    ModalID        string // "modal_<rand>"; <dialog> for create/edit
    ModalContentID string // "modal-content_<rand>"; modal-box's inner div

    // Data accessors. Derive* populates these with backend-specific closures.
    Get    func(ctx context.Context, id uint) (T, error)
    List   func(ctx context.Context, search, sortBy string, sortDesc bool, offset, limit int) ([]CRUDSearchResult[T], int64, error)
    Create func(ctx context.Context, data T) (uint, T, error)  // returns assigned ID
    Update func(ctx context.Context, id uint, data T) (T, error)
    Delete func(ctx context.Context, id uint) error
}

// Backend-specific constructors. Each installs closures appropriate to
// its storage; the rest of the library treats them uniformly.
// Single-instance editing (no table at all) is covered by MetaModel's
// RouteForm / RenderDisplayComponent — see §6.2 — so there is no
// "DeriveStructCRUDTable": a struct isn't a table.
func DeriveMapCRUDTable[T any](store map[uint]T, mu *sync.RWMutex, mm MetaModel[T]) CRUDTable[T]
func DeriveGormCRUDTable[T any](db *gorm.DB, mm MetaModel[T]) CRUDTable[T]

// RenderComponent returns the TableView fragment populated from r's
// query parameters (?q, ?sort, ?desc, ?page). The application calls
// this from its own GET {URLBase} handler and embeds the result
// inside its own page shell — see §6.2's end-to-end pattern.
func (c *CRUDTable[T]) RenderComponent(r *http.Request) (templ.Component, error)

// Route registers ONLY the partial endpoints. The main list URL is
// the app's responsibility (it embeds RenderComponent in its page).
// Every handler gates on c.Authz (CanList / CanRead / CanCreate /
// CanUpdate / CanDelete; nil = AllowAll — see §6.5).
// Routes:
//   GET    {base}/rows           table fragment for HTMX swaps into #ListID
//   GET    {base}/create         create form (target: ModalContentID)
//   POST   {base}/create         submit create
//   GET    {base}/{id}/edit      edit form   (target: ModalContentID)
//   POST   {base}/{id}/edit      submit update
//   POST   {base}/{id}/delete    delete      (HX-Request → rows; else 303)
//   GET    {base}/{id}/display   per-row BAREBONE dump fragment — just
//                                the field/value table, no card, no
//                                Edit button. Foundation for future
//                                extended detail views (related
//                                entities, history, …); the caller
//                                wraps it with whatever chrome they
//                                want.
func (c *CRUDTable[T]) Route(mux *http.ServeMux) error

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
`*ViewData` structs. The single-instance views are deliberately
**barebone** — they render just the data, with no surrounding card,
Edit / Cancel buttons, or page title in the dump. The caller's
pageShell or the CRUDTable modal-box supplies chrome.

- `DisplayView(DisplayViewData)` — barebone key/value table for one
  instance. Used by `form_mem` (wrapped in the app's own card) and by
  CRUDTable's `/{id}/display` partial.
- `TableView(TableViewData)` — multi-row list with the table's
  intrinsic chrome (search input, sortable headers, pagination,
  per-row edit/delete, +Create button). The card here IS the table's
  chrome — not the same kind of "wrapping" as for single instances.
- `FormView(FormViewData)` — barebone create or edit form. Optional
  intrinsic title (`d.DisplayName`, the "Edit X" / "Create X" label
  tied to the action). Caller's modal-box / card surrounds it.

The components emit page **fragments** — no `<html>/<body>/<style>`.
The library has no `PageShell` parameter anywhere. The application
provides its own page chrome and embeds the library's
`CRUDTable.RenderComponent` / `MetaModel.RenderDisplayComponent` /
`MetaModel.RenderFormComponent` inside it. (Future `Admin` follows
the same pattern with its own `RenderComponent` method.)

**On the CRUDTable form path.** `CRUDTable` does not currently route
its modal forms through `mm.RenderFormComponent`: the per-row URLs
(`{base}/create`, `{base}/{id}/edit`) don't match `mm.FormURL`, so
the modal handlers build `FormViewData` directly with their own URLs.
This is a deliberate trade-off — keeping `MetaModel.FormURL` as a
single static URL means it's predictable for `RouteForm` callers, but
costs us code reuse inside the table. A future iteration may unify
the two paths (`CRUDTable` calls `RenderFormComponent` with per-row
URL overrides, or `MetaModel` learns a per-render URL extractor); for
now both paths render through the same shared `FormView` templ.

**Backends:**

- **`DeriveMapCRUDTable[T]`** — `map[uint]T` with auto-assigned IDs.
  Reflection-based search (case-insensitive substring across
  `Searchable` fields) and sort (any comparable kind + `time.Time`).
  The mutex protects the whole map. Keeps a struct's `ID` field in
  sync with the map key if present.
- **`DeriveGormCRUDTable[T]`** — reads/writes via `*gorm.DB`.
  Preloads `clause.Associations` on both detail and list reads (the
  table view needs the related short-names to render). Search compiles
  to `db.Where("col LIKE ? OR col2 LIKE ?", needle, needle)` against
  `Searchable` field columns resolved through GORM's NamingStrategy at
  Derive time. Updates wrap `Save` in a transaction with one
  `Association(<m2m>).Replace(slice)` per RelationMany2Many field, so
  the picker selections persist atomically.
- ~~**`DeriveStructCRUDTable[T]`**~~ — removed. Earlier drafts
  speculated a "table" backed by a single `*T` instance, but a struct
  isn't a table. The single-instance use case is fully covered by
  `MetaModel.RouteForm` + `RenderDisplayComponent` directly —
  `examples/form_mem` is the worked example, no CRUDTable involved.

Per-operation hooks (Create / Update / Delete) fire **after** the
operation succeeds and run synchronously so errors surface to the
HTTP response.

**Implementation status (2026-05-26):** `CRUDTable[T]`, `Route`
(partials only, no PageShell), `RenderComponent`, GET /{id}/display
(barebone fragment), `DisplayView` / `TableView` / `FormView` (the
two single-instance views are barebone), `DeriveMapCRUDTable[T]`,
`DeriveGormCRUDTable[T]`, `AuthzInterface` wired into every route,
and the relation pipeline (`CRUDTableInterface`, `DefaultShortValue`,
`MetaField.RelatedCRUD`, `RelationKind` detection in `DeriveMetaModel`,
auto-hide of sibling FK fields, `<select>` + multi-`<select>` form
elements, "+ new" relation-create button, cross-page modal handling
in `handleCreatePost`) all ship. Exercised by `examples/crud_mem`,
`examples/form_mem`, and `examples/crud_gorm` (the latter with
Hero/Weapon/Skill — 1:N + N:M relations, ~50/60/12 seeded rows for
pagination).

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
in `gone/crud/authz.go` for tests and for the single-struct backend's
typical use cases.

`MetaModel.Authz` gates `RouteDisplay` (CanRead) and `RouteForm`
(CanRead on GET; CanUpdate on POST, or CanCreate when the getter is
nil — the "create new" flow). `CRUDTable.Authz` gates every partial
endpoint by the obvious mapping (list/read/create/update/delete).
nil = AllowAll on both.

### 6.6 Auto-derivation

`DeriveMetaModel[T]()` and `DeriveMapCRUDTable[T]()` ship today in
`gone/crud/`. `DeriveGormCRUDTable[T]` is the next concrete backend;
it reuses the same `MetaModel[T]` shape so the rendering layer is
unchanged.

### 6.7 Validation

**Per field, in this order (`DefaultBindForm`):**

1. **Bind.** `MetaField.FromStrings` parses the wire value into the
   field's Go type via reflection. Failure (`"abc"` into an `int`,
   `"notatime"` into `time.Time`) records the error under the field's
   name and **skips step 2 for that field** — there is no Go value to
   validate.
2. **Per-field hook.** `MetaField.FieldValidate(value)` runs only if
   bind succeeded. The validator receives **only its own value** — no
   `MetaField`, no surrounding struct, nothing else. If a custom
   validator needs context (e.g. the field's `DisplayName` for an
   error message), it closes over that context at the point it's
   assigned. Use the helpers in `crud/validators.go` (`NotEmpty`,
   `MinLen`, `MaxLen`, `IntRange`, `FloatRange`, `Email`, `Pattern`)
   or compose with `crud.All(...)`.

**Across the whole submission, after all fields pass:**

3. **Model-level hook.** `MetaModel.Validate(instance)` is the
   user-defined cross-field validator. Receives only the populated
   instance. nil = no model-level validation. Runs only when every
   per-field validator passed (cross-field rules typically assume the
   inputs are individually valid). On failure, the error is stored
   under `ValidationErrors[ModelLevelKey]` (the empty string `""`).

All errors from a single submission accumulate into one
`ValidationErrors` map. The user sees every per-field problem at once,
plus any model-level message rendered as an alert above the form.

```go
// crud/validators.go
type ValidationErrors map[string]string  // field name → message; "" = model-level

const ModelLevelKey = ""

func (e ValidationErrors) Error() string  // "field: msg; field: msg; …"

// Validator signature — value only, no metadata.
type Validator = func(value any) error

// Built-in validators
func NotEmpty(value any) error
func MinLen(n int) Validator
func MaxLen(n int) Validator
func IntRange(min, max int64) Validator
func FloatRange(min, max float64) Validator
func Email(value any) error
func Pattern(re *regexp.Regexp, reason string) Validator
func All(vs ...Validator) Validator  // first failure wins
```

`DefaultBindForm` returns `ValidationErrors` (which implements `error`)
on any failure, or nil on success. Handlers extract it with
`errors.As(err, &verrs)`, split into per-field (`fieldErrors`) and
model-level (`modelErr`), and populate `FormViewData.FieldErrors` /
`ErrMsg`. The form view renders each per-field error in red directly
under its input (or the field's `FormHelp` if there is no error), and
the model-level message as a DaisyUI alert above the form.

The HTMX modal flow stays open on validation failure (via
`HX-Retarget: #<modal-content-id>`) so the user can fix and resubmit.
Submitted values are preserved in the re-rendered form via
`GenFormElements` on the partially-populated instance. Note that
validation re-renders return **HTTP 200** with the invalid-state HTML
in the body — HTMX only swaps response bodies on 2xx by default, so a
4xx would hide the form's error feedback.

See `examples/crud_mem/main.go` for per-field validators (Name
min/max, Power 0–100), and `examples/form_mem/main.go` for a
cross-field `MetaModel.Validate` rule (MaxRequests must exceed Port).
`crud/validators_test.go` covers every built-in validator plus the
bind → per-field → model-level pipeline (incl. the skip rules).

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

The example shells (`examples/*/page.templ`) load **DaisyUI v5** and
the **Tailwind v4 browser CDN**:

```html
<link href="https://cdn.jsdelivr.net/npm/daisyui@5" rel="stylesheet" type="text/css"/>
<script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
```

The base `daisyui@5` CSS already includes `light` and `dark` themes
(per DaisyUI's docs); load `daisyui@5/themes.css` additionally only if
you need the 30+ named themes (cupcake, retro, synthwave, …).

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

**Create / edit forms open in stacked DaisyUI modal dialogs.** The
library exposes two well-known dialogs through
`crud.PageModals()`, which the application embeds **once** in its page
shell:

- **L1** (`crud-modal-l1` / `crud-modal-l1-body`) — the table's own
  create/edit forms. "+ Create" and per-row "edit" buttons `hx-get`
  the form into `#crud-modal-l1-body`.
- **L2** (`crud-modal-l2` / `crud-modal-l2-body`) — a stacked second
  dialog for `+ create new` buttons inside relation pickers, so a
  nested create can run without losing the L1 form's state.

Each dialog has an X close button in the corner; backdrop click also
closes it (HTML5 `<form method="dialog">`). The handlers detect L1
vs L2 from the request's `HX-Target` header and respond accordingly:
on validation error they `HX-Retarget` back to the same modal body
(so the re-rendered form lands in the dialog); on L1 success they
`HX-Retarget` to the table's `#table_<rand>` and return the
refreshed `TableContent` fragment + `closeModal: l1`; on L2 success
they `HX-Reswap: none` + `closeModal: l2`, so L1 keeps its state
and the L1 page (or any other page) doesn't have to host the related
table's list area.

**Relation widget refresh after L2 save.** Every relation `<select>`
the library renders carries `hx-trigger="refresh-relation from:body"
hx-get="{relatedBase}/options" hx-vals='js:{"selected": …current
selection…}' hx-target="this" hx-swap="innerHTML"`. The L2 success
response adds `"refresh-relation"` alongside `closeModal: l2` in its
`HX-Trigger` JSON. The browser dispatches that event on the body,
every relation `<select>` re-fetches its own `<option>` list, and the
freshly-created row appears in every relevant dropdown on the page.
Only the option list swaps — the L1 form's other fields, the wrapper
div, the "+" button, and the `<select>`'s own attributes (`name`,
`multiple`, `size`) all survive. The endpoint is
`GET {base}/options[?single=1][&selected=…]`, registered by
`CRUDTable.Route`; belongs-to widgets pass `?single=1` so the
response keeps the `— none —` placeholder.

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
