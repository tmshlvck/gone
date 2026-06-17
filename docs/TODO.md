# gone — TODO

Concrete features we intend to build, with enough of a sketch to
start. Design rationale and the longer "maybe someday / pending real
need" list live in [`DESIGN.md`](DESIGN.md); user reference is
[`CRUD.md`](CRUD.md) + [`AUTH.md`](AUTH.md).

Nothing on deck — CSV import/export shipped (below), alongside the 0.9
per-session timezone + theme + configurable pagination work.

## Shipped

### CSV import / export (CRUDTable)

Round-tripping a table's rows through CSV, driven by the existing
`MetaModel` field set. Reached via the table toolbar's **⋮** menu; see
[`CRUD.md`](CRUD.md#csv-export--import).

- **Export** — `GET {base}/export.csv`, streams every row matching the
  current `?q`/`?sort` (filter-then-export). Columns are `ID` + the
  non-hidden scalar `MetaField`s; cells use `formatValue` (the form
  pre-fill stringification). Gated by `Authz.CanList`.
- **Import** — `GET`/`POST {base}/import`, a paste-or-upload form.
  Each row feeds back through `MetaModel.BindForm` (full validation).
  Gated by create or update.

Decisions as built:

- **Upsert by ID**: non-blank `ID` column → update; blank/absent →
  create.
- **PATCH import**: only columns present in the header are bound, so a
  partial CSV updates just those fields and never wipes the rest.
- **Partial failure**: validation is fail-closed (any bad row rejects
  the whole file, nothing written, errors reported inline).
  Persistence is *not* transactional across rows — the `Accessor` has
  no Tx handle. A future optional `TxAccessor` could close that gap.
- **Relations as IDs**: single relation → FK id column (e.g.
  `OwnerID`); many-to-many → `;`-separated id list in one cell;
  has-many (read-only inverse) excluded.
- **`NoExport` flag**: omits a field from export (secrets) while
  keeping it importable; a blank cell on import is left unchanged so a
  secret can't be wiped. Closes the `formatValue`-bypasses-`Redact`
  export leak.
- **Confirm-delete toggle**: a per-browser cookie
  (`gone_crud_confirm_delete`) that suppresses the per-row delete
  confirm dialog — a lightweight stand-in for bulk delete.

Still deferred (pending real need):

- **Natural-key relation columns**: relation columns are IDs only;
  matching related rows by a human-readable key is future work.
- **Bulk edit / Delete All**: checkbox row selection + a per-field
  "set this field" bulk-edit form (NetBox-style), and an explicit
  "Delete All" sweep. Parked — the confirm-delete toggle covers the
  common case; revisit if a real need shows up (likely Admin-only,
  flag-gated).

---

Anything previously listed here that isn't one of the above was either
folded into [`DESIGN.md`](DESIGN.md)'s open-questions log (per-row
authz, SLO, passkey-test mock authenticator, plural-slug derivation,
self-service SSO linking, …) or is parked pending a real need.
