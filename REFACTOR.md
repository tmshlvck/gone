# gone/crud — refactor proposal

Working document. The goal is to fold the lessons from the `alpine`
experiment (`../gone-alpine`) back into the **HTMX** codebase — *without*
adopting the huma/JSON/Alpine front-end. We keep server-rendered templ +
HTMX; we borrow the alpine branch's *Go-side* ergonomics.

Status: draft for iteration. Nothing here is committed. Each section ends
with **Open questions** to settle before we touch code.

---

## 1. Where the complexity is today

Rough line counts in `crud/` (excluding generated `views_templ.go` and
tests):

| file           | LOC | what                                               |
|----------------|-----|----------------------------------------------------|
| `table.go`     | 783 | CRUDTable, Map backend, HTTP handlers, HX wiring   |
| `relation.go`  | 620 | relation detect/render/bind + `/options` + interface |
| `meta.go`      | 580 | MetaModel, reflection, default hooks               |
| `validators.go`| 297 | field validators                                   |
| `admin.go`     | 267 | Admin aggregator                                    |
| `common.go`    | 125 | HX helpers, Mux/PageShellFunc re-exports           |
| `single.go`    |  76 | MetaModel render/bind primitives                   |

The alpine rewrite of the same surface is ~2.6k LOC vs ~6k here. Not all
of that gap is portable (a lot of it is "the browser does the rendering
now"), but three structural choices account for most of the *Go-side*
difference, and all three port back to HTMX:

1. **Config is supplied up front, not patched in afterward.** No
   `DeriveMetaModel()` → `MustFindField(...).X = ...` ritual.
2. **One router (chi), no `net/http` compatibility shim.** No `Mux`
   interface, no manual `urlBase` threading, no "don't use `chi.Mount`"
   footgun.
3. **Relations are addressed by URL, not by an in-process pointer
   graph.** This is what let the alpine `CRUDTableInterface` collapse
   from 11 methods to a 3-method `AdminEntry`.

Plus the thing you flagged: **a partial-vs-full render seam** so handlers
stop hand-writing `if isHTMXRequest(r) { fragment } else { redirect }`
(45 `HX-*` touch points across the package today).

---

## 2. Theme A — describe the model once, up front

### Today

`examples/crud_gorm/main.go` is the honest benchmark. Building one table
is: derive, then mutate the derived struct through `MustFindField`, then
derive the table, then mutate *that*, then wire relations by reaching
back into `table.MetaData`:

```go
heroMM, err := crud.DeriveMetaModel[Hero]()        // full defaults materialized
// ...
heroMM.MustFindField("ID").ReadOnly = true         // patch after the fact
f := heroMM.MustFindField("Name")
f.FormHelp = "Display name, 2–40 characters."
f.FieldValidate = crud.All(crud.NotEmpty, crud.MinLen(2), crud.MaxLen(40))
// ... 30 more lines of MustFindField ...
heroTable = crud.DeriveGormCRUDTable[Hero](heroMM, nil, db)
heroTable.Slug = "heroes"
heroTable.PageSize = 10
heroTable.MetaData.MustFindField("Weapons").RelatedCRUD = &weaponTable  // pointer graph
```

Problems this creates:

- **Two-phase, mutate-after-derive.** The "source of truth" for a field
  is split between reflection defaults and a pile of post-hoc
  assignments. You can't read a model's shape in one place.
- **`MustFindField` panics at startup** on a typo — better than a silent
  bug, but it's a runtime check standing in for what should be a
  construction-time one.
- **Exported mutable internals.** `MetaModel.Fields`, `.DisplayValues`,
  `.BindForm`, etc. are all public and writable because the override
  story *requires* poking them. The API surface is "every field is
  public so you can patch it."
- **Order-dependent.** RelatedCRUD must be set *after* `DeriveGorm...`
  because it needs the table pointers; PageSize/Slug must be set after
  derive but before Route. Easy to get wrong, nothing enforces it.

### Proposed: a declarative config passed *into* the constructor

You build the config you want; the constructor reflects `T`, merges your
overrides over the derived defaults, validates field names against the
real type, and hands back a ready table. One call, one place, fail-fast.

```go
heroTable := crud.NewGormTable(db, crud.Table[Hero]{
    Slug:     "heroes",
    Title:    "Heroes",
    PageSize: 10,
    Fields: crud.Fields{
        "ID":      {ReadOnly: true},
        "Name":    {Help: "Display name, 2–40 characters.",
                    Validate: crud.All(crud.NotEmpty, crud.MinLen(2), crud.MaxLen(40))},
        "Realm":   {Help: "Origin (e.g. Gondor, Mirkwood).", Validate: crud.MaxLen(40)},
        "Power":   {Help: "Power level, 0–100.", Validate: crud.IntRange(0, 100)},
        "Weapons": {Label: "Weapons (read-only)", Help: "Edit via /weapons.",
                    Relation: "weapons"},   // related table's slug — a string, not a pointer
        "Skills":  {Help: "Hold Ctrl/Cmd to pick multiple.", Relation: "skills"},
    },
})
```

Key shifts:

- **`crud.Fields` is `map[string]FieldOverride`** keyed by Go field name.
  The constructor walks the reflected type; an override for an unknown
  field is a **returned error** (or a single panic-at-startup in a
  `Must` variant), not a deferred `MustFindField`.
- **Only what you override appears.** Everything else stays on reflected
  defaults. The `FieldOverride` struct is small and all-optional —
  `Label, Help, ReadOnly, Hidden, Validate, InputType, Relation, …`.
- **`MetaModel` internals stop being part of the public contract.**
  `DisplayValues/GenFormElements/BindForm` and the per-field hooks move
  to unexported fields (or a clearly-separated "advanced" struct). The
  90% caller never sees them.
- **Relations are declared inline by slug** (`Relation: "weapons"`),
  killing the post-derive pointer wiring. See Theme C.

`DeriveMetaModel` / `DeriveGormCRUDTable` / `MustFindField` stay as the
**low-level** path for the rare caller who needs a custom hook — but the
examples and docs lead with `NewGormTable`/`NewMapTable`.

**Note on the alpine path you mentioned:** alpine moved *all* of this to
struct tags (`doc:"…"`, `readOnly:"true"`, `minLength:"2"`,
`x-validate:"ip"`, `x-relation:"heroes"`). That's clean when the model is
yours to annotate, but it (a) puts presentation concerns in the domain
struct and (b) can't carry a Go closure like `crud.IntRange(0,100)`. The
config-struct approach above keeps tags optional (we can still *read*
`gorm:`/`json:` tags for defaults) while letting validators stay real Go
functions. **Open question below.**

### Open questions (A)

- **Field key: Go name or JSON/slug?** Map keys as Go field names
  (`"Name"`) match the current reflection model; the alpine branch keyed
  on JSON names. Go names are clearer for a templ/HTMX stack with no JSON
  layer — proposed.

Answer: Keep go names.

- **Config struct vs functional options?** `crud.Table[T]{...}` literal
  (proposed, reads as data) vs `crud.NewTable(db, crud.WithField(...),
  crud.PageSize(10))`. The literal is more declarative and diff-friendly;
  options compose better if we expect many orthogonal knobs. Lean
  literal.

Answer: literal = pass the map of fieldName -> configstruct

- **How much to hide?** Do we fully unexport `MetaModel.Fields`, or keep
  it exported-but-discouraged for one release to ease migration?

Answer: Keep it public.

- **Tags as a *source of defaults*** — keep reading `gorm:` for relation
  detection (we must) and maybe `validate:`/`doc:` as low-priority
  defaults the config struct overrides? Or config-struct only?

Answer: `gorm:...` yes, otherwise just config struct.

---

## 3. Theme B — commit to chi, delete the compat layer

### Today

The library routes through a hand-rolled `Mux` interface
(`HandleFunc(pattern, handler)`, a subset of `*http.ServeMux`) so it can
claim router-agnosticism. The cost of that claim:

- **`PageShellFunc` + `Mux` + the `(string, error)` return convention**
  exist to thread the absolute `urlBase` around by hand
  (`normalizePrefix`, the `baseUrl + "/" + Slug` math in every `Route`).
- **A documented footgun**: "don't mount behind `http.StripPrefix` /
  `chi.Mount` / `chi.Route`" because rendered HTML carries absolute URLs
  and prefix-stripping hides the prefix. So we're *already* chi-hostile;
  we just don't admit it.
- **No route grouping**, so authz is enforced by a per-handler
  `authzGate(w, r, "create")` string-switch instead of middleware on a
  group of routes.

The alpine branch took `chi.Router` directly and the routing code got
markedly smaller (`RegisterRoutes(r chi.Router, urlBase string, shell)`),
because chi owns prefix composition and you stop simulating it.

### Proposed

- **Take `chi.Router` (and `chi.URLParam`) directly.** Delete the `Mux`
  interface and the `auth.Mux`/`crud.Mux` re-export. `auth` and `crud`
  both just import chi. (`go.mod` already pulls chi transitively via the
  alpine work; here it becomes a direct dep.)
- **Register relative, record the base for rendering.** Mirror alpine:
  `RegisterRoutes(r chi.Router, urlBase string, shell PageShell)` mounts
  patterns relative to `r`, and `urlBase` is recorded *only* so rendered
  `hx-get`/`action` URLs resolve absolutely. This makes
  `chi.Route("/admin", …)` composition work instead of being forbidden —
  the prefix is passed once, explicitly, rather than reverse-engineered.
- **Authz via group middleware.** Replace the `authzGate` switch with a
  middleware that maps method→action and calls the right `Can*`. Mutating
  routes go in a `r.Group` with that middleware; the read routes in
  another. The five-way string switch disappears.

```go
func (t *Table[T]) RegisterRoutes(r chi.Router, urlBase string, shell PageShell) {
    t.urlBase = strings.TrimRight(urlBase, "/") + "/" + t.Slug
    r.Get("/", t.page(shell))                 // full page OR fragment — see Theme D
    r.Group(func(r chi.Router) {
        r.Use(t.authz(opRead))
        r.Get("/{id}/edit", t.editForm)
        r.Get("/{id}", t.display)
        r.Get("/options", t.options)
    })
    r.Group(func(r chi.Router) {
        r.Use(t.authz(opWrite))
        r.Post("/", t.create)
        r.Put("/{id}", t.update)              // real verbs, not /{id}/edit POST
        r.Delete("/{id}", t.delete)
    })
}
```

### Open questions (B)

- **Hard chi dependency acceptable?** It contradicts today's "any
  `*http.ServeMux`" promise. Given we already forbid the only
  composition tricks ServeMux/chi differ on, the promise is mostly
  theatre — but confirm we're OK making chi mandatory for *callers*
  (their `main` must build a `chi.Router`).

Answer: Yes/

- **`auth` package too?** `AuthSimple`/`AuthGORM.Route` also take `Mux`.
  Either convert both for consistency (proposed) or leave auth on the
  interface and only convert crud (smaller blast radius, but two router
  stories).

Answer: Convert both.

- **Method-based routes (PUT/DELETE) vs POST-only.** HTMX speaks all
  verbs via `hx-put`/`hx-delete`; moving off `POST /{id}/edit` to
  `PUT /{id}` is cleaner REST and aligns with a future JSON API
  (TODO #3). Any reason to keep POST-only? (Some proxies/CSRF setups.)

Answer: Yeah, but we would loose any possibility to work with HTMX / JS disabled.
As the matter of fact I have been thinking about going in the oppsite direction
and make the app work as plain MPA with hx-boost when applicable and use other hx-*
functions only where needed.

I get that we have a routing aid that helps either rended whole page with the shell
or the partial, so maybe we should pull it outside crud/ to htmx/ or site/? We will
probably want to add other helpers, like helpers for hybrid apps that will offer JSON
versions of some CRUD endpoints so when user hits /admin/heroes/1 it would either
render whole page, HTMX partial or JSON based on accept header and presence of HTMX
headers.

As the matter of fact I believe the components and routes should nicely compose
in both dimensions - we should be able to place the component to a page with minimalistic
requirements (just the mandatory styling and HTMX library) regardless where we put it (modal,
page, specific column, whatever). And we should be also mount the component anywhere in the



---

## 4. Theme C — address relations by URL, slim the interface

This is the highest-leverage cut and the reason the alpine `AdminEntry`
is 3 methods where our `CRUDTableInterface` is 11.

### Today

`relation.go` (620 LOC) carries an in-process pointer graph:
`MetaField.RelatedCRUD CRUDTableInterface`, wired by `AutoWireRelations`
or by hand (`MustFindField("Weapons").RelatedCRUD = &weaponTable`). To
serve that graph, `CRUDTableInterface` must expose **11 methods** —
`SearchOptions`, `GetOptionsByID`, `HTMXCreateURL`, `AutoWireRelations`,
`URLBase`, `ModelName`, … — almost all of which exist *only* so a
relation `<select>` on table A can pull options from table B in-process.

But the options are already fetched **over HTTP** at render time
(`relationSelect` calls `mf.RelatedCRUD.SearchOptions(...)`) and refreshed
via an `hx-get` to `{related}/options`. So we keep *both* an in-process
pointer *and* an HTTP endpoint for the same data.

### Proposed: the relation only needs the related table's URL base

A relation field carries a **string** (`Relation: "weapons"` → resolves
to `/weapons` or `/admin/weapons`), not a `CRUDTableInterface` pointer.
The `<select>` is populated by an `hx-get` to `{base}/options` on first
render (or server-side by a single resolve call through a tiny
`OptionSource` the table already implements for *itself*). Cross-table
wiring becomes: "what's the URL base of the table whose slug is
`weapons`?" — resolved once at `Admin.RegisterRoutes` time from the slug
map, or passed explicitly.

Consequences:

- **`CRUDTableInterface` collapses** to what `Admin` actually needs:
  `slug()`, `title()`, `component()`/`render(r)`, and `routes(r, base)`.
  Everything option-related leaves the interface.
- **`AutoWireRelations`, `RelatedCRUD`, `SearchOptions`,
  `GetOptionsByID`, the pointer graph — all deleted.** `relation.go`
  shrinks toward just: detect relation kind (reflection, unchanged),
  render the `<select>`/`<select multiple>` with an `hx-get` options URL,
  bind posted IDs back (unchanged). Estimate: 620 → ~300 LOC.
- **Tables stop knowing about each other as Go values.** A table is
  self-contained; the only cross-reference is a slug string. This also
  removes the `DeriveAdminAutoWire` vs `DeriveAdmin` split.
- **Bonus: it sets up TODO #3 (JSON API).** Once options come from a URL,
  that URL can be the JSON list endpoint, and the HTML and JSON surfaces
  share one data path.

The L2 "+ create new" modal stays (it's a UX feature, not coupling); its
target URL is `{relatedBase}/create`, already a string.

Answer: Yes, absolutely. Please simplify and just link another table - which
would need to be able to generate the id - label pairs, but it makes sense
since it already has the data.

### Open questions (C)

- **Where does the slug→base resolution happen?** Options: (1) `Admin`
  owns a slug→urlBase map and stamps each table at route time; (2) a
  convention that all tables share a root (`/admin/{slug}`) so base is
  derivable; (3) the relation override carries the full base
  (`Relation: "/admin/weapons"`). (1) is most flexible, (3) most
  explicit. Lean (1) for Admin-managed tables, (3) escape hatch for
  standalone.

Answer: 1, we use table

- **Server-side first paint vs hx-get on load.** Today options render
  server-side so the `<select>` is populated without a round-trip. To
  keep that without the pointer graph, the table needs to resolve "give
  me options for slug X" at render — which means *some* registry lookup.
  Acceptable, or do we accept an `hx-trigger="load"` fetch for the first
  paint (simpler, one less coupling, tiny flash)?
- **Unresolved-ID display** (`#N (unresolved)`) — keep; it's good
  behaviour and independent of the wiring change.

---

## 5. Theme D — the HTMX partial-vs-full seam you asked about

### Today

The library registers **only fragment** endpoints; the app registers the
full-page `GET base` itself and must remember to embed `PageModals()`.
Handlers branch by hand, and there are **two URLs for one view** (`GET
base` = page via shell, `GET base/view` = the same table as a fragment).
Mutations are a thicket of `HX-Trigger` / `HX-Retarget` / `HX-Reswap`
(45 `HX-*` references in the package).

```go
if isHTMXRequest(r) {
    w.Header().Set("HX-Trigger", fmt.Sprintf(`{"closeModal":%q}`, modalID))
    w.Header().Set("HX-Retarget", "#"+c.ListID)
    w.Header().Set("HX-Reswap", "innerHTML")
    return TableContent(d)
}
http.Redirect(w, r, c.urlBase, http.StatusSeeOther)
```

### Proposed: one content function, a responder picks the envelope

Introduce a small render seam — call it `Frame` — that every handler
returns *content* through. The seam decides the envelope from the
request:

```go
// hxResponse renders `content`. For an HX-Request it writes the bare
// fragment; for a normal navigation it wraps `content` in the page
// shell (full HTML). Handlers never branch on HX-Request again.
func (t *Table[T]) respond(w http.ResponseWriter, r *http.Request, content templ.Component) {
    if hx.IsRequest(r) {
        hx.Fragment(w, r, content)
        return
    }
    t.shell(w, r, t.title, content)   // full page
}
```

This collapses the two-URL split: **`GET base` is the only list URL.**
With `HX-Request` it returns the table fragment (swapped into the list
target, `hx-push-url` keeps the address bar honest); without, it returns
the full page. `/view` goes away. Same idea for the detail view: `GET
base/{id}` is a real, shareable URL that serves a full page on navigation
and a fragment on swap.

For mutations, wrap the HX-header juggling in named intents so the
header strings live in one place:

```go
hx.Trigger(w, hx.CloseModal(modalID))      // sets HX-Trigger JSON
hx.Retarget(w, "#"+t.listID, "innerHTML")  // sets HX-Retarget + HX-Reswap
return t.tableFragment(r)
// non-HTMX fallback (no JS): respond() / 303 handled by the seam
```

A new tiny **`crud/hx` helper package** (or an `hx.go` file) owns:
`IsRequest`, `CurrentURL`, `Fragment`, `Trigger`/`CloseModal`/`OpenModal`,
`Retarget`. That's the "abstraction layer for working with HTMX" — it
turns the stringly-typed header protocol into a handful of named
functions and gives us *one* full-vs-partial decision point.

### What this buys

- Handlers read as "compute content, return it" — the
  fragment-or-page decision is structural, made once.
- One canonical URL per view (page == fragment source), so `hx-boost`,
  `hx-push-url`, deep links, and reload all work without a second route.
- The `HX-*` protocol strings are centralized and testable instead of
  smeared across `table.go`.
- The library can now own the page route too (it knows the shell), so the
  app stops hand-writing `GET base` + remembering `PageModals()`.

### Open questions (D)

- **Where does the shell come from at fragment time?** The seam needs the
  app's `PageShell` to build the full page. Table already gets it via
  `RegisterRoutes(..., shell)`. Confirm every render path has it (Admin
  passes its own).
- **Separate `crud/hx` package vs internal file?** A public `hx` package
  is reusable by app handlers too (they face the same partial/full
  question) and documents the contract; an internal file is less surface.
  Lean public, small.
- **Modal stack contract.** Today: per-slug L1 + shared L2, driven by
  `HX-Trigger {openModal/closeModal}` and `modalIDsFromHeader` parsing
  `HX-Target`. Worth keeping the two-level model, or can the seam own a
  single generic modal stack in `gone.js` (push/pop) so L1/L2/Ln are
  uniform and `modalIDsFromHeader` disappears? Probably a follow-up, but
  flag it.

---

## 6. What the end-state caller looks like

`examples/crud_gorm/main.go` today is ~290 lines; the model/seed half
stays, but `buildTables` + `main`'s routing shrink to roughly:

```go
func main() {
    db := openAndMigrate()
    r := chi.NewRouter()
    r.Use(middleware...)                       // app owns the stack

    heroes := crud.NewGormTable(db, crud.Table[Hero]{
        Slug: "heroes", Title: "Heroes", PageSize: 10,
        Fields: crud.Fields{
            "ID":      {ReadOnly: true},
            "Name":    {Validate: crud.All(crud.NotEmpty, crud.MinLen(2), crud.MaxLen(40))},
            "Power":   {Validate: crud.IntRange(0, 100)},
            "Weapons": {Relation: "weapons", ReadOnly: true},
            "Skills":  {Relation: "skills"},
        },
    })
    weapons := crud.NewGormTable(db, crud.Table[Weapon]{ Slug: "weapons", /* … */ })
    skills  := crud.NewGormTable(db, crud.Table[Skill]{  Slug: "skills",  /* … */ })

    admin := crud.NewAdmin(heroes, weapons, skills)   // no AutoWire step
    r.Route("/admin", func(r chi.Router) {
        admin.RegisterRoutes(r, "/admin", pageShell)  // composes under the prefix — allowed now
    })
    http.ListenAndServe(":8080", r)
}
```

No `MustFindField`, no `DeriveMetaModel` two-step, no `RelatedCRUD`
pointer wiring, no per-table `Route` calls, no hand-written `GET base`
page handlers, no `PageModals()` to remember, mounting under `/admin`
*allowed*.

---

## 7. Suggested sequencing

Each step is independently shippable and independently revertible.

1. **D first (HX seam).** Lowest risk, immediate readability win, no API
   break for callers — purely internal. Lands the `hx` helpers and the
   `respond()` seam; collapse `/view` into `GET base`. *Gate: examples
   still work, tests green.*
2. **B (chi).** Mechanical: swap `Mux`→`chi.Router`, `urlBase` threading,
   authz middleware. Breaks caller `main`s (they build a chi router now)
   — a clear, one-time migration.
3. **C (relations by URL).** The big deletion. Depends on B for the
   slug→base resolution at route time. Removes most of the interface and
   the pointer graph.
4. **A (config up front).** The new constructors (`NewGormTable`,
   `NewMapTable`) layered over the now-simpler internals; keep
   `Derive*`+`MustFindField` as the documented low-level path. Rewrite
   examples and `docs/CRUD.md` to lead with the config struct.

A and C touch the most code; doing D and B first means they land on a
codebase that's already lost the worst of the HX-header and routing
noise.

## 8. Decisions to lock before coding

Consolidated from the per-theme questions — these are the ones worth
your call:

1. **chi mandatory for callers?** (B) — yes/no.
2. **Convert `auth` to chi too, or only `crud`?** (B)
3. **Config-struct literal vs functional options** for the new
   constructor. (A)
4. **Fully unexport `MetaModel` internals**, or deprecate-in-place for a
   release? (A)
5. **Relation slug→base resolution**: Admin-owned registry vs explicit
   full base on the override. (C)
6. **Server-side first paint of relation options** (needs a registry
   lookup) vs `hx-trigger="load"` fetch (simpler, tiny flash). (C)
7. **`crud/hx` as a public package** vs internal file. (D)
8. **Generic modal stack in `gone.js`** now, or leave the L1/L2 model and
   revisit. (D)

Once these are settled I'll turn the chosen path into a concrete patch
series following §7.
