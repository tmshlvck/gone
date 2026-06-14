package crud

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
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

// DefaultShortLabel derives a short, human-readable label for one related
// instance — the text shown in a relation <select> option and in a relation
// table cell. It works in stages, returning the first that yields a value:
//
//  1. a "Name" field (case-insensitive), if a non-empty string;
//  2. a "Label" field (case-insensitive), if a non-empty string;
//  3. a "Title" field (case-insensitive), if a non-empty string;
//  4. any string field whose name contains "name" (Username, FullName, …);
//  5. any string field whose name contains "title" (Subtitle, JobTitle, …);
//  6. an identifier — an "id" field, else a "…ID"/"…Id" foreign-key-style
//     integer field — rendered as "#<n>";
//  7. a JSON dump of the instance, as a last resort.
//
// Stages 1–5 return the label alone (no "id:" prefix). A non-struct instance
// is formatted with fmt.
func DefaultShortLabel(instance any) string {
	rv := reflect.ValueOf(instance)
	for rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return fmt.Sprintf("%v", instance)
	}
	if s := stringFieldNamed(rv, "name"); s != "" {
		return s
	}
	if s := stringFieldNamed(rv, "label"); s != "" {
		return s
	}
	if s := stringFieldNamed(rv, "title"); s != "" {
		return s
	}
	if s := stringFieldContaining(rv, "name"); s != "" {
		return s
	}
	if s := stringFieldContaining(rv, "title"); s != "" {
		return s
	}
	if s := idLabel(rv); s != "" {
		return s
	}
	if b, err := json.Marshal(instance); err == nil {
		return string(b)
	}
	return fmt.Sprintf("%v", instance)
}

// stringFieldNamed returns the value of the exported string field whose name
// equals want (case-insensitive), or "".
func stringFieldNamed(rv reflect.Value, want string) string {
	t := rv.Type()
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if sf.IsExported() && sf.Type.Kind() == reflect.String && strings.EqualFold(sf.Name, want) {
			return rv.Field(i).String()
		}
	}
	return ""
}

// stringFieldContaining returns the first non-empty exported string field
// whose name contains sub (case-insensitive), or "".
func stringFieldContaining(rv reflect.Value, sub string) string {
	t := rv.Type()
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() || sf.Type.Kind() != reflect.String {
			continue
		}
		if strings.Contains(strings.ToLower(sf.Name), sub) {
			if s := rv.Field(i).String(); s != "" {
				return s
			}
		}
	}
	return ""
}

// idLabel renders an identifier field as "#<n>": an exact "id" field
// (case-insensitive) first, then a foreign-key-style "…ID"/"…Id" integer
// field. Returns "" if no non-zero integer identifier is found.
func idLabel(rv reflect.Value) string {
	t := rv.Type()
	for i := 0; i < t.NumField(); i++ {
		if sf := t.Field(i); sf.IsExported() && strings.EqualFold(sf.Name, "id") {
			if s := intLikeString(rv.Field(i)); s != "" {
				return "#" + s
			}
		}
	}
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if sf.IsExported() && (strings.HasSuffix(sf.Name, "ID") || strings.HasSuffix(sf.Name, "Id")) {
			if s := intLikeString(rv.Field(i)); s != "" {
				return "#" + s
			}
		}
	}
	return ""
}

// intLikeString renders a non-zero integer reflect.Value as a decimal string,
// or "" if it isn't an integer kind or is zero.
func intLikeString(v reflect.Value) string {
	switch v.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if n := v.Uint(); n != 0 {
			return strconv.FormatUint(n, 10)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if n := v.Int(); n != 0 {
			return strconv.FormatInt(n, 10)
		}
	}
	return ""
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
	return templ.Raw(html.EscapeString(shortLabelFor(mf, value)))
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
		sb.WriteString(html.EscapeString(shortLabelFor(mf, rv.Index(i).Interface())))
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
// The <select> links to the related table purely by URL (mf.RelatedURLBase,
// stamped by WireRelations / Admin) — no in-process pointer. It renders with
// lightweight placeholder <option>s for the current selection (id-only, no
// label, since the related rows aren't loaded here) and an hx-get that fires
// on `load` to fetch the real, labelled option list from the related table's
// /options endpoint, then again on every `refresh-relation` event (e.g.
// after an L2 "+ new" save adds a row). hx-vals re-sends the current
// selection each time so it survives the swap; only the option list swaps,
// so the wrapper, name, and "+" button persist and the L1 form's other
// values are untouched.
//
// The "+ new" button hx-gets {RelatedURLBase}/create into the L2 modal body.
func relationSelect(mf MetaField, single uint, multi []uint, isMulti bool) templ.Component {
	name := mf.FormFieldName
	if name == "" {
		name = mf.Name
	}
	relBase := mf.RelatedURLBase

	selSet := map[uint]struct{}{}
	if isMulti {
		for _, id := range multi {
			selSet[id] = struct{}{}
		}
	} else if single != 0 {
		selSet[single] = struct{}{}
	}

	// hx-* attributes that fetch the option list on load and re-fetch on a
	// "refresh-relation" event broadcast from the body. The endpoint lives
	// on the related table (it owns the options); ?single=1 tells it to
	// include the "— none —" placeholder for belongs-to fields. hx-vals
	// (evaluated in the browser via the "js:" prefix; "this" is the
	// <select>) re-sends the current selection so a refresh keeps it.
	hxAttrs := ""
	if relBase != "" {
		optsURL := relBase + "/options"
		var hxVals string
		if isMulti {
			hxVals = `js:{"selected": [...this.selectedOptions].map(o => o.value)}`
		} else {
			optsURL += "?single=1"
			hxVals = `js:{"selected": this.value}`
		}
		hxAttrs = fmt.Sprintf(
			` hx-trigger="load, refresh-relation from:body" hx-get=%q`+
				` hx-vals='%s' hx-target="this" hx-swap="innerHTML"`,
			html.EscapeString(optsURL),
			hxVals)
	}

	var sb strings.Builder
	sb.WriteString(`<div class="join">`)

	// The <select> input itself. The multi-select is a native listbox — it
	// deliberately does NOT use DaisyUI's .select class, whose fixed
	// single-line height (field-sizing based) collapses a multi-row <select
	// multiple> so the options overlap. A plain bordered box lets size="5"
	// govern, one option per line.
	if isMulti {
		sb.WriteString(fmt.Sprintf(
			`<select name=%q multiple size="5" class="join-item w-full border border-base-300 rounded-box bg-base-100 p-1"%s>`,
			html.EscapeString(name), hxAttrs))
	} else {
		sb.WriteString(fmt.Sprintf(
			`<select name=%q class="select join-item w-full"%s>`,
			html.EscapeString(name), hxAttrs))
	}
	sb.WriteString(renderPlaceholderOptions(selSet, !isMulti))
	sb.WriteString(`</select>`)

	// "+ new" button — only when the relation is wired to a related table.
	// Always targets the L2 modal body so the L1 form's state survives the
	// nested create. The crud-relation-add-btn class is matched by a
	// `display: none` rule in PageModals so the button is hidden when this
	// same form renders inside L2 (no L3 modal for a nested-nested create).
	if relBase != "" {
		label := mf.RelatedTypeName
		if label == "" {
			label = "item"
		}
		sb.WriteString(fmt.Sprintf(
			`<button type="button" class="btn btn-outline join-item crud-relation-add-btn"`+
				` hx-get=%q hx-target="#%s" hx-swap="innerHTML"`+
				` title="Create new %s">+</button>`,
			html.EscapeString(relBase+"/create"),
			ModalL2BodyID,
			html.EscapeString(label)))
	}

	sb.WriteString(`</div>`)
	return templ.Raw(sb.String())
}

// renderPlaceholderOptions writes the <option>s a relation <select> shows
// before its hx-get(load) replaces them with the real, labelled list. They
// carry only the selected ids (id-only labels — the related rows aren't
// loaded here) so the select's value/selectedOptions are correct when the
// load fires and hx-vals re-sends them. includeNone prepends "— none —"
// (value=0) for belongs-to fields.
func renderPlaceholderOptions(selected map[uint]struct{}, includeNone bool) string {
	var sb strings.Builder
	if includeNone {
		sel := ""
		if _, ok := selected[0]; ok {
			sel = " selected"
		}
		sb.WriteString(fmt.Sprintf(`<option value="0"%s>— none —</option>`, sel))
	}
	for id := range selected {
		if id == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf(`<option value="%d" selected>#%d</option>`, id, id))
	}
	return sb.String()
}

// renderOptionsHTML writes the <option> children of a relation <select>.
// includeNone prepends the "— none —" placeholder (value=0) for
// belongs-to fields; many-to-many fields don't need it.
//
// Currently-selected IDs that aren't in the options list are surfaced
// as "#N (unresolved)" so the user doesn't silently lose a referenced
// row that fell outside the option cap.
func renderOptionsHTML(opts []CRUDRelationOption, selected map[uint]struct{}, includeNone bool) string {
	var sb strings.Builder
	if includeNone {
		sel := ""
		if _, ok := selected[0]; ok {
			sel = " selected"
		}
		sb.WriteString(fmt.Sprintf(`<option value="0"%s>— none —</option>`, sel))
	}
	rendered := map[uint]struct{}{0: {}}
	for _, opt := range opts {
		sel := ""
		if _, ok := selected[opt.ID]; ok {
			sel = " selected"
		}
		sb.WriteString(fmt.Sprintf(
			`<option value="%d"%s>%s</option>`,
			opt.ID, sel, html.EscapeString(opt.ShortName)))
		rendered[opt.ID] = struct{}{}
	}
	for sid := range selected {
		if _, ok := rendered[sid]; ok {
			continue
		}
		sb.WriteString(fmt.Sprintf(
			`<option value="%d" selected>#%d (unresolved)</option>`,
			sid, sid))
	}
	return sb.String()
}

// handleOptions is the GET {base}/options handler. It returns the
// <option> children of a relation <select> populated from this
// CRUDTable's current rows.
//
// Query parameters:
//   - selected=<id>: marks an option selected. Repeatable for
//     many-to-many widgets (hx-vals sends an array → multiple
//     selected=… params).
//   - single=1: include the "— none —" placeholder for belongs-to
//     widgets. Belongs-to widgets pass this so their refresh response
//     keeps the placeholder option intact.
//
// The endpoint lives on the related CRUD (the one that owns the
// options), so any relation widget anywhere on the page can refresh
// itself by hitting this URL — no per-parent knowledge needed.
func (c *CRUDTable[T]) handleOptions(w http.ResponseWriter, r *http.Request) templ.Component {
	isSingle := r.URL.Query().Get("single") == "1"
	selected := map[uint]struct{}{}
	for _, s := range r.URL.Query()["selected"] {
		if s == "" {
			continue
		}
		if n, err := strconv.ParseUint(s, 10, 64); err == nil {
			selected[uint(n)] = struct{}{}
		}
	}
	opts, _, err := c.SearchOptions(r.Context(), "")
	if failInternal(w, err) {
		return nil
	}
	return templ.Raw(renderOptionsHTML(opts, selected, isSingle))
}

// relationSingleBindStrings parses the posted FK (uint) and writes it to
// instance.<FKFieldName>. "0" / empty clears the relation.
func relationSingleBindStrings(mf MetaField, strs []string, instance any) error {
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

// relationMultipleBindStrings parses the posted IDs and writes a slice of
// zero structs (with only ID set) to instance.<Name>. The GORM backend
// later calls Association().Replace() to persist the join rows.
func relationMultipleBindStrings(mf MetaField, strs []string, instance any) error {
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

// relationHasManyBindStrings is a no-op — has-many is read-only on the
// parent form.
func relationHasManyBindStrings(mf MetaField, strs []string, instance any) error {
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
