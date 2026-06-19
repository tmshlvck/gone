# Changelog

All notable changes to this project are documented here. The format is based
on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Entries describe what changed **for apps that embed gone** — the public API
surface, not internal refactors.

## [0.1.1] - Unreleased

### Removed

- **`CRUDTable.Segment` and `CRUDTableInterface.URLSlug()` are gone.** Set a
  table's URL by passing an explicit `componentPath` to `RegisterRoutes`
  (empty still derives `lowercase(Name)+"s"`, e.g. `Hero`→`/heros`); tables
  under `Admin` always use that derived plural and no longer take a per-table
  override.

### Changed

- **CSV export download filename** is now `<lowercase model name>_table.csv`
  (e.g. `hero_table.csv`), derived from the model name instead of the URL
  slug — independent of where the table is routed.

## [0.1.0]

- Initial release: `gone/crud` (CRUDTable, Admin, MetaModel, validators,
  relation pickers, CSV import/export, Accessor backends), `gone/auth`
  (AuthSimple, AuthGORM, TOTP, passkeys, SSO), and `gone/site` (page shell
  helpers, theme + timezone, source-IP middleware).
