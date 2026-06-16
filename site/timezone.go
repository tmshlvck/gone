package site

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// tzCtxKey carries the per-request *time.Location on the context.
type tzCtxKey struct{}

// WithTimezone returns a child context carrying loc as the session's display
// zone. A nil loc is normalized to UTC.
func WithTimezone(ctx context.Context, loc *time.Location) context.Context {
	if loc == nil {
		loc = time.UTC
	}
	return context.WithValue(ctx, tzCtxKey{}, loc)
}

// Timezone returns the location stamped by WithTimezone, or time.UTC when none
// is set. This is the single read point for "which zone does this session
// display times in" — CRUD cells, form pre-fill, and bind all consult it.
func Timezone(ctx context.Context) *time.Location {
	if loc, ok := ctx.Value(tzCtxKey{}).(*time.Location); ok && loc != nil {
		return loc
	}
	return time.UTC
}

// TimezoneMiddleware stamps each request's context with the location returned
// by resolve. resolve reads wherever the app keeps the preference (a cookie —
// see TimezonePicker — a session, or a per-user DB column); a nil resolve or a
// nil/zero result yields UTC. This is the only HTTP glue: an app that prefers
// to can call WithTimezone in its own middleware instead.
func TimezoneMiddleware(resolve func(*http.Request) *time.Location) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var loc *time.Location
			if resolve != nil {
				loc = resolve(r)
			}
			next.ServeHTTP(w, r.WithContext(WithTimezone(r.Context(), loc)))
		})
	}
}

// locCache memoizes name → *time.Location (or the lookup error) so the
// per-request resolver doesn't hit the zoneinfo database every call.
var locCache sync.Map

// loadLocation resolves an IANA name with memoization. "" and "UTC" short-
// circuit to time.UTC.
func loadLocation(name string) (*time.Location, error) {
	if name == "" || name == "UTC" {
		return time.UTC, nil
	}
	if v, ok := locCache.Load(name); ok {
		if loc, ok := v.(*time.Location); ok {
			return loc, nil
		}
		return nil, v.(error)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		locCache.Store(name, err)
		return nil, err
	}
	locCache.Store(name, loc)
	return loc, nil
}
