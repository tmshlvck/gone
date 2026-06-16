package site

import "time"

// TimeFormatter renders a time.Time, already resolved to a location, as a
// human-facing string. It is app-global presentation policy — an object the
// app owns and reuses everywhere it shows a time (CRUD cells, auth "last
// used", and its own emails / PDFs / logs), deliberately NOT carried on the
// request context (those non-HTTP paths have none).
//
// Override by embedding DefaultTimeFormatter and shadowing FormatTime; because
// consumers hold the interface, dynamic dispatch picks up the override, and
// embedding keeps you compiling if the interface grows (a future Formats
// aggregate may add FormatMoney / FormatMeasure).
//
// The location is passed in, not read from context, so the same formatter
// works in and out of an HTTP request. In a request, callers pair it with the
// session zone: f.FormatTime(site.Timezone(ctx), t).
type TimeFormatter interface {
	FormatTime(loc *time.Location, t time.Time) string
}

// defaultTimeLayout renders the wall clock, the zone abbreviation, and the
// numeric offset — e.g. "2024-06-15 14:35:00 CEST (+02:00)" — so the zone is
// unambiguous even for abbreviations a reader might not know. Both the
// abbreviation and the offset are DST-correct because they come from the
// value's own instant.
const defaultTimeLayout = "2006-01-02 15:04:05 MST (-07:00)"

// DefaultTimeFormatter is the canonical TimeFormatter. A zero time renders as
// the empty string; a nil location is treated as UTC.
type DefaultTimeFormatter struct{}

func (DefaultTimeFormatter) FormatTime(loc *time.Location, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format(defaultTimeLayout)
}

// FormatTime is a convenience that formats with DefaultTimeFormatter — for
// callers that don't need a custom policy.
func FormatTime(loc *time.Location, t time.Time) string {
	return DefaultTimeFormatter{}.FormatTime(loc, t)
}

// ZoneLabel renders just the zone of t in loc — "CEST (+02:00)" — for marking
// a form input's active zone. Uses the instant t (so the offset is DST-correct
// for the value being edited); a zero t falls back to "now".
func ZoneLabel(loc *time.Location, t time.Time) string {
	if loc == nil {
		loc = time.UTC
	}
	if t.IsZero() {
		t = time.Now()
	}
	return t.In(loc).Format("MST (-07:00)")
}
