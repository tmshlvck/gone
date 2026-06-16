package site

import (
	"net/http"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
)

// TZMode selects the picker's UI: the full menu or a UTC/browser-local toggle.
type TZMode int

const (
	// TZModeFull offers UTC, Browser-local, and every entry in Zones.
	TZModeFull TZMode = iota
	// TZModeSimple offers only UTC and Browser-local.
	TZModeSimple
)

// TimezonePicker is a navbar control that lets the viewer choose the session
// display zone. It persists the choice in a long-lived cookie (so no session
// store is needed) and exposes a Resolve method to feed TimezoneMiddleware.
//
// The cookie value encodes the chosen *kind* so the menu can re-highlight it:
//
//	utc                  → UTC
//	local:<iana>         → Browser-local (detected zone, sticky)
//	tz:<iana>            → an explicit Zones entry
//
// Selecting an option POSTs via HTMX (so a CSRF-protected app's existing token
// hook applies, and a CSRF-free app just works) and the server replies
// HX-Refresh so every rendered time re-renders in the new zone.
type TimezonePicker struct {
	Prefix string   // route + (default) the POST target; default "/timezone"
	Cookie string   // cookie name; default "gone_tz"
	Mode   TZMode   // Full (default) or Simple
	Zones  []string // offered in Full mode; pass CommonZones for the full list
}

func (p *TimezonePicker) prefix() string {
	if p.Prefix == "" {
		return "/timezone"
	}
	return p.Prefix
}

func (p *TimezonePicker) cookie() string {
	if p.Cookie == "" {
		return "gone_tz"
	}
	return p.Cookie
}

// RegisterRoutes mounts the POST handler that records a selection. r is the
// router the app serves the picker's pages on (so the cookie path covers them).
func (p *TimezonePicker) RegisterRoutes(r chi.Router) {
	r.Post(p.prefix(), p.handleSet)
}

func (p *TimezonePicker) handleSet(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	choice := r.PostFormValue("tz")           // "utc" | "local" | "tz:<iana>"
	browserTz := r.PostFormValue("browserTz") // detected IANA, for "local"

	var val string
	switch {
	case choice == "local":
		z := browserTz
		if _, err := loadLocation(z); err != nil || z == "" {
			z = "UTC"
		}
		val = "local:" + z
	case strings.HasPrefix(choice, "tz:"):
		z := strings.TrimPrefix(choice, "tz:")
		if _, err := loadLocation(z); err != nil {
			val = "utc"
		} else {
			val = "tz:" + z
		}
	default: // "utc" or anything unexpected
		val = "utc"
	}

	SetPref(w, p.cookie(), val)
	// Reload so all rendered times re-render in the new zone.
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusNoContent)
}

// Resolve reads the cookie and returns the session's *time.Location, defaulting
// to UTC. Pass it to TimezoneMiddleware.
func (p *TimezonePicker) Resolve(r *http.Request) *time.Location {
	_, zone := splitChoice(Pref(r, p.cookie()))
	loc, err := loadLocation(zone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// splitChoice parses a cookie value into (kind, ianaZone). "utc" → ("utc", "");
// "local:Europe/Zurich" → ("local", "Europe/Zurich"); "tz:Europe/Zurich" →
// ("tz", "Europe/Zurich").
func splitChoice(v string) (kind, zone string) {
	if i := strings.IndexByte(v, ':'); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}

// Component renders the picker's <select> for the current request, reflecting
// the cookie's current selection.
func (p *TimezonePicker) Component(r *http.Request) templ.Component {
	kind, zone := "utc", ""
	if v := Pref(r, p.cookie()); v != "" {
		kind, zone = splitChoice(v)
	}
	localSuffix := ""
	if kind == "local" && zone != "" {
		localSuffix = " (" + zone + ")"
	}
	zones := p.Zones
	if p.Mode == TZModeSimple {
		zones = nil
	}
	return timezonePicker(tzPickerData{
		PostURL:     p.prefix(),
		Kind:        kind,
		Zone:        zone,
		LocalSuffix: localSuffix,
		Zones:       zones,
	})
}

// tzPickerData drives the timezonePicker templ.
type tzPickerData struct {
	PostURL     string
	Kind        string // "utc" | "local" | "tz"
	Zone        string // selected IANA name (for kind "tz")
	LocalSuffix string // " (Europe/Zurich)" when browser-local is active
	Zones       []string
}
