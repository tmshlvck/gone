package crud

import (
	"strings"
	"testing"
	"time"
)

type sampleConfig struct {
	Hostname string
	Port     int
	Enabled  bool
	Ratio    float64
	Count    uint64
	Started  time.Time
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
	if err := mm.BindForm(mm, form, &out); err != nil {
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
	if err := mm.BindForm(mm, form, &out); err != nil {
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
	err := mm.BindForm(mm, form, &out)
	if err == nil {
		t.Fatal("expected error for non-numeric Port")
	}
	if !strings.Contains(err.Error(), "Port") {
		t.Errorf("error %q should mention the field name", err)
	}
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
	cells := mm.DisplayValues(mm, v)
	if len(cells) != len(mm.Fields) {
		t.Fatalf("DisplayValues returned %d cells, want %d", len(cells), len(mm.Fields))
	}
	for i, c := range cells {
		if c == nil {
			t.Errorf("cell[%d] (%s) is nil", i, mm.Fields[i].Name)
		}
	}
}
