# gone — TODO

Concrete features we intend to build, with enough of a sketch to
start. Design rationale and the longer "maybe someday / pending real
need" list live in [`DESIGN.md`](DESIGN.md); user reference is
[`CRUD.md`](CRUD.md) + [`AUTH.md`](AUTH.md).

Three things on deck:

## 1. API keys (AuthGORM)

Optional, per-deployment bearer-token credentials owned by a user.
The library ships the model + storage + a verification function and
the account-page UI to manage keys — but wires the check into **no**
routes by default. The app decides which of its own REST endpoints
accept a key.

**Model** — `gone/auth/apikey.go` (single file, mirroring
`passkey.go` / `totp_account.go`):

```go
type APIKeyGORM struct {
    ID         uint
    UserID     uint       `gorm:"index;not null"` // effective principal
    HashedKey  string     `gorm:"uniqueIndex"`     // hash of the raw key, never the key
    Prefix     string     `gorm:"index;size:8"`    // first chars, shown in the UI to identify a key
    Name       string                              // user-supplied label
    LastUsedAt *time.Time
    ExpiresAt  *time.Time                          // nil = no expiry
    Disabled   bool
    CreatedAt  time.Time
}
```

The raw key is shown to the user **once**, at creation; only its hash
is stored. Format something like `gone_<prefix>_<random>`; verify by
hashing the presented key and matching `HashedKey` (+ check
`!Disabled` and `ExpiresAt`).

**Enablement.** A flag on `AuthGORM` (e.g. `EnableAPIKeys bool`). When
set:

- `NewAuthGORM` AutoMigrates `APIKeyGORM`.
- The account page renders an "API keys" card (list with prefix +
  name + last-used + expiry, a "New key" form, per-row Revoke) — same
  shape as the passkeys card. API keys are independent of the login
  method, so they stay available to SSO-only users (unlike password /
  passkey enrolment).
- The add / revoke account routes mount under
  `/account/{ref}/apikey/...`.

When the flag is off: no table, no card, no routes — zero surface.

**Verification function** — exported, always available, wired nowhere
by default:

```go
// AuthenticateAPIKey hashes the presented key, looks up the matching
// non-disabled, non-expired APIKeyGORM, bumps LastUsedAt, and returns
// the owning user as an auth.User (so the app's existing Authz checks
// apply unchanged). Returns ErrInvalidAPIKey on any miss.
func (a *AuthGORM) AuthenticateAPIKey(rawKey string) (User, error)
```

The app calls this from its own handler / middleware for the specific
endpoints that should accept `Authorization: Bearer <key>`. Because
the function returns the owning `User`, all read/write authz decisions
reuse that user's groups — no separate permission model. Key-
authenticated requests carry no session, so they bypass CSRF (there's
no cookie to forge against) but still pass authz.

## 2. CSV import / export (CRUDTable)

Round-trip a table's rows through CSV, driven by the existing
`MetaModel` field set.

**Export** — a toolbar button → `GET {base}/export.csv`. Streams every
matching row (respecting the current `?search` / `?sort`, so "filter
then export" works) as CSV. Columns are the non-hidden `MetaField`s;
cell values come from the same stringification the table uses. Gated
by `Authz.CanList` / `CanRead`.

**Import** — a toolbar "Import" button opening a file-upload form →
`POST {base}/import` (multipart). Parse the header row to map columns
to `MetaField`s, then per data row run `MetaField.BindStrings` + the
validation pipeline, and create (or upsert by ID — decide and
document). Gated by `Authz.CanCreate` / `CanUpdate`.

Open decisions to settle when building:

- **Upsert semantics**: create-only, or update-when-ID-present? Lean
  upsert-by-ID, with create when the ID column is blank.
- **Relations**: how to render / parse N:M and FK columns — likely a
  delimited list of related IDs (or a natural-key lookup). Start with
  IDs; natural keys later.
- **Partial failure**: all-or-nothing in one transaction (clean, but
  one bad row rejects the whole file) vs. per-row with a report. Lean
  all-or-nothing for v1, returning the failing row + validation errors
  inline.

## 3. JSON API from CRUDTable

A `JSONAPI` derived from the same `MetaModel` + data closures a
`CRUDTable` already holds, so an app gets a machine-readable surface
for free alongside the HTML one.

```go
func DeriveJSONAPI[T any](mm *MetaModel[T], az auth.Authz, /* data source */) JSONAPI
func (j *JSONAPI) Route(mux Mux, base string) (string, error)
```

Endpoints:

- `GET    {base}` — list (`?search`, `?sort`, `?offset`, `?limit`)
- `GET    {base}/{id}` — one entity, top-level relations preloaded
- `POST   {base}` — create
- `PUT    {base}/{id}` — update
- `DELETE {base}/{id}` — delete
- `GET    {base}/openapi.json` — spec generated from the `MetaModel`
  (patterns prototyped in [`../openapi/openapi.go`](../openapi/openapi.go))

Auth model, reusing the existing pieces:

- **Authentication**: session cookie, or an API key (item 1) the app
  resolves to its owning user, or anonymous — per route policy.
- **Authorization**: the same `auth.Authz` interface the CRUDTable
  uses; API-key requests resolve to the owning user before authz runs.
- **CSRF**: enforced for cookie-authenticated requests, skipped for
  header-authenticated ones (no session to forge against).
- **Coexistence**: JSON lives at a separate path by default (`/heroes`
  HTML, `/api/heroes` JSON) rather than negotiating one URL by
  `Accept` header.

Lands as `gone/jsonapi` (or `crud/jsonapi.go` if it stays thin).

---

Anything previously listed here that isn't one of the above was either
folded into [`DESIGN.md`](DESIGN.md)'s open-questions log (per-row
authz, SLO, passkey-test mock authenticator, plural-slug derivation,
self-service SSO linking, …) or is parked pending a real need.
