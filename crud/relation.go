package crud

import (
	"context"
	"errors"
	"fmt"
	"html"
	"reflect"
	"strconv"
	"strings"

	"github.com/a-h/templ"
)

// RelationKind classifies a relation field detected during reflection.
// DeriveMetaModel sets it based on the Go field type plus its `gorm:"..."`
// tag (when present). Rendering and binding switch on this.
type RelationKind int

const (
	NotRelation       RelationKind = iota
	RelationSingle                 // belongs-to: a single struct field with a sibling FK uint
	RelationMany2Many              // many-to-many: slice of struct with `gorm:"many2many:..."` tag
	RelationHasMany                // has-many: slice of struct with `gorm:"foreignKey:..."` tag — read-only in the parent form
)

// CRUDRelationOption is the type-erased row used in cross-model relation
// pickers (e.g. a <select> on Hero pulling options from a Skill CRUD).
type CRUDRelationOption struct {
	ID        uint
	Instance  any
	ShortName string
}

// CRUDTableInterface is the non-generic surface a relation widget needs to
// resolve options and to open the related entity's create modal. Modal
// targeting is handled by the library's fixed L2 body ID (ModalL2BodyID),
// so the interface only carries URL + option access. *CRUDTable[T]
// satisfies it; relation fields hold one through MetaField.RelatedCRUD.
type CRUDTableInterface interface {
	DisplayName() string
	HTMXTableURL() string                                                                       // base URL — useful for Admin index / detail jumps
	HTMXCreateURL() string                                                                      // GET this to fetch the create-form fragment
	SearchOptions(ctx context.Context, search string) ([]CRUDRelationOption, int64, error)
	GetOptionsByID(ctx context.Context, ids []uint) ([]CRUDRelationOption, error)
}

// DefaultShortValue derives a short human-readable label from an instance.
// "ID: Name" when both fields are present and non-zero; falls back to
// either alone, then to fmt.Sprintf as a last resort.
func DefaultShortValue(instance any) string {
	rv := reflect.ValueOf(instance)
	for rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return fmt.Sprintf("%v", instance)
	}
	idF := rv.FieldByName("ID")
	nameF := rv.FieldByName("Name")
	hasID := idF.IsValid() && idF.Kind() >= reflect.Uint && idF.Kind() <= reflect.Uint64 && idF.Uint() != 0
	hasIntID := idF.IsValid() && idF.Kind() >= reflect.Int && idF.Kind() <= reflect.Int64 && idF.Int() != 0
	hasName := nameF.IsValid() && nameF.Kind() == reflect.String && nameF.String() != ""
	switch {
	case hasID && hasName:
		return fmt.Sprintf("%d: %s", idF.Uint(), nameF.String())
	case hasIntID && hasName:
		return fmt.Sprintf("%d: %s", idF.Int(), nameF.String())
	case hasID:
		return fmt.Sprintf("#%d", idF.Uint())
	case hasIntID:
		return fmt.Sprintf("#%d", idF.Int())
	case hasName:
		return nameF.String()
	}
	return fmt.Sprintf("%v", instance)
}

// idOf extracts the uint ID from an instance via reflection on the "ID"
// field. Returns 0 when no ID is found.
func idOf(instance any) uint {
	rv := reflect.ValueOf(instance)
	for rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return 0
	}
	f := rv.FieldByName("ID")
	if !f.IsValid() {
		return 0
	}
	switch f.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return uint(f.Uint())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return uint(f.Int())
	}
	return 0
}

// ──────────────────────────────────────────────────────────────────────────
// CRUDTableInterface implementation for *CRUDTable[T].
// ──────────────────────────────────────────────────────────────────────────

func (c *CRUDTable[T]) DisplayName() string   { return c.MetaData.DisplayName }
func (c *CRUDTable[T]) HTMXTableURL() string  { return c.URLBase }
func (c *CRUDTable[T]) HTMXCreateURL() string { return c.URLBase + "/create" }

// SearchOptions returns up to relationOptionLimit options matching search.
func (c *CRUDTable[T]) SearchOptions(ctx context.Context, search string) ([]CRUDRelationOption, int64, error) {
	results, total, err := c.List(ctx, search, "", false, 0, relationOptionLimit)
	if err != nil {
		return nil, 0, err
	}
	out := make([]CRUDRelationOption, len(results))
	for i, r := range results {
		out[i] = CRUDRelationOption{
			ID:        r.ID,
			Instance:  r.Row,
			ShortName: DefaultShortValue(r.Row),
		}
	}
	return out, total, nil
}

// GetOptionsByID resolves IDs to options (skipping any unknown).
func (c *CRUDTable[T]) GetOptionsByID(ctx context.Context, ids []uint) ([]CRUDRelationOption, error) {
	out := make([]CRUDRelationOption, 0, len(ids))
	for _, id := range ids {
		v, err := c.Get(ctx, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, CRUDRelationOption{
			ID:        id,
			Instance:  v,
			ShortName: DefaultShortValue(v),
		})
	}
	return out, nil
}

// relationOptionLimit caps the dropdown to a reasonable number of rows;
// past this you'd want a typeahead instead of a vanilla <select>.
const relationOptionLimit = 500

// ──────────────────────────────────────────────────────────────────────────
// Relation-field default hooks.
//
// These get installed on MetaFields detected as relations during
// DeriveMetaModel. Hooks render <select> pickers, render short-name
// summaries for display, and bind the selected IDs back into the parent
// struct.
// ──────────────────────────────────────────────────────────────────────────

// relationSingleDisplay renders one related instance as its short name.
func relationSingleDisplay(mf MetaField, value any) templ.Component {
	if value == nil {
		return templ.Raw("")
	}
	if idOf(value) == 0 {
		return templ.Raw(`<span class="opacity-50">—</span>`)
	}
	return templ.Raw(html.EscapeString(DefaultShortValue(value)))
}

// relationMultipleDisplay renders a slice of related instances as a <ul>
// of short names.
func relationMultipleDisplay(mf MetaField, value any) templ.Component {
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Slice || rv.Len() == 0 {
		return templ.Raw(`<span class="opacity-50">—</span>`)
	}
	var sb strings.Builder
	sb.WriteString(`<ul class="list-disc list-inside">`)
	for i := 0; i < rv.Len(); i++ {
		sb.WriteString(`<li>`)
		sb.WriteString(html.EscapeString(DefaultShortValue(rv.Index(i).Interface())))
		sb.WriteString(`</li>`)
	}
	sb.WriteString(`</ul>`)
	return templ.Raw(sb.String())
}

// relationSingleFormElement renders a <select> + "+ new" button for a
// belongs-to relation. value is the FK uint (extracted by
// DefaultGenFormElements via the FKFieldName indirection).
func relationSingleFormElement(mf MetaField, value any) templ.Component {
	var selectedID uint
	switch v := value.(type) {
	case uint:
		selectedID = v
	case uint64:
		selectedID = uint(v)
	case int:
		selectedID = uint(v)
	case int64:
		selectedID = uint(v)
	}
	return relationSelect(mf, selectedID, nil, false)
}

// relationMultipleFormElement renders a <select multiple> + "+ new"
// button for a many-to-many relation. value is the slice field (so we
// extract IDs from it).
func relationMultipleFormElement(mf MetaField, value any) templ.Component {
	selected := relationSelectedIDs(value)
	return relationSelect(mf, 0, selected, true)
}

// relationSelectedIDs walks a slice-of-struct value and returns the IDs
// (via idOf) of each element. Returns nil for non-slice values.
func relationSelectedIDs(value any) []uint {
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Slice {
		return nil
	}
	out := make([]uint, 0, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		if id := idOf(rv.Index(i).Interface()); id != 0 {
			out = append(out, id)
		}
	}
	return out
}

// relationSelect renders the picker. inputName is mf.FormFieldName
// (which carries the IDs in the POSTed form):
//   - single: name=<FKFieldName>, e.g. "OwnerID"
//   - multiple: name=<RelationName>, e.g. "Skills"
//
// The "+ new" button hx-gets the related create URL into the related
// CRUD's modal-content target. If RelatedCRUD is unset we render the
// picker with whatever options it can (none) and skip the "+" button.
func relationSelect(mf MetaField, single uint, multi []uint, isMulti bool) templ.Component {
	name := mf.FormFieldName
	if name == "" {
		name = mf.Name
	}
	options := []CRUDRelationOption{}
	if mf.RelatedCRUD != nil {
		opts, _, err := mf.RelatedCRUD.SearchOptions(context.Background(), "")
		if err == nil {
			options = opts
		}
	}

	var sb strings.Builder
	sb.WriteString(`<div class="join">`)

	// The <select> input itself.
	if isMulti {
		sb.WriteString(fmt.Sprintf(
			`<select name=%q multiple size="5" class="select join-item w-full">`,
			html.EscapeString(name)))
	} else {
		sb.WriteString(fmt.Sprintf(
			`<select name=%q class="select join-item w-full">`,
			html.EscapeString(name)))
		// Empty placeholder for "no selection" on single relations.
		sb.WriteString(`<option value="0">— none —</option>`)
	}
	selSet := map[uint]struct{}{}
	if isMulti {
		for _, id := range multi {
			selSet[id] = struct{}{}
		}
	} else if single != 0 {
		selSet[single] = struct{}{}
	}
	// Render the resolved options. If the currently-selected ID isn't in
	// the option list (because of a cap or stale data) we still surface
	// it so the user knows what's stored.
	rendered := map[uint]struct{}{}
	for _, opt := range options {
		sel := ""
		if _, ok := selSet[opt.ID]; ok {
			sel = " selected"
		}
		sb.WriteString(fmt.Sprintf(
			`<option value="%d"%s>%s</option>`,
			opt.ID, sel, html.EscapeString(opt.ShortName)))
		rendered[opt.ID] = struct{}{}
	}
	for sid := range selSet {
		if _, ok := rendered[sid]; ok || sid == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf(
			`<option value="%d" selected>#%d (unresolved)</option>`,
			sid, sid))
	}
	sb.WriteString(`</select>`)

	// "+ new" button — only when we have a RelatedCRUD to point at.
	// Always targets the L2 modal body so the L1 form's state survives
	// the nested create.
	if mf.RelatedCRUD != nil {
		url := mf.RelatedCRUD.HTMXCreateURL()
		if url != "" {
			sb.WriteString(fmt.Sprintf(
				`<button type="button" class="btn btn-outline join-item"`+
					` hx-get=%q hx-target="#%s" hx-swap="innerHTML"`+
					` title="Create new %s">+</button>`,
				html.EscapeString(url),
				ModalL2BodyID,
				html.EscapeString(mf.RelatedCRUD.DisplayName())))
		}
	}

	sb.WriteString(`</div>`)
	return templ.Raw(sb.String())
}

// relationSingleFromStrings parses the posted FK (uint) and writes it to
// instance.<FKFieldName>. "0" / empty clears the relation.
func relationSingleFromStrings(mf MetaField, strs []string, instance any) error {
	rv := reflect.ValueOf(instance)
	for rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return errors.New("relation: instance is not a struct")
	}
	target := mf.FKFieldName
	if target == "" {
		return errors.New("relation: FKFieldName not set")
	}
	f := rv.FieldByName(target)
	if !f.IsValid() {
		return fmt.Errorf("relation: no field %q on instance", target)
	}
	if !f.CanSet() {
		return fmt.Errorf("relation: field %q not settable", target)
	}
	val := ""
	if len(strs) > 0 {
		val = strs[0]
	}
	if val == "" || val == "0" {
		f.SetUint(0)
		// Also clear the embedded struct so a re-render shows no stale value.
		clearRelationStruct(rv, mf.Name)
		return nil
	}
	n, err := strconv.ParseUint(val, 10, 64)
	if err != nil {
		return err
	}
	f.SetUint(n)
	// Stamp the ID into the embedded struct so the renderer can preselect
	// the right option on a validation re-render (before GORM has reloaded
	// the row).
	stampRelationStructID(rv, mf.Name, uint(n))
	return nil
}

// relationMultipleFromStrings parses the posted IDs and writes a slice of
// zero structs (with only ID set) to instance.<Name>. The GORM backend
// later calls Association().Replace() to persist the join rows.
func relationMultipleFromStrings(mf MetaField, strs []string, instance any) error {
	rv := reflect.ValueOf(instance)
	for rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return errors.New("relation: instance is not a struct")
	}
	f := rv.FieldByName(mf.Name)
	if !f.IsValid() {
		return fmt.Errorf("relation: no field %q on instance", mf.Name)
	}
	if !f.CanSet() {
		return fmt.Errorf("relation: field %q not settable", mf.Name)
	}
	if f.Kind() != reflect.Slice {
		return fmt.Errorf("relation: field %q is not a slice", mf.Name)
	}
	elemType := f.Type().Elem()
	sl := reflect.MakeSlice(f.Type(), 0, len(strs))
	for _, s := range strs {
		if s == "" || s == "0" {
			continue
		}
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return err
		}
		elem := reflect.New(elemType).Elem()
		idF := elem.FieldByName("ID")
		if idF.IsValid() && idF.CanSet() {
			idF.SetUint(n)
		}
		sl = reflect.Append(sl, elem)
	}
	f.Set(sl)
	return nil
}

// relationHasManyFromStrings is a no-op — has-many is read-only on the
// parent form.
func relationHasManyFromStrings(mf MetaField, strs []string, instance any) error {
	return nil
}

func clearRelationStruct(rv reflect.Value, name string) {
	f := rv.FieldByName(name)
	if !f.IsValid() || !f.CanSet() {
		return
	}
	if f.Kind() != reflect.Struct {
		return
	}
	f.Set(reflect.Zero(f.Type()))
}

func stampRelationStructID(rv reflect.Value, name string, id uint) {
	f := rv.FieldByName(name)
	if !f.IsValid() || !f.CanSet() || f.Kind() != reflect.Struct {
		return
	}
	idF := f.FieldByName("ID")
	if !idF.IsValid() || !idF.CanSet() {
		return
	}
	switch idF.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		idF.SetUint(uint64(id))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		idF.SetInt(int64(id))
	}
}
