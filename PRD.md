# gone — Go Object Navigation Elements — PRD

## 1. Purpose

`gone` is a Go library that renders **HTMX-driven CRUD UIs** from a
model's metadata. Describe a model once and get three renderings of the
same entity — a table, a form, and a dump — plus an Admin component
that aggregates many tables under one URL prefix with a sidebar
navigation.

The library emits HTML **fragments**: no `<html>`/`<body>`/`<style>`
chrome. The caller composes the page shell and embeds the library's
`Render(r)` methods inside it.

Items beyond CRUD rendering — auth, CSRF, JSON API, RBAC — are sketched
in [`TODO.md`](TODO.md) and not built yet.

## 2. Goals

1. **Describe a model once.** A `MetaModel[T]` drives table, form, and
   dump renderings of the same entity.
2. **Auto-derive metadata from a Go struct.** Reflection + `gorm` tags
   pick field types, sortability, searchability, and relation shape
   (belongs-to, has-many, many-to-many). The caller post-mutates the
   derived model to override any default.
3. **Backend-agnostic data plane.** `CRUDTable` holds closures
   (`Get`/`List`/`Create`/`Update`/`Delete`) populated by a backend-
   specific `Derive*CRUDTable` constructor. GORM and an in-memory map
   are first-class; new backends drop in by writing a constructor.
4. **Pluggable validation.** Per-field and model-level (cross-field)
   validators run during form binding; errors come back as a structured
   map the form template renders inline.
5. **Safe HTML by default.** Every interpolated value escapes; `templ.Raw`
   is the escape hatch.
6. **Library, not framework.** Components return `templ.Component` and
   register on whatever HTTP router the caller already uses, as long as
   it satisfies the small `crud.Mux` interface.

## 3. Non-goals

- **Authentication** (sessions, password hashing, OIDC, TOTP) — see TODO.
- **CSRF** — see TODO.
- **RBAC / per-resource ACL** — see TODO.
- **JSON API endpoints** — see TODO.
- **Field-level audit logging** — see TODO.
- **GraphQL / Background jobs** — out of scope.

## 4. Stack

| Concern              | Choice                                                     |
|----------------------|------------------------------------------------------------|
| Language             | Go 1.24+ (generics + 1.22 `ServeMux` patterns)             |
| ORM                  | GORM v2 with pure-Go `glebarez/sqlite` driver in examples  |
| Templating           | **templ** (`a-h/templ`) — type-safe, compiled              |
| Router               | stdlib `net/http` semantics; `chi` compatible              |
| Styling              | DaisyUI v5 + Tailwind v4 (caller loads, library emits classes) |
| Structured logging   | `log/slog` (stdlib)                                        |

## 5. Core abstractions

### 5.1 `Mux`

```go
type Mux interface {
    HandleFunc(pattern string, handler http.HandlerFunc)
}
```

Both `*http.ServeMux` and `chi.Router` satisfy it. The library never
takes a concrete router type. Callers wanting middleware layering with
chi use `chi.Group` (not `chi.Route`, which would prefix-mount and
double the absolute paths the library registers).

### 5.2 `MetaField`

Non-generic struct describing one field for rendering and form binding.

```go
type MetaField struct {
    Name          string
    DivID         string  // per-instance HTML id
    DisplayName   string  // table/form label
    FormInputType string  // "text" | "number" | "checkbox" | "datetime-local" | "email" | …
    FormHelp      string
    Hidden        bool    // omit from table + form + dump
    ReadOnly      bool    // displayed but not editable in the form
    Multiple      bool    // <select multiple>
    Sortable      bool    // column header is a sort link
    Searchable    bool    // included in case-insensitive substring search

    // Relation metadata (auto-detected from struct shape + gorm tag).
    RelationKind  RelationKind        // NotRelation / RelationSingle / RelationMany2Many / RelationHasMany
    RelatedCRUD   CRUDTableInterface  // wired by the caller after Derive
    FKFieldName   string              // RelationSingle only: e.g. "OwnerID"
    FormFieldName string              // POST key (defaults to Name; relation single → FKFieldName)

    DisplayValue   func(mf MetaField, value any) templ.Component
    GenFormElement func(mf MetaField, value any) templ.Component
    FromStrings    func(mf MetaField, strs []string, instance any) error
    FieldValidate  Validator
}
```

Hook signatures stay value-typed (not generic) so a heterogeneous
`[]MetaField` works. The defaults installed by `DeriveMetaModel` are
exported (`DefaultDisplayValue`, `DefaultGenFormElement`,
`DefaultFromStrings`, `DefaultShortValue`) so callers can compose on
top of them.

### 5.3 `MetaModel[T]`

Pure metadata + render + bind helpers. **No routing state, no data
accessors, no authz** — those live on `CRUDTable`. A bare MetaModel
is enough to render and bind forms in caller-written handlers.

```go
type MetaModel[T any] struct {
    Fields      []MetaField
    Name        string  // Go type name
    Slug        string  // url-safe singular; default lowercase Name
    DisplayName string
    DivID       string

    DisplayValues   func(mm MetaModel[T], instance T) []templ.Component
    GenFormElements func(mm MetaModel[T], instance T) []templ.Component
    BindForm        func(mm MetaModel[T], form url.Values, out *T) error
    Validate        func(instance T) error  // cross-field, optional
}

func DeriveMetaModel[T any]() (MetaModel[T], error)

func (mm *MetaModel[T]) FindField(name string) (*MetaField, error)
func (mm *MetaModel[T]) MustFindField(name string) *MetaField

func (mm *MetaModel[T]) RenderDisplay(instance T) templ.Component
func (mm *MetaModel[T]) RenderForm(instance T, opts FormOpts) templ.Component

// TryBindForm parses the request form and binds it onto out — wraps
// ParseForm + the BindForm closure into one call.
func (mm *MetaModel[T]) TryBindForm(r *http.Request, out *T) error
```

`FormOpts` collapses the render parameters into one struct:

```go
type FormOpts struct {
    ActionURL   string             // form action and hx-post
    HXTarget    string             // hx-target (e.g. "#hero-modal-l1-body")
    SubmitLabel string             // "Save", "Create"
    Title       string             // optional <h3> above the form
    Errors      ValidationErrors   // empty / nil = fresh form
    SuccessMsg  string             // optional green banner above the form
}
```

`Errors` is the raw `ValidationErrors` map. The form template iterates
it: `ModelLevelKey` ("") becomes the alert banner above the form;
other keys render under their matching field.

### 5.4 `CRUDTable[T]`

Wraps a `MetaModel[T]` with data closures, authz, and HTMX routing
state. Backend-specific constructors install the closures.

```go
type CRUDTable[T any] struct {
    MetaData      MetaModel[T]
    Authz         AuthzInterface
    Slug          string  // url-safe plural; default ≈ lowercase(Name) + "s"
    PageSize      int
    CreateEnabled bool
    EditEnabled   bool
    DeleteEnabled bool

    // Data closures installed by Derive*CRUDTable.
    Get    func(ctx context.Context, id uint) (T, error)
    List   func(ctx context.Context, search, sortBy string, sortDesc bool, offset, limit int) ([]CRUDSearchResult[T], int64, error)
    Create func(ctx context.Context, data T) (uint, T, error)
    Update func(ctx context.Context, id uint, data T) (T, error)
    Delete func(ctx context.Context, id uint) error

    // urlBase is private; absolute path computed by Route.
    // Read via HTMXTableURL / HTMXCreateURL.
}

// Backend constructors.
func DeriveMapCRUDTable[T any](mm MetaModel[T], authz AuthzInterface, store map[uint]T, mu *sync.RWMutex) CRUDTable[T]
func DeriveGormCRUDTable[T any](mm MetaModel[T], authz AuthzInterface, db *gorm.DB) CRUDTable[T]
```

**Slug uniqueness is required.** Two CRUDTables on the same page (or
inside the same Admin) MUST have distinct `Slug` values. The default
`lowercase(Name) + "s"` heuristic already gives different slugs to
different Go types; collisions only happen if a caller deliberately
overrides two tables to share a slug. Symptoms of a collision: the
per-slug L1 modal IDs (`{slug}-modal-l1`) overlap, so opening one
table's edit form may target the other's modal body. Treat this as a
configuration bug, not a runtime case the library handles.

#### Routing

```go
// Route registers all CRUD endpoints under prefix + "/" + Slug.
// urlBase becomes that absolute path; all HTMX URLs in rendered
// fragments derive from it.
func (c *CRUDTable[T]) Route(mux Mux, prefix string) error
```

For `Slug = "heroes"`, `prefix = "/admin"`, the registered endpoints are:

| Method | Path                              | Returns                          |
|--------|-----------------------------------|----------------------------------|
| GET    | `/admin/heroes/view`              | table fragment (refresh swap)    |
| GET    | `/admin/heroes/create`            | create form fragment             |
| POST   | `/admin/heroes/create`            | create submit                    |
| GET    | `/admin/heroes/{id}/edit`         | edit form fragment               |
| POST   | `/admin/heroes/{id}/edit`         | edit submit                      |
| POST   | `/admin/heroes/{id}/delete`       | delete                           |
| GET    | `/admin/heroes/{id}/display`      | per-row dump fragment            |
| GET    | `/admin/heroes/options`           | relation-picker `<option>` list  |

Every handler gates on `c.Authz` (CanList / CanRead / CanCreate /
CanUpdate / CanDelete; nil → AllowAll).

#### Render

```go
func (c *CRUDTable[T]) Render(r *http.Request) (templ.Component, error)
```

Returns a fragment containing:

- A `<div id="{slug}-table-wrapper">` with the table, search, sort,
  pagination, and per-row action buttons.
- Two `<dialog>` elements scoped to this table:
  - `#{slug}-modal-l1` — this table's own create/edit modal.
  - `#{slug}-modal-l2` — nested create from this table's relation
    widgets (modal hosts a foreign entity's create form).

Per-table modals make `CRUDTable` self-contained: the caller embeds
`crudtab.Render(r)` inside its page shell and gets working modals
without a separate `PageModals` helper.

#### URL accessors

```go
func (c *CRUDTable[T]) HTMXTableURL() string   // e.g. "/admin/heroes/view"
func (c *CRUDTable[T]) HTMXCreateURL() string  // e.g. "/admin/heroes/create"
```

`HTMXTableURL` is what an `Admin` sidebar link hx-gets to swap this
table into the working pane. `HTMXCreateURL` is what a relation widget's
"+ create new" button hx-gets to open the create form in L2.

### 5.5 `CRUDTableInterface`

Non-generic surface for cross-model relation pickers and `Admin`:

```go
type CRUDTableInterface interface {
    DisplayName() string
    Slug() string
    Route(mux Mux, prefix string) error
    Render(r *http.Request) (templ.Component, error)
    HTMXTableURL() string
    HTMXCreateURL() string
    SearchOptions(ctx context.Context, search string) ([]CRUDRelationOption, int64, error)
    GetOptionsByID(ctx context.Context, ids []uint) ([]CRUDRelationOption, error)
}

type CRUDRelationOption struct {
    ID        uint
    Instance  any
    ShortName string
}
```

### 5.6 `Admin`

Aggregates multiple CRUDTables under one URL prefix with a sidebar.

```go
type Admin struct {
    Tables []CRUDTableInterface
    Authz  AuthzInterface
    Slug   string  // default "admin"
}

func DeriveAdmin(tables []CRUDTableInterface, authz AuthzInterface) Admin

func (a *Admin) Route(mux Mux, prefix string) error
func (a *Admin) Render(r *http.Request) (templ.Component, error)
```

`Admin.Route(mux, "/admin")` registers:

| Method | Path                | Returns                                            |
|--------|---------------------|----------------------------------------------------|
| GET    | `/admin`            | index fragment (sidebar + empty working area)      |
| GET    | `/admin/{slug}`     | the matching table's `Render(r)` fragment          |

`Admin.Route` does **not** route the child tables — the caller does
that explicitly. This keeps data accessors out of Admin's signature and
lets the caller mount Admin and its tables under arbitrary prefixes.

The sidebar emits HTMX-driven links: clicking "Heroes" hx-gets
`/admin/heroes` (returning Hero table's `Render(r)`) and swaps it into
the working pane. No page reloads.

For the standalone case (`/admin` reached directly in the browser), the
caller writes a tiny page-shell route that wraps `admin.Render(r)` —
the library never emits page chrome.

### 5.7 `AuthzInterface`

```go
type AuthzInterface interface {
    CanList(r *http.Request) bool
    CanRead(r *http.Request) bool
    CanCreate(r *http.Request) bool
    CanUpdate(r *http.Request) bool
    CanDelete(r *http.Request) bool
}
```

Taking `*http.Request` keeps the surface router-agnostic.
`crud.AllowAll{}` is the no-op default; `nil` on a CRUDTable means
AllowAll. Every CRUDTable endpoint gates on its `Authz` before
touching data.

## 6. Validation

### Pipeline

`DefaultBindForm` walks fields in order:

1. **Bind.** `MetaField.FromStrings` parses the wire value into the
   field's Go type via reflection. Bind failure records the error under
   the field name and **skips per-field validation** for that field —
   there's no Go value to validate.
2. **Per-field validation.** `MetaField.FieldValidate(value)` runs only
   if bind succeeded. The validator receives only its own value — no
   `MetaField`, no surrounding struct. Use the helpers in
   `crud/validators.go` (`NotEmpty`, `MinLen`, `MaxLen`, `IntRange`,
   `FloatRange`, `Email`, `Pattern`) or compose with `crud.All(...)`.

After all fields are processed:

3. **Model-level validation.** `MetaModel.Validate(instance)` runs only
   if every field passed (cross-field rules typically assume the inputs
   are individually valid). On failure the message is stored under
   `ValidationErrors[ModelLevelKey]` (the empty string `""`).

```go
type ValidationErrors map[string]string  // field name → message; "" = model-level

const ModelLevelKey = ""

func (e ValidationErrors) Error() string

type Validator = func(value any) error
```

### Built-in validators

```go
func NotEmpty(value any) error
func MinLen(n int) Validator
func MaxLen(n int) Validator
func IntRange(min, max int64) Validator
func FloatRange(min, max float64) Validator
func Email(value any) error
func Pattern(re *regexp.Regexp, reason string) Validator
func All(vs ...Validator) Validator  // first failure wins
```

### Form binding (`MetaField.FromStrings`)

`DefaultFromStrings` handles `string`, signed/unsigned ints, floats,
`bool` (with the hidden-input/checkbox pairing trick), and `time.Time`
(HTML `datetime-local`). Relations have their own `FromStrings`
implementations that read the FK field (single) or build a slice of
zero-valued structs with just `ID` populated (many-to-many).

### Re-rendering on failure

`DefaultBindForm` returns `ValidationErrors` (which implements `error`)
on any failure, or `nil` on success. Handlers pass it directly into
`FormOpts.Errors` for re-render — no splitting needed. The form view
finds `ModelLevelKey` for the alert banner and renders the remaining
entries under their matching fields. Submitted values survive the
re-render via `GenFormElements` on the partially-populated instance.

Validation re-renders return **HTTP 200** with the invalid-state HTML
in the body — HTMX only swaps response bodies on 2xx by default.

## 7. Styling

`gone/crud` emits markup with **DaisyUI** + **TailwindCSS** classes and
**assumes both are loaded by the caller's page shell**. The library
bundles no CSS and serves no static assets — keeps gone a true library
and lets the caller pick the DaisyUI theme.

The example shells load **DaisyUI v5** and the **Tailwind v4 browser CDN**:

```html
<link href="https://cdn.jsdelivr.net/npm/daisyui@5" rel="stylesheet" type="text/css"/>
<script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
```

DaisyUI v5 dropped the `-bordered` modifiers; the base `input` /
`select` / `textarea` classes include borders. Library views use the
unsuffixed class names. Production apps should run their own Tailwind
build with DaisyUI as a plugin; the CDN is dev-only.

Component fragments emit **no `<html>`/`<body>`/`<style>`** — they're
fragments. Direct browser navigation goes through the caller's
page-shell; HTMX requests (`HX-Request: true`) get the raw fragment.

The example page shells include a light/dark theme toggle
(DaisyUI's `swap swap-rotate` with `data-theme-toggle` checkbox), pure
client-side: `prefers-color-scheme` on first load, persisted to
`localStorage`, applied as `data-theme` on `<html>`. Library-agnostic —
drop the snippet into any page shell.

## 8. HTMX modals

Create / edit forms open in two stacked DaisyUI modal dialogs:

- **L1 — per-table** — `#{slug}-modal-l1` / `#{slug}-modal-l1-body` —
  the table's own create/edit forms. Emitted by each CRUDTable's
  `Render` output. Opened by `+ Create` and per-row `edit` buttons.
- **L2 — shared singleton** — `#crud-modal-l2` / `#crud-modal-l2-body`
  — nested create opened by a `+ new` button inside a relation widget.
  Hosts the foreign entity's create form so the L1 form's state
  survives. The L2 dialog is auto-embedded inside each CRUDTable's
  `Render` output (via the library's internal `PageModals`); if
  multiple CRUDTables render on one page the duplicate dialogs are
  inert (browsers tolerate, `getElementById` returns the first).

**Slug uniqueness is required** for the per-table L1 IDs to stay
distinct — see §5.4.

Each dialog has an X close button in the top-right and a backdrop
click handler (HTML5 `<form method="dialog">`).

**Modal-open timing.** The flow is *fetch → response → swap → open*:
clicking `+ Create` fires the `hx-get`; the server returns the form
HTML plus `HX-Trigger: openModal:<id>`; HTMX swaps the form into the
target body; the bridge JS dispatches `dialog.showModal()`. The modal
does not appear until the response arrives, so on slow networks the
click feels MPA-like — the page sits still until the form is ready.
Acceptable default for admin and generic CRUD workflows; switching to
"open immediately with spinner, then swap" is a small client-side
change if a future use case demands it.

### Signaling outcomes

The form's HTMX attributes are static: `hx-post=<action> hx-target=#{slug}-modal-l*-body`.
The server's response headers decide what happens:

| Outcome              | Response                                                          |
|----------------------|-------------------------------------------------------------------|
| Validation error     | `HX-Retarget: #<bodyID>`, `HX-Reswap: innerHTML`, body = form     |
| L1 save success      | `HX-Retarget: #<wrapper>`, body = refreshed table fragment + `HX-Trigger: closeModal:<modalID>` |
| L2 save success      | `HX-Reswap: none`, `HX-Trigger: {closeModal:<id>, refresh-relation: true}` |

The page-shell JS bridges `closeModal` to `dialog.close()` and
`refresh-relation` to "every relation `<select>` re-fetches its
options". Both are tiny event listeners.

### Cross-page relation create

When the L2 modal's create finishes, every relation `<select>` on the
page re-fetches `{relatedBase}/options` (sending its current selection
via `hx-vals='js: …'`) so the freshly-created row appears in dropdowns
without disturbing any other in-flight form field. The "+" button on
relation widgets that render inside L2 is hidden by a one-line CSS rule
(`#crud-modal-l2 .crud-relation-add-btn { display: none }`) — no L3
modal exists.

## 9. Pagination

`CRUDTable.PageSize` (default 20) controls rows per page. `?page=N`
selects the page (1-indexed). The renderer emits a DaisyUI `join`
button group with prev/next + clickable numbers when there's more than
one page. All page links are HTMX with `hx-target="#{slug}-table-wrapper"`
plus `hx-push-url` so bookmarking and back-button work. Search and sort
changes reset to page 1.

## 10. Testing

**Primary lever: HTTP end-to-end tests** using `net/http/httptest`. The
package's exported API is the routes registered by `Route()`, and most
behavior worth verifying is observable through them: did the list
render the right rows, did search filter, did a POST persist, does a
checked checkbox round-trip, does HX-Request flip the delete response
from 303-redirect to fragment-return.

**Unit tests** cover the reflection-heavy primitives that aren't
naturally observable through HTTP: `DeriveMetaModel` returning the
right `FormInputType` per Go kind, `DefaultBindForm` round-tripping
each scalar type, the unchecked-checkbox bind quirk.

What we **don't** unit-test exhaustively: templ rendering output
byte-for-byte (brittle and templ-generate is already a source of
correctness).

Run with `go test ./...` — no external dependencies, no DB, no browser.
GORM tests use an in-memory SQLite (`glebarez/sqlite`'s shared-cache
DSN) for self-contained execution.

## 11. Examples

| Path                   | What it shows                                                            |
|------------------------|--------------------------------------------------------------------------|
| `examples/form_mem`    | Single struct, manual handlers using `MetaModel.RenderForm` / `TryBindForm` |
| `examples/crud_mem`    | One CRUDTable, in-memory map backend, MPA-style pageShell                |
| `examples/crud_gorm`   | Three CRUDTables (Hero, Weapon, Skill) with 1:N and N:M relations, GORM backend, tabbed pageShell |
| `examples/admin_gorm`  | Same three CRUDTables wired into `Admin` with HTMX sidebar swap          |

Each example is self-contained: `go run ./examples/<name>` starts a
server on `:8080`. No external dependencies; SQLite is in-memory; CDN
assets load from jsDelivr / unpkg.
