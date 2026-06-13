package crud

import (
	"fmt"
	"html"
	"reflect"
	"strings"
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

	// DisplayValue / GenFormElement / BindStrings override the three generic
	// per-field transforms; nil keeps the derived hook:
	//   - DisplayValue   renders the table / detail cell from the value.
	//   - GenFormElement renders the whole form <input> (markup + value).
	//   - BindStrings    parses the submitted form value(s) into the struct.
	// Compose them for bespoke fields without dropping to the Derive* path.
	// The Redact / PasswordInput / HashWith helpers are ready-made hooks for
	// the common secret/password cases (see their docs).
	DisplayValue   func(mf MetaField, value any) templ.Component
	GenFormElement func(mf MetaField, value any) templ.Component
	BindStrings    func(mf MetaField, strs []string, instance any) error
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

	// ShortLabel overrides DefaultShortLabel for this model — the short label
	// shown for one of its rows wherever it appears as a relation (the
	// <select> options served by this table, and relation cells on other
	// tables). nil keeps DefaultShortLabel.
	ShortLabel func(instance T) string
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
	if fc.BindStrings != nil {
		f.BindStrings = fc.BindStrings
	}
}

// ──────────────────────────────────────────────────────────────────────────
// Ready-made hooks for the common secret / password fields. Each plugs into
// one of Field's generic transform hooks; nothing here is special-cased in
// the recipe.
// ──────────────────────────────────────────────────────────────────────────

// Redact is a DisplayValue hook for a sensitive or opaque field: it never
// renders the value, only whether one is present — "-hidden-" when non-empty
// (handles strings, []byte, etc.), "-empty-" otherwise. Pair with
// ReadOnly:true so the field shows redacted but is never sent to a form or
// bound (a round-trip can't corrupt it):
//
//	"TOTPSecret": {Label: "TOTP", ReadOnly: true, DisplayValue: crud.Redact},
func Redact(_ MetaField, value any) templ.Component {
	if valuePresent(value) {
		return templ.Raw(`<span class="italic">-hidden-</span>`)
	}
	return templ.Raw(`<span class="italic">-empty-</span>`)
}

// PasswordInput is a GenFormElement hook rendering an empty password box, so
// a stored value (e.g. a hash) is never echoed to the browser. Pair with
// HashWith on BindStrings and Redact on DisplayValue for a write-only
// password field.
func PasswordInput(mf MetaField, _ any) templ.Component {
	return templ.Raw(fmt.Sprintf(
		`<input type="password" name=%q value="" autocomplete="new-password" class="input"/>`,
		html.EscapeString(mf.Name)))
}

// HashWith returns a BindStrings hook for a write-only password field: a
// non-blank submitted value is passed through hash and written to the field;
// a blank or whitespace-only value leaves the stored value unchanged (so an
// edit that doesn't touch the box keeps the current password). The field must
// be a string. hash may also reject weak inputs by returning an error, which
// surfaces as a field validation error.
//
//	"PasswordHash": {Label: "Password", InputType: "password",
//	    DisplayValue:   crud.Redact,
//	    GenFormElement: crud.PasswordInput,
//	    BindStrings:    crud.HashWith(auth.HashPassword),
//	},
func HashWith(hash func(plaintext string) (string, error)) func(mf MetaField, strs []string, instance any) error {
	return func(mf MetaField, strs []string, instance any) error {
		pw := ""
		if len(strs) > 0 {
			pw = strs[0]
		}
		if strings.TrimSpace(pw) == "" {
			return nil // blank → keep the existing value
		}
		h, err := hash(pw)
		if err != nil {
			return err
		}
		return setStringFieldByName(instance, mf.Name, h)
	}
}

// valuePresent reports whether v holds a non-empty value (non-empty string /
// slice / map, non-nil pointer, or non-zero scalar).
func valuePresent(v any) bool {
	if v == nil {
		return false
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String, reflect.Slice, reflect.Map, reflect.Array:
		return rv.Len() > 0
	case reflect.Pointer, reflect.Interface:
		return !rv.IsNil()
	default:
		return !rv.IsZero()
	}
}

// setStringFieldByName writes val into the named string field of instance
// (a pointer to a struct), via reflection.
func setStringFieldByName(instance any, name, val string) error {
	rv := reflect.ValueOf(instance)
	for rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	f := rv.FieldByName(name)
	if !f.IsValid() || !f.CanSet() || f.Kind() != reflect.String {
		return fmt.Errorf("crud: HashWith field %q must be a settable string", name)
	}
	f.SetString(val)
	return nil
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
	if cfg.ShortLabel != nil {
		t.ShortLabel = cfg.ShortLabel
	}
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
