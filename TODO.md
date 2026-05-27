# gone — TODO

Sections that were sketched in the early PRD but are not built yet.
Pulled out of `PRD.md` so the PRD describes what `gone` **is** today, and
this file collects what it intends to become.

## Security

### Escaping

`templ` escapes all interpolated values by default. Raw HTML requires
`templ.Raw(string)`. Per-field display overrides in `MetaField.DisplayValue`
may return either an escaped text component or a `templ.Component` the
override already trusted as safe.

This part is in place — listed here only because the rest of §Security
isn't, and it logically belongs with them.

### CSRF

A single CSRF token in the session, validated on every mutating route
(POST / PUT / DELETE / PATCH) against either:

- form field `csrf_token`, or
- header `X-CSRF-Token` (HTMX path).

Token is **rotated on login** (session-fixation defense) and **cleared
on logout**. Anonymous CSRF works (token created on first form render).
Read-only routes (GET / HEAD / OPTIONS) bypass CSRF.

The library would emit:

- `auth.CSRFField(ctx)` → `<input type="hidden" name="csrf_token" …>`
- `auth.CSRFHeaders(ctx)` → JSON for HTMX `hx-headers=` on delete buttons

API-key requests bypass CSRF (header-only auth, no session involved).

### Passwords

Argon2id by default (pure Go, no cgo). Bcrypt accepted as legacy. Cost
parameters configurable with sensible defaults.

### API keys

Selected endpoints accept API keys, authenticated via either:

- `Authorization: Bearer <key>` header, or
- query string for short polls where the route policy allows it.

Keys are hashed at rest. API-key requests bypass CSRF but still pass
authorization. Effective principal = owning user; all
`read_authz` / `write_authz` decisions use that user.

## Auth surface

```go
type Auth struct {
    UserLoader   func(ctx context.Context, r *http.Request) (*User, error)
    CSRFField    func(ctx context.Context) templ.Component
    CSRFHeaders  func(ctx context.Context) templ.Component
    RequireUser  http.Handler   // 401 → redirect to login (or HX-Redirect)
    OptionalUser http.Handler   // injects *User|nil into context
}
```

Pluggable login mechanisms:

- `login.Password` — username/password against `User` (argon2id).
- `login.TOTP` — second factor on top of `login.Password`.
- `login.OIDC` — federated login; creates / links `User` rows on first
  sign-in via the `sub` claim.

All three call back into `Auth.Login(ctx, *User)` to write the session
cookie. Session storage via `scs` (or an alternative). The session
opaquely holds `{user_id, login_at, csrf}`.

## JSON API

A `JSONAPI` component would wrap the same `MetaModel` (and the same
data closures the `CRUDTable` uses) and expose:

- `GET    {base}` — list (with `?search`, `?sort_by`, `?offset`, `?limit`)
- `GET    {base}/{id}` — one entity with top-level relations preloaded
- `POST   {base}` — create
- `PUT    {base}/{id}` — update
- `DELETE {base}/{id}` — delete
- `GET    {base}/openapi.json` — spec generated from the `MetaModel` (the
  patterns are prototyped in `openapi/openapi.go`)

### HTML + JSON coexistence

A mounted `CRUDTable` would additionally surface a `JSONAPI` for the
same metadata. Auth rules:

- **Authentication**: session cookie, API key, or anonymous (depending
  on route policy).
- **Authorization**: same `read_authz` / `write_authz` callbacks. API
  keys resolve to their owning user before authz runs.
- **CSRF**: skipped for header-authenticated requests, enforced for
  session-cookie requests (defense against XSRF from a browser session
  abusing the JSON endpoint).
- **Content negotiation**: by default JSON endpoints live at a separate
  path (`/heroes` HTML, `/api/heroes` JSON) rather than negotiating one URL.

## Authorization model

A reusable RBAC + per-resource ACL model. The DNS-zone walk-through is
the reference scenario:

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

### Schema sketch

```go
type User struct {
    ID           uint
    Username     string
    Email        string
    PasswordHash string
    TOTPSecret   string  // optional, encrypted at rest
    OIDCSubject  string  // optional, federated logins
    Disabled     bool
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
    ID   uint
    Code string                       // "zone.create", "record.update", …
}

// Grant binds a principal (user OR group) to a Role, optionally scoped
// to a resource. ResourceType is the Go type name; ResourceID is the
// stringified primary key.
type Grant struct {
    ID           uint
    UserID       *uint
    GroupID      *uint
    RoleID       uint
    ResourceType string             // "" for global, else e.g. "Zone"
    ResourceID   string             // "" for global, else "42"
    ExpiresAt    *time.Time
}

type APIKey struct {
    ID         uint
    UserID     uint                 // inherits this user's grants
    HashedKey  string               // hash of the raw key
    Name       string               // user-supplied label
    LastUsedAt *time.Time
    ExpiresAt  *time.Time
    Disabled   bool
}
```

## Other deferred items

- **Observability defaults**: structured logs via `log/slog`, Prometheus
  metrics, request IDs.
- **Proxy support**: trust list for `X-Forwarded-*` headers; optional
  PROXY-protocol listener.
- **Plural slug derivation**: today defaults to `strings.ToLower(Name) + "s"`,
  which is wrong for irregular plurals (Hero→heros, Person→persons,
  Sheep→sheeps). A `Pluralize` tag or a small dictionary would help.
- **Field-level audit logging**: opt-in via GORM hooks per model.
- **Multi-DB user federation**: out of scope; the single `User` table
  with optional external-auth links is enough.
