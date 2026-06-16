# gone — TODO

Concrete features we intend to build, with enough of a sketch to
start. Design rationale and the longer "maybe someday / pending real
need" list live in [`DESIGN.md`](DESIGN.md); user reference is
[`CRUD.md`](CRUD.md) + [`AUTH.md`](AUTH.md).

Two things on deck:

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

## 2. Per-session timezone (gone/site + CRUDTable + auth)

Storage is already UTC everywhere — `site.ForceUTC(db)` (done) normalizes
every `time.Time` to UTC at write, so SQL sort / range / compare operate on
the instant. This item is **presentation + input only**: let each session
*display* and *enter* times in a chosen zone while the stored instant stays
UTC. Three modes:

1. **UTC** — the default.
2. **Browser-local** — the viewer's own zone, detected client-side.
3. **Explicit** — a named IANA zone (e.g. `Europe/Zurich`).

Two cleanly separated concerns, by scope:

- **Which offset — request-scoped → context.** One per-session `*time.Location`
  resolved per request; display, form pre-fill, and bind all read it, so a
  round-trip is consistent (what you see in zone Z you edit in zone Z).
- **How to render — app-global → an injected `TimeFormatter`.** *Not* on the
  context: the same formatter is reused outside HTTP (emails, PDFs, logs), so
  it's an object the app owns and hands to components, defaulting to
  `DefaultTimeFormatter`. (Reserve a `Formats` aggregate name for later
  money / measure formatters; ship only `TimeFormatter` now.)

**`gone/site`:**

```go
// Request-scoped zone.
func WithTimezone(ctx context.Context, loc *time.Location) context.Context
func Timezone(ctx context.Context) *time.Location // defaults to time.UTC
func TimezoneMiddleware(resolve func(*http.Request) *time.Location) func(http.Handler) http.Handler

// App-global formatting policy. Interface + embeddable default so an app
// overrides by embedding and shadowing one method (dynamic dispatch via the
// interface). Used by crud + auth + the app's own non-HTTP code.
type TimeFormatter interface { FormatTime(loc *time.Location, t time.Time) string }
type DefaultTimeFormatter struct{} // "2006-01-02 15:04:05 MST (-07:00)"; blank if zero
func FormatTime(loc *time.Location, t time.Time) string // convenience: DefaultTimeFormatter

var CommonZones []string // curated IANA list for the picker's full menu
```

**Timezone picker — `site.TimezonePicker`** (cookie-backed, no session needed):

```go
type TZMode int
const (
    TZModeFull   TZMode = iota // UTC / Browser-local / each Zones entry
    TZModeSimple               // UTC / Browser-local only
)
type TimezonePicker struct {
    Prefix string
    Cookie string   // default "gone_tz"
    Mode   TZMode
    Zones  []string // offered in Full mode; pass site.CommonZones for all
}
func (p *TimezonePicker) RegisterRoutes(r chi.Router)        // POST {Prefix} → set cookie → HX-Refresh
func (p *TimezonePicker) Resolve(r *http.Request) *time.Location // for TimezoneMiddleware
func (p *TimezonePicker) Component(r *http.Request) templ.Component // navbar <select>
```

Cookie encodes the *kind* so the UI can highlight the right option:
`utc` | `local:<iana>` | `tz:<iana>`. Browser-local is detect-once / sticky:
the option submits `Intl.DateTimeFormat().resolvedOptions().timeZone` (via
HTMX `hx-vals`), stored as a concrete zone. The picker POSTs via `hx-post`, so
a CSRF-protected app's existing htmx token hook covers it and a CSRF-free app
(crud_gorm) just works. `LoadLocation` results are memoized.

**Consuming it — `gone/crud`:**

- *Display*: the time-cell hook returns a `templ.ComponentFunc` reading
  `site.Timezone(ctx)` and formatting via the table's `TimeFormatter`
  (`MetaModel.TimeFormatter`, default `DefaultTimeFormatter`). Custom per-field
  formatting still goes through the existing `DisplayValue` hook.
- *Form pre-fill*: the `datetime-local` input renders `t.In(loc)` as the
  zone-less `2006-01-02T15:04` value **plus an adjacent zone label**
  (`CEST (+02:00)`) so the active zone is never a guess. Fixed input layout
  (not formatter-driven — the browser widget requires it).
- *Bind*: `TryBindForm(r, out)` resolves `loc` from `r.Context()`; after the
  normal UTC `BindForm` it reinterprets each `time.Time` / `*time.Time`
  field's zone-less wall clock in `loc` (skipping zero / nil), and `ForceUTC`
  stores UTC. Raw `BindForm(form, out)` stays UTC for tests / non-HTTP callers.

**Consuming it — `gone/auth`:** passkey / SSO "last used" (and any account-page
time) format through the same `TimeFormatter` + `site.Timezone(r.Context())`,
so all of gone's time output is consistent. `AuthGORM.TimeFormatter` overrides;
default `DefaultTimeFormatter`.

**Showcase — `examples/crud_gorm`:** wrap the mux with
`site.TimezoneMiddleware(picker.Resolve)`, mount `picker.RegisterRoutes`, render
`picker.Component(r)` in the navbar (`TZModeFull`, `Zones: site.CommonZones`),
so the Weapon `Forged` column and edit form switch between UTC / browser-local /
Europe/Zurich live.

**App-side variants (no extra library surface):** a DB-saved per-user
preference = the app supplies its own `resolve` (reads the saved zone) and
persists on selection; a 3-way UTC/saved/browser-local toggle = `TZModeSimple`
plus that resolve. The `auth` persistence (a `UserGORM` column + account-page
control) is a later follow-on.

Out of scope for v1: date-only / time-only (`date` / `time` input) fields —
no instant to zone-shift; money / measure formatters (the `Formats` aggregate).

---

Anything previously listed here that isn't one of the above was either
folded into [`DESIGN.md`](DESIGN.md)'s open-questions log (per-row
authz, SLO, passkey-test mock authenticator, plural-slug derivation,
self-service SSO linking, …) or is parked pending a real need.
