# gone/crud — construction + structure refactor (plan)

Companion to [`REFACTOR.md`](REFACTOR.md) and
[`REFACTOR-HTMX.md`](REFACTOR-HTMX.md). Those landed Stage 1 (chi + routing)
and Stage 2 (relations-by-URL, the `Table[T]` recipe). This document plans
the **final construction API + a structural tidy-up** of `crud/`.

Goal: a stable, honest, DRY construction surface, and a `crud/` directory
where each file owns one concept. Status: plan for review; nothing built.

---

## 0. The shape we're moving to

Three explicit stages — **derive metadata → bind a data source → construct
the table** — and a fourth, separate step that places it in the URL
namespace:

```go
// 1. metadata: derive from the struct, overlaying a preset (partial overrides)
mm := crud.DeriveMetaModel[Hero](crud.MetaModel[Hero]{
    DisplayName: "Heroes",
    Fields: []crud.MetaField{
        {Name: "Name",  FieldValidate: crud.NotEmpty},
        {Name: "Power", FormHelp: "0–100"},
    },
})

// 2. data: a backend that satisfies Accessor[Hero], told the metadata
data := crud.GORMAccessor[Hero](mm, db)        // or MapAccessor(mm, store, mu)

// 3. table: generic, *constructed* (not "derived") from metadata + data + knobs
table := crud.NewTable(mm, data, 10, az)        // pageSize, authz — NO path

// 4. routing: bind the component to a point in the path namespace
table.RegisterRoutes(root, "", "/admin/heroes") // router, routerPrefix, componentPath
```

Two principles drive every decision below:

- **`MetaModel` is pure metadata** (no data, no authz, no routing) — so the
  data plane is a separate `Accessor[T]` interface, not closures hung off the
  table, and not fields on `MetaModel`.
- **Construction is path-free.** `NewTable` / `NewForm` / `NewDetail` don't
  take a slug or path. *Where* a component lives is a routing fact, supplied
  at `RegisterRoutes`. The Form-embedded-in-Table case forces this: the
  embedded form's action URL comes from the table's mount point, so the path
  can't belong to construction.

---

## 1. The data plane: `Accessor[T]`

Replace the five `Get/List/Create/Update/Delete` closures on `CRUDTable[T]`
with one interface and per-backend implementations.

```go
// accessor.go
type Accessor[T any] interface {
    Get(ctx context.Context, id uint) (T, error)
    List(ctx context.Context, search, sortBy string, desc bool, offset, limit int) ([]CRUDSearchResult[T], int64, error)
    Create(ctx context.Context, in T) (id uint, out T, err error)
    Update(ctx context.Context, id uint, in T) (T, error)
    Delete(ctx context.Context, id uint) error
}

type CRUDSearchResult[T any] struct { ID uint; Row T }
var ErrNotFound = errors.New("not found")
```

Backends become types that *implement* it, each told the `MetaModel` so a
user's `Searchable`/`Sortable` customizations reach search and sort:

```go
// mapaccessor.go
func MapAccessor[T any](mm MetaModel[T], store map[uint]T, mu *sync.RWMutex) Accessor[T]
// gormaccessor.go
func GORMAccessor[T any](mm MetaModel[T], db *gorm.DB) Accessor[T]
```

A custom/safe-view backend is just any value satisfying `Accessor[T]` — no
constructor needed (this is the alpine `NewResource` escape hatch, for free).
`SingleAccessor` (one row behind get/set) is a trivial later add; omitted now.

`CRUDTable[T]` then holds `MetaData MetaModel[T]` + `Data Accessor[T]` + the
knobs, and every handler calls `c.Data.List(...)` etc. The per-backend
`DeriveMapCRUDTable`/`DeriveGormCRUDTable` are deleted.

---

## 2. Metadata: `DeriveMetaModel[T](preset)`

```go
func DeriveMetaModel[T any](preset MetaModel[T]) MetaModel[T]
```

- Reflects `T` for defaults, then overlays `preset`: non-empty strings win,
  non-nil hooks/`Validate` win, `ReadOnly`/`Hidden` are additive (true wins).
  **Relation metadata + `DivID` stay derive-authoritative** (a preset can't
  break relation detection).
- **Panics** (no `error` return) on a non-struct `T` or a preset field `Name`
  the struct doesn't have — programming errors fail at startup, the
  `regexp.MustCompile` idiom we've used throughout.
- Overrides are honest **`MetaField`** values (`DisplayName`, `FormHelp`,
  `FieldValidate`, `DisplayValue`/`GenFormElement`/`BindStrings`, …) — no
  `Field`-alias layer hiding that a `MetaField` serves both table and form.

This **deletes `config.go`** (the `Table[T]`/`Field` recipe,
`NewGormTable`/`NewMapTable`). The secret-field helpers
(`Redact`/`PasswordInput`/`HashWith` + `valuePresent`/`setStringFieldByName`)
move to `fields.go`.

> Trade-off accepted: the reflection bools `Sortable`/`Searchable` derive to
> `true` for scalars; additive merge can turn them *on* but not force-*off*
> (a zero `false` reads as "no override"). Forcing off uses direct field
> mutation on the returned `mm`. Documented, not worked around.

---

## 3. Routing: `RegisterRoutes(router, routerPrefix, componentPath)`

Rename and generalize the current `(mountBase, slug)`:

```go
func (t *CRUDTable[T]) RegisterRoutes(r chi.Router, routerPrefix, componentPath string)
```

- **`routerPrefix`** (was `mountBase`) — the absolute **path** at which `r`
  itself is served (`""` for a root router; the strip prefix for a
  `chi.Route`/`chi.Mount` sub-router). It's a path, not a URL/URI — hence the
  rename.
- **`componentPath`** (was `slug`) — where this component sits **relative to
  `r`**, and it may be **multi-segment** (`"/admin/heroes"`, not just
  `"heroes"`). Routes register at `componentPath + …` on `r`; the absolute
  base for rendered `hx-*`/`action` URLs is `routerPrefix + componentPath`.

The win: a multi-segment `componentPath` removes the forced
`chi.Route("/admin", …)` wrapper. A table can mount itself anywhere on a root
router:

| want | call |
|---|---|
| `/heroes` on root | `RegisterRoutes(root, "", "/heroes")` |
| `/admin/heroes` on root (no sub-router) | `RegisterRoutes(root, "", "/admin/heroes")` |
| under a stripping `chi.Route("/admin")` | `RegisterRoutes(r, "/admin", "/heroes")` |

One rule, unchanged: **`routerPrefix + componentPath` is the absolute path the
component is reachable at**, and `routerPrefix` must equal where `r` is served.

**`Slug` as a stored field goes away.** It conflated three things now sourced
separately: the path (→ `componentPath` at routing), the relation match (→ Go
type name, already), and the sidebar label (→ `DisplayName`). The only
remaining use — a unique per-page **modal id** — derives from a sanitized
`componentPath` (e.g. `/admin/heroes` → `admin-heroes-modal-l1`), set at
`RegisterRoutes`.

`Render(r)` keeps emitting absolute URLs from the stored
`routerPrefix+componentPath`, so it must be called after `RegisterRoutes`
(already true).

### Admin

`Admin` composes `componentPath`s for its children on **one** router — no
nested `chi.Route` per table:

```go
admin.RegisterRoutes(root, "", "/admin")
// internally: each child RegisterRoutes(root, "", "/admin/"+segment),
// plus GET /admin/{segment} page handlers and the index redirect.
```

Each child's path **segment** + sidebar **label** come from the table
(segment defaulting to a lowercased plural of the model name, overridable;
label = `DisplayName`). Admin no longer forces the caller to set up a
stripping sub-router.

---

## 4. Form & Detail as reusable, route-free render/bind

The Form's value is **rendering fields + binding strings through the
MetaModel** — both path-free and accessor-free:

```go
// form.go
type FormOpts struct { ActionURL, HXTarget, SubmitLabel, Title string; Errors ValidationErrors; SuccessMsg string }
func (mm *MetaModel[T]) RenderForm(instance T, opts FormOpts) templ.Component // ActionURL is just a string
func (mm *MetaModel[T]) TryBindForm(r *http.Request, out *T) error            // parse + bind, no accessor
// detail.go
func (mm *MetaModel[T]) RenderDisplay(instance T) templ.Component
```

The **table owns** the data accessor and the `{id}`-parametrized route
(`POST {componentPath}/{id}/edit`); it loads the row via `Data.Get`, renders
the form with `RenderForm` (passing the action URL it computed from its own
`componentPath`), binds on submit with `TryBindForm`, and saves via
`Data.Update`. The form needs **no parametrized accessor of its own**.

**This deletes the table's `createFormView`/`editFormView`** and its inline
`ParseForm + BindForm` — the table reuses `RenderForm`/`TryBindForm` exactly
like a standalone caller (`examples/form_mem`) does. One render path, one bind
path.

Optional (decision below): expose `NewForm`/`NewDetail` as *standalone
routable components* for a full-page edit form / detail page (each with its
own `Accessor` + `RegisterRoutes(router, routerPrefix, componentPath)`). The
render/bind core is shared with the table either way.

---

## 5. File layout

| file | holds |
|---|---|
| `meta.go` | `MetaModel`, `MetaField`, `DeriveMetaModel(preset)`, default field hooks, `formatValue`/`boolBadge`/`displayTime` |
| `form.go` | `FormOpts`, `RenderForm`, `TryBindForm` (from `single.go`) |
| `detail.go` | `RenderDisplay` (from `single.go`) |
| `fields.go` | `Redact`/`PasswordInput`/`HashWith` + helpers (from deleted `config.go`) |
| `accessor.go` | `Accessor[T]`, `CRUDSearchResult`, `ErrNotFound` |
| `mapaccessor.go` | `MapAccessor[T]` + its reflection search/sort helpers |
| `gormaccessor.go` | `GORMAccessor[T]` |
| `table.go` | `CRUDTable`, `NewTable`, `RegisterRoutes`, handlers (reuse Form), `buildTableViewData` |
| `relation.go` | relation field hooks, `DefaultShortLabel` + label stages, `relationSelect`/options |
| `tableiface.go` | `CRUDTableInterface` + `*CRUDTable[T]` impls (`URLBase`/`Render`/`InstanceShortLabel`/`StampRelations`/…) + `WireRelations` |
| `admin.go` | `Admin` |
| `validators.go`, `common.go`, `views.templ` | unchanged (minus dead bits) |

`single.go` is split; `config.go` is deleted. Splitting the non-generic
interface + table-impl methods out of the 700-line `relation.go` into
`tableiface.go` leaves `relation.go` about just relations.

---

## 6. Additional cleanup (found in the scan)

Per your ask — things that are unnecessarily complex, low-value, or
duplicated:

1. **Drop the `MetaModel` hook *fields*.** `DisplayValues`, `GenFormElements`,
   `BindForm` are `func` fields on `MetaModel`, but they're **only ever set to
   the `Default*` implementations** — nothing overrides them. They add a layer
   of indirection (`mm.BindForm(mm, …)` instead of `DefaultBindForm(mm, …)`)
   and an "override the whole model render" capability no one uses. Replace
   with plain methods (`mm.DisplayValues(instance)` calling the per-field
   hooks). Per-field customization still lives on `MetaField` (which *is*
   used). Removes 3 fields + the self-passing call convention.
2. **Remove dead fields.** `MetaModel.Slug`, `MetaModel.DivID`,
   `MetaField.DivID` have no readers (grep finds none in code or templ).
   `MetaField.DivID` was per-instance wrapper ids that nothing renders. Verify
   and delete. (`Slug` on `MetaModel` is separate from the table slug, which
   is itself going away — §3.)
3. **One form path / one bind path** — §4 (delete `createFormView`/
   `editFormView` + inline bind).
4. **Stale doc.** `meta.go`'s package comment still says relations/validation
   are "stubbed and will land in later iterations" — they shipped. Fix.
5. **`relation.go` split** — §5 (`tableiface.go`).

Flagged but **out of scope** for this pass (note, don't fix now):

6. **Modal HX wiring.** The L1/L2 two-level modal — `modalIDsFromHeader`
   parsing `HX-Target`, the `openModal`/`closeModal` `HX-Trigger` dance, the
   per-component modal ids, the `crud-relation-add-btn { display:none }` hack
   for hiding the nested "+ new" inside L2 — is the densest remaining
   complexity. It works and it's verified; a generic push/pop modal stack in
   `gone.js` would simplify it, but that's its own change. Leave a `TODO`.

---

## 7. Decisions to lock before building

1. **`NewTable` signature** — `NewTable(mm, data, pageSize, authz)` (path-free,
   positional, no config struct), with `HideUnauthorized` / `ShortLabel` /
   granular `CreateEnabled` as public fields set afterward. Confirm positional
   over a small `Options` struct (you said positional, refactor later).
2. **Standalone `NewForm`/`NewDetail` routable components** — build now, or
   keep Form/Detail as render/bind primitives only (`RenderForm`/`RenderDisplay`
   on `MetaModel`) and add routable wrappers later? (Lean: primitives now,
   routable wrappers when a real form-page/detail-page example needs them.)
3. **`MetaModel` hook fields → methods** (cleanup #1) — do it in this pass, or
   keep the override seam one more release? (Lean: do it; nothing uses it.)
4. **`DeriveMetaModel` panics, drops `error`** — confirm (vs keeping the
   `(…, error)` return for the non-struct case).
5. **Admin child segment source** — derive from model name (lowercased plural)
   with an override, vs. an explicit `NewAdmin([]Entry{table, path})`. (Lean:
   derive + override.)

## 8. Sequencing (one breaking change, green at the end)

This is an atomic API change — examples/tests/docs all move at once; the build
is red mid-way. Order to minimize thrash:

1. `accessor.go` + `mapaccessor.go` + `gormaccessor.go` (extract the backends
   behind `Accessor[T]`; table still works via a temporary adapter).
2. `CRUDTable.Data Accessor[T]`; rewrite handlers to `c.Data.*`; delete the
   five closures + `Derive*CRUDTable`.
3. `DeriveMetaModel(preset)` + delete `config.go`; metadata cleanups (#1, #2,
   #4 in §6).
4. Split `single.go` → `form.go`/`detail.go`; table reuses `RenderForm`/
   `TryBindForm` (§4); delete `createFormView`/`editFormView`.
5. Routing rename `(routerPrefix, componentPath)` + multi-segment + drop
   `Slug`; Admin path composition (§3); split `tableiface.go`.
6. `NewTable`; rewrite all examples to the 3-step construction.
7. Rewrite `docs/CRUD.md` to the final API; update tests throughout.

Each numbered step is a commit; the tree compiles again at 2, 4, 6, 7.
