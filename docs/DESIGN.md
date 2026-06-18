# gone — design notes

The **why** behind the library. For the **what / how** — API surface,
usage, examples — see [`CRUD.md`](CRUD.md) and
[`AUTH.md`](AUTH.md); this file doesn't repeat them. The
full design-document history (the original per-package PRDs, with
their v1/v2 staging and superseded sketches) lives in git history if
you need the archaeology.

## Philosophy (shared by both packages)

- **Library, not framework.** Every component returns a
  `templ.Component` and registers its routes on the caller's
  `chi.Router`. We never own `main`, the router, or the middleware
  stack. (chi is a hard dependency: the components use its method
  routing, route groups for authz, and relative mounting. The earlier
  "any `*http.ServeMux`" promise was theatre — the only composition
  trick the two routers differ on is exactly the one we rely on.)
- **Fragments, not pages — the app owns the page.** The library emits
  HTML fragments — no `<html>`/`<body>`/`<style>` chrome — for its
  in-component interactions. The application owns the page *routes*: it
  renders a component's `Render(r)` output inside its own page shell
  (head, theme, scripts, nav). The shared shape for that chrome is
  `gone/site.Shell`; the wire-level HTMX plumbing (request
  classification, HX-\* response directives, modal control) lives in
  `gone/htmx`. Splitting these out keeps an app free to reuse them on
  its own non-CRUD pages.
- **Describe once, derive, merge.** A model is described one time as a
  `MetaModel[T]` preset; `DeriveMetaModel` reflects `T` + gorm tags for
  sensible defaults, then overlays the preset's per-field overrides and
  validates field names (a typo panics at startup — the
  `regexp.MustCompile` idiom). No code generation, no annotations beyond
  the gorm tags. `FindField` returns a mutable pointer for the rare
  post-construction tweak.
- **Safe HTML by default.** templ escapes every interpolated value;
  `templ.Raw` is the explicit escape hatch.
- **Real multi-page navigation; HTMX only where it earns its keep.**
  Page-to-page navigation is ordinary `<a href>` links (Admin's sidebar
  included) — every navigation is a full page load. Only in-component
  interactions — sort, search, paginate, modal create/edit, delete —
  use targeted HTMX swaps. We don't use `hx-boost`: per htmx's own
  ["some people don't like
  hx-boost"](https://htmx.org/quirks/#some-people-don-t-like-hx-boost),
  it buys little over a real MPA in modern browsers.
- **Absolute URLs, but composable.** Components render absolute paths
  (`hx-get`, form `action`, …), so a component must know its full
  external URL. Rather than infer it, it is *told*: `RegisterRoutes(r,
  routerPrefix, componentPath)` registers routes at `componentPath`
  relative to `r` and records `routerPrefix` (the absolute prefix where
  `r` is served) for link generation. `componentPath` can be
  multi-segment (`/admin/heroes`), so a table mounts on the root mux
  without a stripping `chi.Route` — though stripping mounts stay
  first-class, since the hidden prefix is just supplied as
  `routerPrefix`.

## gone/crud

Renders HTMX-driven CRUD UIs from a model's metadata: one
`MetaModel[T]` drives a table, a form, and a detail dump of the same
entity, plus an `Admin` that aggregates many tables under one URL
prefix.

**Key decisions:**

- **`MetaModel[T]` is the single source.** Table/form/dump are three
  renderings of the same metadata, so field labels, sortability,
  validation, and relation shape are described once. Reflection +
  gorm tags infer belongs-to / has-many / many-to-many.
- **`Accessor[T]` is the data plane.** `CRUDTable` holds one
  `Accessor[T]` interface value — `Get`/`List`/`Create`/`Update`/
  `Delete` — built by a backend constructor (`GORMAccessor` /
  `MapAccessor`) from the same `MetaModel`. `NewTable(mm, accessor,
  pageSize, authz)` pairs the two. GORM and an in-memory map are
  first-class; a new backend is just a new `Accessor` implementation —
  the rendering/validation/routing code is backend-blind. Construction
  says *what* (metadata + data + behaviour); `RegisterRoutes` says
  *where* (the path), so a table is built once and mountable anywhere.
- **Admin registers its children; the app owns pages.** A `CRUDTable`'s
  `RegisterRoutes` mounts only its fragment endpoints; the app writes
  the page route and embeds `Render(r)` in its shell.
  `Admin.RegisterRoutes(r, routerPrefix, componentPath, shell)` composes
  every child path on one router — child fragments under
  `componentPath/{slug}`, a per-slug page handler wrapping the active
  table in the app's `site.Shell`, and an index redirect — so an Admin
  app stays a few lines and needs no stripping `chi.Route`. The caller
  lists tables once; it never calls a child's `RegisterRoutes`
  separately.
- **Relations link by URL, not by a pointer.** A relation `<select>`
  loads its options over HTTP from the *related* table's own `/options`
  endpoint (fired on `load`, refreshed on `refresh-relation`); the
  related table generates the id→label pairs because it already owns
  the data. So a `MetaField` carries the related table's URL
  (`RelatedURLBase`, a string), not a `CRUDTableInterface` pointer.
  `WireRelations` stamps those URLs after routing by matching Go type
  names; `Admin` calls it automatically. This decouples tables (no
  in-process graph), collapsed `CRUDTableInterface` from 11 methods to
  7, and means the HTML and a future JSON API can share one data path.
- **Sidebar navigation is plain links.** Each Admin sidebar entry is an
  `<a href>` to `/{base}/{slug}`; clicking it is a full page load, and
  the server marks the active entry from the request path (no JS, no
  swap, for the highlight). User-defined sidebar links are the one
  exception — they `hx-get` a fragment into the working area
  (`#crud-admin-main`) so they can host arbitrary app content without a
  full reload.
- **`auth.Authz` takes `*http.Request`.** Five methods
  (`Can{List,Read,Create,Update,Delete}`), all `(r) bool`. Taking the
  request keeps the gate router-agnostic; `nil` means AllowAll;
  `auth.AuthzAllowAll{}` is the explicit no-op. It's the same interface
  `gone/auth` implements, so the two packages compose without an
  adapter — each `crud` fragment handler gates on the table's `Authz`
  (per the action it serves) before running.
- **Mutation/read observation is a decorator over `Accessor`, not a
  `CRUDTable` field or middleware.** `ObserveAccessor` wraps any
  `Accessor[T]` and fires a callback after each successful op. The
  decorator was chosen over the two alternatives because the `Accessor`
  is the **single chokepoint** every write funnels through: the
  create/edit/delete handlers *and* CSV import all call `Data.*`. A
  hook field on `CRUDTable` would have to be fired from 4+ handler sites
  and would silently miss the import path; HTTP middleware can't see a
  mutation that a custom `Accessor` performs outside a request. Wrapping
  the data plane catches every path with zero changes to `CRUDTable` or
  the backends, and composes (audit + notify by stacking wraps).
  - **The callback is auth-agnostic; the app resolves identity.** It
    receives the `ctx` the handler already passed to `Data` (which is
    `r.Context()`), so the app's closure calls
    `auth.CurrentUsername(ctx)` itself. `crud` therefore never
    imports `auth` for auditing — the dependency stays one-directional
    (apps wire the two together), matching how `Authz` composes.
  - **Synchronous, after-commit, must-not-block.** The callback runs in
    the request goroutine once the write has returned, so a failed op
    fires nothing and the canonical sink is a non-blocking send to a
    buffered channel. Keeping it synchronous (vs. an internal queue)
    leaves the back-pressure policy — drop, block, or batch — to the app.
  - **Reads are opt-in (`ObserveReads`).** `List` fires on every render,
    search keystroke, and sort, so read events are far higher volume
    than writes; auditing them is a separate, deliberate choice rather
    than the default.

**Out of scope for crud (lives in auth or the app):** authentication,
CSRF, RBAC beyond the Authz gate, JSON API, GraphQL, background jobs.
Audit logging is now *enabled* by the `ObserveAccessor` hook, but the
sink and policy (what to record, where, retention) stay the app's —
`crud` provides the chokepoint, not the log.

## gone/auth

Sessions, CSRF, multi-method login, and the Authz gate. Two `Auth`
implementations behind one interface: `AuthSimple` (in-memory,
username/password only) and `AuthGORM` (GORM-backed, with TOTP,
passkeys, and SSO).

**Key decisions:**

- **scs is a hard dependency, no wrapper.** The early plan for a
  small `SessionStore` interface "so callers can swap stores" was
  dropped — scs already abstracts its own backends, so a second
  abstraction buys nothing. Sessions + CSRF are middleware: wrap the
  mux, get a session + CSRF token in `r.Context()`, no per-route
  plumbing.
- **One flat package.** Authz and CSRF both live in `gone/auth/`
  rather than separate packages. Authz's stock impls already depend
  on `Auth`/`User` from this package, and each piece is small (~100
  LOC) — the same threshold that keeps the CSRF helpers here too.
- **Each Auth impl owns its templates.** The plain `AuthSimple` login
  form and the `AuthGORM` form (password + passkey + SSO buttons)
  don't share enough structure to justify a shared template layer.
  `loginFormData` is shared; the rendering paths diverge per impl.
- **Groups are first-class; richer roles are the app's job.** `User`
  carries `Groups []Group` (N:M) — enough for "admin writes, everyone
  reads" via the stock `AuthzLoggedInReadAdminWrite`. Per-resource
  ownership, role hierarchies, and permission sets are the app's
  design space: implement `auth.Authz` directly.
- **Login finalizes through one staged path.** Password, passkey, and
  SSO callbacks all converge on `loginStage1`, which detours through
  `/login/totp` when the user has TOTP enrolled. Strong first factors
  (passkey, SSO) still respect a TOTP second factor if one is set,
  without each login method re-implementing the staging.
- **Identity getters take `ctx`, not `*http.Request`; no "load user"
  middleware.** Both `CurrentUser` and `CurrentUsername` take a
  `context.Context`. The session payload already rides in `r.Context()`
  (scs caches it there), so neither getter needs the request — page
  handlers just call `CurrentUser(r.Context())`, and code that only has
  a ctx (a crud audit hook is the motivating case) can call them too.
  `CurrentUser` originally took `*http.Request`; it was narrowed to
  `ctx` once it provably used nothing else, which also made it usable
  from the `Accessor` data plane. The split into two getters is by cost:
  `CurrentUser` materializes the full `User` (a DB lookup for AuthGORM);
  `CurrentUsername` is the plain session-string read for callers that
  only want a *label* (audit lines), with no lookup. We added the label
  getter rather than a middleware that stuffs the materialized `User`
  into the context — a middleware would contradict the deliberate "no
  LoadUser middleware, `CurrentUser` looks up on demand" stance, and full
  materialization is unnecessary when the caller only wants a name.
  `CurrentUser` is implemented on top of `CurrentUsername`, so the
  session-key read lives in exactly one place per impl.
  - The one case that genuinely wants the request — authenticating from
    a header (bearer token / API key) rather than the session cookie — is
    handled the standard way: a middleware reads the header and writes
    the resolved user into the ctx, after which `CurrentUser(ctx)` finds
    it. See [`HOWTO-BEARER-TOKENS.md`](HOWTO-BEARER-TOKENS.md). So
    ctx-only is *more* composable than `*http.Request`, not less.

### SSO design

GitHub doesn't speak OIDC, so SSO ships **two** provider types behind
one internal `ssoProvider` interface:

- `OIDCProvider` — discovery + ID-token + nonce verification (Google,
  Okta, Keycloak, Authentik, Dex, …).
- `OAuth2Provider` — caller supplies a `UserInfo` fetch for non-OIDC
  IdPs (GitHub today).

Both run authorization-code + PKCE; state/nonce/PKCE-verifier live in
the session across the redirect. Mapping a callback identity to a
local user (full policy in [`AUTH.md`](AUTH.md)):

1. existing `(provider, subject)` link → log in;
2. `AutoLinkByEmail` + matching local email → link + log in;
3. else auto-create (unless `DisableAutoCreate`) → log in;
4. else `ErrSSONoAccount` (403).

- **`AutoLinkByEmail` defaults off.** Linking an SSO identity to an
  existing local account by email trusts the IdP to have verified
  that email. True for a corporate Okta / on-prem Keycloak; *not*
  safe for arbitrary public IdPs (an IdP that doesn't verify email is
  an account-takeover vector). So it's per-provider and off by
  default.
- **Auto-created users get `Username = Email`.** Full address, so two
  providers returning different emails never collide; a pre-existing
  local user with the same email blocks auto-create (UNIQUE
  constraint) rather than being silently adopted.
- **`SSOOnly` flag.** Auto-provisioned users can't enrol a local
  password or passkey (those account-page surfaces 403) — the SSO
  identity is their credential. TOTP stays available (it layers on any
  method). An admin clears the flag to grant local-credential access.
- **One user → many identities.** `SSOIdentityGORM` is a link table
  unique on `(provider, subject)`, not a column on `UserGORM` — so a
  person can link corporate Okta + personal Google to one account.
  (An earlier draft had a single `OIDCSubject` column; replaced for
  exactly this reason.)

**Deferred / out of scope for auth:** password reset + email
verification (needs an email abstraction the library doesn't have),
single sign-out / SLO (destroying the local session is enough — we
don't propagate logout to the IdP), API keys and JSON-API content
negotiation (separate concerns, see [`TODO.md`](TODO.md)). Field-level
audit logging (a before/after diff per column) is also not built; the
row-level `crud` `ObserveAccessor` hook plus `CurrentUsername` is
the building block an app composes audit logging from today.

## Open questions / future work

A decision log — resolved choices worth remembering, and the ones
still open. Implementation-track items live in [`TODO.md`](TODO.md).

- **Self-service / admin-side SSO linking.** Today SSO identities
  arrive only via first login. A logged-in user can't yet attach a
  new provider to their existing account, and an admin can't move
  links between users. The account page already has the
  Unlink half; the missing piece is a "link, don't create" round-trip
  (the `/login/sso/{name}` flow with a session flag + an account-page
  `next`). Tracked in TODO.
- **Passkey backup-eligibility nudge.** WebAuthn exposes whether a
  credential is device-bound or synced (`BackupState`). A user with a
  single non-synced passkey is one lost device away from lockout and
  should be nudged to enrol a second. Not built.
- **Passkey naming on enrolment.** Currently the user names the
  passkey by hand. Auto-suggesting from AAGUID would be smoother but
  is wrong for the ~15% of authenticators with unrecognised AAGUIDs,
  and User-Agent sniffing is unreliable. If revisited: auto-suggest
  as a hint, always let the user override.
- **`AdminGroup` name is hardcoded `"admin"`.** Django convention;
  fine until an app needs a different privileged-group name, at which
  point make it configurable on `AuthGORM`.
- **Open-redirect on `next` — resolved.** `safeNext` rejects absolute
  URLs and `//host` paths, so `?next=` only ever redirects to a
  same-origin path. Kept here as a reminder that the guard is
  load-bearing.
- **Session payload is gob.** scs's default. JSON would be more
  debuggable but needs a custom store key; gob is fine until there's
  a concrete reason to inspect session blobs.
- **API keys / JSON API / CSV.** Bearer-token programmatic access,
  a JSON API derived from the same `MetaModel`, and CSV import/export
  on `CRUDTable` are the active build queue — fully sketched in
  [`TODO.md`](TODO.md).

### Parked — engineering, revisit on real need

Not decisions so much as work we've consciously not done. Cheap to
list, expensive to forget:

- **Per-row authz.** `Authz.Can*(r)` doesn't see the row ID — decided:
  per-row visibility is the app's space (filter at the data layer /
  implement `auth.Authz` directly), not a core-interface concern.
- **Mock authenticator for passkey unit tests.** The WebAuthn ceremony
  is exercised live in `examples/auth_gorm`; a CBOR+ECDSA mock would
  let the unit tests cover the full round-trip.
- **Plural slug derivation.** Defaults to `ToLower(Name)+"s"`, wrong
  for irregular plurals (Hero→heros, Person→persons). A `Pluralize`
  tag or small dictionary would fix it.
- **Observability defaults.** `log/slog` structured logs, Prometheus
  metrics, request IDs.
- **Proxy support.** Trust list for `X-Forwarded-*`; optional
  PROXY-protocol listener.
