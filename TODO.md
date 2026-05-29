# gone — TODO

Features outside the scope of the two PRDs:

- `gone/crud` design is documented in [`PRD-CRUD.md`](PRD-CRUD.md);
  see [`docs/CRUD.md`](docs/CRUD.md) for the user-facing reference.
- `gone/auth` + `gone/authz` design is in [`PRD-AUTH.md`](PRD-AUTH.md)
  (in flight).

What's collected here is what's *not* covered by either PRD yet.

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

Will land in `gone/auth/apikey/` after the base session/login work.

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

- **Per-row authz on CRUDTable**: today's `Authz.CanRead(r)` etc. don't
  see the row ID. For per-resource grants we'd want
  `CanReadRow(r, id uint)`. Worth doing in the same milestone as
  RBAC — see PRD-AUTH §17 open question.
- **Observability defaults**: structured logs via `log/slog`, Prometheus
  metrics, request IDs.
- **Proxy support**: trust list for `X-Forwarded-*` headers; optional
  PROXY-protocol listener.
- **Plural slug derivation**: today defaults to `strings.ToLower(Name) + "s"`,
  which is wrong for irregular plurals (Hero→heros, Person→persons,
  Sheep→sheeps). A `Pluralize` tag or a small dictionary would help.
- **Field-level audit logging**: opt-in via GORM hooks per model.
- **Multi-DB user federation**: out of scope; the single `User` table
  with optional external-auth links (`OIDCSubject`) is enough.
- **Password reset / email verification**: needs an email-sending
  abstraction the library doesn't have yet.
