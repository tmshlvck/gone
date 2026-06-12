# gone — Stage 1: chi router, routing surface, HTMX components

Companion to [`REFACTOR.md`](REFACTOR.md). That document lays out four
themes (A config-up-front, B chi, C relations-by-URL, D the HTMX render
split) and records the decisions taken inline. This is the **detailed
design for the first stage we ship**: the chi conversion, the routing
surface, and the HTMX helper packages. Themes A and C follow in Stage 2,
on top of this surface.

Status: decisions locked (see §10); implementing now.

> **Terminology.** Earlier drafts said "seam." That was just jargon for
> *the one place that decides whether to answer with a bare HTML fragment
> or a whole page.* Since the **app** now owns whole pages (§0), that
> decision mostly leaves the library — what remains is a small
> `Fragment` writer and an optional `Respond` helper. No more "seam."

---

## 0. The model: real MPA, fragments only for in-component interactivity

We do **not** use `hx-boost`. Per htmx's own
[quirks note](https://htmx.org/quirks/#some-people-don-t-like-hx-boost),
boosting buys little over a real multi-page app in modern browsers while
adding head-merge / script / history edge cases. So:

- **Every URL is a real page.** Navigation (top menu, sidebar links, "go
  to this page") is a plain `<a href>` — actual browser navigation, no
  htmx. The **app** renders the whole page: its chrome (head, theme, nav)
  wrapping a component's `Render(r)` output. Works identically with JS off.
- **Interactivity within a component** (table sort/search/paginate, open a
  modal form, submit it, delete a row) uses targeted `hx-get`/`hx-post`
  that return a **fragment** swapped into a specific element inside the
  current page. These never change which nav item is active.

The consequences that shape the whole design:

- **The app owns pages, navigation, and active-menu state.** The library
  does not impose a page-shell or nav model (no `Shell` struct, no
  `NavItem` — see §1). The example apps render components into their own
  small templ shells; real apps do it their way.
- **The library owns only fragments.** A component registers just its
  in-component interactivity routes — none of them is meant to be loaded
  as a whole page. The app registers the page route(s) and calls
  `component.Render(r)` to embed the content.
- **No active-menu problem.** Navigation is navigation; the app re-renders
  its shell on every page load and marks the active link from
  `r.URL.Path` however it likes. Nothing in the library needs to know.

**Keep state in the URL.** Put `hx-push-url="true"` on the in-component
controls that change list state (sort / search / page). The fragment swap
updates the table *and* the address bar, so a reload or deep-link to
`/admin/heroes?sort=power&page=3` re-renders the full page in that same
state — the list handler reads those params from the query either way.

**Tradeoff we accept:** full navigation doesn't preserve scroll/focus or
persistent elements (media players, sockets) across pages — none of which
a CRUD admin has. bfcache keeps it snappy. Right call for this app.

---

## 1. Two new packages

Pulled out of `crud/` so the HTMX plumbing is reusable by an app's own
pages, and split by dependency weight.

### `gone/htmx` — the HTMX wire protocol, typed *(decided: in-house)*

Stateless helpers over `*http.Request` / `http.ResponseWriter`. No templ,
no chi, no other gone packages. Turns stringly-typed headers into named
functions and is the home for the **backend-driven modal** control we want
(§7: the server decides open/close, the client just obeys).

```go
package htmx

// ── request classification ──
func IsRequest(r *http.Request) bool   // HX-Request: true
func IsBoosted(r *http.Request) bool   // HX-Boosted: true (kept for completeness)
func Target(r *http.Request) string    // HX-Target  (id of the swap target)
func TriggerName(r *http.Request) string // HX-Trigger-Name
func CurrentURL(r *http.Request) (*url.URL, bool) // HX-Current-URL, parsed

// ── response directives (fluent; one Apply) ──
type Resp struct{ /* … */ }
func Reply() *Resp
func (*Resp) Retarget(sel string) *Resp
func (*Resp) Reswap(spec string) *Resp
func (*Resp) PushURL(u string) *Resp
func (*Resp) Trigger(event string, detail any) *Resp // client event; JSON detail
func (*Resp) Apply(w http.ResponseWriter)

// ── backend-driven modal control ──
// OpenModal/CloseModal emit the HX-Trigger events gone.js listens for, so
// the server opens or closes a dialog by id. Same mechanism as today, but
// behind named calls instead of hand-written HX-Trigger JSON.
func (*Resp) OpenModal(id string) *Resp
func (*Resp) CloseModal(id string) *Resp
```

Reuse alternatives (`dajooo/go-htmx`, `angelofallars/htmx-go`) offer the
same surface, but gone is itself a library — we don't impose a dep for
~80 lines of stable code. Decided: in-house.

### `gone/site` — page-composition helpers (minimal for now)

Depends on `gone/htmx` + `templ`. We create the package now because we'll
want it (Stage 2 Admin sidebar/active-nav), but we **do not** ship a
`Shell` struct or `NavItem` yet — that was too heavy-handed. For Stage 1
it carries only what the fragment handlers and example apps actually use:

```go
package site

// Shell is the app's page-chrome function: wrap content in the app's full
// HTML document with the given title. Same shape as the old PageShellFunc;
// the app supplies it, the library never defines one.
type Shell func(w http.ResponseWriter, r *http.Request, title string, content templ.Component)

// Fragment writes a templ component as a bare HTML fragment response
// (Content-Type + render, no chrome). Used by every in-component handler.
func Fragment(w http.ResponseWriter, r *http.Request, c templ.Component)

// Respond is an optional convenience for an app route that wants ONE URL to
// serve both a fragment (htmx) and a full page (navigation). Apps that keep
// pages and fragments on separate URLs (our default) don't need it.
func Respond(w http.ResponseWriter, r *http.Request, shell Shell, title string, content templ.Component) {
    if htmx.IsRequest(r) {
        Fragment(w, r, content)
        return
    }
    shell(w, r, title, content)
}
```

Room to grow later (the `Shell`/`NavItem`/active-nav we deferred) without
disturbing Stage 1.

---

## 2. The routing surface: `RegisterRoutes(router, mountBase, slug)`

Your proposal, adopted. A component **registers its fragment routes
relative to the router it's handed, at `slug`**, and **renders absolute
links as `mountBase + slug`**. No shell parameter — the component never
renders a whole page (§0).

```go
// RegisterRoutes mounts the component's in-component (fragment) routes on r.
// Two strings carry the position:
//
//   mountBase — the ABSOLUTE path where r itself is served. The caller knows
//               this; chi can't report it at registration time.
//   slug      — where this component sits RELATIVE to r (e.g. "/heroes").
//
// The component's absolute base, used for every rendered hx-get / action /
// link, is path.Join(mountBase, slug). Routes register on r at slug +
// subpaths, so the component composes under stripping mounts and groups
// alike (contract below).
func (t *Table[T]) RegisterRoutes(r chi.Router, mountBase, slug string)
```

### Worked example (your scenario)

`router` is mounted at `/admin` via a **stripping** mount; add the heroes
table at `/heroes`, and the app owns the page route:

```go
r.Route("/admin", func(r chi.Router) {          // chi.Route STRIPS "/admin"
    heroTable.RegisterRoutes(r, "/admin", "/heroes")        // fragment routes
    r.Get("/heroes", func(w http.ResponseWriter, req *http.Request) {
        content, _ := heroTable.Render(req)                 // app owns the page
        appShell(w, req, "Heroes", content)
    })
})
```

Inside `RegisterRoutes` (fragments only — the page route `GET /heroes`
belongs to the app, so the list-refresh fragment lives at `/heroes/view`):

```go
t.base = path.Join(mountBase, slug)           // "/admin/heroes" — for rendering
r.Get(slug+"/view", t.listRows)               // sort/search/paginate target  → /admin/heroes/view
r.Get(slug+"/create",      t.createForm)      //                              → /admin/heroes/create
r.Post(slug+"/create",     t.createPost)
r.Get(slug+"/{id}/edit",   t.editForm)        //                              → /admin/heroes/1/edit
r.Post(slug+"/{id}/edit",  t.editPost)
r.Post(slug+"/{id}/delete",t.deletePost)      //                              → /admin/heroes/1/delete
r.Get(slug+"/{id}/display",t.rowDisplay)
r.Get(slug+"/options",     t.options)         // relation picker options
```

### The composition contract (stripping vs grouping)

One rule: **`mountBase` must equal the absolute prefix at which the passed
router is served.**

| caller wiring                         | router strips? | call                                       | registers (e.g. `/view`) | served at            |
|---------------------------------------|----------------|--------------------------------------------|--------------------------|----------------------|
| root                                  | n/a            | `RegisterRoutes(root, "", "/heroes")`      | `/heroes/view`           | `/heroes/view`       |
| `r.Route("/admin", …)`                | **yes**        | `RegisterRoutes(r, "/admin", "/heroes")`   | `/heroes/view`           | `/admin/heroes/view` |
| `r.Mount("/admin", sub)`              | **yes**        | `RegisterRoutes(sub, "/admin", "/heroes")` | `/heroes/view`           | `/admin/heroes/view` |
| `r.Group(…)` at root (no strip)       | **no**         | `RegisterRoutes(g, "", "/admin/heroes")`   | `/admin/heroes/view`     | `/admin/heroes/view` |

The absolute links in rendered HTML are always right because they're built
from `mountBase + slug`, never reverse-engineered from the request. This
also retires the DESIGN.md footgun "don't mount behind `chi.Mount` /
`chi.Route`" — stripping mounts are first-class now because the component
is *told* its absolute base.

---

## 3. The component surface

Each component exposes two things; no formal interface is forced yet
(Stage 2's Admin may introduce a small one).

```go
// Render returns the component's content for embedding in the app's page.
// The app wraps it in its own chrome. Pure — no header writes, no chrome.
func (t *Table[T]) Render(r *http.Request) (templ.Component, error)

// RegisterRoutes mounts the in-component fragment routes; see §2.
func (t *Table[T]) RegisterRoutes(r chi.Router, mountBase, slug string)
```

Every fragment handler reduces to: compute content, write it as a
fragment. Mutating handlers add backend-driven directives via `gone/htmx`:

```go
func (t *Table[T]) deletePost(w http.ResponseWriter, r *http.Request) {
    id, _ := htmx /* parse */
    if err := t.data.Delete(r.Context(), id); err != nil { /* 500/404 */ }
    if htmx.IsRequest(r) {
        content, _ := t.listFragment(r)                         // refreshed rows
        htmx.Reply().Retarget("#"+t.listID).Reswap("innerHTML").Apply(w)
        site.Fragment(w, r, content)
        return
    }
    http.Redirect(w, r, t.base, http.StatusSeeOther)            // JS-off fallback
}
```

All `HX-*` strings now live in `gone/htmx`, not smeared across `table.go`
(the 45 touch-points collapse to a handful of named calls).

---

## 4. The list page vs the list fragment

Because the **app** owns the page route (`GET /admin/heroes`) and the
**component** owns fragments, the list-refresh fragment keeps its own URL
(`/heroes/view`, as today). We are *not* collapsing the two into one URL
this stage — that collapse only made sense when the library owned the page
route (which it no longer does). `Render(r)` returns the full table view
(table + its modal container) for the app to embed; `/view` returns just
the rows/footer for sort/search/paginate/delete swaps.

`PageModals()` becomes part of what `Render(r)` emits (or the app includes
once), so the app no longer has to remember it separately.

---

## 5. `Admin` for Stage 1

Kept, lightly. `Admin.Render(r)` still returns the sidebar + the active
table's content (the existing `AdminView`); the **app** wraps that in its
chrome. A convenience `Admin.RegisterRoutes(r, mountBase, shell)` *(decided:
keep the wrapper)* registers, for each child table: its fragment routes,
plus a `GET {mountBase}/{slug}` page handler that wraps `Admin.Render(r)`
in the supplied `shell` function, plus the `GET {mountBase}` index
redirect. So an Admin app stays a few lines:

```go
r.Route("/admin", func(r chi.Router) {
    admin.RegisterRoutes(r, "/admin", appShell)   // child fragments + pages + index
})
```

The richer "Admin builds a `site.Shell` with active-nav" idea is deferred
to Stage 2 (it needs the `Shell`/`NavItem` we dropped from §1). For now the
sidebar's active marking is computed the same way it is today, inside
`AdminView`, from the request path.

---

## 6. chi conversion mechanics (auth + crud, one atomic change)

`auth_gorm` / `auth_simple` / `auth_sso` wire **both** `auth.Route` and
`crud.Route` through one shared mux, and chi's `HandleFunc` doesn't parse
the `"GET /path"` method-prefix syntax `auth` uses — so the conversion is
all-or-nothing. Concretely:

- **Delete** `auth.Mux` and the `crud.Mux = auth.Mux` re-export. Route
  registrars take `chi.Router`.
- **Method registration:** `mux.HandleFunc("GET "+p, h)` → `r.Get(p, h)`,
  `"POST "+p` → `r.Post(p, h)`. Path params `{id}` are unchanged (chi uses
  the same syntax).
- **Path params:** `r.PathValue("id")` → `chi.URLParam(r, "id")`.
- **`PageShellFunc`** → `site.Shell` (same shape; auth keeps taking an
  app-supplied shell function for its login/account pages).
- **Authz:** replace the per-handler `authzGate(w, r, "create")` switch
  with chi group middleware mapping method→action to the right `Can*`.
- **HTTP verbs:** **keep POST** for mutations *(decided)* so forms work
  with JS off; revisit PUT/DELETE when the JSON API (TODO #3) lands.

`auth`'s routes are currently absolute-path based; converting it to the
`(mountBase, slug)` relative model is part of this change, mirroring crud.

---

## 7. Modal control: backend-driven *(decided)*

The server decides when a dialog opens or closes; the client just listens.
Today that's hand-written `HX-Trigger: {"openModal":"…"}`. We keep the
mechanism but move it behind `htmx.Reply().OpenModal(id)` /
`.CloseModal(id)`, and keep the small `gone.js` listener that maps those
events to `dialog.showModal()` / `.close()`. The per-slug L1 + shared L2
two-level model stays as-is for Stage 1; a generic push/pop modal stack is
a later polish.

---

## 8. End-state caller (routing half)

```go
func main() {
    db := openAndMigrate()
    r := chi.NewRouter()
    r.Use(sessionMW, csrfMW)                       // app owns the stack

    heroTable   := crud.DeriveGormCRUDTable[Hero](heroMM, nil, db)   // Stage-1 constructor unchanged
    heroTable.Slug = "heroes"
    // … weapons, skills …

    admin := crud.DeriveAdminAutoWire([]crud.CRUDTableInterface{&heroTable, &weaponTable, &skillTable}, nil)
    r.Route("/admin", func(r chi.Router) {
        admin.RegisterRoutes(r, "/admin", appShell)
    })

    // Or a single table on its own, anywhere:
    r.Route("/catalog", func(r chi.Router) {
        weaponTable.RegisterRoutes(r, "/catalog", "/weapons")        // fragments
        r.Get("/weapons", func(w http.ResponseWriter, req *http.Request) {
            c, _ := weaponTable.Render(req)
            appShell(w, req, "Weapons", c)
        })
    })

    http.ListenAndServe(":8080", r)
}
```

(The `crud.Table[T]{…}` config constructor and relations-by-URL are Stage
2; Stage 1 keeps the existing `DeriveGormCRUDTable` + `MustFindField`
configuration, only the *routing* changes.)

---

## 9. Sequencing within Stage 1

1. **`gone/htmx`** (header helpers + modal directives) + unit tests. Pure
   addition, green.
2. **`gone/site`** (`Shell` type, `Fragment`, `Respond`) + unit tests.
   Pure addition, green.
3. **chi conversion** of `crud` + `auth` + all examples, integrating
   `gone/htmx`/`gone/site`, in one atomic commit (it has to be — §6). Drop
   `Mux`/`PageShellFunc`; `RegisterRoutes(mountBase, slug)`; authz
   middleware; `Admin.RegisterRoutes` wrapper; app-owned page routes in
   examples. Build every example, run every test, then commit.

Themes A (config-up-front) and C (relations-by-URL) from REFACTOR.md
follow in Stage 2.

---

## 10. Decisions — locked

1. **`gone/htmx` in-house** (not a third-party dep). ✅
2. **Create `gone/site`** now, minimal — no `Shell` struct / `NavItem`
   yet; just `Shell` func type + `Fragment` + `Respond`. ✅
3. **No library page-shell/nav.** Apps render components into their own
   templ shells and own navigation + active-menu. ✅
4. **Nav active-match:** deferred (it's the app's job in Stage 1). ✅
5. **Keep POST mutations** + `hx-get`/`hx-post`; JS-off niceties discussed
   later. ✅
6. **`Admin.RegisterRoutes` convenience wrapper** kept (takes an app
   `shell` func). ✅
7. **Backend-driven modals** via `htmx.Reply().OpenModal/CloseModal`. ✅
