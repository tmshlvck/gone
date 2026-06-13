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

func TestNewMapTableAppliesRecipe(t *testing.T) {
	store, mu := newCfgStore()
	tbl := NewMapTable(store, mu, Table[cfgHero]{
		Slug:     "heroes",
		Title:    "Heroes",
		PageSize: 7,
		Fields: Fields{
			"ID":   {ReadOnly: true},
			"Name": {Label: "Full name", Help: "2–40 chars.", Validate: All(NotEmpty, MaxLen(40))},
		},
	})

	if tbl.Slug != "heroes" {
		t.Errorf("Slug = %q, want heroes", tbl.Slug)
	}
	if tbl.PageSize != 7 {
		t.Errorf("PageSize = %d, want 7", tbl.PageSize)
	}
	if tbl.MetaData.DisplayName != "Heroes" {
		t.Errorf("DisplayName = %q, want Heroes", tbl.MetaData.DisplayName)
	}
	id := tbl.MetaData.MustFindField("ID")
	if !id.ReadOnly {
		t.Error("ID.ReadOnly = false, want true")
	}
	name := tbl.MetaData.MustFindField("Name")
	if name.DisplayName != "Full name" {
		t.Errorf("Name.DisplayName = %q, want Full name", name.DisplayName)
	}
	if name.FormHelp != "2–40 chars." {
		t.Errorf("Name.FormHelp = %q", name.FormHelp)
	}
	if name.FieldValidate == nil {
		t.Error("Name.FieldValidate not set")
	}
	// Mutations enabled by default.
	if !tbl.CreateEnabled || !tbl.EditEnabled || !tbl.DeleteEnabled {
		t.Error("expected create/edit/delete enabled by default")
	}
}

func TestNewMapTableDefaults(t *testing.T) {
	store, mu := newCfgStore()
	tbl := NewMapTable(store, mu, Table[cfgHero]{})
	// Default slug = lowercase(type)+"s".
	if tbl.Slug != "cfgheros" {
		t.Errorf("default Slug = %q, want cfgheros", tbl.Slug)
	}
	// Title defaults to the type name.
	if tbl.MetaData.DisplayName != "cfgHero" {
		t.Errorf("default DisplayName = %q, want cfgHero", tbl.MetaData.DisplayName)
	}
	// Untouched fields keep reflected defaults: Name is searchable, not read-only.
	name := tbl.MetaData.MustFindField("Name")
	if name.ReadOnly {
		t.Error("Name unexpectedly read-only")
	}
	if !name.Searchable {
		t.Error("Name should stay searchable (reflected default)")
	}
}

func TestReadOnlyDisablesMutations(t *testing.T) {
	store, mu := newCfgStore()
	tbl := NewMapTable(store, mu, Table[cfgHero]{ReadOnly: true})
	if tbl.CreateEnabled || tbl.EditEnabled || tbl.DeleteEnabled {
		t.Errorf("ReadOnly recipe should disable all mutations; got C=%v E=%v D=%v",
			tbl.CreateEnabled, tbl.EditEnabled, tbl.DeleteEnabled)
	}
}

func TestUnknownFieldPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for unknown field name")
		}
	}()
	store, mu := newCfgStore()
	_ = NewMapTable(store, mu, Table[cfgHero]{
		Fields: Fields{"Nmae": {Help: "typo"}}, // misspelled "Name"
	})
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
	store := map[uint]secretModel{}
	tbl := NewMapTable(store, &sync.RWMutex{}, Table[secretModel]{
		Fields: Fields{
			"Handle": {ReadOnly: true, DisplayValue: Redact},
		},
	})
	h := tbl.MetaData.MustFindField("Handle")
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
		if got := renderHook(t, Redact(*h, c.val)); got != c.want {
			t.Errorf("Redact(%v) = %q, want %q", c.val, got, c.want)
		}
	}
}

// TestPasswordFieldViaHelpers composes a write-only password field from the
// generic hooks + Redact / PasswordInput / HashWith — no bespoke Field flag.
func TestPasswordFieldViaHelpers(t *testing.T) {
	store := map[uint]secretModel{}
	tbl := NewMapTable(store, &sync.RWMutex{}, Table[secretModel]{
		Fields: Fields{
			"PasswordHash": {
				Label:          "Password",
				InputType:      "password",
				DisplayValue:   Redact,
				GenFormElement: PasswordInput,
				BindStrings:    HashWith(func(pw string) (string, error) { return "H(" + pw + ")", nil }),
			},
		},
	})
	f := tbl.MetaData.MustFindField("PasswordHash")

	// Empty password box — the stored hash never leaks into the form.
	form := renderHook(t, f.GenFormElement(*f, "argon2$secret"))
	if !strings.Contains(form, `type="password"`) || !strings.Contains(form, `value=""`) {
		t.Errorf("password input = %q, want empty type=password box", form)
	}
	if strings.Contains(form, "argon2$secret") {
		t.Error("stored hash leaked into the form input")
	}
	// Display redacted.
	if got := renderHook(t, f.DisplayValue(*f, "argon2$secret")); got != "-hidden-" {
		t.Errorf("display = %q, want -hidden-", got)
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

func TestModelValidateWired(t *testing.T) {
	store, mu := newCfgStore()
	sentinel := func(cfgHero) error { return nil }
	tbl := NewMapTable(store, mu, Table[cfgHero]{Validate: sentinel})
	if tbl.MetaData.Validate == nil {
		t.Error("model-level Validate not wired onto MetaModel")
	}
}
