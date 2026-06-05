# gone/auth — sessions, login, passkeys, authorization

User-facing reference for `github.com/tmshlvck/gone/auth`. Design
rationale is in [`../PRD-AUTH.md`](../PRD-AUTH.md). For the parallel
CRUD reference see [`CRUD.md`](CRUD.md).

## What it does

- **Sessions + CSRF** — middleware built on `alexedwards/scs/v2`.
- **Two `Auth` implementations**:
  - **`AuthSimple`** — in-memory users, argon2id hashes. For tests,
    prototypes, and one-admin setups.
  - **`AuthGORM`** — GORM-backed users + groups + passkeys + TOTP.
    Multi-method login form. Self-service account page.
- **Authz interface** — five `Can*(r)` methods consumed by
  `gone/crud`. Stock impls cover anonymous / logged-in / read-only /
  admin-write. App-defined impls drop in for richer policy.
- **Login modes** (AuthGORM):
  - Password (+ optional TOTP).
  - Passkeys (WebAuthn discoverable login, with conditional UI
    autofill). Bypasses TOTP.
- **Account page** — change password, enrol/reset TOTP, list /
  delete passkeys. Admins can disable other users' TOTP (rescue);
  enrolment is always self-service.

The library emits HTML fragments and JSON; page chrome (head, theme,
DaisyUI/Tailwind/HTMX) is supplied by the caller via a `PageShellFunc`
— same convention as `gone/crud`.

## Quick taste

```go
import (
    "log"
    "net/http"
    "time"

    "github.com/alexedwards/scs/v2"
    "github.com/tmshlvck/gone/auth"
)

func main() {
    sm := scs.New()
    sm.Lifetime = 24 * time.Hour
    sm.Cookie.HttpOnly = true
    sm.Cookie.SameSite = http.SameSiteLaxMode

    sa := auth.NewAuthSimple(sm)
    if err := sa.UserAdd("admin", "admin@local", "admin"); err != nil {
        log.Fatal(err)
    }

    mux := http.NewServeMux()
    sa.Route(mux, "", pageShell)
    mux.HandleFunc("GET /heroes", protected(sa, pageShell, renderHeroes))

    handler := sm.LoadAndSave(auth.CSRFWrap(sm)(mux))
    log.Fatal(http.ListenAndServe(":8080", handler))
}
```

`pageShell` is the same `PageShellFunc` type CRUD uses. `protected`
is the app's "redirect to login if anonymous" wrapper — see
[Page shell integration](#page-shell-integration) below for the
shape.

## Stack assumed

| Concern                 | Choice                                                       |
|-------------------------|--------------------------------------------------------------|
| Sessions                | `alexedwards/scs/v2` (hard dep)                              |
| Password hashing        | argon2id via `alexedwards/argon2id`                          |
| TOTP                    | `pquerna/otp` (AuthGORM only)                                |
| Passkeys / WebAuthn     | `github.com/go-webauthn/webauthn` (AuthGORM only)            |
| CSRF                    | hand-rolled (see `examples/sessions`)                        |
| ORM                     | GORM v2 (`gorm.io/gorm`) for AuthGORM                        |
| Templ                   | [templ](https://github.com/a-h/templ) for page fragments     |
| Styling                 | DaisyUI v5 / Tailwind v4 in the caller's page shell          |

The library bundles no CSS, no JS, no static assets. Examples load
DaisyUI + Tailwind + HTMX from jsDelivr/unpkg.

## Core types

### `Auth` interface

```go
type Auth interface {
    Route(mux Mux, baseUrl string, shell PageShellFunc) (string, error)
    CurrentUser(r *http.Request) User
    LoginURL(next string) string
    LogoutURL(next string) string
    IsAuthPath(path string) bool         // public auth pages — see Page shell
    Login(ctx context.Context, u User) error
    Logout(ctx context.Context) error
}
```

Both `AuthSimple` and `AuthGORM` satisfy it. Apps depend on the
interface; switching impls is one constructor call.

### `User` and `Group` interfaces

```go
type User interface {
    Username() string
    Email() string
    Groups() []Group
    HasGroup(name string) bool
}

type Group interface {
    Name() string
}
```

Per-impl extras (PasswordHash, TOTPSecret, raw DB row) live on the
concrete type. App-level authz that needs them type-asserts:

```go
if a, ok := u.(auth.UserGORMAdapter); ok {
    row := a.U  // *auth.UserGORM
    // … use row.ID, row.CreatedAt, etc.
}
```

### `Authz` interface

Five methods, one signature. Consumed by `crud.CRUDTable.Authz`.

```go
type Authz interface {
    CanList(r *http.Request) bool
    CanRead(r *http.Request) bool
    CanCreate(r *http.Request) bool
    CanUpdate(r *http.Request) bool
    CanDelete(r *http.Request) bool
}
```

Stock impls:

| Helper                          | Behaviour                                                       |
|---------------------------------|-----------------------------------------------------------------|
| `AuthzAllowAll`                 | Everything permits. Equivalent to `nil`.                        |
| `AuthzDenyAll`                  | Everything denies. Read-only snapshots, tests, default-deny.    |
| `AuthzLoggedIn{Auth}`           | Permits when `Auth.CurrentUser(r) != nil`.                      |
| `AuthzLoggedInReadOnly{Auth}`   | Reads need login; writes always denied.                         |
| `AuthzLoggedInReadAdminWrite{Auth, AdminGroup}` | Reads need login; writes need `AdminGroup` (default `"admin"`).  |
| `AuthzOrAllow(a)`               | Returns `a` or `AuthzAllowAll` if `a` is nil. Library boundary helper. |

`nil` is treated as `AuthzAllowAll` by every consumer. For richer
policy, write your own:

```go
type ZoneAuthz struct {
    Auth auth.Auth
    DB   *gorm.DB
}

func (z ZoneAuthz) CanUpdate(r *http.Request) bool {
    u := z.Auth.CurrentUser(r)
    if u == nil { return false }
    var ok bool
    z.DB.Raw("SELECT EXISTS(SELECT 1 FROM zone_admins WHERE username = ?)",
        u.Username()).Scan(&ok)
    return ok
}
// (CanList / CanRead / CanCreate / CanDelete similarly)
```

## Sessions + CSRF

`gone/auth` does not own the session manager — the caller does. Pass
`*scs.SessionManager` into the `Auth` constructor; use
`sm.LoadAndSave` as the outermost middleware; wrap mutating routes
with `auth.CSRFWrap(sm)`.

```go
sm := scs.New()
sm.Lifetime = 24 * time.Hour
sm.Cookie.HttpOnly = true
sm.Cookie.SameSite = http.SameSiteLaxMode

sa := auth.NewAuthSimple(sm)
// (or auth.NewAuthGORM(sm, db))

mux := http.NewServeMux()
sa.Route(mux, "", pageShell)

// Pipeline: scs.LoadAndSave → auth.CSRFWrap → app mux.
handler := sm.LoadAndSave(auth.CSRFWrap(sm)(mux))
```

### CSRF middleware

```go
func CSRFWrap(sm *scs.SessionManager) func(http.Handler) http.Handler
func CSRFToken(ctx context.Context) string
func CSRFField(ctx context.Context) templ.Component   // hidden <input>
func CSRFHeaders(ctx context.Context) templ.Attributes // hx-headers spread
```

- Session gets a token on first request.
- `GET` / `HEAD` / `OPTIONS` bypass.
- All other methods read `X-CSRF-Token` first, fall back to
  `csrf_token` form field. Constant-time compare. 403 on mismatch.
- Token rotates on `Login()` (session-fixation defense), clears on
  `Logout()`.

The "everything in HTMX gets the CSRF header automatically" recipe
lives in your page chrome. Drop a meta tag + one event listener:

```html
<meta name="csrf-token" content={ auth.CSRFToken(ctx) }/>

<script>
document.addEventListener('htmx:configRequest', (event) => {
    const meta = document.querySelector('meta[name="csrf-token"]');
    if (meta) event.detail.headers['X-CSRF-Token'] = meta.getAttribute('content');
});
</script>
```

CRUDTable's modal Save/Delete buttons use HTMX, so this hook is
what lets the modals round-trip past `CSRFWrap` without per-element
wiring. See `examples/auth_simple/page.templ` for the full shell.

## AuthSimple

Quick-and-dirty `Auth` implementation. Users live in memory. Every
user is implicitly a member of the `"admin"` group. Use for tests,
prototypes, and "single-admin" deployments.

```go
sm := scs.New()
sa := auth.NewAuthSimple(sm)
if err := sa.UserAdd("admin", "admin@local", "admin"); err != nil {
    log.Fatal(err)
}

mux := http.NewServeMux()
sa.Route(mux, "", pageShell)
```

API:

```go
func NewAuthSimple(sm *scs.SessionManager) *AuthSimple

// Config (not on the Auth interface):
func (s *AuthSimple) UserAdd(username, email, password string) error
func (s *AuthSimple) UserDel(username string) error
func (s *AuthSimple) Passwd(username, password string) error

// AuthSimple fields you may set after New:
//   AfterLogin string   — default "/", where POST /login redirects to
```

Passwords are argon2id-hashed at rest (PHC-encoded strings — same
format AuthGORM uses, so the storage representation never has to
migrate).

Errors:

```go
var (
    ErrUserExists     = errors.New("auth: user already exists")
    ErrUserNotFound   = errors.New("auth: user not found")
    ErrInvalidPassword = errors.New("auth: invalid password")
    ErrEmptyUsername  = errors.New("auth: empty username")
)
```

## AuthGORM

GORM-backed `Auth`. Same `auth.Auth` surface; swap from `AuthSimple`
by changing one constructor.

```go
sm := scs.New()
db, _ := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})

ag, err := auth.NewAuthGORM(sm, db)  // AutoMigrates UserGORM + GroupGORM + PasskeyGORM
if err != nil { log.Fatal(err) }
ag.AfterLogin = "/admin"

// Optional: passkeys (set RP fields to enable).
ag.RPDisplayName = "My App"
ag.RPID = "localhost"
ag.RPOrigins = []string{"http://localhost:8080"}

// Optional: TOTP issuer label.
ag.TOTPIssuer = "My App"

// Seed.
ag.GroupAdd("admin")
ag.UserAdd("admin", "admin@local", "admin")
ag.UserMod("admin", []string{"admin"})

mux := http.NewServeMux()
ag.Route(mux, "", pageShell)
```

API:

```go
func NewAuthGORM(sm *scs.SessionManager, db *gorm.DB) (*AuthGORM, error)

// User CRUD.
func (a *AuthGORM) UserAdd(username, email, password string) error
func (a *AuthGORM) UserDel(username string) error
func (a *AuthGORM) Passwd(username, password string) error

// Group CRUD.
func (a *AuthGORM) GroupAdd(name string) error
func (a *AuthGORM) GroupDel(name string) error

// Replace a user's group memberships by name. Groups must already exist.
func (a *AuthGORM) UserMod(username string, groupNames []string) error

// Passkey listing / deletion. Enrolment is the WebAuthn ceremony —
// browser-driven, no PasskeyAdd entry point.
func (a *AuthGORM) Passkeys(userID uint) ([]PasskeyGORM, error)
func (a *AuthGORM) PasskeyDel(userID, passkeyID uint) error

// AuthGORM fields you may set after New:
//   AfterLogin    string   — default "/"
//   TOTPIssuer    string   — default "gone" (issuer in otpauth URLs)
//   RPDisplayName string   — required for passkeys ("My App")
//   RPID          string   — required for passkeys ("app.example.com")
//   RPOrigins     []string — required for passkeys (full origins)
```

### Models

`UserGORM` and `GroupGORM` have plain exported fields (Go disallows
a method and field of the same name on the same type, and the CRUD
library introspects exported fields). Adapters wrap them to satisfy
`auth.User` / `auth.Group`.

```go
type UserGORM struct {
    ID             uint
    Username       string  // unique
    Email          string  // unique
    PasswordHash   string  // empty = passwordless (passkey-only)
    TOTPSecret     string  // empty = TOTP not enrolled
    WebAuthnHandle []byte  // 32-byte opaque user handle for WebAuthn
    Disabled       bool
    Groups         []GroupGORM   `gorm:"many2many:auth_user_groups"`
    Passkeys       []PasskeyGORM `gorm:"foreignKey:UserID"`
    CreatedAt, UpdatedAt time.Time
}

type GroupGORM struct {
    ID    uint
    Name  string  // unique
    Users []UserGORM `gorm:"many2many:auth_user_groups"`
    CreatedAt, UpdatedAt time.Time
}

type PasskeyGORM struct {
    ID              uint
    UserID          uint
    CredentialID    []byte  // unique
    PublicKey       []byte
    SignCount       uint32
    Transports      string  // CSV: "internal,usb,nfc,ble,hybrid"
    Name            string  // user-supplied label
    AAGUID          []byte
    BackupEligible, BackupState bool
    CreatedAt, LastUsedAt time.Time
}
```

### CRUDTables over the auth models

Derive a CRUDTable on `UserGORM` (or `GroupGORM`) the same way you
would for any GORM model. Gate writes by group with
`AuthzLoggedInReadAdminWrite`:

```go
gate := auth.AuthzLoggedInReadAdminWrite{Auth: ag}

userMM, _ := crud.DeriveMetaModel[auth.UserGORM]()
userMM.DisplayName = "Users"
userTable := crud.DeriveGormCRUDTable[auth.UserGORM](userMM, gate, db)
userTable.Slug = "users"

groupMM, _ := crud.DeriveMetaModel[auth.GroupGORM]()
groupMM.DisplayName = "Groups"
groupTable := crud.DeriveGormCRUDTable[auth.GroupGORM](groupMM, gate, db)
groupTable.Slug = "groups"

admin := crud.DeriveAdminAutoWire(
    []crud.CRUDTableInterface{&userTable, &groupTable},
    nil, // index-level authz: nil so anonymous /admin redirects via shell instead of 403
)
admin.Route(mux, "/", pageShell)
```

Full wiring with login + Admin + auth modules under one mux is in
[`examples/auth_gorm`](../examples/auth_gorm).

## Two-stage login (TOTP)

When `UserGORM.TOTPSecret` is set, AuthGORM diverts a successful
password POST to `/login/totp` instead of `AfterLogin`. The browser
sees a second page asking for a 6-digit code.

Flow:

```
GET /login              → password form
POST /login             → if TOTP enrolled: 303 /login/totp
                          else: 303 AfterLogin / next
GET /login/totp         → code form (anonymous, session has pending state)
POST /login/totp        → 303 AfterLogin / next; session promoted
```

Session keys during transit:

- `auth:totp_pending_user` — username from successful password step
- `auth:totp_pending_next` — `?next=...` from the original form

Re-submitting `/login` with a different user clears any stale
pending state.

### Account-page enrolment

The account page has a "Two-factor authentication" card:

- Disabled, self → "Enable TOTP" button.
- In-flight setup → QR code + verification input.
- Enabled, self → "Disable TOTP" button (with `hx-confirm`).
- Enabled, admin viewing someone else → "Disable TOTP" (the rescue
  case). Admins cannot enrol TOTP for someone else.

Enrolment routes:

```
POST /account/{id}/totp/begin    — generate secret, return QR + form
POST /account/{id}/totp/verify   — validate code, commit to DB
POST /account/{id}/totp/cancel   — drop in-flight secret
POST /account/{id}/totp/disable  — clear TOTPSecret in DB
```

## Passkeys

WebAuthn-based passwordless login. Available on AuthGORM only.
Mounted iff `RPDisplayName / RPID / RPOrigins` are all set.

### Configuration

```go
ag.RPDisplayName = "My App"
ag.RPID          = "app.example.com"   // bare host the browser sees
ag.RPOrigins     = []string{"https://app.example.com"}
```

For local dev: `RPID = "localhost"`, `RPOrigins = ["http://localhost:8080"]`.

### Enrolment (account page)

Self only — admins cannot enrol on behalf of others.

```
POST /account/{id}/passkey/begin            — JSON: CredentialCreation
POST /account/{id}/passkey/finish           — JSON: attestation → persist
POST /account/{id}/passkey/{pkid}/delete    — drop a credential
```

Enrolment requests `residentKey: required` so the credential is a
true discoverable passkey (Bitwarden / 1Password / iCloud Keychain
treat non-discoverable creds as server-side-only and won't offer
them on discoverable login).

The JS that drives the ceremony is part of `passkeyEnrolScript()` in
`auth/views.templ` and is automatically rendered alongside the Add
form on the account page. No app-side JS required.

### Login

Two entry points share one backend:

```
POST /login/passkey/options   — JSON: CredentialAssertion (discoverable)
POST /login/passkey/finish    — JSON: assertion → log in
```

1. **Explicit button**: "🔑 Use passkey" button on the login form.
2. **Conditional UI** (autofill): runs automatically on page load
   when `PublicKeyCredential.isConditionalMediationAvailable()`
   returns true. The browser silently surfaces matching passkeys
   when the username field is focused — no click required if you've
   got a single matching key.

Both paths skip the TOTP stage entirely (passkey UV is already a
strong factor; layering TOTP on top is friction without gain).

### Disable (opt out of passkeys)

Leave `RPDisplayName / RPID / RPOrigins` empty:

```go
ag, _ := auth.NewAuthGORM(sm, db)
// (skip RPDisplayName / RPID / RPOrigins)
```

Result:
- No "Use passkey" button on the login form.
- No `autocomplete="username webauthn"` on the username field.
- No Passkeys card on the account page.
- `/login/passkey/*` and `/account/{id}/passkey/*` return 404.
- `PasskeyGORM` table stays in the schema (AutoMigrate runs
  unconditionally) — useful for retaining rows if you ever flip it
  back on.

## Account page

`/account/{id}` is the all-in-one self-service / admin-management
page. `id` is either a numeric UserGORM ID or the literal `me`
(resolves to the current user). One template handles both
contexts via an `IsSelf` flag.

Sections:

| Card                    | Self                                   | Admin viewing other          |
|-------------------------|----------------------------------------|------------------------------|
| Change password         | ✓ (re-auth with current password)      | ✓ (re-auth with own current) |
| Two-factor (TOTP)       | Enable / Disable                       | Disable only                 |
| Passkeys                | List + Add + per-row Delete            | List + per-row Delete        |

The password card always requires the **acting** user's current
password — privilege grants the right to act, not a free password
reset.

### Header link

The example `pageShell` renders the user's name as a link to
`/account/me`:

```html
<a href="/account/me" class="link link-hover">
    Signed in as <b>{ username }</b>
</a>
```

### Admin link

Inside the User CRUDTable, override the ID column to open the
account page in a modal:

```go
userMM.MustFindField("ID").DisplayValue = func(mf crud.MetaField, value any) templ.Component {
    return userIDLink(fmt.Sprintf("%v", value), "users-modal-l1-body")
}
```

where `userIDLink` is a small templ in your example:

```templ
templ userIDLink(id, modalBodyID string) {
    <button
        type="button"
        class="link link-primary"
        hx-get={ "/account/" + id }
        hx-target={ "#" + modalBodyID }
        hx-swap="innerHTML"
        title="Change password / manage passkeys / TOTP"
    >{ id }</button>
}
```

AuthGORM picks the right modal id off `HX-Target`, renders the
fragment, and fires `HX-Trigger: openModal`. On password-change
success in modal mode the response is `HX-Reswap: none` +
`HX-Trigger: closeModal` so the admin stays on the table.

## Page shell integration

The page shell is a `PageShellFunc` — the same type CRUD uses. It's
called for every page render the library produces. To make the
shell anonymous-aware:

```go
pageShell := func(w http.ResponseWriter, r *http.Request,
                  title string, content templ.Component) {
    u := ag.CurrentUser(r)
    if u == nil && !ag.IsAuthPath(r.URL.Path) {
        http.Redirect(w, r, ag.LoginURL(r.URL.Path), http.StatusSeeOther)
        return
    }
    username := ""
    if u != nil { username = u.Username() }
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    pageLayout(title, auth.CSRFToken(r.Context()), username,
               ag.LogoutURL(""), content).Render(r.Context(), w)
}
```

`ag.IsAuthPath(r.URL.Path)` matches:
- `/login`
- `/login/totp`
- `/login/passkey/options`
- `/login/passkey/finish`

— the paths anonymous and partially-authenticated users must reach.
Without this check, the page shell would trap stage 2 of TOTP login
(or the passkey ceremony) in a redirect loop.

## Examples

| Path                          | Demonstrates                                                            |
|-------------------------------|-------------------------------------------------------------------------|
| [`examples/auth_simple`](../examples/auth_simple) | `AuthSimple` with seed admin user. CRUDTable behind a gated page shell. |
| [`examples/auth_gorm`](../examples/auth_gorm)     | Full AuthGORM: User + Group CRUDTables under Admin; `AuthzLoggedInReadAdminWrite`; account page modal; TOTP; passkeys. |

```sh
go run ./examples/auth_gorm
# open http://localhost:8080/login — login admin / admin
```

## Errors

Sentinel errors apps may inspect via `errors.Is`:

```go
// Common (both impls):
auth.ErrUserExists       // UserAdd of an existing username
auth.ErrUserNotFound     // UserDel / Passwd / Authenticate on missing user
auth.ErrEmptyUsername    // mutating call with username == ""
auth.ErrInvalidPassword  // Authenticate with wrong password

// AuthGORM groups:
auth.ErrGroupExists      // GroupAdd of an existing name
auth.ErrGroupNotFound    // GroupDel / UserMod referencing missing group
auth.ErrEmptyGroupName   // GroupAdd of ""
```

HTTP handlers translate these into appropriate status codes; the
sentinels are exported so apps that drive Authenticate / UserAdd /
etc. directly (tests, post-signup flows) can branch on them.

## Tests

`go test ./auth/...` runs the suite — over 100 tests, no external
deps. Highlights:

- CSRF middleware (form + header path, conditional bypass for GETs).
- Authz stock impls (AllowAll, DenyAll, LoggedIn, LoggedInReadOnly,
  LoggedInReadAdminWrite).
- AuthSimple: UserAdd / UserDel / Passwd matrix, login round-trip,
  session-rotation on login.
- AuthGORM: same matrix + groups; password change; TOTP enrolment
  + login; staged login (password → TOTP); passkey schema /
  conditional mounting / route shapes / per-row delete.
- Account page: anonymous → 303 to login; self / admin policy
  matrix; modal-mode close + no-swap on success; page-mode renders
  inline success banner; admin must still use *own* current
  password.

The full WebAuthn ceremony (registration + assertion verification)
is exercised live in `examples/auth_gorm`; a unit-level mock
authenticator is a follow-up.

## See also

- [`PRD-AUTH.md`](../PRD-AUTH.md) — design rationale and open
  questions (including SSO, which is specced but not yet built).
- [`docs/CRUD.md`](CRUD.md) — CRUDTable / Admin reference.
- [`README.md`](../README.md) — top-level project overview.
