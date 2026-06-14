package crud

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/a-h/templ"
)

type cfgHero struct {
	ID     uint
	Name   string
	Realm  string
	Power  int
	Active bool
}

func newCfgStore() (map[uint]cfgHero, *sync.RWMutex) {
	return map[uint]cfgHero{1: {ID: 1, Name: "Aragorn"}}, &sync.RWMutex{}
}

// mustField fetches a field by name, failing the test if absent.
func mustField[T any](t *testing.T, mm *MetaModel[T], name string) *MetaField {
	t.Helper()
	f, err := mm.FindField(name)
	if err != nil {
		t.Fatalf("FindField(%q): %v", name, err)
	}
	return f
}

// TestDeriveMetaModelMergesPreset covers the overlay rules: preset's
// non-empty DisplayName / per-field strings / hooks / additive flags win over
// the reflected defaults; NewTable then enables mutations by default.
func TestDeriveMetaModelMergesPreset(t *testing.T) {
	mm := DeriveMetaModel[cfgHero](MetaModel[cfgHero]{
		DisplayName: "Heroes",
		Fields: []MetaField{
			{Name: "ID", ReadOnly: true},
			{Name: "Name", DisplayName: "Full name", FormHelp: "2–40 chars.", FieldValidate: All(NotEmpty, MaxLen(40))},
		},
	})
	if mm.DisplayName != "Heroes" {
		t.Errorf("DisplayName = %q, want Heroes", mm.DisplayName)
	}
	if id := mustField(t, &mm, "ID"); !id.ReadOnly {
		t.Error("ID.ReadOnly = false, want true")
	}
	name := mustField(t, &mm, "Name")
	if name.DisplayName != "Full name" {
		t.Errorf("Name.DisplayName = %q, want Full name", name.DisplayName)
	}
	if name.FormHelp != "2–40 chars." {
		t.Errorf("Name.FormHelp = %q", name.FormHelp)
	}
	if name.FieldValidate == nil {
		t.Error("Name.FieldValidate not set")
	}

	store, mu := newCfgStore()
	tbl := NewTable(mm, MapAccessor(mm, store, mu), 7, nil)
	if tbl.PageSize != 7 {
		t.Errorf("PageSize = %d, want 7", tbl.PageSize)
	}
	if !tbl.CreateEnabled || !tbl.EditEnabled || !tbl.DeleteEnabled {
		t.Error("expected create/edit/delete enabled by default")
	}
}

// TestDeriveMetaModelDefaults covers the zero preset: type-name DisplayName
// and reflected per-field defaults left intact.
func TestDeriveMetaModelDefaults(t *testing.T) {
	mm := DeriveMetaModel[cfgHero](MetaModel[cfgHero]{})
	if mm.DisplayName != "cfgHero" {
		t.Errorf("default DisplayName = %q, want cfgHero", mm.DisplayName)
	}
	name := mustField(t, &mm, "Name")
	if name.ReadOnly {
		t.Error("Name unexpectedly read-only")
	}
	if !name.Searchable {
		t.Error("Name should stay searchable (reflected default)")
	}
	// URLSlug falls back to a lowercased plural of the model name.
	store, mu := newCfgStore()
	tbl := NewTable(mm, MapAccessor(mm, store, mu), 0, nil)
	if tbl.URLSlug() != "cfgheros" {
		t.Errorf("default URLSlug = %q, want cfgheros", tbl.URLSlug())
	}
}

// TestSegmentOverridesURLSlug covers the irregular-plural escape hatch.
func TestSegmentOverridesURLSlug(t *testing.T) {
	mm := DeriveMetaModel[cfgHero](MetaModel[cfgHero]{})
	store, mu := newCfgStore()
	tbl := NewTable(mm, MapAccessor(mm, store, mu), 0, nil)
	tbl.Segment = "heroes"
	if tbl.URLSlug() != "heroes" {
		t.Errorf("URLSlug = %q, want heroes", tbl.URLSlug())
	}
}

func TestUnknownPresetFieldPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for unknown preset field name")
		}
	}()
	_ = DeriveMetaModel[cfgHero](MetaModel[cfgHero]{
		Fields: []MetaField{{Name: "Nmae", FormHelp: "typo"}}, // misspelled "Name"
	})
}

func TestNonStructPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for non-struct T")
		}
	}()
	_ = DeriveMetaModel[int](MetaModel[int]{})
}

type secretModel struct {
	ID           uint
	Name         string
	PasswordHash string
	Handle       []byte
}

func renderHook(t *testing.T, c templ.Component) string {
	t.Helper()
	var sb strings.Builder
	if err := c.Render(context.Background(), &sb); err != nil {
		t.Fatalf("render: %v", err)
	}
	return sb.String()
}

// TestRedactHelper covers crud.Redact as a DisplayValue hook (paired with
// ReadOnly to make a shown-but-uneditable secret field).
func TestRedactHelper(t *testing.T) {
	mm := DeriveMetaModel[secretModel](MetaModel[secretModel]{
		Fields: []MetaField{{Name: "Handle", ReadOnly: true, DisplayValue: Redact}},
	})
	h := mustField(t, &mm, "Handle")
	if !h.ReadOnly {
		t.Error("Handle should be ReadOnly (shown, not bound)")
	}
	cases := []struct {
		val  any
		want string
	}{
		{"argon2$hash", "-hidden-"},
		{"", "-empty-"},
		{[]byte{1, 2}, "-hidden-"},
		{[]byte{}, "-empty-"},
	}
	for _, c := range cases {
		got := renderHook(t, Redact(*h, c.val))
		if !strings.Contains(got, c.want) || !strings.Contains(got, "italic") {
			t.Errorf("Redact(%v) = %q, want italic %q", c.val, got, c.want)
		}
	}
}

// TestPasswordFieldViaHelpers composes a write-only password field from the
// generic hooks + Redact / PasswordInput / HashWith — no bespoke field flag.
func TestPasswordFieldViaHelpers(t *testing.T) {
	mm := DeriveMetaModel[secretModel](MetaModel[secretModel]{
		Fields: []MetaField{{
			Name: "PasswordHash", DisplayName: "Password", FormInputType: "password",
			DisplayValue:   Redact,
			GenFormElement: PasswordInput,
			BindStrings:    HashWith(func(pw string) (string, error) { return "H(" + pw + ")", nil }),
		}},
	})
	f := mustField(t, &mm, "PasswordHash")

	// Empty password box — the stored hash never leaks into the form.
	form := renderHook(t, f.GenFormElement(*f, "argon2$secret"))
	if !strings.Contains(form, `type="password"`) || !strings.Contains(form, `value=""`) {
		t.Errorf("password input = %q, want empty type=password box", form)
	}
	if strings.Contains(form, "argon2$secret") {
		t.Error("stored hash leaked into the form input")
	}
	// Display redacted.
	if got := renderHook(t, f.DisplayValue(*f, "argon2$secret")); !strings.Contains(got, "-hidden-") {
		t.Errorf("display = %q, want redacted -hidden-", got)
	}
	// Non-blank input is hashed into the field.
	m := secretModel{PasswordHash: "old"}
	if err := f.BindStrings(*f, []string{"newpw"}, &m); err != nil {
		t.Fatalf("BindStrings: %v", err)
	}
	if m.PasswordHash != "H(newpw)" {
		t.Errorf("PasswordHash = %q, want H(newpw)", m.PasswordHash)
	}
	// Blank / whitespace-only keeps the existing value.
	keep := secretModel{PasswordHash: "kept"}
	for _, blank := range []string{"", "   "} {
		if err := f.BindStrings(*f, []string{blank}, &keep); err != nil {
			t.Fatalf("BindStrings(%q): %v", blank, err)
		}
		if keep.PasswordHash != "kept" {
			t.Errorf("blank input %q changed PasswordHash to %q", blank, keep.PasswordHash)
		}
	}
}

func TestShortLabelOverride(t *testing.T) {
	type svHero struct {
		ID    uint
		Name  string
		Realm string
	}
	type svWeapon struct {
		ID      uint
		Name    string
		OwnerID uint
		Owner   svHero
	}
	hmm := DeriveMetaModel[svHero](MetaModel[svHero]{})
	htbl := NewTable(hmm, MapAccessor(hmm, map[uint]svHero{}, &sync.RWMutex{}), 0, nil)
	htbl.ShortLabel = func(h svHero) string { return h.Name + " (" + h.Realm + ")" }
	wmm := DeriveMetaModel[svWeapon](MetaModel[svWeapon]{})
	wtbl := NewTable(wmm, MapAccessor(wmm, map[uint]svWeapon{}, &sync.RWMutex{}), 0, nil)

	// The override drives this table's own label (used by its /options).
	if got := htbl.InstanceShortLabel(svHero{Name: "Aragorn", Realm: "Gondor"}); got != "Aragorn (Gondor)" {
		t.Errorf("InstanceShortLabel = %q, want Aragorn (Gondor)", got)
	}
	// And WireRelations propagates it to a related table's relation cell.
	WireRelations(&htbl, &wtbl)
	owner := mustField(t, &wtbl.MetaData, "Owner")
	if owner.RelatedShortLabel == nil {
		t.Fatal("Owner.RelatedShortLabel not stamped by WireRelations")
	}
	if got := owner.RelatedShortLabel(svHero{Name: "Aragorn", Realm: "Gondor"}); got != "Aragorn (Gondor)" {
		t.Errorf("relation cell label = %q, want Aragorn (Gondor)", got)
	}
}

func TestModelValidateWired(t *testing.T) {
	sentinel := func(cfgHero) error { return nil }
	mm := DeriveMetaModel[cfgHero](MetaModel[cfgHero]{Validate: sentinel})
	if mm.Validate == nil {
		t.Error("model-level Validate not wired onto MetaModel from preset")
	}
}
