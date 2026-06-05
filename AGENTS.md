# Repository map

Short pointer for anyone (human or LLM) landing on the repo. The
detail lives in the linked docs.

## Packages

| Path             | Purpose                                                                                                   |
|------------------|-----------------------------------------------------------------------------------------------------------|
| `gone/crud`      | HTMX-driven CRUD UIs from struct metadata. CRUDTable + Admin + MetaModel + validators + relation pickers. |
| `gone/auth`      | Sessions / CSRF / login / authz. AuthSimple + AuthGORM impls. TOTP. Passkeys. Account page.               |
| `gone/openapi`   | (Experimental) OpenAPI spec generation from MetaModel — prototype, not wired into CRUDTable yet.          |

## Documentation

Two flavours of doc per package:

| Audience          | CRUD                        | Auth                        |
|-------------------|-----------------------------|-----------------------------|
| User reference    | [`docs/CRUD.md`](docs/CRUD.md) | [`docs/AUTH.md`](docs/AUTH.md) |
| Design rationale  | [`PRD-CRUD.md`](PRD-CRUD.md)   | [`PRD-AUTH.md`](PRD-AUTH.md)   |

The reference docs explain *what's there and how to use it*. The
PRDs explain *why it's shaped that way* and capture open questions
+ future-direction notes.

Other top-level files:

- [`README.md`](README.md) — overview + quick start + example index.
- [`TODO.md`](TODO.md) — specced but unbuilt (SSO, API keys, JSON API).
- `NOTES.md` — scratchpad, not authoritative.

## Examples

| Path                            | What it shows                                          |
|---------------------------------|--------------------------------------------------------|
| `examples/form_mem`             | Single-struct form via MetaModel; no CRUDTable.        |
| `examples/crud_mem`             | One CRUDTable over an in-memory map.                   |
| `examples/crud_gorm`            | Three CRUDTables with relations, GORM backend.         |
| `examples/admin_gorm`           | Same schema wrapped in `crud.Admin`. Sidebar custom link demo. |
| `examples/auth_simple`          | `AuthSimple` gating a CRUDTable.                       |
| `examples/auth_gorm`            | Full AuthGORM: User/Group admin, account modal, TOTP, passkeys. |

Run any of them with `go run ./examples/<name>`.

## Tests

`go test ./...` — 200+ tests across `gone/crud` and `gone/auth`.
No external services required.

## Conventions

- Library emits HTML *fragments* and JSON; page chrome (head /
  theme / scripts) is the caller's `PageShellFunc`.
- One flat `auth` package — `AuthSimple`, `AuthGORM`, `Authz`, CSRF
  helpers all live together. Mirrors how `crud` keeps `CRUDTable`,
  `Admin`, `MetaModel` in one package.
- Backend selection by constructor: `DeriveMapCRUDTable[T]` for
  in-memory, `DeriveGormCRUDTable[T]` for GORM. Same pattern for
  Auth: `NewAuthSimple` vs `NewAuthGORM`.
- Tests are `_test.go` next to the code, no separate test packages.
