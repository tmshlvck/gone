# gone — TODO

Concrete features we intend to build, with enough of a sketch to
start. Design rationale and the longer "maybe someday / pending real
need" list live in [`DESIGN.md`](DESIGN.md); user reference is
[`CRUD.md`](CRUD.md) + [`AUTH.md`](AUTH.md).

One item on deck (per-session timezone + theme + configurable pagination shipped in 0.9):

## 1. CSV import / export (CRUDTable)

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

---

Anything previously listed here that isn't one of the above was either
folded into [`DESIGN.md`](DESIGN.md)'s open-questions log (per-row
authz, SLO, passkey-test mock authenticator, plural-slug derivation,
self-service SSO linking, …) or is parked pending a real need.
