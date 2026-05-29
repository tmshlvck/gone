# gone/auth + gone/authz — PRD

> Design document for the security primitives in `gone`: sessions,
> CSRF, password / TOTP / OIDC login, user / group / role / permission
> model, and the authz interface that `gone/crud` (and future
> `gone/jsonapi`) gate on.
>
> Companion to [`PRD-CRUD.md`](PRD-CRUD.md). The CRUD package already
> defines an authz interface (`crud.AuthzInterface`) and treats `nil`
> as `AllowAll`; this PRD plans its move into a leaf-of-import-tree
> `authz/` package plus the rest of the security stack on top.
>
> Reference implementation for sessions + CSRF lives at
> [`examples/sessions`](examples/sessions/) — the actual middleware
> below is intended to look like that example, formalized into a
> reusable library.

## 1. Purpose

Give `gone` apps a batteries-included surface for:

- **Sessions** — cookie-based, server-stored. `alexedwards/scs/v2` is
  the de-facto choice; the library accepts a small interface so
  callers can swap stores.
- **CSRF** — token-in-session, validated as form field or
  `X-CSRF-Token` header (HTMX path). Read-only methods bypass.
- **Login** — username/password against argon2id hashes, optional
  TOTP second factor, optional OIDC federation.
- **User management** — render an account page where the user can
  change password / enable TOTP / disable account.
- **Authz** — `authz.Interface` used by `crud.CRUDTable` and friends
  to gate routes; plus RBAC helpers (`User` / `Group` / `Role` /
  `Permission` / `Grant`) that produce an `authz.Interface` per
  resource.

The library never owns page chrome — login and account pages render
the same `templ.Component` fragments that `gone/crud` does, wrapped
in the caller's `PageShellFunc`.

## 2. Goals

1. **Sessions and CSRF as middleware** — caller wraps their mux with
   our middleware, gets a session + CSRF token in `r.Context()`. No
   per-route plumbing.
2. **Pluggable login mechanisms** — password / TOTP / OIDC are
   independent. Don't pull `coreos/go-oidc` if you don't need OIDC;
   it lives in a subpackage with its own import path.
3. **Authz interface stays small** — `Can{List,Read,Create,Update,Delete}(r)`
   — five methods, all take `*http.Request`, all return `bool`.
   `crud/` already uses this shape.
4. **RBAC opt-in** — the User/Group/Role/Permission/Grant tables live
   in `authz/`. Apps that want simpler authz (e.g. "any logged-in
   user can do anything") use `authz.AllowAll{}` and skip the RBAC
   tables entirely.
5. **Compatible with the rest of `gone`** — CRUDTable's `Authz` field
   accepts `authz.Interface` (today `crud.AuthzInterface` — moves).
   Login / account pages plug into the existing `PageShellFunc`
   pattern.

## 3. Non-goals (this PRD)

- **API keys** — header / query-string auth for programmatic
  clients. Sketched in TODO; comes after the session story is solid.
- **JSON API content negotiation** — separate PRD (deferred).
- **Field-level audit logging** — deferred.
- **Multi-DB user federation** — out of scope; a single `User` table
  with optional external-auth links (`OIDCSubject`) is enough.

## 4. Stack

| Concern                | Choice                                                |
|------------------------|-------------------------------------------------------|
| Session middleware     | `alexedwards/scs/v2` in examples; no hard dep in lib  |
| Password hashing       | argon2id via `golang.org/x/crypto/argon2`             |
| TOTP                   | `pquerna/otp` (active 2025+)                          |
| OIDC                   | `coreos/go-oidc` + `golang.org/x/oauth2` (subpkg)     |
| CSRF                   | hand-rolled (see `examples/sessions`)                 |
| RBAC storage           | GORM tables; in-memory adapter for tests              |

The library exposes a small `SessionStore` interface so callers can
swap scs for any backend that satisfies it.

## 5. Package layout

```
gone/
├── authz/                        — pure: Interface, AllowAll, RBAC helpers
│   ├── authz.go                    Interface, AllowAll
│   ├── rbac.go                     Permission, Role, Grant, Can, NewRBAC
│   └── store.go                    storage abstraction (Gorm impl in subpkg)
├── auth/                         — sessions, CSRF, login, user mgmt
│   ├── session.go                  SessionStore interface + scs adapter
│   ├── csrf.go                     CSRF middleware + CSRFField / CSRFHeaders
│   ├── user.go                     User, Group, UserStore interface
│   ├── auth.go                     Auth struct + LoadUser / Require middleware
│   ├── login.go                    LoginPassword / Login / Logout
│   ├── route.go                    Auth.Route registering login/logout/account
│   ├── views.templ                 login form, account page (fragments)
│   ├── password/                   argon2id wrappers
│   ├── totp/                       TOTP secret gen + verify
│   └── oidc/                       optional OIDC subpackage
```

Rationale:

- **`authz/` is leaf-of-import-tree.** No dependencies on `auth/`.
  `crud/` and `auth/` and any future package can import `authz/`
  without cycles.
- **`auth/` central package** holds the cohesive set: session,
  CSRF, user store, login. Things that always go together.
- **`auth/{password,totp,oidc}/` subpackages** isolate optional deps.
  An app that doesn't enable TOTP shouldn't transitively pull
  `pquerna/otp`.
- **No `common/` package.** Anti-pattern in Go; shared bits live in
  the most natural cohesive home.

`crud/authz.go` (which currently holds `AuthzInterface` + `AllowAll`)
moves into `authz/`. `crud.CRUDTable.Authz`'s type becomes
`authz.Interface`. Callers passing `nil` need no code changes.

## 6. `authz` package

```go
package authz

// Interface is the gate every component (CRUDTable, JSONAPI, Admin
// pages) consults before touching data. Receives *http.Request so
// implementations can read user info from r.Context().
//
// nil = AllowAll, by convention enforced inside each consumer.
type Interface interface {
    CanList(r *http.Request) bool
    CanRead(r *http.Request) bool
    CanCreate(r *http.Request) bool
    CanUpdate(r *http.Request) bool
    CanDelete(r *http.Request) bool
}

type AllowAll struct{}

func (AllowAll) CanList(*http.Request) bool   { return true }
func (AllowAll) CanRead(*http.Request) bool   { return true }
func (AllowAll) CanCreate(*http.Request) bool { return true }
func (AllowAll) CanUpdate(*http.Request) bool { return true }
func (AllowAll) CanDelete(*http.Request) bool { return true }

// DenyAll is the symmetric helper — useful for read-only views and
// tests.
type DenyAll struct{}
// (all methods return false)
```

### 6.1 RBAC types

```go
// Permission codes are strings, namespaced by resource: "zone.list",
// "record.update", etc. Free-form — apps pick their conventions.
type Permission struct {
    ID   uint
    Code string
}

// Role bundles permissions. Apps usually seed a handful: "superadmin",
// "zone-admin", "user".
type Role struct {
    ID          uint
    Name        string
    Permissions []Permission `gorm:"many2many:role_permissions"`
}

// Grant binds a principal (user OR group) to a Role, optionally scoped
// to a resource. ResourceType is the Go type name; ResourceID is the
// stringified primary key. "" / "" = global grant.
type Grant struct {
    ID           uint
    UserID       *uint
    GroupID      *uint
    RoleID       uint
    ResourceType string
    ResourceID   string
    ExpiresAt    *time.Time
}
```

### 6.2 Permission resolution

```go
// Resolver answers "does this principal have this permission on this
// resource?" — by walking grants directly on the user, then grants on
// any group the user belongs to.
type Resolver interface {
    Can(ctx context.Context, userID uint, permCode, resourceType, resourceID string) bool
}

// GormResolver: production resolver backed by *gorm.DB.
func NewGormResolver(db *gorm.DB) Resolver

// MemoryResolver: in-memory map for tests.
func NewMemoryResolver(grants []Grant, roles []Role) Resolver
```

### 6.3 Building an `Interface` from RBAC

A CRUDTable for resource type `"Zone"` typically wants to gate:

- `CanList`   → `zone.list`
- `CanRead`   → `zone.read`
- `CanCreate` → `zone.create`
- `CanUpdate` → `zone.update`
- `CanDelete` → `zone.delete`

`authz` provides a helper that wires this:

```go
// NewRBAC produces an Interface that consults Resolver for permission
// codes prefixed with resourcePrefix. The codes are formed as
// "{resourcePrefix}.{action}" where action ∈ list/read/create/update/delete.
//
// userIDFromRequest extracts the current user's ID from the request
// (returns 0 for anonymous, in which case all checks return false).
// Apps typically wire this to read from r.Context() populated by
// auth.LoadUser middleware.
func NewRBAC(
    res Resolver,
    resourcePrefix string,
    userIDFromRequest func(*http.Request) uint,
) Interface
```

Per-resource scoping (e.g. "can edit zone #42 specifically") is a
future extension — the `Resolver.Can` shape already supports it via
`resourceID`, but the per-row gating on CRUDTable doesn't pass row IDs
through to `Authz.CanRead/Update/Delete` yet. Adding that is a
follow-up.

## 7. `auth` package

### 7.1 Session abstraction

```go
package auth

// SessionStore is the minimal surface auth needs. scs.SessionManager
// satisfies it. Library doesn't import scs directly.
type SessionStore interface {
    GetString(ctx context.Context, key string) string
    PutString(ctx context.Context, key, val string)
    Destroy(ctx context.Context) error
    RenewToken(ctx context.Context) error
    LoadAndSave(next http.Handler) http.Handler
}

// SCSAdapter wraps *scs.SessionManager — opt-in via the auth/scs subpkg
// (so the base package doesn't depend on scs).
//
//	import "github.com/tmshlvck/gone/auth/scsadapter"
//	sm := scs.New()
//	store := scsadapter.New(sm)
//	a := auth.NewAuth(store, ...)
```

### 7.2 Auth struct

```go
type Auth struct {
    Session     SessionStore
    Users       UserStore
    PassParams  password.Params  // argon2id parameters
    TOTPIssuer  string           // shown in TOTP QR codes
    LoginURL    string           // where Require redirects when unauth (default "/login")
    AfterLogin  string           // where to land after successful login (default "/")
}

func NewAuth(session SessionStore, users UserStore) *Auth
```

`Auth` is the central coordinator. Its methods produce middleware and
handle the login / logout / account flows.

### 7.3 User store

```go
type User struct {
    ID           uint
    Username     string
    Email        string
    PasswordHash string
    TOTPSecret   string    // encrypted-at-rest is the caller's concern; stored as-is by the lib
    OIDCSubject  string    // optional
    Disabled     bool
    CreatedAt    time.Time
    UpdatedAt    time.Time
}

type UserStore interface {
    FindByID(ctx context.Context, id uint) (*User, error)
    FindByUsername(ctx context.Context, username string) (*User, error)
    FindByOIDCSubject(ctx context.Context, sub string) (*User, error)
    Create(ctx context.Context, u *User) error
    Update(ctx context.Context, u *User) error
}

// GormUsers is the default backend. Memory store available for tests.
func NewGormUserStore(db *gorm.DB) UserStore
func NewMemoryUserStore() UserStore
```

### 7.4 Middleware

```go
// LoadUser injects the currently-logged-in *User into r.Context().
// Reads user_id from the session, looks up via UserStore, sets ctx.
// Anonymous request → no user in ctx; downstream handlers should
// treat that as anonymous, not as error.
func (a *Auth) LoadUser(next http.Handler) http.Handler

// Require gates anonymous requests: redirects to a.LoginURL with a
// "next" query param. For HTMX requests, returns HX-Redirect header
// instead of a 303.
func (a *Auth) Require(next http.Handler) http.Handler

// User pulls the *User from r.Context() (set by LoadUser). nil for
// anonymous.
func User(ctx context.Context) *User
```

### 7.5 CSRF middleware

Matches the reference implementation in `examples/sessions`. Token in
session, validated as `X-CSRF-Token` header or `csrf_token` form
field. GET / HEAD / OPTIONS bypass.

```go
// CSRF wraps the supplied handler. Ensures the session has a token,
// validates it on mutating methods, sends 403 on mismatch.
func (a *Auth) CSRF(next http.Handler) http.Handler

// CSRFToken returns the current session's CSRF token; the empty
// string for sessions without one (shouldn't happen if CSRF
// middleware ran).
func (a *Auth) CSRFToken(ctx context.Context) string

// Templ helpers — emit the hidden form field and the hx-headers JSON
// for HTMX-driven mutations.
func (a *Auth) CSRFField(ctx context.Context) templ.Component
func (a *Auth) CSRFHeaders(ctx context.Context) templ.Component
```

Token is **rotated on login** (session-fixation defense) and **cleared
on logout**. Anonymous CSRF works — the token is created on first
request.

### 7.6 Login mechanisms

```go
// VerifyPassword: look up user by username, verify argon2id hash.
// Returns (nil, ErrInvalidCredentials) for both unknown user and
// wrong password (no enumeration).
func (a *Auth) VerifyPassword(ctx context.Context, username, password string) (*User, error)

// VerifyTOTP: returns nil iff code matches user.TOTPSecret with
// allowed clock-skew window.
func (a *Auth) VerifyTOTP(user *User, code string) error

// Login: rotate the session, write the user_id, regenerate CSRF.
// Caller calls this after VerifyPassword (and VerifyTOTP, if enabled).
func (a *Auth) Login(ctx context.Context, u *User) error

// Logout: destroy the session.
func (a *Auth) Logout(ctx context.Context) error
```

OIDC login is symmetric but lives in `auth/oidc/` because of the
dependency it pulls.

### 7.7 Routes

```go
// Route mounts the login / logout / account pages at baseUrl. shell
// wraps each page in the app's chrome — the library never emits page
// chrome.
//
// Registered:
//   GET    {baseUrl}/login             render login form
//   POST   {baseUrl}/login             verify + Login + redirect
//   POST   {baseUrl}/logout            Logout + redirect to LoginURL
//   GET    {baseUrl}/account           render account page (change pw, TOTP setup)
//   POST   {baseUrl}/account/password  submit password change
//   POST   {baseUrl}/account/totp      enable / disable TOTP
//
// shell == nil is allowed for tests / fragment-only callers.
//
// Returns the absolute urlBase the auth pages were mounted at.
func (a *Auth) Route(mux crud.Mux, baseUrl string, shell crud.PageShellFunc) (string, error)
```

Reusing `crud.Mux` and `crud.PageShellFunc` keeps the security stack
consistent with the rest of `gone`. Cross-package import (`auth/`
→ `crud/`) is one direction; `crud` doesn't import `auth`.

## 8. `auth/password` subpackage

```go
package password

// Params controls argon2id cost. Tuned for ~100ms on commodity hardware
// in 2026 (subject to revisit).
type Params struct {
    Time    uint32
    Memory  uint32
    Threads uint8
    SaltLen uint32
    KeyLen  uint32
}

func DefaultParams() Params

// Hash produces a PHC-encoded string: "$argon2id$v=19$m=...,t=...,p=...$salt$hash"
func Hash(password string, p Params) (string, error)

// Verify is constant-time. Returns nil for a match, ErrMismatch otherwise.
func Verify(password, encodedHash string) error
```

## 9. `auth/totp` subpackage

```go
package totp

// Secret holds the otpauth URL and the base32 secret. The URL is
// what you render as a QR code on the enrollment page.
type Secret struct {
    URL    string
    Base32 string
}

// NewSecret generates a fresh secret for user@issuer.
func NewSecret(issuer, accountName string) (Secret, error)

// Verify checks `code` against `base32Secret` with the default RFC 6238
// window (±1 step).
func Verify(base32Secret, code string) bool
```

## 10. `auth/oidc` subpackage (deferred)

Sketch only:

```go
package oidc

type Provider struct {
    Issuer       string
    ClientID     string
    ClientSecret string
    RedirectURL  string
}

func (p *Provider) Route(mux crud.Mux, baseUrl string, auth *auth.Auth) error
// Registers GET /login/oidc → 302 to provider, GET /login/oidc/callback → exchange + auth.Login.
```

Implementation deferred — gets its own milestone.

## 11. Login + account flow

Reference experience (login):

1. Anonymous request → `Require` middleware → 303 to `/login?next=/admin`.
2. GET `/login` → renders `loginPage` templ (CSRF token, optional error, optional TOTP prompt if user enabled it).
3. POST `/login` → `VerifyPassword` → `VerifyTOTP` (if enabled) → `Login` → 303 to `next` or `AfterLogin`.
4. Session cookie set; CSRF token rotated; `LoadUser` middleware picks up `*User` on subsequent requests.

Account page (`/account`):

- Change password (current + new + confirm).
- Enable TOTP: server generates a Secret, renders QR + asks for a confirmation code; on success writes `user.TOTPSecret` and updates.
- Disable TOTP: clears `user.TOTPSecret` (with password re-prompt).

Each page is a `templ.Component` returned by `auth.Render*`; `Route`
wraps in the caller's `PageShellFunc`. Same pattern as
`CRUDTable.Render` + `PageShellFunc`.

## 12. CSRF flow

Identical to `examples/sessions`, codified:

1. Session middleware (`scs.LoadAndSave`) runs first.
2. `Auth.CSRF` middleware:
   - Ensures the session has a `csrf_token` (creates one if absent).
   - On GET / HEAD / OPTIONS: pass through.
   - On other methods: check `X-CSRF-Token` header, fall back to
     `csrf_token` form field. Constant-time compare. 403 on mismatch.
3. Templ pages render `<input type="hidden" name="csrf_token" value="...">`
   via `Auth.CSRFField(ctx)`.
4. HTMX swap forms can also use `hx-headers='{"X-CSRF-Token":"..."}'`
   via `Auth.CSRFHeaders(ctx)`.

API-key authenticated requests (future) bypass CSRF — header auth, no
session, no XSRF surface.

## 13. Integration with `gone/crud`

- `crud.CRUDTable.Authz` accepts `authz.Interface`. Today the type
  lives in `crud/`; the move to `authz/` is a non-breaking rename
  (callers pass `nil` and that still means AllowAll).
- An app with RBAC writes:
  ```go
  zoneAuthz := authz.NewRBAC(resolver, "zone", auth.UserIDFromRequest)
  zoneTable := crud.DeriveGormCRUDTable[Zone](zoneMM, zoneAuthz, db)
  ```
  Now every CRUD endpoint on `/zones/*` consults RBAC permissions
  `zone.list` / `zone.read` / `zone.create` / `zone.update` /
  `zone.delete`, scoped to the logged-in user.
- Login is mounted next to admin:
  ```go
  a := auth.NewAuth(session, users)
  a.Route(mux, "/", pageShell)            // /login, /logout, /account
  protected := chi.Chain(a.LoadUser, a.CSRF, a.Require)
  // wrap admin/CRUD routes with `protected`.
  ```

## 14. Schema sketch

```go
// auth package
type User struct {
    ID           uint
    Username     string `gorm:"uniqueIndex"`
    Email        string `gorm:"uniqueIndex"`
    PasswordHash string
    TOTPSecret   string
    OIDCSubject  string `gorm:"index"`
    Disabled     bool
    CreatedAt    time.Time
    UpdatedAt    time.Time
}

type Group struct {
    ID    uint
    Name  string `gorm:"uniqueIndex"`
    Users []User `gorm:"many2many:user_groups"`
}

// authz package
type Permission struct {
    ID   uint
    Code string `gorm:"uniqueIndex"`
}

type Role struct {
    ID          uint
    Name        string       `gorm:"uniqueIndex"`
    Permissions []Permission `gorm:"many2many:role_permissions"`
}

type Grant struct {
    ID           uint
    UserID       *uint  `gorm:"index"`
    GroupID      *uint  `gorm:"index"`
    RoleID       uint
    ResourceType string `gorm:"index"`
    ResourceID   string `gorm:"index"`
    ExpiresAt    *time.Time
}
```

Seeding a basic role / permission set is the app's concern; the
library provides `authz.NewGormResolver(db)` and the migration helpers
do the rest.

## 15. Testing

- **CSRF**: GET passes; POST without token → 403; POST with valid form
  token → pass; POST with valid header → pass; POST with mismatched
  token → 403; HTMX path mirrors form path.
- **Password**: `Hash` then `Verify` round-trips; wrong password →
  ErrMismatch; corrupted hash → error.
- **TOTP**: `NewSecret` produces a valid otpauth URL; `Verify` accepts
  current step's code from a known reference clock; rejects code from
  too-far-in-the-past.
- **Session**: `Login` rotates token; `Logout` destroys it; CSRF
  token regenerates after login.
- **Authz Resolver**: user with role grant → `Can` returns true for
  role's permissions; group grant via user-in-group → also true;
  expired grant → false.
- **`auth.Route` end-to-end**: login form renders; POST with right
  credentials → 303 to AfterLogin; wrong credentials → re-render
  with error; `Require` redirects anonymous → `/login?next=...`;
  HTMX request → `HX-Redirect` header.

All tests use the memory `UserStore` / memory `Resolver` — no DB
needed for unit tests; GORM-backed flavor exercised through one
integration test per backend.

## 16. Examples (planned)

| Path                          | Demonstrates                                                  |
|-------------------------------|---------------------------------------------------------------|
| `examples/sessions`           | *reference today.* Hand-rolled CSRF + scs session + simple login. Pre-library impl. |
| `examples/auth_basic`         | `auth.Auth` with username/password against a memory user store. Login + protected page. |
| `examples/auth_totp`          | TOTP enrollment + login with second factor.                   |
| `examples/admin_with_rbac`    | CRUDTable + Admin gated by `authz.NewRBAC`. User/Group/Role/Permission CRUDTables themselves. |

## 17. Open questions

- **Token storage**: scs stores serialized session blobs in (cookie /
  memory / Redis / DB). Should auth `Login` write `user_id` as
  `string` (current scs pattern) or `int64`? scs's typed accessors
  prefer one — pick later.
- **Per-row authz on CRUDTable**: today's `Authz.Can{Read,Update,Delete}(r)`
  doesn't see the row ID. For per-resource grants we'd want
  `CanReadRow(r, id uint)` etc. — schema change to `authz.Interface`.
  Worth doing in the same milestone as authz/, or defer? Lean toward
  doing it now: small breaking change, easier than retrofitting.
- **Password reset / email verification**: deferred or in scope?
  Lean toward deferred — needs an email-sending abstraction the
  library doesn't have yet.
- **Cost of argon2id** vs. constraints (laptops, low-end VPS):
  defaults need real benchmarking on commodity hardware before we
  publish them.
- **Login form chrome**: should the library ship a default opinionated
  login templ (DaisyUI), or only the form `<input>`s with the caller
  wrapping them? Current preference: ship the form + the caller can
  override the templ if desired. Matches CRUDTable's pattern.
