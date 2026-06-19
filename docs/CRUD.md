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
table := crud.NewTable(mm, data, site.PageSize(10), az)                      // table config (pager, authz)
table.RegisterRoutes(root, "", "/admin/heroes")                             // WHERE it lives
```

- `DeriveMetaModel` reflects `Hero`, then overlays your overrides.
- `GORMAccessor` / `MapAccessor` build the data plane from the **same** `mm`
  (they read it to learn which fields are searchable / sortable / relations).
- `NewTable` pairs the two and adds table-level config (`pager`, `authz`,
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
    // 3. Table config: default page size (20), no authz.
    table := crud.NewTable(mm, data, site.DefaultSettings{}, nil)

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
    NoExport bool // omit from CSV export (secrets); still importable

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
  {Name: "TOTPSecret", ReadOnly: true, DisplayValue: crud.Redact, NoExport: true},
  ```

- **A write-only password field** — empty box in the form, redacted in the
  table; a non-blank entry is re-hashed and stored, a blank entry keeps the
  current value:

  ```go
  {Name: "PasswordHash", DisplayName: "Password", FormInputType: "password",
      DisplayValue:   crud.Redact,
      GenFormElement: crud.PasswordInput,
      BindStrings:    crud.HashWith(auth.HashPassword), // auth.HashPassword = argon2id
      NoExport:       true},
  ```

`auth.HashPassword` hashes the same way the login path verifies. See
`examples/auth_gorm` for the User table using all of this.

> **CSV note:** `Redact` only affects *display* — CSV export reads the raw
> field, so a secret without `NoExport: true` would leak its stored value into
> an export. Set `NoExport` on every sensitive field. It still imports (a blank
> cell is left unchanged, so it can't be wiped to an empty/`hash("")` value).

### Time fields and UTC storage

A `time.Time` field is auto-detected and rendered as a
`datetime-local` input. **Storage is always UTC**; *display and entry*
happen in the session's display zone (default UTC — see
[Per-session display timezone](#per-session-display-timezone)):

- **Display** (table cell + detail row): rendered in the session zone
  via the model's `TimeFormatter`, with the zone abbreviation + offset
  (e.g. `2024-06-15 14:30:00 CEST (+02:00)`) — DST-correct per row,
  since the offset comes from each value's own instant.
- **Bind** (form → struct): the zone-less `datetime-local` value
  (`2006-01-02T15:04`) is interpreted in the session zone, then stored
  as the right UTC instant. (`MetaModel.BindForm` without a request
  stays pure UTC, for tests / non-HTTP callers.)

> **You must guarantee UTC at rest — call `site.ForceUTC(db)` once,
> right after `gorm.Open`.** This is essential for correctness, not a
> nicety.

A Go `time.Time` always carries a location, and a value that reaches
the database in a non-UTC zone is stored *with that offset*. The most
common source is ordinary app code: **`time.Now()` returns
`time.Local`, not UTC.** The hazard is not instant fidelity (the instant
round-trips fine) — it's that **SQL compares the stored value, not the
instant**:

- On **SQLite**, a `time.Time` is stored as RFC3339 *text including its
  offset* (`2024-06-15T14:30:00+02:00`). Two rows that are the same
  instant in different zones then sort and range-filter by their
  wall-clock string — so a CRUDTable sort on a time column, or a
  `WHERE … BETWEEN`, can return wrong results.
- On **Postgres**, `timestamptz` (GORM's default for `time.Time`)
  normalizes to a UTC instant and is safe; a `timestamp`
  *without* time zone column has the same hazard as SQLite.

`site.ForceUTC(db)` removes the hazard uniformly on every backend. It
wraps `db.NowFunc` so GORM's automatic `CreatedAt` / `UpdatedAt` are
generated in UTC, and registers before-create / before-update callbacks
that convert any explicitly-set `time.Time` / `*time.Time` field to UTC
before it's written:

```go
db, _ := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
if err := site.ForceUTC(db); err != nil { // before any writes
    log.Fatal(err)
}
```

With it in place, values at rest are always UTC, SQL ordering and range
filters operate on the instant, and per-session timezone display (next)
is purely a presentation concern on top. The GORM examples
(`crud_gorm`, `admin_gorm`, `auth_gorm`, `auth_sso`) all call it; the
`Forged` column on `crud_gorm`'s Weapon showcases a time field.

### Per-session display timezone

The zone a session sees is one `*time.Location` on the request context;
display, form pre-fill, and bind all read it, so a round-trip is
consistent (what you see in zone Z you edit in zone Z). The pieces live
in `gone/site`:

```go
// Stamp each request's context with the session zone. resolve reads
// wherever the app keeps the choice (cookie / session / per-user DB);
// nil / a nil result → UTC.
mux.Use(site.TimezoneMiddleware(resolve))
site.Timezone(ctx) *time.Location            // read point (default UTC)
```

- **`site.TimezonePicker`** is a drop-in navbar control (cookie-backed,
  no session needed). It offers UTC / browser-local / a `Zones` list
  (pass `site.CommonZones`), persists the choice, and exposes
  `Resolve` for the middleware:

  ```go
  tz := &site.TimezonePicker{Mode: site.TZModeFull, Zones: site.CommonZones}
  mux.Use(site.TimezoneMiddleware(tz.Resolve))
  tz.RegisterRoutes(mux)
  // render tz.Component(r) in your navbar
  ```

- **How times render** is the app-global `site.TimeFormatter` (default
  `site.DefaultTimeFormatter`), set per model via `MetaModel.TimeFormatter`
  and reused by `gone/auth` for account-page timestamps. It's an object
  the app owns — *not* on the context — so the same formatter works in
  emails / PDFs. Override by embedding `site.DefaultTimeFormatter` and
  shadowing `FormatTime`; per-field formatting still overrides via
  `DisplayValue`.

`crud_gorm` wires all of this; `examples/admin_gorm` shows the parallel
**theme** preference — a cookie-backed `site.ThemeToggle` whose value the
shell reads with `site.Theme(r, …)` server-side (no flash). Both prefs
persist via the shared `site.SetPref` / `site.Pref` cookie helpers.

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

### Observing changes — `ObserveAccessor` (audit / notify hooks)

To react to row changes — audit logging, cache invalidation, pushing updates
down a channel — wrap any `Accessor[T]` in an observer. It fires a callback
after each **successful** operation, and because every mutation path (the
create/edit/delete handlers **and** CSV import) goes through `Data`, one wrap
catches them all:

```go
type ChangeKind int
const ( ChangeCreate ChangeKind = iota; ChangeUpdate; ChangeDelete; ChangeRead; ChangeList )

type ChangeEvent[T any] struct {
    Kind  ChangeKind
    ID    uint
    Row   T    // resulting row (create/update/read); zero for delete unless re-read; zero for list
    Count int  // rows returned — ChangeList only
}

func ObserveAccessor[T any](inner Accessor[T], on func(context.Context, ChangeEvent[T])) Accessor[T]
func ObserveDeletes[T any](inner Accessor[T], on func(context.Context, ChangeEvent[T])) Accessor[T]
func ObserveReads[T any](inner Accessor[T], on func(context.Context, ChangeEvent[T])) Accessor[T]
```

- **`ObserveAccessor`** — fires on writes only (create / update / delete).
- **`ObserveDeletes`** — same, but re-reads the row before deleting so the
  delete event carries the old contents (one extra `Get` per delete).
- **`ObserveReads`** — also fires `ChangeRead` per `Get` and `ChangeList` per
  `List`, for a full audit trail. Reads are *far* higher volume than writes
  (a `ChangeList` fires on every render, search keystroke, and sort), so this
  is opt-in; keep the callback cheap and consider filtering by `Kind`.

The callback runs **synchronously inside the request**, after the write
commits, so it must not block — the canonical use is a non-blocking send to a
buffered channel drained by a goroutine:

```go
changes := make(chan crud.ChangeEvent[Hero], 64)
go func() { for e := range changes { log.Printf("%s id=%d", e.Kind, e.ID) } }()

data := crud.ObserveAccessor(
    crud.MapAccessor(mm, store, &mu),
    func(ctx context.Context, e crud.ChangeEvent[Hero]) {
        select {
        case changes <- e:
        default: // worker behind — drop rather than stall the request
        }
    },
)
table := crud.NewTable(mm, data, site.PageSize(10), gate)
```

**Identifying the user.** The callback receives the same `ctx` the handler
passed to `Data` (`r.Context()`), and the session rides along in it — so an
audit hook resolves *who* with `auth.CurrentUsername(ctx)` (no
`*http.Request`, no user lookup; see [AUTH.md](AUTH.md#auth-interface)).
Non-HTTP callers (CSV export, background jobs) carry no session, so that
returns `""` — audit those as anonymous/system. The decorator itself stays
auth-agnostic: crud never imports auth for this, the app's closure does the
lookup. `examples/observe_crud_mem` is a runnable end-to-end demo.

## Building the table — `NewTable`

```go
func NewTable[T any](mm MetaModel[T], data Accessor[T], pager site.PaginationSettings, authz auth.Authz) CRUDTable[T]
```

`pager` supplies the page size (see [Pagination](#pagination)): pass the app's
`site.DefaultSettings` for 20/page, `site.PageSize(n)` for a specific size, or
`site.PageSize(0)` for no pagination (all rows); `nil` also means the default.
`authz` gates every route (nil = allow all). Create / edit / delete are enabled
by default. The returned `CRUDTable[T]` is a plain struct with public fields,
so anything the constructor doesn't cover is set afterward:

```go
table := crud.NewTable(mm, data, site.PageSize(10), gate)
table.CreateEnabled = false           // granular mutation toggles
table.HideUnauthorized = true         // omit disallowed buttons (vs render disabled)
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
  segments, e.g. `"/heroes"` or `"/admin/heroes"`. Empty falls back to a
  derived plural of the model name (`Hero`→`"heros"`); pass an explicit path
  for irregular plurals or any custom placement.
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
| GET    | `/admin/heroes/export.csv`   | CSV download of all matching rows         |
| GET    | `/admin/heroes/import`       | CSV import form fragment                  |
| POST   | `/admin/heroes/import`       | CSV import submit (multipart)             |
| GET    | `/admin/heroes/{id}/display` | per-row dump fragment                     |
| GET    | `/admin/heroes/options`      | relation-picker option list               |

Every handler gates on the table's `Authz` (nil = allow all). The
`create`/`edit`/`delete` routes are only registered when the matching
`*Enabled` toggle is on; `import` is registered when either create or edit is.

### CSV export / import

The toolbar's **⋮** menu carries the table-wide actions:

Export and import are **not symmetric**: export carries every non-secret,
non-internal column (read-only ones included, for reference); import writes
back only the bindable ones and silently ignores the rest.

- **Export CSV** — downloads every row matching the current search/sort as
  CSV (`export.csv`, gated on the list permission; the download is named
  `<lowercase model name>_table.csv`, e.g. `hero_table.csv`). Columns are `ID` plus every
  field that is **not** `Hidden`, **not** `NoExport`, and not the `ID` field
  itself — so read-only columns (timestamps, computed values, the has-many
  inverse) *are* exported. Scalar cells use the same plain-text rendering as
  the form pre-fill.
- **Import CSV…** — opens a form (paste text *or* upload a file) that upserts
  rows: a non-blank `ID` column updates that row, a blank/absent `ID` creates
  a new one. Import binds every field that is **not** `Hidden`, **not**
  `ReadOnly`, and not the `ID` field; any other column in the file (read-only,
  has-many, or unrecognized) is ignored. Each bound column runs through its
  field's normal `BindStrings` + validation. Gated on create or update.

  Import is a **PATCH**: only columns present in the header are written, so a
  partial CSV updates just those fields and leaves the rest of the row intact
  (it won't wipe omitted columns to zero).

  Import is **fail-closed on validation**: if any row fails to parse or
  validate, the whole file is rejected and nothing is written, with the
  offending rows reported on the form. Persistence itself isn't transactional
  across rows (the `Accessor` has no transaction handle), so a backend error
  mid-write can leave earlier rows applied.

  **Relations** round-trip as IDs, by kind:
  - **single** (`RelationSingle`) — the FK id under the FK column (e.g.
    `OwnerID`); exported and imported; `0`/blank clears it;
  - **many-to-many** (`RelationMany2Many`) — a `;`-separated id list in one
    cell (e.g. `5;8;12`); exported and imported; a blank cell clears the set;
  - **has-many** (`RelationHasMany`, the read-only inverse) — exported as the
    same `;`-list for reference, but **ignored on import** (it's read-only;
    edit the owning side instead).

  Natural-key matching isn't supported — relation columns are IDs only (see
  below). Times are read and written as UTC wall clock, not
  session-zone-adjusted.

- **Confirm before delete** — a per-browser toggle (cookie
  `gone_crud_confirm_delete`). On by default; turn it off to make the per-row
  delete buttons fire on the first click, without the confirm dialog — a
  lightweight stand-in for bulk delete.

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
mounted at `componentPath + "/" + a lowercased plural of the model name`
(`Hero`→`"heros"`). It also registers an index redirect
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

Children's URL segments are a lowercased plural of the Go type name
(`lowercase(Name)+"s"`), so irregular plurals get the literal default —
`Hero`→`/admin/heros`, `UserGORM`→`/admin/usergorms`. Admin has no per-table
override; if you need clean URLs, route the tables yourself (each
`CRUDTable.RegisterRoutes(r, "", "/admin/users")`) and render the page +
sidebar in your own handler instead of using `Admin`.

**Custom sidebar links** (`SidebarTop` / `SidebarBottom`) point at an
app-owned URL. Clicking one HTMX-fetches it into the working area
(`#crud-admin-main`) and updates the address bar (`hx-push-url`), so the
app's handler should return a *fragment* on `HX-Request` and a
shell-wrapped page on a direct hit. The sidebar's active highlight
follows custom-link clicks too (a small built-in delegated script moves
`menu-active`, since — unlike model entries — a custom link only swaps
the content area, not the whole page). `examples/admin_gorm` has a
`/testlink` demo.

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

A table's page size comes from the `site.PaginationSettings` passed to
`NewTable` (stored as `CRUDTable.Pagination`):

```go
type PaginationSettings interface {
    PaginationSizeDefault() uint16 // 0 = no pagination (show every row)
}
```

- `site.DefaultSettings{}` → 20 rows per page (also the `nil` fallback).
- `site.PageSize(n)` → n per page.
- `site.PageSize(0)` → **no pagination**: every matching row in one shot.

`?page=N` selects the page (1-indexed). The renderer emits a DaisyUI `join`
button group with prev/next + numbers (omitted when unpaginated). Page / sort /
search links are HTMX with `hx-push-url`, so the state is a real, shareable URL
— a reload re-renders the full page in that state. Search and sort reset to
page 1.

`PaginationSettings` is one half of `site.Settings` (the other is
`TimeFormatter`); `site.DefaultSettings` implements both, so an app holds one
config value and hands it to each consumer by the narrow interface it needs —
the `MetaModel.TimeFormatter` for time cells, the `NewTable` `pager` for
paging. Override either by embedding `site.DefaultSettings` and shadowing one
method.

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
| `examples/admin_gorm` | Same schema wrapped in `Admin` — zero per-field config (empty presets, default slugs), relations auto-wired. Also the one example that ships an **app-owned styling polish** (a small `<style>` block in its page shell that softens DaisyUI v5's focus outline + font smoothing); the others run on bare DaisyUI defaults. |
| `examples/auth_gorm`  | `AuthGORM` + `Admin` over User/Group, with `Authz`, a write-only password field, and a `DisplayValue` override. |

`go run ./examples/<name>` — each starts on `:8080`.
