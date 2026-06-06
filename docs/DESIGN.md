# gone — design notes

The **why** behind the library. For the **what / how** — API surface,
usage, examples — see [`CRUD.md`](CRUD.md) and
[`AUTH.md`](AUTH.md); this file doesn't repeat them. The
full design-document history (the original per-package PRDs, with
their v1/v2 staging and superseded sketches) lives in git history if
you need the archaeology.

## Philosophy (shared by both packages)

- **Library, not framework.** Every component returns a
  `templ.Component` and registers on whatever router the caller
  already has, as long as it satisfies the small `crud.Mux`
  interface (a subset of `*http.ServeMux`). We never own `main`, the
  router, or the middleware stack.
- **Fragments, not pages.** The library emits HTML fragments — no
  `<html>`/`<body>`/`<style>` chrome. The caller supplies a
  `PageShellFunc` that wraps fragments in the page shell (head,
  theme, scripts, nav). This is what lets one set of components serve
  full-page loads and HTMX swaps from the same handler.
- **Describe once, derive, then override.** A model is described one
  time (`MetaModel[T]`); reflection + struct tags fill in sensible
  defaults; the caller post-mutates the derived model to override any
  specific field. No code generation, no annotations beyond the gorm
  tags already present.
- **Safe HTML by default.** templ escapes every interpolated value;
  `templ.Raw` is the explicit escape hatch.
- **Absolute URLs in rendered HTML.** Components render absolute
  paths (`hx-get`, form `action`, …). A component must therefore
  know its full external URL — there's no prefix-stripping layer.
  Consequence: don't mount behind `http.StripPrefix` / `chi.Mount` /
  `chi.Route` (they hide the prefix from handlers, so rendered URLs
  would omit it). For middleware layering on chi, use `chi.Group`,
  which preserves the absolute path.

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
- **Closures are the data plane.** `CRUDTable` holds
  `Get`/`List`/`Create`/`Update`/`Delete` closures, populated by a
  backend-specific `Derive*CRUDTable` constructor. GORM and an
  in-memory map are first-class; a new backend is just a new
  constructor — the rendering/validation/routing code is backend-
  blind.
- **Admin owns its children.** `Admin.Route` calls each child table's
  `Route(mux, urlBase, nil)` internally (shell `nil`, so children
  don't register their own page handler). The caller lists tables and
  calls `Admin.Route` once — it never calls `table.Route` separately.
  `DeriveAdminAutoWire` additionally fills cross-table relation
  pointers by matching Go type names, so relation pickers populate
  with no manual wiring.
- **Sidebar swaps are scoped.** Admin's sidebar uses `hx-boost`
  targeting `#crud-admin-root` (the admin subtree), so Admin can be
  embedded inside a larger layout without the swap clobbering
  surrounding chrome. User-defined sidebar links instead target the
  working area (`#crud-admin-main`) so they can host arbitrary content
  fragments.
- **`AuthzInterface` takes `*http.Request`.** Five methods
  (`Can{List,Read,Create,Update,Delete}`), all `(r) bool`. Taking the
  request keeps the gate router-agnostic; `nil` means AllowAll. This
  is the same shape `gone/auth` implements, so the two packages
  compose without an adapter.

**Out of scope for crud (lives in auth or TODO):** authentication,
CSRF, RBAC beyond the Authz gate, JSON API, audit logging, GraphQL,
background jobs.

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
negotiation (separate concerns, see [`TODO.md`](TODO.md)), field-level
audit logging.

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
