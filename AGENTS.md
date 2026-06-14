# Repository map

Short pointer for anyone (human or LLM) landing on the repo. The
detail lives in the linked docs.

## Packages

| Path             | Purpose                                                                                                   |
|------------------|-----------------------------------------------------------------------------------------------------------|
| `gone/crud`      | HTMX-driven CRUD UIs from struct metadata. CRUDTable + Admin + MetaModel + validators + relation pickers. |
| `gone/auth`      | Sessions / CSRF / login / authz. AuthSimple + AuthGORM impls. TOTP. Passkeys. Account page.               |
| `gone/htmx`      | Typed HTMX wire protocol: request classification + response-directive builder (Retarget/Reswap/Trigger).  |
| `gone/site`      | Page-composition helpers: the Shell func shape, a Fragment writer, and a fragment-or-page Respond helper.  |

## Documentation

Everything lives under [`docs/`](docs/):

| Doc                              | Scope                                                            |
|----------------------------------|-----------------------------------------------------------------|
| [`docs/CRUD.md`](docs/CRUD.md)   | User reference for `gone/crud` — what's there and how to use it. |
| [`docs/AUTH.md`](docs/AUTH.md)   | User reference for `gone/auth`.                                  |
| [`docs/DESIGN.md`](docs/DESIGN.md) | *Why* it's shaped this way — design decisions + open-questions / future-work log, both packages. |
| [`docs/TODO.md`](docs/TODO.md)   | Active build queue (API keys, CSV import/export, JSON API).      |

Root files:

- [`README.md`](README.md) — overview + quick start + example index.
- [`AGENTS.md`](AGENTS.md) — this file.

## Examples

| Path                            | What it shows                                          |
|---------------------------------|--------------------------------------------------------|
| `examples/form_mem`             | Single-struct form via MetaModel; no CRUDTable.        |
| `examples/crud_mem`             | One CRUDTable over an in-memory map.                   |
| `examples/crud_gorm`            | Three CRUDTables with relations, GORM backend.         |
| `examples/admin_gorm`           | Same schema wrapped in `crud.Admin`. Sidebar custom link demo. |
| `examples/auth_simple`          | `AuthSimple` gating a CRUDTable.                       |
| `examples/auth_gorm`            | Full AuthGORM: User/Group admin, account modal, TOTP, passkeys. |
| `examples/auth_sso`             | `auth_gorm` + SSO (Google / GitHub / Okta) via env vars. |

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
