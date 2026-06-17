# gone/site — page chrome, per-session preferences, and deployment helpers

This document is the user-facing reference for
`github.com/tmshlvck/gone/site`. For the CRUD and auth packages see
[`CRUD.md`](CRUD.md) and [`AUTH.md`](AUTH.md); for design rationale see
[`DESIGN.md`](DESIGN.md).

`site` is deliberately small. gone is a library, not a framework: the app
owns its page (head, theme, navigation). `site` holds the cross-cutting,
quality-of-life pieces an embedding app reaches for — composing fragments
into pages, displaying time correctly across sessions, remembering a viewer's
preferences, guaranteeing UTC at rest, and gating access by source IP. Each
piece is independently adoptable; nothing here forces a structure on you.

| Concern | Symbols |
|---------|---------|
| [Page composition](#page-composition) | `Shell`, `Fragment`, `Respond` |
| [UTC at rest](#utc-at-rest) | `ForceUTC` |
| [Per-session timezone](#per-session-timezone) | `Timezone`, `WithTimezone`, `TimezoneMiddleware`, `TimezonePicker`, `CommonZones` |
| [Time formatting & settings](#time-formatting-and-settings) | `TimeFormatter`, `DefaultTimeFormatter`, `FormatTime`, `ZoneLabel`, `Settings`, `PaginationSettings`, `PageSize` |
| [Theme toggle](#theme-toggle) | `ThemeToggle`, `Theme`, `ThemeCookie` |
| [Preference cookies](#preference-cookies) | `SetPref`, `Pref` |
| [Source-IP allow-list](#allowedips--source-ip-allow-list) | `AllowedIPs` |

---

## Page composition

The library emits **HTML fragments**; the surrounding `<html>/<head>/<body>`
is the app's. These three helpers are the seam.

- **`Shell`** is the *type* an app's page-chrome function takes:
  `func(w, r, title string, content templ.Component)`. The library never
  defines one — you write it to wrap content in your document and may also
  redirect or set headers (e.g. bounce an anonymous user to `/login`).
- **`Fragment(w, r, c)`** writes a templ component as a bare HTML response
  (Content-Type set, no chrome). This is what every in-component HTMX
  handler returns.
- **`Respond(w, r, shell, title, content)`** serves a bare fragment to an
  HTMX request or the full `shell`-wrapped page to a browser navigation —
  a convenience for the occasional single URL that must answer both. Most
  apps keep page routes and fragment routes separate and don't need it.

---

## UTC at rest

**`ForceUTC(db *gorm.DB) error`** makes a GORM database store every
`time.Time` in UTC, on any backend (SQLite, Postgres, …). Call it once,
right after `gorm.Open` and before any writes:

```go
db, _ := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
if err := site.ForceUTC(db); err != nil { log.Fatal(err) }
```

Why it matters: a `time.Time` carrying a non-UTC location is otherwise
stored with that offset. SQLite keeps the literal text
(`2024-06-15T14:30:00+02:00`), so two rows that are the *same instant* in
different zones sort and range-filter by their wall-clock string instead of
their instant — a silent correctness bug. `ForceUTC` removes it by both
forcing GORM's auto `CreatedAt`/`UpdatedAt` (via `NowFunc`) and converting
any explicitly-set time field (via before-create/update callbacks) to UTC.
`time.Now()` returns *local* time, so even hand-assigned timestamps need
this. With it, storage is uniformly UTC and per-session zone display becomes
a pure presentation concern on top. See also
[`CRUD.md`](CRUD.md#time-fields-and-utc-storage).

---

## Per-session timezone

Storage is UTC; *display* can follow each viewer's chosen zone. The choice
rides the request context so the library's time cells can read it.

- **`TimezoneMiddleware(resolve func(*http.Request) *time.Location)`** stamps
  each request's context with the location `resolve` returns (nil/zero →
  UTC). `resolve` reads wherever the app keeps the preference.
- **`Timezone(ctx) *time.Location`** is the single read point — CRUD cells,
  form pre-fill, and form bind all consult it. Defaults to UTC.
- **`WithTimezone(ctx, loc)`** stamps a context directly, if you prefer your
  own middleware over `TimezoneMiddleware`.
- **`TimezonePicker`** is a ready-made navbar control: a `<select>` of
  UTC / browser-local / any IANA zone that persists the choice in a
  long-lived cookie (no session store needed) and exposes `Resolve` to feed
  `TimezoneMiddleware`. `TZModeFull` offers the full `Zones` list;
  `TZModeSimple` offers only UTC + browser-local. Selecting an option POSTs
  via HTMX and replies `HX-Refresh` so every rendered time re-renders.
- **`CommonZones`** is a curated IANA list to pass as the picker's `Zones`.

```go
tz := &site.TimezonePicker{Mode: site.TZModeFull, Zones: site.CommonZones}
mux.Use(site.TimezoneMiddleware(tz.Resolve))
tz.RegisterRoutes(mux)
// render tz.Component(r) in your navbar
```

See `examples/crud_gorm` for the full wiring (a Weapon "Forged" time column
that renders and edits in the chosen zone while storage stays UTC).

---

## Time formatting and settings

How a time is *rendered* is app-global policy, deliberately NOT on the
context (non-HTTP paths — emails, PDFs, logs — have no request).

- **`TimeFormatter`** is the one-method interface
  `FormatTime(loc *time.Location, t time.Time) string`. The location is
  passed in (not read from context) so the same formatter works in and out
  of a request; in a request, pair it with the session zone:
  `f.FormatTime(site.Timezone(ctx), t)`.
- **`DefaultTimeFormatter`** renders e.g. `2024-06-15 14:35:00 CEST (+02:00)`
  — wall clock plus a DST-correct abbreviation and numeric offset, so the
  zone is never ambiguous. Zero time → empty string.
- **`FormatTime(loc, t)`** is a package-level convenience using the default.
- **`ZoneLabel(loc, t)`** renders just the zone — `CEST (+02:00)` — for
  marking a form input's active zone.

Override by embedding `DefaultTimeFormatter` and shadowing `FormatTime`;
consumers hold the interface, so dynamic dispatch picks up your override and
embedding keeps you compiling if the interface grows.

**`Settings`** aggregates the app-global config gone components consult —
time formatting plus pagination:

- **`PaginationSettings`** / **`PaginationSizeDefault() uint16`** — default
  rows-per-page (`0` = no pagination, show every row).
- **`DefaultSettings`** — `DefaultTimeFormatter` + a 20-row default. Embed it
  and shadow one method to override a single concern:

  ```go
  type appSettings struct{ site.DefaultSettings }
  func (appSettings) PaginationSizeDefault() uint16 { return 50 }
  ```

- **`PageSize(n)`** returns a `PaginationSettings` with a fixed default for a
  table that wants its own size: `crud.NewTable(mm, data, site.PageSize(10), authz)`.

---

## Theme toggle

A cookie-backed light/dark switch, read by the app's shell (not the
library), so it stays a cookie rather than context.

- **`ThemeToggle(light, dark string)`** is a templ navbar control. On change
  it flips the document's `data-theme` between the two named DaisyUI themes
  *and* writes `ThemeCookie` — instant (no round-trip), and correct on the
  next server render. Any DaisyUI theme pair works (`"corporate"`/`"business"`).
- **`Theme(r, fallback) string`** returns the chosen theme (or `fallback`),
  for the shell to emit server-side and avoid a flash of the wrong theme:

  ```go
  <html data-theme={ site.Theme(r, "light") }>
  ```

- **`ThemeCookie`** is the cookie name the two share.

---

## Preference cookies

The shared storage primitive behind the timezone picker and theme toggle.

- **`SetPref(w, name, value)`** writes a per-browser preference cookie:
  long-lived (one year), path `/`, `SameSite=Lax`, **not** `HttpOnly` (so a
  client-side toggle can read/update it without a round-trip). An empty
  value deletes the cookie.
- **`Pref(r, name) string`** reads one, `""` when absent.

These are for low-stakes, non-secret per-browser settings. When a preference
must be server-owned or per-user, supply your own resolver (e.g. to
`TimezoneMiddleware`) backed by a session or DB column instead.

---

## AllowedIPs — source-IP allow-list

**`AllowedIPs(entries ...string) func(http.Handler) http.Handler`** is chi
middleware that rejects (403) any request whose source address is not inside
one of the allow-list entries. It's the source-IP gate for an internal
dashboard that should answer only known operator networks.

Each entry is a CIDR prefix or a bare address (a bare address is a single
host — `/32` for IPv4, `/128` for IPv6). IPv4 and IPv6 are both supported.

```go
mux.Use(site.AllowedIPs(
    "203.0.113.0/24",   // IPv4 CIDR
    "2001:db8:42::/48", // IPv6 CIDR
    "198.51.100.7",     // single IPv4 host
    "::1",              // single IPv6 host
))
```

Contract and caveats:

- **Matches `r.RemoteAddr`.** Standalone, that's the real client. Behind a
  reverse proxy it's the *proxy* until `chi`'s `middleware.RealIP` rewrites
  it — so placement depends on deployment. **This is the critical bit:** see
  [`BEHIND_PROXY.md`](BEHIND_PROXY.md) for the two correct wirings and why
  enabling `RealIP` while directly exposed is a spoofing hole.
- **O(N) linear scan** over the entries — sized for the handful of operator
  prefixes this is meant for, not large block-lists.
- **Panics at construction on an invalid entry** — a malformed allow-list is
  a startup misconfiguration, caught at boot rather than per request.
- Tolerates both `ip:port` (net/http) and bare `ip` (as `RealIP` may leave)
  in `RemoteAddr`, and unmaps IPv4-in-IPv6 so `::ffff:203.0.113.5` matches a
  plain IPv4 prefix.

---

## See also

- [`BEHIND_PROXY.md`](BEHIND_PROXY.md) — reverse-proxy deployment, `RealIP`,
  and Caddy / nginx / Apache / HAProxy config examples.
- [`CRUD.md`](CRUD.md) — the CRUD package that consumes `Settings`,
  `Timezone`, and `TimeFormatter`.
