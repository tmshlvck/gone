# gone/auth — PRD

> Design document for the security primitives in `gone`: sessions,
> CSRF, password / TOTP / OIDC login, user / group model, and the
> authorization (`Authz`) interface that `gone/crud` (and future
> `gone/jsonapi`) gate on.
>
> Companion to [`PRD-CRUD.md`](PRD-CRUD.md). The CRUD package already
> consumes the `Authz` interface (was `crud.AuthzInterface`, then
> `gone/authz/`, now folded into `gone/auth/`) and treats `nil` as
> `AuthzAllowAll`. This PRD lays out the rest of the security stack
> on top.
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
- **Login** — multi-method on top of `AuthGORM`:
    - username/password (with optional TOTP second factor),
    - **passkeys** (WebAuthn, via `go-webauthn/webauthn`) — sufficient
      on their own; if a user has both a passkey and TOTP, the
      passkey path skips the code prompt,
    - **SSO** (OIDC, via `coreos/go-oidc`) — one or more configured
      providers ("Continue with GitHub", "Continue with Google") that
      also bypass TOTP.
  `AuthSimple` ships username/password only — passkeys and OIDC
  require persistent per-credential state that the memory store
  doesn't justify.
- **Authz** — `auth.Authz` used by `crud.CRUDTable` and friends to
  gate routes; plus four stock struct implementations
  (`AuthzAllowAll`, `AuthzLoggedIn`, `AuthzLoggedInReadOnly`,
  `AuthzLoggedInReadAdminWrite`) for the common patterns. Role /
  per-resource / ownership semantics are the app's design space —
  implement `auth.Authz` directly when you need them.

The library never owns page chrome — login and account pages render
the same `templ.Component` fragments that `gone/crud` does, wrapped
in the caller's `PageShellFunc`.

## 2. Goals

1. **Sessions and CSRF as middleware** — caller wraps their mux with
   our middleware, gets a session + CSRF token in `r.Context()`. No
   per-route plumbing. Built directly on `alexedwards/scs/v2` — no
   intermediate session abstraction.
2. **Two Auth implementations, unified by interface** — `Auth`,
   `User`, `Group` are interfaces in `gone/auth/`. V1 ships
   `AuthSimple` — in-memory users configured via
   `UserAdd(username, email, password)`, argon2id hashes, plain login
   form. V2 will ship `AuthGORM` with GORM-backed storage, passkeys,
   and SSO. Each impl owns its own page templates — the v1 plain
   form and the v2 multi-method form don't share enough structure
   to unify.
3. **Authz interface stays small** — `Can{List,Read,Create,Update,Delete}(r)`
   — five methods, all take `*http.Request`, all return `bool`.
   `crud/` already uses this shape.
4. **Groups are first-class; deeper roles are the app's job** — the
   library ships `User` with `Groups []Group` (N:M), enough for
   "admin can write" out of the box. Anything richer
   (per-resource ownership, role hierarchies, permissions) is the
   app's responsibility — satisfy `auth.Authz` directly.
5. **Compatible with the rest of `gone`** — CRUDTable's `Authz` field
   accepts `auth.Authz`. Login pages plug into the existing
   `PageShellFunc` pattern.

## 3. Non-goals (this PRD)

**Deferred to v2** (still in this library's roadmap, just not v1):

- **AuthGORM** with GORM-backed users and argon2id password hashes.
  V1 ships `AuthSimple` only; AuthGORM lands in v2 with passkeys,
  OIDC, and account management.
- **Account management page** — change password, view profile.
  Memory store has no password change in v1.
- **Password reset / email verification** — needs an email
  abstraction the library doesn't have. Affects passwordless-user
  bootstrap when neither SSO nor an existing-user-with-password
  path is available.
- **Single Sign-Out (SLO)** — destroying the local session is
  sufficient; we don't propagate logout to the OIDC provider.

**Out of scope entirely**:

- **API keys** — header / query-string auth for programmatic
  clients. Sketched in TODO; lives outside the session story.
- **JSON API content negotiation** — separate PRD (deferred).
- **Field-level audit logging** — deferred.
- **Multi-DB user federation** — a single `User` table with optional
  external-auth links is enough.

## 4. Stack

| Concern                | Choice                                                |
|------------------------|-------------------------------------------------------|
| Session middleware     | `alexedwards/scs/v2` — direct hard dep                |
| Password hashing       | argon2id via `alexedwards/argon2id` (v1 + v2)         |
| TOTP                   | `pquerna/otp` (AuthGORM)                              |
| Passkeys / WebAuthn    | `github.com/go-webauthn/webauthn` (AuthGORM)          |
| OIDC                   | `coreos/go-oidc/v3` + `golang.org/x/oauth2` (AuthGORM) |
| CSRF                   | hand-rolled (see `examples/sessions`)                 |

scs is a hard dependency — the original "small `SessionStore`
interface so callers can swap stores" plan was dropped (scs already
abstracts its own backends; double abstraction has no payoff).

## 5. Package layout

```
gone/
└── auth/                         — Auth + Authz + CSRF + impls
    ├── auth.go                     Auth, User, Group interfaces
    ├── authz.go                    Authz interface +
    │                               AuthzAllowAll, AuthzDenyAll,
    │                               AuthzLoggedIn, AuthzLoggedInReadOnly,
    │                               AuthzLoggedInReadAdminWrite,
    │                               AuthzOrAllow
    ├── csrf.go                     CSRFWrap middleware + CSRFToken /
    │                               CSRFField / CSRFHeaders helpers
    ├── simple.go                   AuthSimple + UserAdd / UserDel /
    │                               Passwd + login/logout handlers
    ├── orm.go                    — v2: AuthGORM (GORM-backed)
    └── views.templ                 AuthSimple's login form
                                    (AuthGORM ships its own)
```

Everything lives in one flat `gone/auth/` package. Authz used to be
a separate `gone/authz/` package but folded in: the stock impls
already depend on `Auth` and `User` from this package, and the whole
file is ~100 LOC — same threshold as the CSRF helpers, which live
in `auth/` for the same reason.

Each auth implementation ships its own page chrome — the v1 plain
login form and the v2 form (with passkey + SSO buttons) don't share
enough structure to justify abstracting the templates.

`crud.CRUDTable.Authz` is typed `auth.Authz`; callers passing `nil`
need no code changes (the consumer coerces via `AuthzOrAllow`).

## 6. `auth` package

### 6.1 `Auth` interface

```go
package auth

import (
    "context"
    "net/http"

    "github.com/a-h/templ"
    "github.com/tmshlvck/gone/crud"
)

// Auth is what every page handler and authz helper interacts with.
// AuthSimple (v1) and AuthGORM (v2) both implement it. Apps depend
// on Auth, not the concrete impl — swapping happens by changing one
// constructor call.
type Auth interface {
    // Route mounts the impl's login + logout pages at baseUrl. Each
    // impl ships its own templates (simple form vs. multi-method).
    // Returns the absolute urlBase the pages were mounted at.
    Route(mux crud.Mux, baseUrl string, shell crud.PageShellFunc) (string, error)

    // CurrentUser returns the user the session points to, or nil for
    // anonymous. Page handlers call this and decide their response
    // (redirect to login, render access-denied, render redacted view).
    CurrentUser(r *http.Request) User

    // LoginURL / LogoutURL build the URL to the respective endpoint
    // with the supplied next path encoded as "?next=...". Empty next
    // returns just the path. Use to render nav links and redirects.
    LoginURL(next string) string
    LogoutURL(next string) string

    // IsAuthPath reports whether path is one of the auth-managed
    // pages that must remain accessible to anonymous (or partially
    // authenticated) users — login, staged TOTP step, passkey /
    // OIDC ceremony endpoints, etc. Page shells use this to skip
    // their "redirect anonymous → /login" guard so the login flow
    // itself isn't trapped.
    IsAuthPath(path string) bool

    // Programmatic session writes — for tests and post-signup
    // auto-login. The supplied User is whatever the impl can later
    // round-trip back through CurrentUser.
    Login(ctx context.Context, u User) error
    Logout(ctx context.Context) error
}
```

AuthGORM's `IsAuthPath` matches all of: `/login`, `/login/totp`,
`/login/passkey/options`, `/login/passkey/finish`, and any
`/login/oidc/{name}` / `/login/oidc/{name}/callback` pair for a
registered provider. AuthSimple's matches only `/login` (no staged
flows, no SSO).

Notably absent:

- **No `LoadUser` middleware.** `CurrentUser(r)` does the session
  lookup on demand.
- **No `Require` middleware.** Page handlers (or the page shell)
  call `CurrentUser` themselves and choose redirect vs. render.
- **No `Backend` indirection.** Each impl handles its own credential
  flow; the user-facing interface is `Auth` itself.

### 6.2 `User` + `Group` interfaces

```go
// User exposes the subset of user state page handlers and authz
// helpers consult. Per-impl extras (PasswordHash, OIDCSubject,
// CredentialIDs for passkeys) stay on the concrete impl's user type;
// callers type-assert when they need them.
//
// Passkey-only users may not have a meaningful Username/Email —
// Username() can return "" in those cases.
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

The session **is** the user store: `Login` serialises the (concrete)
user; `CurrentUser` deserialises it. No external lookup on subsequent
requests. Group / email / etc. changes only apply on next login —
acceptable for v1 AuthSimple; v2 AuthGORM may add a refresh path.

### 6.3 CSRF (session-scoped, not on the `Auth` interface)

CSRF is tied to the session manager, not to the auth method.
Package-level helpers in `gone/auth/`:

```go
// CSRF wraps the supplied handler. Ensures the session has a token,
// validates it on mutating methods, sends 403 on mismatch.
func CSRFWrap(sm *scs.SessionManager) func(http.Handler) http.Handler

// CSRFToken returns the current session's CSRF token; "" for
// sessions without one (shouldn't happen if CSRF middleware ran).
func CSRFToken(ctx context.Context) string

// Templ helpers — emit the hidden form field and the hx-headers JSON
// for HTMX-driven mutations.
func CSRFField(ctx context.Context) templ.Component
func CSRFHeaders(ctx context.Context) templ.Component
```

Token is **rotated on login** (session-fixation defense) and **cleared
on logout**. Anonymous CSRF works — the token is created on first
request. Each `Auth` impl calls `scs.SessionManager.RenewToken` in
its `Login` to drive the rotation.

### 6.4 AuthSimple

V1's only impl. Users live in memory, configured by code at startup.
Passwords are argon2id-hashed at rest — even for the prototype, so
the small example doubles as a check that the hashing path is wired
right before AuthGORM lands.

```go
package auth

import "github.com/alexedwards/scs/v2"

type AuthSimple struct {
    Sessions   *scs.SessionManager
    LoginPath  string  // default "/login"
    AfterLogin string  // default "/"
    // internal: users map[string]*simpleUser
}

func NewAuthSimple(sm *scs.SessionManager) *AuthSimple
```

Concrete configuration methods (NOT on `Auth` — each impl exposes its
own config surface). Passwords are hashed with argon2id
(`alexedwards/argon2id`, which wraps `golang.org/x/crypto/argon2`) at
rest. PHC-encoded strings; same format AuthGORM will use, so the hash
column doesn't have to migrate when the GORM backend lands:

```go
// UserAdd creates a user with the given email and password. The
// password is hashed before storage. Returns ErrUserExists if a user
// with the same username is already registered. Every AuthSimple
// user is implicitly a member of the "admin" group — no per-user
// group configuration in v1 (use AuthGORM when you need richer
// group semantics).
func (s *AuthSimple) UserAdd(username, email, password string) error

// UserDel removes the named user. Returns ErrUserNotFound if absent.
func (s *AuthSimple) UserDel(username string) error

// Passwd replaces the named user's password. The new password is
// re-hashed. Returns ErrUserNotFound if absent.
func (s *AuthSimple) Passwd(username, password string) error
```

Plus the `Auth` methods (`Route`, `CurrentUser`, `LoginURL`,
`LogoutURL`, `Login`, `Logout`) — see §6.1.

Bootstrap example:

```go
sm := scs.New()
sa := auth.NewAuthSimple(sm)
if err := sa.UserAdd("admin", "admin@local", "admin"); err != nil {
    log.Fatal(err)
}

var a auth.Auth = sa    // up-cast for downstream callers
```

### 6.5 AuthGORM

GORM-backed Auth implementation. Same `auth.Auth` surface; users +
groups live in `auth_users` / `auth_groups` (+ `auth_user_groups`
join) tables. Password storage via `alexedwards/argon2id` — same as
AuthSimple, so apps swap impls by changing the constructor.

```go
type AuthGORM struct {
    Sessions   *scs.SessionManager
    DB         *gorm.DB
    AfterLogin string // default "/"

    // TOTPIssuer is embedded in the otpauth URLs generated for
    // enrolment. Defaults to "gone". (See §6.5.1.)
    TOTPIssuer string

    // WebAuthn relying-party info (§6.5.2). Required for passkeys.
    RPDisplayName string   // shown in the browser UI ("Acme Corp")
    RPID          string   // bare host, e.g. "app.example.com"
    RPOrigins     []string // allowed origins, e.g. ["https://app.example.com"]

    // OIDC providers registered via AddOIDCProvider. AuthGORM
    // renders one button per provider on the login page (§6.5.3).
    // Empty = no SSO.
    OIDCProviders []OIDCProvider

    // PublicURL is the externally-reachable base URL of the app
    // (no trailing slash, e.g. "https://app.example.com"). Used
    // to build OIDC redirect URLs the IdP can call back; the
    // library can't infer this from r.Host because reverse
    // proxies often hide the public hostname. Required when
    // OIDCProviders is non-empty.
    PublicURL string
}

func NewAuthGORM(sm *scs.SessionManager, db *gorm.DB) (*AuthGORM, error)
//   auto-migrates UserGORM + GroupGORM + PasskeyGORM + OIDCIdentityGORM
```

Concrete configuration methods (NOT on the `auth.Auth` interface):

```go
// User CRUD.
func (a *AuthGORM) UserAdd(username, email, password string) error
func (a *AuthGORM) UserDel(username string) error
func (a *AuthGORM) Passwd(username, password string) error

// Group CRUD.
func (a *AuthGORM) GroupAdd(name string) error
func (a *AuthGORM) GroupDel(name string) error

// Set a user's group memberships by name (replaces the m2m list).
// Groups must already exist — ErrGroupNotFound otherwise.
func (a *AuthGORM) UserMod(username string, groupNames []string) error

// Passkey listing / deletion. Enrolment is driven by the WebAuthn
// JS ceremony — see §6.5.2 — so there's no PasskeyAdd entry point.
func (a *AuthGORM) Passkeys(userID uint) ([]PasskeyGORM, error)
func (a *AuthGORM) PasskeyDel(userID, passkeyID uint) error

// OIDC provider registration. Each provider gets a button on the
// login page. Mutates AuthGORM.OIDCProviders; call before Route()
// so the buttons render on the rendered login form. See §6.5.3.
func (a *AuthGORM) AddOIDCProvider(p OIDCProvider) error
```

Models exposed so apps can derive CRUDTables over them:

```go
type UserGORM struct {
    ID           uint
    Username     string `gorm:"uniqueIndex"`
    Email        string `gorm:"uniqueIndex"`
    PasswordHash string  // hidden from CRUDAdmin via mm.MustFindField("PasswordHash").Hidden = true
    Disabled     bool
    Groups       []GroupGORM `gorm:"many2many:auth_user_groups"`
    CreatedAt    time.Time
    UpdatedAt    time.Time
}

type GroupGORM struct {
    ID    uint
    Name  string `gorm:"uniqueIndex"`
    Users []UserGORM `gorm:"many2many:auth_user_groups"`
    CreatedAt time.Time
    UpdatedAt time.Time
}
```

`UserGORM` and `GroupGORM` have plain exported fields (Go disallows
a method named Username and a field named Username on the same type;
the CRUD library introspects exported fields). Adapters
(`UserGORMAdapter` / `GroupGORMAdapter`) wrap the rows to satisfy
`auth.User` / `auth.Group` — `CurrentUser` returns the adapter; apps
that need the raw row type-assert to it and read `.U` / `.G`.

The login template shipped with AuthGORM is the multi-method form
described in §6.5.1–6.5.3. `examples/auth_gorm` demonstrates the
wiring end-to-end: AuthGORM seed of admin/admin in the `admin`
group, CRUDTables for users and groups mounted under `crud.Admin`,
gated by `AuthzLoggedInReadAdminWrite`. PasswordHash is hidden from
the admin UI; passwords go through `AuthGORM.Passwd`.

#### 6.5.1 TOTP (shipped)

Optional second factor for password sign-ins. UserGORM gains
`TOTPSecret string`; non-empty means stage 2 is required. Enrolment
is a self-service flow on the account page (§7.4); admins can
disable someone else's TOTP for "lost the phone" recovery. Passkey
and SSO sign-ins skip TOTP — those methods are already strong.

Library: `pquerna/otp`. QR rendered server-side as a base64 PNG
data URL — no JS QR dependency.

Session keys during stage-1 → stage-2 transit:

  auth:totp_pending_user   — the password-authenticated username
  auth:totp_pending_next   — the user's original ?next=... value
  auth:totp_setup_secret   — in-flight enrolment secret (account page)

#### 6.5.2 Passkeys / WebAuthn

Library: `github.com/go-webauthn/webauthn`. A user may have **many**
passkeys (different devices). Each PasskeyGORM row stores the
WebAuthn credential the browser produced at enrolment plus
metadata for management (user-supplied label, last-used timestamp,
sign counter for replay defense).

**Enrolment** (account page → "Add passkey" button):

  POST /account/{id}/passkey/begin   — server returns CreationOptions
                                       JSON, stashes the challenge in
                                       the session. Self only.
  POST /account/{id}/passkey/finish  — browser POSTs the attestation
                                       (CredentialCreationResponse);
                                       server verifies, persists the
                                       row, returns the updated passkey
                                       list fragment.
  POST /account/{id}/passkey/{pkid}/delete — drop one credential.

**Login** — the login page tries two paths:

  1. **Conditional UI** (autofill): on page load, the JS calls
     `navigator.credentials.get({ mediation: "conditional" })`. The
     browser silently surfaces matching passkeys when the username
     field is focused. Best UX on supporting browsers.

  2. **Explicit button**: "Use passkey" button calls the same
     `navigator.credentials.get()` without `mediation: "conditional"`
     — the platform UI pops immediately.

  Both paths use the same backend round-trip:

    POST /login/passkey/options   — server generates challenge,
                                    stashes in session, returns
                                    RequestOptions JSON (with empty
                                    allowCredentials → "discoverable",
                                    so any credential for this RP
                                    matches).
    POST /login/passkey/finish    — browser POSTs assertion; server
                                    verifies signature against the
                                    stored public key, identifies the
                                    user from the credential row, and
                                    finalises the session via
                                    a.Login(ctx, u) — bypassing TOTP.

Session keys during ceremony:

  auth:passkey_login_challenge   — bytes returned at /options
  auth:passkey_setup_challenge   — bytes returned at /account/.../begin

RPDisplayName / RPID / RPOrigins on AuthGORM are required (and
validated at construction) when at least one PasskeyGORM row exists
or any AuthGORM.Route attempt would mount the passkey endpoints.
For dev, `RPID = "localhost"` + `RPOrigins = ["http://localhost:8080"]`
works.

#### 6.5.3 SSO (OIDC + OAuth2) — shipped

Libraries:
- `github.com/coreos/go-oidc/v3/oidc` — ID-token verification, nonce
  check, discovery against `IssuerURL`.
- `golang.org/x/oauth2` — authorization-code flow + PKCE on both
  provider types.

GitHub doesn't speak OIDC, so the v1 design ships **two** provider
types behind one internal `ssoProvider` interface:

- `OIDCProvider` (Google, Okta, on-prem Keycloak / Authentik / Dex /
  ZITADEL): performs discovery, verifies ID tokens + nonce.
- `OAuth2Provider` (GitHub today, other non-OIDC IdPs in future):
  caller supplies `UserInfo func(ctx, accessToken)` that fetches the
  provider-specific user-info REST endpoint and returns the same
  `ssoIdentity{Subject, Email, DisplayName, Claims}` shape.

Both share the same policy fields:

```go
type providerPolicy struct {
    DefaultGroups   []string  // always added on first login
    GroupsClaim     string    // optional claim → group names
    CreateGroups    bool      // auto-create unknown groups from claim
    GroupMapper     func(map[string]any) []string  // optional hook
    AutoLinkByEmail bool      // trust email to link to existing local user
    DisableAutoCreate bool    // refuse new identities; admin must pre-provision
}
```

Preset constructors: `auth.GoogleProvider(clientID, secret, redirect)`,
`auth.OktaProvider(domain, clientID, secret, redirect)`,
`auth.GitHubProvider(clientID, secret, redirect)`.

Routes (registered once per `AuthGORM` when at least one provider is
configured):

```
GET  /login/sso/{name}           — start ceremony (state + PKCE + nonce
                                   stashed in session, redirect to IdP)
GET  /login/sso/{name}/callback  — IdP redirects back with code+state;
                                   server validates state, exchanges
                                   code (provider-specific), maps
                                   identity → user, finalizes via
                                   loginStage1 (so TOTP-enrolled
                                   SSO users still get the second step)
```

**SSO-only flag**. `UserGORM` gets a `SSOOnly bool` field. Set to
true automatically when first-login SSO auto-creates a user. While
the flag is set:

- Account page hides the password and passkey cards; renders a
  short "this account is SSO-managed" notice in their place. TOTP
  card stays — TOTP layers on top of any sign-in method.
- `POST /account/{id}` (password change) returns 403.
- `POST /account/{id}/passkey/begin` and `/passkey/finish` return
  403.
- `POST /account/{id}/sso/{identityID}/delete` allowed, except for
  the last linked identity (would lock the user out).

Admin clears the flag in the admin UI (the field renders as a
checkbox via `crud.MetaModel`), unlocking the local-credential
surfaces.

**User-mapping policy** on callback identity `(Provider=P,
Subject=S, Email=E)`:

  1. `SSOIdentityGORM` row where `(Provider=P, Subject=S)` exists →
     update `LastUsedAt` + Email/DisplayName snapshots, load
     `UserGORM`, finalize. (Disabled users rejected here.)
  2. `provider.AutoLinkByEmail && UserGORM(Email=E, !Disabled)`
     exists → create the identity link, finalize.
  3. `!provider.DisableAutoCreate` → create
     `UserGORM(Username=E, Email=E, SSOOnly=true)`, assign groups
     (DefaultGroups ∪ GroupsClaim-derived ∪ GroupMapper-derived,
     deduped), create identity link, finalize.
  4. Else → 403 with `ErrSSONoAccount`.

**Username derivation** for auto-create: full email address. No
collisions across providers (different emails → different
usernames). Local UNIQUE constraint on `username` means a
pre-existing local `alice@example.com` blocks auto-create; the
callback returns `ErrSSONoAccount` with a "username already in
use" message.

**Schema**. One link table:

```go
type SSOIdentityGORM struct {
    ID          uint
    UserID      uint   `gorm:"index;not null"`
    Provider    string `gorm:"size:64;uniqueIndex:idx_sso_provider_subject"`
    Subject     string `gorm:"size:255;uniqueIndex:idx_sso_provider_subject"`
    Email       string
    DisplayName string
    CreatedAt   time.Time
    LastUsedAt  time.Time
}
```

One user → many identities (so a person with corporate Okta + a
personal Google can link both to one local user). The
`(Provider, Subject)` unique index guarantees an IdP-issued
identity maps to at most one local user.

**Account page** lists linked identities with per-row Unlink
buttons (self only). Adding a new SSO link from the account page
is *not* shipped — identities arrive via first sign-in only.
Defer "self-service link" to a follow-up.

**Session keys** during ceremony (all cleared on successful
callback or on a new start):

```
auth:sso_state     — random state for CSRF
auth:sso_pkce      — PKCE code_verifier (S256)
auth:sso_nonce     — OIDC nonce (replay defense)
auth:sso_provider  — provider name (which IdP redirected back)
auth:sso_next      — original ?next= URL
```

**Login form** renders one `<a class="btn btn-outline">Sign in with
X</a>` per registered provider, in registration order, below the
password form (or below the passkey button when present). With
zero providers configured the section disappears entirely.

**Trust posture**. Public IdPs (Google, generic GitHub) should keep
`AutoLinkByEmail=false` — anyone who can get an ID token claiming
`alice@example.com` could otherwise take over a local `alice@example.com`.
Trusted IdPs (corporate Okta, your own on-prem Keycloak) can flip
the flag on; their email verification is sufficient. The example
`examples/auth_sso/main.go` ships with this posture: Google + GitHub
off, Okta on.

### 6.6 `Authz` interface + stock impls

`Authz` is the gate every component (CRUDTable, JSONAPI, Admin
pages) consults before touching data. It used to live in its own
`gone/authz/` package; folded into `gone/auth/` because (a) the
stock impls depend on `Auth` and `User` anyway, and (b) it's small
enough to not warrant a separate package — same shape as CSRF
helpers living alongside the rest of auth.

```go
package auth

// Authz: five Can* methods on *http.Request. Return true to permit,
// false to deny (consumer responds 403). nil is treated as
// AuthzAllowAll by every consumer — wrap with AuthzOrAllow at the
// boundary if you want to call methods directly.
type Authz interface {
    CanList(r *http.Request) bool
    CanRead(r *http.Request) bool
    CanCreate(r *http.Request) bool
    CanUpdate(r *http.Request) bool
    CanDelete(r *http.Request) bool
}

// AuthzAllowAll: every check returns true. Equivalent to nil.
type AuthzAllowAll struct{}

// AuthzDenyAll: every check returns false. For read-only snapshots,
// tests, and "lock down by default".
type AuthzDenyAll struct{}

// AuthzLoggedIn permits every action iff the request bears an
// authenticated user. Anonymous requests are denied uniformly.
type AuthzLoggedIn struct {
    Auth Auth  // any concrete impl (AuthSimple, AuthGORM)
}

// AuthzLoggedInReadOnly: reads (CanList / CanRead) require login;
// writes (CanCreate / CanUpdate / CanDelete) always denied, even for
// logged-in users.
type AuthzLoggedInReadOnly struct {
    Auth Auth
}

// AuthzLoggedInReadAdminWrite: any logged-in user reads; only members
// of AdminGroup write. AdminGroup defaults to "admin" when empty.
type AuthzLoggedInReadAdminWrite struct {
    Auth       Auth
    AdminGroup string // empty → "admin"
}

// AuthzOrAllow: returns a non-nil Authz — either the supplied one
// or AuthzAllowAll. Consumers call this before invoking methods so
// the dispatch loop doesn't double-check nil.
func AuthzOrAllow(a Authz) Authz
```

Semantics (admin = logged-in user in `AdminGroup`; logged-in = any
other authenticated user):

| Helper                          | Anon read | Logged read | Admin read | Anon write | Logged write | Admin write |
|---------------------------------|-----------|-------------|------------|------------|--------------|-------------|
| `AuthzAllowAll`                 | ✓         | ✓           | ✓          | ✓          | ✓            | ✓           |
| `AuthzLoggedIn`                 | ✗         | ✓           | ✓          | ✗          | ✓            | ✓           |
| `AuthzLoggedInReadOnly`         | ✗         | ✓           | ✓          | ✗          | ✗            | ✗           |
| `AuthzLoggedInReadAdminWrite`   | ✗         | ✓           | ✓          | ✗          | ✗            | ✓           |
| `AuthzDenyAll`                  | ✗         | ✗           | ✗          | ✗          | ✗            | ✗           |

For richer policies — per-resource, ownership, role hierarchies —
apps implement `auth.Authz` directly:

```go
type ZoneAuthz struct {
    Auth auth.Auth
    DB   *gorm.DB
}

func (z ZoneAuthz) CanUpdate(r *http.Request) bool {
    u := z.Auth.CurrentUser(r)
    if u == nil { return false }
    var allowed bool
    z.DB.Raw("SELECT EXISTS(SELECT 1 FROM zone_admins WHERE username = ?)",
        u.Username()).Scan(&allowed)
    return allowed
}
// (CanList / CanRead / CanCreate / CanDelete similarly)
```

## 7. Login flow

V1 experience (login) with AuthSimple:

1. Page handler (or page shell) calls `a.CurrentUser(r)`, sees nil,
   and redirects:
   ```go
   if a.CurrentUser(r) == nil {
       http.Redirect(w, r, a.LoginURL(r.URL.Path), http.StatusSeeOther)
       return
   }
   ```
   Alternatively, the handler renders an "access denied" page or a
   redacted anonymous view — that's the handler's call.
2. GET `/login?next=/admin` → AuthSimple's templ renders (CSRF token,
   hidden `next` field, optional error).
3. POST `/login` → AuthSimple verifies username/password against its
   in-memory map, calls its own `Login(ctx, user)`, redirects to
   `next` (when safe) or `AfterLogin`.
4. Session cookie set; CSRF token rotated. Subsequent
   `CurrentUser(r)` calls return the user that was logged in.

AuthGORM's login page is a multi-method picker:

```
┌─ Sign in ──────────────────────────────────┐
│ [ username ]                               │
│ [ password ]                               │
│ [ Sign in ]                                │
│                                            │
│ ─── or ───                                 │
│                                            │
│ [ 🔑  Use passkey ]                         │
│                                            │
│ ─── or ───                                 │
│                                            │
│ [ 🐙  Continue with GitHub ]                │
│ [ G   Continue with Google ]                │
└────────────────────────────────────────────┘
```

The username field carries `autocomplete="username webauthn"` so
browsers offer conditional-UI passkey autofill alongside saved
passwords. JS on page load calls `navigator.credentials.get({
mediation: "conditional" })` if the browser supports it; if a passkey
is silently chosen, the assertion is POSTed to /login/passkey/finish
and the user lands authenticated without a click. The explicit
"Use passkey" button is the fallback for browsers without
conditional-UI support.

Flow by entry point:

  Password    → /login (stage 1) → /login/totp (if TOTPSecret set)
              → AfterLogin / next.
  Passkey     → /login/passkey/finish → AfterLogin / next. TOTP
              skipped.
  SSO         → /login/oidc/{name} → IdP → /login/oidc/{name}/callback
              → AfterLogin / next. TOTP skipped.

Bypass rationale: passkeys verify possession + user verification
(biometric / PIN) on the device; OIDC inherits the IdP's MFA. Both
are at least as strong as password + TOTP; requiring TOTP again
would be friction without security benefit.

The account page (`/account/{id}`) hosts management for all four:

  Card 1 — Change password (§7.4).
  Card 2 — Two-factor authentication (TOTP, §6.5.1).
  Card 3 — Passkeys: list + "Add passkey" + per-row delete (§6.5.2).
  Card 4 — Linked accounts: list of OIDCIdentityGORM rows + per-row
            Unlink + "Link <provider>" buttons (§6.5.3). Self only;
            admins don't manage others' SSO links.

## 8. CSRF flow

Identical to `examples/sessions`, codified as package-level helpers
in `gone/auth/`:

1. Session middleware (`scs.LoadAndSave`) runs first.
2. `auth.CSRFWrap(sm)` middleware:
   - Ensures the session has a `csrf_token` (creates one if absent).
   - On GET / HEAD / OPTIONS: pass through.
   - On other methods: check `X-CSRF-Token` header, fall back to
     `csrf_token` form field. Constant-time compare. 403 on mismatch.
3. Templ pages render `<input type="hidden" name="csrf_token" value="...">`
   via `auth.CSRFField(ctx)`.
4. HTMX swap forms can also use `hx-headers='{"X-CSRF-Token":"..."}'`
   via `auth.CSRFHeaders(ctx)`.

API-key authenticated requests (future) bypass CSRF — header auth, no
session, no XSRF surface.

## 9. Integration with `gone/crud`

- `crud.CRUDTable.Authz` accepts `auth.Authz`. The type lives in
  `authz/`; callers that pass `nil` (most examples) need no change.
- For "logged-in users can edit; everyone else is locked out":
  ```go
  zoneTable := crud.DeriveGormCRUDTable[Zone](zoneMM,
      auth.AuthzLoggedIn{Auth: a}, db)
  ```
- For "read-only access for logged-in users":
  ```go
  reportTable := crud.DeriveGormCRUDTable[Report](reportMM,
      auth.AuthzLoggedInReadOnly{Auth: a}, db)
  ```
- For "everyone in `admin` group writes; everyone else reads":
  ```go
  zoneTable := crud.DeriveGormCRUDTable[Zone](zoneMM,
      auth.AuthzLoggedInReadAdminWrite{Auth: a}, db)
  // Or with a custom group name:
  //   auth.AuthzLoggedInReadAdminWrite{Auth: a, AdminGroup: "editors"}
  ```
- For anything more fine-grained — per-resource, ownership, per-row
  rules — the app implements `auth.Authz` directly (see §6.6).
- Bootstrap (AuthSimple flavour):
  ```go
  sm := scs.New()
  sa := auth.NewAuthSimple(sm)
  if err := sa.UserAdd("admin", "admin@local", "admin"); err != nil {
      log.Fatal(err)
  }
  var a auth.Auth = sa  // up-cast so authz helpers / handlers see Auth

  a.Route(mux, "/", pageShell)            // /login, /logout

  // Compose: scs LoadAndSave wraps everything; auth.CSRFWrap wraps
  // mutating routes; page handlers (or the page shell) call
  // a.CurrentUser to decide redirect-vs-render-anonymous-vs-deny.
  handler := sm.LoadAndSave(auth.CSRFWrap(sm)(mux))
  ```

## 10. Schema sketch

V1 has **no DB schema** — AuthSimple keeps users in memory. The
sketch below is what AuthGORM's tables will look like in v2;
capturing it here so the migration path is clear.

The session-visible interface (`auth.User`) carries only
`Username() / Email() / Groups() / HasGroup(name)`. Concrete impls
expose their own ID / credentials / etc. via type assertion.

```go
// AuthGORM tables. UserGORMAdapter wraps *UserGORM to satisfy
// auth.User; CRUDTable[UserGORM] introspects the exported fields.
type UserGORM struct {
    ID           uint
    Username     string `gorm:"uniqueIndex;size:64"`
    Email        string `gorm:"uniqueIndex;size:255"`
    PasswordHash string `gorm:"size:255"`     // empty = passwordless (passkey/SSO only)
    TOTPSecret   string `gorm:"size:64"`       // empty = TOTP not enrolled
    Disabled     bool
    Groups       []GroupGORM        `gorm:"many2many:auth_user_groups"`
    Passkeys     []PasskeyGORM      `gorm:"foreignKey:UserID"`
    OIDCLinks    []OIDCIdentityGORM `gorm:"foreignKey:UserID"`
    CreatedAt    time.Time
    UpdatedAt    time.Time
}

type GroupGORM struct {
    ID        uint
    Name      string     `gorm:"uniqueIndex;size:64"`
    Users     []UserGORM `gorm:"many2many:auth_user_groups"`
    CreatedAt time.Time
    UpdatedAt time.Time
}

// PasskeyGORM: one row per credential. A user may have many.
// Storage maps to go-webauthn's webauthn.Credential plus a
// user-supplied label and timing for the account-page UI.
type PasskeyGORM struct {
    ID              uint
    UserID          uint   `gorm:"index;not null"`
    CredentialID    []byte `gorm:"uniqueIndex;size:255"`
    PublicKey       []byte // COSE-encoded
    SignCount       uint32 // replay defense; updated after each auth
    Transports      string // CSV: "internal,usb,nfc,ble,hybrid"
    AttestationType string
    AAGUID          []byte `gorm:"size:16"` // authenticator model
    BackupEligible  bool   // capability flag from the registration
    BackupState     bool   // current state — true once synced cross-device
    Name            string // user-visible label ("iPhone", "Yubikey 5")
    CreatedAt       time.Time
    LastUsedAt      time.Time
}

// OIDCIdentityGORM: one row per (user, provider). A user may link
// multiple providers (GitHub + Google) and a provider's subject is
// stable across logins so re-auth is a lookup, not a re-create.
type OIDCIdentityGORM struct {
    ID        uint
    UserID    uint   `gorm:"index;not null"`
    Provider  string `gorm:"size:64;uniqueIndex:idx_provider_subject"`
    Subject   string `gorm:"size:255;uniqueIndex:idx_provider_subject"`
    Email     string `gorm:"size:255"` // last-known, refreshed on each login
    CreatedAt time.Time
    UpdatedAt time.Time
}
```

V1 AuthSimple has no schema — its `simpleUser` and `simpleGroup`
types are unexported structs in `auth/`, serialised into the session
on `Login` and deserialised by `CurrentUser`.

## 11. Testing

- **CSRF**: `auth.CSRFWrap(sm)` — GET passes; POST without token → 403;
  POST with valid form token → pass; POST with valid header → pass;
  POST with mismatched token → 403; HTMX path mirrors form path.
- **Session**: AuthSimple.Login rotates token; Logout destroys it;
  CSRF token regenerates after login.
- **Authz helpers** (against any `auth.Auth` impl):
  `AuthzLoggedIn` denies anonymous, permits logged-in;
  `AuthzLoggedInReadOnly` permits logged-in reads, denies all writes;
  `AuthzLoggedInReadAdminWrite` permits writes only for users in
  `AdminGroup` (default "admin"); custom `AdminGroup` field honoured.
- **AuthSimple config**: `SetPassword` creates / updates;
  `AddToGroup` adds to existing group / creates new; the chain is
  idempotent; usernames are case-sensitive.
- **AuthSimple routes**: login form renders with `next` hidden
  field; POST with right credentials → 303 to `next` (or
  `AfterLogin` when empty); wrong credentials → re-render with
  error; `LoginURL("/admin")` returns `"/login?next=%2Fadmin"`.
- **`Auth.CurrentUser`**: nil for anonymous; returns the stored
  user after `Login`; survives across requests (session round-trip);
  username/email/groups round-trip intact.

AuthGORM-specific suites:

- **TOTP**: helper roundtrip (generate→validate); two-stage redirect
  when secret set / cleared; pending → session promotion; wrong-code
  rejection; pending state cleared by fresh /login; admin can disable
  others, can't enrol for others.
- **Passkey**: WebAuthn ceremony using `go-webauthn`'s mock
  authenticator — registration end-to-end (begin → finish writes
  PasskeyGORM); login end-to-end (options → finish identifies user,
  finalises session without TOTP); login skips TOTP even when user
  has TOTPSecret; deletion drops the row; sign-counter regression
  rejection.
- **OIDC**: spin up a mini issuer in-test (`oidc-mock-provider` or
  hand-rolled JWKS endpoint). Coverage: state-mismatch rejection;
  PKCE verifier check; user mapping (match-by-subject / match-by-email /
  AutoCreate / no-match → 403); SSO login skips TOTP; relink an
  existing provider replaces the row's Email but preserves the ID.

Most AuthSimple suites stay unchanged — passkeys + SSO are AuthGORM-
only.

## 12. Examples (planned)

| Path                          | Demonstrates                                                  |
|-------------------------------|---------------------------------------------------------------|
| `examples/sessions`           | *reference today.* Hand-rolled CSRF + scs session + simple login. Pre-library impl. |
| `examples/auth_basic`         | `auth.AuthSimple` with admin/admin. Login + a protected page; page shell calls `CurrentUser` and redirects to login when anonymous. |
| `examples/admin_with_auth`    | CRUDTable + Admin gated by `auth.AuthzLoggedInReadAdminWrite{Auth: a}`. AuthSimple with one admin user. |
| `examples/auth_gorm`          | `auth.AuthGORM` + CRUDAdmin over UserGORM/GroupGORM; seed admin/admin in admin group; AuthzLoggedInReadAdminWrite gates writes. |
| `examples/auth_gorm_passkey`  | AuthGORM with passkey enrolment on the account page + passkey login (conditional UI). RP set up for localhost. |
| `examples/auth_gorm_oidc`     | AuthGORM with two OIDC providers (e.g. GitHub + a hand-rolled mock issuer). Demonstrates AutoCreate + DefaultGroups. |

## 13. Open questions

- **Session payload format**: scs uses `encoding/gob` by default;
  registering AuthSimple's user type via `gob.Register` is one line.
  JSON would be debuggable but require its own custom store key.
  Lean gob.
- **`Get` prefix on URL helpers**: the user-facing description named
  these `GetLoginUrl` / `GetLogoutUrl`; this PRD uses `LoginURL` /
  `LogoutURL` to match Go idiom (`http.Request.URL`, no `Get`). Flag
  if you'd rather keep the Get prefix.
- **`User` interface ID**: dropped for v1 (passkey-only users may not
  have a meaningful integer ID; type-assert to the concrete impl
  when you need one). If most authz impls end up needing an ID, add
  `ID() string` later.
- **Login form chrome**: AuthSimple ships an opinionated login templ
  (DaisyUI) wrapped by the caller's `PageShellFunc`. AuthGORM ships
  its own templ — not a shared layer.
- **AdminGroup default**: hardcoded "admin" is the Django convention;
  worth bikeshedding once apps actually use it.
- **`next` validation**: open-redirect risk if POST `/login` redirects
  to an arbitrary `next` value. Validate that `next` is a same-origin
  path; reject absolute URLs and `//host` paths. Standard but worth
  capturing explicitly.
- **Passkey conditional UI default**: ship it on by default? Browsers
  without support degrade gracefully (no autofill — user clicks the
  button). Risk: it surfaces a passkey to anyone who lands on /login,
  which is the whole point, but could surprise users new to passkeys.
  Lean toward "on by default" with an `AuthGORM.PasskeyConditionalUI bool`
  to disable for tests / kiosk deployments.
- **Passkey naming on enrolment**: ask the user explicitly ("Name
  this passkey: ___") or auto-derive from `User-Agent` + AAGUID lookup?
  Hand-name is clearer but adds a step; auto-name is wrong for the
  ~15% of authenticators with unrecognised AAGUIDs. Probably:
  auto-suggest from AAGUID, let user override before saving.
- **User-Agent based passkey defaults are flaky** — UAs lie, especially
  on mobile. Treat the auto-suggested name as a hint, not authority.
- **OIDC AutoCreate default**: off in this PRD (safer). But many
  apps will want it on — every team that uses Google Workspace
  wants @company.com users to log in without admin pre-creation.
  Worth a sample `AutoCreate=true` example with `DefaultGroups`
  scoping ("auto-created users go in 'unverified' group; admin
  promotes").
- **OIDC subject collisions**: two OIDC providers can theoretically
  return the same `sub` value. We index on (Provider, Subject), so
  no DB collision, but the mapping policy needs to be careful not to
  accidentally cross-link. Already handled by the per-provider scope
  of the lookup.
- **Linking accounts**: should the account page let a logged-in user
  link a new SSO provider to their existing account? PRD §6.5.3
  describes `/account/{id}/oidc/link/{name}`. That flow is "/login/oidc"
  reused but with a different "next" target (account page) and a
  "link-only" flag in the session so the callback doesn't create a
  new user. Worth detailing.
- **Backup-eligibility warnings**: WebAuthn surfaces whether a
  credential is synced across devices (`BackupState=true`) or bound
  to one device. Users with a single non-synced passkey should be
  nudged to enrol a second one. Out of scope for v1; tracked as a
  follow-up.
- **`OIDCSubject` on UserGORM was removed**: earlier drafts had a
  single OIDC subject column on UserGORM. Replaced by OIDCIdentityGORM
  rows since a user can link multiple providers. The old field is gone;
  migration for existing AuthGORM tables is the AutoMigrate diff plus
  a one-shot transfer query in §10 once the schema lands.
