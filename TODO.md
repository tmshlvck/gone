# gone — TODO

What's specced but not built yet, or sketched as a future direction.

## Documentation map

- `gone/crud` design → [`PRD-CRUD.md`](PRD-CRUD.md); user reference
  → [`docs/CRUD.md`](docs/CRUD.md).
- `gone/auth` design → [`PRD-AUTH.md`](PRD-AUTH.md); user reference
  → [`docs/AUTH.md`](docs/AUTH.md).

Everything below is *not* covered by either reference yet.

## SSO (OIDC)

PRD-AUTH §6.5.3 specs federated login via `coreos/go-oidc/v3` +
`golang.org/x/oauth2`. Authorization-code with PKCE, ID-token
verification, per-(user, provider) `OIDCIdentityGORM` rows so a
user can link several providers.

Schema, route layout, user-mapping policy (subject → email →
AutoCreate → 403), session keys (state / verifier / nonce / next),
and the multi-provider login button row are all in the PRD. What's
left is the implementation:

```go
type OIDCProvider struct {
    Name         string   // path segment, e.g. "github"
    DisplayName  string   // button label, e.g. "GitHub"
    IssuerURL    string
    ClientID     string
    ClientSecret string
    Scopes       []string

    AutoCreate    bool     // first-time-from-this-provider: create user?
    DefaultGroups []string // groups for auto-created users
}

func (a *AuthGORM) AddOIDCProvider(p OIDCProvider) error
```

Routes (per registered provider):

```
POST /login/oidc/{name}            — redirect to IdP authorize
GET  /login/oidc/{name}/callback   — verify token, log in (bypasses TOTP)
```

Account-page "Linked accounts" card with Link / Unlink buttons is
part of the same milestone.

## API keys

Selected endpoints accept API keys, authenticated via either:

- `Authorization: Bearer <key>` header, or
- query string for short polls where the route policy allows it.

Keys are hashed at rest. API-key requests bypass CSRF (header-only
auth, no session involved) but still pass authorization. Effective
principal = owning user; all read / write authz decisions use that user.

```go
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

Will land in `gone/auth/apikey.go` (single file, follows the
pattern set by `passkey.go` / `totp_account.go` etc.).

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
- **Authorization**: same authz interface. API keys resolve to their
  owning user before authz runs.
- **CSRF**: skipped for header-authenticated requests, enforced for
  session-cookie requests (defense against XSRF from a browser session
  abusing the JSON endpoint).
- **Content negotiation**: by default JSON endpoints live at a separate
  path (`/heroes` HTML, `/api/heroes` JSON) rather than negotiating
  one URL.

Future: `gone/jsonapi`.

## Other deferred items

- **Per-row authz on CRUDTable**: today's `Authz.CanRead(r)` etc.
  don't see the row ID. Decision (PRD-AUTH): per-row visibility is
  the app's design space — implement `auth.Authz` directly and
  filter at the SQL/data layer. Not planned for the core interface.
- **Software authenticator for passkey unit tests**: the existing
  passkey tests cover schema / routes / IsAuthPath / UI, but the
  full WebAuthn ceremony is exercised live in `examples/auth_gorm`,
  not in unit tests. Adding a mock authenticator (CBOR + ECDSA
  signing) would close the gap.
- **Observability defaults**: structured logs via `log/slog`,
  Prometheus metrics, request IDs.
- **Proxy support**: trust list for `X-Forwarded-*` headers; optional
  PROXY-protocol listener.
- **Plural slug derivation**: today defaults to
  `strings.ToLower(Name) + "s"`, which is wrong for irregular plurals
  (Hero→heros, Person→persons, Sheep→sheeps). A `Pluralize` tag or a
  small dictionary would help.
- **Field-level audit logging**: opt-in via GORM hooks per model.
- **Password reset / email verification**: needs an email-sending
  abstraction the library doesn't have yet. Relevant for
  passwordless users (passkey-only) who lose access to all their
  devices — currently the admin-disable rescue path is the only way
  back.
- **Single Sign-Out (SLO)** for OIDC: destroying the local session
  is sufficient. SLO requires per-provider machinery we're
  intentionally skipping.
