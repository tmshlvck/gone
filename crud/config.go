package crud

import (
	"fmt"
	"sync"

	"github.com/a-h/templ"
	"github.com/tmshlvck/gone/auth"
	"gorm.io/gorm"
)

// Config-up-front constructors. These wrap the low-level
// DeriveMetaModel + Derive*CRUDTable + post-mutation path with a single
// declarative call:
//
//	heroes := crud.NewGormTable(db, crud.Table[Hero]{
//	    Slug: "heroes", Title: "Heroes", PageSize: 10,
//	    Fields: crud.Fields{
//	        "ID":   {ReadOnly: true},
//	        "Name": {Help: "2–40 chars.", Validate: crud.All(crud.NotEmpty, crud.MaxLen(40))},
//	    },
//	})
//
// The recipe (Table[T]) describes the model once; the constructor reflects
// T, merges the overrides over the reflected defaults, and returns a ready
// CRUDTable[T]. Unknown field names and a non-struct T are programming
// errors caught at construction — the constructors panic (regexp.MustCompile
// precedent), so a typo or renamed field fails at startup, not at first
// render.
//
// The returned CRUDTable[T] is the same struct the Derive* path produces;
// its fields stay public, so anything the recipe doesn't cover (granular
// CreateEnabled, a custom MetaField hook, …) can still be set afterward. The
// Derive* functions remain the low-level escape hatch.

// Field overrides one MetaField's derived defaults. The zero value changes
// nothing — set only what you want to override. Keyed by Go field name in a
// Fields map.
//
// ReadOnly and Hidden are additive: a true value turns the flag on; false
// leaves the reflected default (so they can't force a derived-read-only
// has-many field back to editable — use the Derive* path for that). Every
// other field overrides when non-empty / non-nil.
type Field struct {
	Label     string    // -> MetaField.DisplayName  (empty = keep derived = Go field name)
	Help      string    // -> MetaField.FormHelp     (form hint under the input)
	InputType string    // -> MetaField.FormInputType (e.g. "email", "password", "date")
	ReadOnly  bool      // if true, show in detail/list but omit from the form
	Hidden    bool      // if true, omit entirely (list, detail, form)
	Validate  Validator // -> MetaField.FieldValidate (per-field server validator)

	// DisplayValue / GenFormElement override the cell renderer / form input
	// for this field. nil keeps the derived hook. For the rare field that
	// needs bespoke HTML (a clickable id, a custom widget) without dropping
	// to the Derive* path.
	DisplayValue   func(mf MetaField, value any) templ.Component
	GenFormElement func(mf MetaField, value any) templ.Component
}

// Fields maps Go field name to its override. An entry for a name T doesn't
// have is a construction-time panic (typo / stale reference).
type Fields map[string]Field

// Table is the declarative recipe for a CRUDTable[T]: identity, paging,
// authz, per-field overrides, and an optional cross-field validator. Pass it
// to NewGormTable / NewMapTable. Every field is optional; the zero Table
// yields an all-defaults table.
type Table[T any] struct {
	Slug     string     // URL slug (plural); empty = lowercase(TypeName)+"s"
	Title    string     // display name; empty = the Go type name
	PageSize int        // rows per page; 0 = library default (20)
	Authz    auth.Authz // nil = allow all

	// ReadOnly disables create, edit, and delete in one switch (a view-only
	// table). For finer control, leave it false and set CreateEnabled /
	// EditEnabled / DeleteEnabled on the returned table.
	ReadOnly bool

	// HideUnauthorized omits mutation buttons the user isn't allowed to use
	// instead of rendering them disabled. See CRUDTable.HideUnauthorized.
	HideUnauthorized bool

	// Fields holds per-field overrides, keyed by Go field name.
	Fields Fields

	// Validate is the model-level cross-field validator (-> MetaModel.Validate).
	// Runs after every per-field validator passes.
	Validate func(instance T) error
}

// metaModel derives the MetaModel for T and applies the recipe's model- and
// field-level overrides. Panics on a non-struct T or an unknown field name.
func (cfg Table[T]) metaModel() MetaModel[T] {
	mm, err := DeriveMetaModel[T]()
	if err != nil {
		panic(fmt.Errorf("crud.Table: %w", err))
	}
	if cfg.Title != "" {
		mm.DisplayName = cfg.Title
	}
	if cfg.Validate != nil {
		mm.Validate = cfg.Validate
	}
	for name, fc := range cfg.Fields {
		f, err := mm.FindField(name)
		if err != nil {
			panic(fmt.Errorf("crud.Table[%s].Fields: %w", mm.Name, err))
		}
		fc.applyTo(f)
	}
	return mm
}

// applyTo merges a Field's non-zero overrides onto a derived MetaField.
func (fc Field) applyTo(f *MetaField) {
	if fc.Label != "" {
		f.DisplayName = fc.Label
	}
	if fc.Help != "" {
		f.FormHelp = fc.Help
	}
	if fc.InputType != "" {
		f.FormInputType = fc.InputType
	}
	if fc.ReadOnly {
		f.ReadOnly = true
	}
	if fc.Hidden {
		f.Hidden = true
	}
	if fc.Validate != nil {
		f.FieldValidate = fc.Validate
	}
	if fc.DisplayValue != nil {
		f.DisplayValue = fc.DisplayValue
	}
	if fc.GenFormElement != nil {
		f.GenFormElement = fc.GenFormElement
	}
}

// applyTo stamps the recipe's table-level settings onto a freshly derived
// CRUDTable (Slug / PageSize / mutation toggles / HideUnauthorized).
func (cfg Table[T]) applyTo(t *CRUDTable[T]) {
	if cfg.Slug != "" {
		t.Slug = cfg.Slug
	}
	if cfg.PageSize != 0 {
		t.PageSize = cfg.PageSize
	}
	if cfg.ReadOnly {
		t.CreateEnabled, t.EditEnabled, t.DeleteEnabled = false, false, false
	}
	t.HideUnauthorized = cfg.HideUnauthorized
}

// NewGormTable builds a GORM-backed CRUDTable[T] from a declarative recipe.
// Equivalent to DeriveMetaModel + per-field post-mutation + DeriveGormCRUDTable
// + Slug/PageSize assignment, in one call. Panics on misconfiguration (see
// Table).
func NewGormTable[T any](db *gorm.DB, cfg Table[T]) CRUDTable[T] {
	t := DeriveGormCRUDTable[T](cfg.metaModel(), cfg.Authz, db)
	cfg.applyTo(&t)
	return t
}

// NewMapTable builds an in-memory-map-backed CRUDTable[T] from a recipe over
// a caller-owned store + mutex (the map and mutex stay the caller's, as with
// DeriveMapCRUDTable). Panics on misconfiguration (see Table).
func NewMapTable[T any](store map[uint]T, mu *sync.RWMutex, cfg Table[T]) CRUDTable[T] {
	t := DeriveMapCRUDTable[T](cfg.metaModel(), cfg.Authz, store, mu)
	cfg.applyTo(&t)
	return t
}
