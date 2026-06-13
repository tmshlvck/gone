package crud

import (
	"sync"
	"testing"
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

func TestModelValidateWired(t *testing.T) {
	store, mu := newCfgStore()
	sentinel := func(cfgHero) error { return nil }
	tbl := NewMapTable(store, mu, Table[cfgHero]{Validate: sentinel})
	if tbl.MetaData.Validate == nil {
		t.Error("model-level Validate not wired onto MetaModel")
	}
}
