package crud

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
)

type sampleConfig struct {
	Hostname string
	Port     int
	Enabled  bool
	Ratio    float64
	Count    uint64
	Started  time.Time
}

type blobModel struct {
	ID     uint
	Handle []byte // e.g. an opaque WebAuthn handle — a BLOB column
}

// TestByteSliceBindAndDisplay covers the []byte robustness fix: a byte-slice
// field must bind (UTF-8 string → bytes, empty → empty) instead of erroring
// "unsupported kind slice" — which previously blocked creating any row with
// such a field — and display as its string (HTML-escaped).
func TestByteSliceBindAndDisplay(t *testing.T) {
	mm, err := DeriveMetaModel[blobModel]()
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	h := mm.MustFindField("Handle")
	if h.RelationKind != NotRelation {
		t.Fatalf("Handle should be a scalar field, got relation kind %v", h.RelationKind)
	}

	// Empty string → empty bytes, no error (the actual create blocker).
	var empty blobModel
	if err := h.BindStrings(*h, []string{""}, &empty); err != nil {
		t.Fatalf("BindStrings empty: %v", err)
	}
	if len(empty.Handle) != 0 {
		t.Errorf("empty Handle len = %d, want 0", len(empty.Handle))
	}

	// Non-empty string → its UTF-8 bytes.
	var m blobModel
	if err := h.BindStrings(*h, []string{"hi"}, &m); err != nil {
		t.Fatalf("BindStrings: %v", err)
	}
	if string(m.Handle) != "hi" {
		t.Errorf("Handle = %q, want hi", m.Handle)
	}

	// Display renders the bytes as their string, HTML-escaped.
	var sb strings.Builder
	if err := h.DisplayValue(*h, []byte("a<b")).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render: %v", err)
	}
	if got := sb.String(); got != "a&lt;b" {
		t.Errorf("DisplayValue = %q, want a&lt;b", got)
	}
}

func TestDeriveMetaModel_FieldsAndInputTypes(t *testing.T) {
	mm, err := DeriveMetaModel[sampleConfig]()
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if mm.Name != "sampleConfig" {
		t.Errorf("Name = %q, want sampleConfig", mm.Name)
	}
	if len(mm.Fields) != 6 {
		t.Fatalf("expected 6 fields, got %d", len(mm.Fields))
	}
	want := []struct {
		name, input string
	}{
		{"Hostname", "text"},
		{"Port", "number"},
		{"Enabled", "checkbox"},
		{"Ratio", "number"},
		{"Count", "number"},
		{"Started", "datetime-local"},
	}
	for i, w := range want {
		if mm.Fields[i].Name != w.name {
			t.Errorf("field[%d].Name = %q, want %q", i, mm.Fields[i].Name, w.name)
		}
		if mm.Fields[i].FormInputType != w.input {
			t.Errorf("field[%d].FormInputType = %q, want %q",
				i, mm.Fields[i].FormInputType, w.input)
		}
	}
}

func TestDeriveMetaModel_SortableSearchable(t *testing.T) {
	mm, _ := DeriveMetaModel[sampleConfig]()
	for _, mf := range mm.Fields {
		if !mf.Sortable {
			t.Errorf("%s should be Sortable (all scalar types are)", mf.Name)
		}
	}
	got := map[string]bool{}
	for _, mf := range mm.Fields {
		got[mf.Name] = mf.Searchable
	}
	if !got["Hostname"] {
		t.Error("Hostname (string) should be Searchable")
	}
	if got["Port"] || got["Enabled"] || got["Ratio"] || got["Count"] || got["Started"] {
		t.Error("non-string fields should not be Searchable by default")
	}
}

func TestDefaultBindForm_AllScalars(t *testing.T) {
	mm, _ := DeriveMetaModel[sampleConfig]()
	form := map[string][]string{
		"Hostname": {"box.example.com"},
		"Port":     {"443"},
		"Enabled":  {"off", "on"}, // hidden+checkbox pair
		"Ratio":    {"0.75"},
		"Count":    {"99"},
		"Started":  {"2026-05-25T10:30"},
	}
	var out sampleConfig
	if err := mm.BindForm(form, &out); err != nil {
		t.Fatalf("BindForm: %v", err)
	}
	if out.Hostname != "box.example.com" {
		t.Errorf("Hostname = %q", out.Hostname)
	}
	if out.Port != 443 {
		t.Errorf("Port = %d", out.Port)
	}
	if !out.Enabled {
		t.Error("Enabled should be true")
	}
	if out.Ratio != 0.75 {
		t.Errorf("Ratio = %v", out.Ratio)
	}
	if out.Count != 99 {
		t.Errorf("Count = %d", out.Count)
	}
	wantTime := time.Date(2026, 5, 25, 10, 30, 0, 0, time.UTC)
	if !out.Started.Equal(wantTime) {
		t.Errorf("Started = %v, want %v", out.Started, wantTime)
	}
}

func TestDefaultBindForm_UncheckedCheckbox(t *testing.T) {
	// An unchecked checkbox sends only the paired hidden "off" value.
	mm, _ := DeriveMetaModel[sampleConfig]()
	out := sampleConfig{Enabled: true} // start true; bind should flip to false
	form := map[string][]string{
		"Enabled": {"off"},
	}
	if err := mm.BindForm(form, &out); err != nil {
		t.Fatalf("BindForm: %v", err)
	}
	if out.Enabled {
		t.Error("Enabled should be false after unchecked-checkbox bind")
	}
}

func TestDefaultBindForm_InvalidNumberFails(t *testing.T) {
	mm, _ := DeriveMetaModel[sampleConfig]()
	form := map[string][]string{"Port": {"notnumber"}}
	var out sampleConfig
	err := mm.BindForm(form, &out)
	if err == nil {
		t.Fatal("expected error for non-numeric Port")
	}
	if !strings.Contains(err.Error(), "Port") {
		t.Errorf("error %q should mention the field name", err)
	}
}

func TestFindField(t *testing.T) {
	mm, _ := DeriveMetaModel[sampleConfig]()
	f, err := mm.FindField("Port")
	if err != nil {
		t.Fatalf("FindField(Port): %v", err)
	}
	// Mutate via the returned pointer; mm.Fields should see the change.
	f.FormHelp = "TCP port"
	if mm.Fields[1].FormHelp != "TCP port" {
		t.Errorf("FindField must return a pointer into mm.Fields, got copy")
	}
	if _, err := mm.FindField("Nope"); err == nil {
		t.Error("expected error for unknown field")
	}
}

func TestMustFindField(t *testing.T) {
	mm, _ := DeriveMetaModel[sampleConfig]()
	mm.MustFindField("Port").FormHelp = "TCP port"
	if mm.Fields[1].FormHelp != "TCP port" {
		t.Errorf("MustFindField did not mutate in place")
	}
	defer func() {
		if r := recover(); r == nil {
			t.Error("MustFindField should panic on missing field")
		}
	}()
	mm.MustFindField("Nope")
}

func TestDefaultDisplayValues_RenderShape(t *testing.T) {
	mm, _ := DeriveMetaModel[sampleConfig]()
	v := sampleConfig{
		Hostname: "box",
		Port:     80,
		Enabled:  true,
		Ratio:    1.5,
		Count:    7,
		Started:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	cells := mm.DisplayValues(v)
	if len(cells) != len(mm.Fields) {
		t.Fatalf("DisplayValues returned %d cells, want %d", len(cells), len(mm.Fields))
	}
	for i, c := range cells {
		if c == nil {
			t.Errorf("cell[%d] (%s) is nil", i, mm.Fields[i].Name)
		}
	}
}

func TestDefaultDisplayValue_BoolBadgeAndUTCTime(t *testing.T) {
	render := func(c templ.Component) string {
		var sb strings.Builder
		if err := c.Render(context.Background(), &sb); err != nil {
			t.Fatalf("render: %v", err)
		}
		return sb.String()
	}
	mf := MetaField{Name: "X"}

	if got := render(DefaultDisplayValue(mf, true)); !strings.Contains(got, "badge-success") || !strings.Contains(got, "yes") {
		t.Errorf("bool true display = %q, want green yes badge", got)
	}
	if got := render(DefaultDisplayValue(mf, false)); !strings.Contains(got, "badge-error") || !strings.Contains(got, "no") {
		t.Errorf("bool false display = %q, want red no badge", got)
	}

	// A non-UTC instant is shown converted to UTC, with the suffix.
	loc := time.FixedZone("CET", 2*3600)
	ts := time.Date(2026, 1, 2, 15, 4, 5, 0, loc) // 13:04:05 UTC
	if got := render(DefaultDisplayValue(mf, ts)); got != "2026-01-02 13:04:05 UTC" {
		t.Errorf("time display = %q, want 2026-01-02 13:04:05 UTC", got)
	}
	if got := render(DefaultDisplayValue(mf, time.Time{})); got != "" {
		t.Errorf("zero time display = %q, want empty", got)
	}
}

func TestDefaultShortLabel_Stages(t *testing.T) {
	type withName struct {
		ID   uint
		Name string
	}
	type withLabel struct {
		ID    uint
		Label string
	}
	type withTitle struct {
		ID    uint
		Title string
	}
	type withFullName struct {
		ID       uint
		FullName string
	}
	type withJobTitle struct {
		ID       uint
		JobTitle string
	}
	type onlyID struct {
		ID    uint
		Width int // contains "id" but must NOT be picked as the identifier
	}
	type onlyFK struct{ OwnerID uint }
	type emptyNameThenLabel struct {
		Name  string
		Label string
	}
	type nothing struct{ Active bool }

	cases := []struct {
		name string
		in   any
		want string
	}{
		{"name wins, no id prefix", withName{5, "Aragorn"}, "Aragorn"},
		{"label", withLabel{5, "Gondor"}, "Gondor"},
		{"title", withTitle{5, "The Two Towers"}, "The Two Towers"},
		{"contains-name", withFullName{5, "Frodo Baggins"}, "Frodo Baggins"},
		{"contains-title", withJobTitle{5, "Steward"}, "Steward"},
		{"id only (Width not mistaken for id)", onlyID{7, 0}, "#7"},
		{"fk suffix", onlyFK{9}, "#9"},
		{"empty Name falls through to Label", emptyNameThenLabel{"", "Shire"}, "Shire"},
		{"json last resort", nothing{Active: true}, `{"Active":true}`},
	}
	for _, c := range cases {
		if got := DefaultShortLabel(c.in); got != c.want {
			t.Errorf("%s: DefaultShortLabel = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestNormalizePrefix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"/", ""},
		{"/admin", "/admin"},
		{"/admin/", "/admin"},
		{"/api/v1", "/api/v1"},
		{"/api/v1/", "/api/v1"},
		{"/api/v1//", "/api/v1"},
	}
	for _, c := range cases {
		if got := normalizePrefix(c.in); got != c.want {
			t.Errorf("normalizePrefix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
