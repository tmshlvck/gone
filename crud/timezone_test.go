package crud

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/tmshlvck/gone/site"
)

type tzModel struct {
	ID      uint
	Name    string
	Started time.Time
}

func renderWith(t *testing.T, ctx context.Context, c templ.Component) string {
	t.Helper()
	var sb strings.Builder
	if err := c.Render(ctx, &sb); err != nil {
		t.Fatalf("render: %v", err)
	}
	return sb.String()
}

// A time cell renders in the session zone carried on the context.
func TestTimeDisplay_UsesSessionZone(t *testing.T) {
	zur, _ := time.LoadLocation("Europe/Zurich")
	mm := DeriveMetaModel[tzModel](MetaModel[tzModel]{})
	f, _ := mm.FindField("Started")

	ts := time.Date(2024, 6, 15, 12, 35, 0, 0, time.UTC) // 14:35 in Zurich (CEST)
	cell := f.DisplayValue(*f, ts)

	if got := renderWith(t, context.Background(), cell); got != "2024-06-15 12:35:00 UTC (+00:00)" {
		t.Errorf("default (UTC) = %q", got)
	}
	ctx := site.WithTimezone(context.Background(), zur)
	if got := renderWith(t, ctx, cell); got != "2024-06-15 14:35:00 CEST (+02:00)" {
		t.Errorf("Zurich = %q, want 14:35:00 CEST (+02:00)", got)
	}
}

// The datetime-local input pre-fills the wall clock in the session zone and
// labels that zone.
func TestTimeInput_PrefillAndLabel(t *testing.T) {
	zur, _ := time.LoadLocation("Europe/Zurich")
	mm := DeriveMetaModel[tzModel](MetaModel[tzModel]{})
	f, _ := mm.FindField("Started")
	ts := time.Date(2024, 6, 15, 12, 35, 0, 0, time.UTC)

	ctx := site.WithTimezone(context.Background(), zur)
	got := renderWith(t, ctx, f.GenFormElement(*f, ts))
	if !strings.Contains(got, `value="2024-06-15T14:35"`) {
		t.Errorf("input value not in Zurich wall clock: %q", got)
	}
	if !strings.Contains(got, "CEST (+02:00)") {
		t.Errorf("missing zone label: %q", got)
	}
}

// TryBindForm reinterprets the submitted wall clock in the session zone; the
// stored instant is the zone's, and read-only/unsubmitted times are untouched.
func TestTryBindForm_ZoneAwareRoundTrip(t *testing.T) {
	zur, _ := time.LoadLocation("Europe/Zurich")
	mm := DeriveMetaModel[tzModel](MetaModel[tzModel]{})

	form := url.Values{"Name": {"x"}, "Started": {"2024-06-15T14:35"}}
	req := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(site.WithTimezone(req.Context(), zur))

	var out tzModel
	if err := mm.TryBindForm(req, &out); err != nil {
		t.Fatalf("bind: %v", err)
	}
	want := time.Date(2024, 6, 15, 12, 35, 0, 0, time.UTC) // 14:35 CEST == 12:35Z
	if !out.Started.Equal(want) {
		t.Errorf("Started = %v, want instant %v", out.Started, want)
	}
}

// Without a session zone, bind stays UTC (the wall clock is taken as UTC).
func TestTryBindForm_DefaultUTC(t *testing.T) {
	mm := DeriveMetaModel[tzModel](MetaModel[tzModel]{})
	form := url.Values{"Name": {"x"}, "Started": {"2024-06-15T14:35"}}
	req := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	var out tzModel
	if err := mm.TryBindForm(req, &out); err != nil {
		t.Fatalf("bind: %v", err)
	}
	want := time.Date(2024, 6, 15, 14, 35, 0, 0, time.UTC)
	if !out.Started.Equal(want) {
		t.Errorf("Started = %v, want %v", out.Started, want)
	}
}
