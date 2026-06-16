// Package crud renders HTMX-driven CRUD UIs (table, form, detail) from a
// model's reflected metadata, paired with a pluggable data Accessor.
//
// MetaField is non-generic; MetaModel[T] is generic over the model type.
// DeriveMetaModel[T] walks T via reflection and installs reflect-based hooks
// on each MetaField; callers customize per field. Scalars (string, int kinds,
// float, bool, time.Time, []byte) and relations (belongs-to / has-many /
// many-to-many, detected from gorm tags) are supported.
package crud

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"html"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/tmshlvck/gone/site"
)

// randSuffix returns a short (8 chars) lowercase URL-safe random string.
// Used by Derive* to mint per-instance DOM IDs in the form "<name>_<suffix>".
// Per-instance IDs let multiple components coexist on the same page
// without collisions.
func randSuffix() string {
	var b [5]byte // 5 bytes → 8 base32 chars
	_, _ = rand.Read(b[:])
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}

// MetaField describes one field for rendering and form binding.
// The hooks accept `any` because MetaField is not generic (so it can live
// in a heterogeneous []MetaField). Implementations reflect on the value
// using MetaField.Name.
type MetaField struct {
	Name          string
	DisplayName   string
	FormInputType string // HTML <input type=...>: "text", "number", "checkbox", "datetime-local", "email", …
	FormHelp      string
	Hidden        bool
	ReadOnly      bool
	Multiple      bool
	Sortable      bool // column header is a sort link
	Searchable    bool // included in case-insensitive substring search

	// Relation metadata — populated by DeriveMetaModel from reflection +
	// gorm tags. RelatedURLBase is left blank at derivation and filled in
	// later by WireRelations / Admin (matching RelatedTypeName against each
	// routed table's ModelName → URLBase): the relation <select> loads its
	// options over HTTP from RelatedURLBase + "/options", so tables link by
	// URL rather than by an in-process pointer.
	RelationKind      RelationKind
	RelatedURLBase    string           // absolute URL of the related table (e.g. "/admin/heroes"); blank until wired
	RelatedShortLabel func(any) string // related table's label fn (its ShortLabel or DefaultShortLabel); stamped by WireRelations, nil until then
	RelatedTypeName   string           // Go type name of the related model (e.g. "Hero"); empty for non-relations
	FKFieldName       string           // RelationSingle only — sibling FK uint, e.g. "OwnerID" for "Owner Hero"
	FormFieldName     string           // POST form key for the input (defaults to Name; relation single uses FKFieldName)

	// DisplayValue renders the field's typed Go value as a templ.Component
	// (a single table cell or dump entry). value is the already-extracted
	// field value, not the whole instance.
	DisplayValue func(mf MetaField, value any) templ.Component

	// GenFormElement renders an <input> / <select> / etc. pre-filled with
	// value. Form name attribute is mf.Name (or mf.FormFieldName for
	// relations).
	GenFormElement func(mf MetaField, value any) templ.Component

	// BindStrings parses wire form values into the field's Go type and
	// writes them into instance via reflection. strs is form[mf.Name]
	// (or form[mf.FormFieldName] for relations); an empty slice means the
	// field was absent (e.g. unchecked checkbox).
	BindStrings func(mf MetaField, strs []string, instance any) error

	// FieldValidate runs after BindStrings has populated the field. It
	// receives only the field's own value — no MetaField, no instance.
	// Cross-field rules belong on MetaModel.Validate. Helpers in
	// validators.go (NotEmpty, MinLen, …) plus All(...) for composition.
	// nil = no validation for this field.
	FieldValidate Validator
}

// MetaModel is the per-type description used to render and bind. T is the
// model type. Pure metadata + render/bind helper methods — no routing state,
// no data accessors, no authz. Those concerns belong on CRUDTable (or in
// user-written handlers that consume RenderDisplay / RenderForm /
// TryBindForm directly). Per-field rendering/binding is customized on the
// MetaField hooks; the model-level methods just loop over them.
type MetaModel[T any] struct {
	Fields []MetaField

	Name        string // Go type name (e.g. "Hero")
	DisplayName string

	// Validate is the user-defined cross-field validator. It receives
	// only the populated instance — no MetaModel, no extra context. nil
	// = no model-level validation. Runs in DefaultBindForm after every
	// per-field validator passes; a non-nil error becomes the
	// ValidationErrors entry under ModelLevelKey ("") and rejects the
	// form submission.
	Validate func(instance T) error

	// TimeFormatter is the app-global policy for rendering time.Time
	// cells; nil → site.DefaultTimeFormatter. The session's *time.Location
	// is read from the render context (site.Timezone); this only decides
	// the layout. Per-field formatting still overrides via DisplayValue.
	// An app's site.DefaultSettings satisfies this (it embeds the formatter).
	TimeFormatter site.TimeFormatter
}

// DisplayValues renders each field of instance to a table/detail cell (the
// slice is parallel to Fields; nil for Hidden fields) via the per-field
// DisplayValue hooks. GenFormElements is the form analogue; BindForm parses a
// submitted form into out. These are thin wrappers over the Default*
// functions — per-field customization lives on MetaField.
func (mm MetaModel[T]) DisplayValues(instance T) []templ.Component {
	return DefaultDisplayValues(mm, instance)
}

func (mm MetaModel[T]) GenFormElements(instance T) []templ.Component {
	return DefaultGenFormElements(mm, instance)
}

func (mm MetaModel[T]) BindForm(form map[string][]string, out *T) error {
	return DefaultBindForm(mm, form, out)
}

// FindField returns a pointer to the named MetaField on mm so callers can
// tweak per-field settings without iterating the slice. The usual way to set
// per-field metadata is the DeriveMetaModel preset; FindField is for the rare
// post-construction tweak. Returns an error if no field matches.
//
//	f, err := mm.FindField("Name")
//	if err != nil { return err }
//	f.FormHelp = "Display name, 2–30 characters."
func (mm *MetaModel[T]) FindField(name string) (*MetaField, error) {
	for i := range mm.Fields {
		if mm.Fields[i].Name == name {
			return &mm.Fields[i], nil
		}
	}
	return nil, fmt.Errorf("MetaModel(%s).FindField: no field %q", mm.Name, name)
}

// DeriveMetaModel reflects T into a MetaModel, then overlays preset — a
// partial MetaModel carrying just the overrides you want:
//
//	mm := crud.DeriveMetaModel[Hero](crud.MetaModel[Hero]{
//	    DisplayName: "Heroes",
//	    Fields: []crud.MetaField{
//	        {Name: "Name",  FieldValidate: crud.NotEmpty},
//	        {Name: "Power", FormHelp: "0–100"},
//	    },
//	})
//
// Merge rules: preset's non-empty DisplayName / Validate win; each preset
// field (matched by Name) overlays its non-empty strings, non-nil hooks, and
// additive ReadOnly/Hidden/Sortable/Searchable (a true turns the flag on;
// forcing one off uses the returned mm directly). Relation metadata
// (RelationKind, RelatedTypeName, FKFieldName, relation hooks) stays
// derive-authoritative.
//
// Panics on a non-struct T or a preset field Name the struct doesn't have —
// programming errors caught at startup (regexp.MustCompile idiom). Pass the
// zero MetaModel[T]{} for pure defaults.
func DeriveMetaModel[T any](preset MetaModel[T]) MetaModel[T] {
	var zero T
	rt := reflect.TypeOf(zero)
	for rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	if rt.Kind() != reflect.Struct {
		panic(fmt.Errorf("crud.DeriveMetaModel: %v is not a struct", rt))
	}

	mm := MetaModel[T]{Name: rt.Name(), DisplayName: rt.Name()}
	if preset.DisplayName != "" {
		mm.DisplayName = preset.DisplayName
	}
	if preset.Validate != nil {
		mm.Validate = preset.Validate
	}
	mm.TimeFormatter = preset.TimeFormatter
	if mm.TimeFormatter == nil {
		mm.TimeFormatter = site.DefaultTimeFormatter{}
	}

	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		mm.Fields = append(mm.Fields, deriveField(f, mm.TimeFormatter))
	}

	// Post-process: hide FK fields that already drive a sibling relation.
	// E.g. for `Owner Hero` + `OwnerID uint`, the Owner field carries the
	// <select> with name="OwnerID"; the bare OwnerID field would otherwise
	// render a duplicate number input.
	fkOwners := map[string]bool{}
	for _, mf := range mm.Fields {
		if mf.RelationKind == RelationSingle && mf.FKFieldName != "" {
			fkOwners[mf.FKFieldName] = true
		}
	}
	for i := range mm.Fields {
		if fkOwners[mm.Fields[i].Name] {
			mm.Fields[i].Hidden = true
		}
	}

	// Overlay per-field overrides (matched by Name; unknown name = panic).
	for _, pf := range preset.Fields {
		target, err := mm.FindField(pf.Name)
		if err != nil {
			panic(fmt.Errorf("crud.DeriveMetaModel[%s].Fields: %w", mm.Name, err))
		}
		mergeMetaField(target, pf)
	}

	return mm
}

// mergeMetaField overlays a preset field's user-set values onto the derived
// field dst. Relation metadata and derive-authoritative ids are left intact.
func mergeMetaField(dst *MetaField, src MetaField) {
	if src.DisplayName != "" {
		dst.DisplayName = src.DisplayName
	}
	if src.FormInputType != "" {
		dst.FormInputType = src.FormInputType
	}
	if src.FormHelp != "" {
		dst.FormHelp = src.FormHelp
	}
	if src.FormFieldName != "" {
		dst.FormFieldName = src.FormFieldName
	}
	if src.ReadOnly {
		dst.ReadOnly = true
	}
	if src.Hidden {
		dst.Hidden = true
	}
	if src.Sortable {
		dst.Sortable = true
	}
	if src.Searchable {
		dst.Searchable = true
	}
	if src.FieldValidate != nil {
		dst.FieldValidate = src.FieldValidate
	}
	if src.DisplayValue != nil {
		dst.DisplayValue = src.DisplayValue
	}
	if src.GenFormElement != nil {
		dst.GenFormElement = src.GenFormElement
	}
	if src.BindStrings != nil {
		dst.BindStrings = src.BindStrings
	}
}

func deriveField(f reflect.StructField, tf site.TimeFormatter) MetaField {
	mf := MetaField{
		Name:           f.Name,
		DisplayName:    f.Name,
		FormInputType:  inputTypeFor(f.Type),
		Sortable:       isSortableKind(f.Type),
		Searchable:     isSearchableKind(f.Type),
		FormFieldName:  f.Name,
		DisplayValue:   DefaultDisplayValue,
		GenFormElement: DefaultGenFormElement,
		BindStrings:    DefaultBindStrings,
	}

	// Detect relations: struct (non-time.Time) → single; slice-of-struct
	// → many2many or has-many based on the gorm tag.
	t := f.Type
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	timeType := reflect.TypeOf(time.Time{})
	gormTag := f.Tag.Get("gorm")

	// time.Time (and *time.Time): display through the model's TimeFormatter,
	// resolving the session zone from the render context. The default display
	// hook handles UTC; this captures a possibly-custom formatter.
	if t == timeType {
		mf.DisplayValue = timeDisplayHook(tf)
	}

	switch {
	case t.Kind() == reflect.Struct && t != timeType:
		mf.RelationKind = RelationSingle
		mf.RelatedTypeName = t.Name()
		mf.FKFieldName = f.Name + "ID"
		mf.FormFieldName = mf.FKFieldName
		mf.DisplayValue = relationSingleDisplay
		mf.GenFormElement = relationSingleFormElement
		mf.BindStrings = relationSingleBindStrings
		mf.Sortable = false
		mf.Searchable = false
	case t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Struct && t.Elem() != timeType:
		elem := t.Elem()
		for elem.Kind() == reflect.Pointer {
			elem = elem.Elem()
		}
		mf.RelatedTypeName = elem.Name()
		// Distinguish many2many vs has-many via the gorm tag.
		switch {
		case strings.Contains(gormTag, "many2many"):
			mf.RelationKind = RelationMany2Many
			mf.Multiple = true
			mf.DisplayValue = relationMultipleDisplay
			mf.GenFormElement = relationMultipleFormElement
			mf.BindStrings = relationMultipleBindStrings
		default:
			// foreignKey:... or no tag — treat as has-many: read-only on
			// the parent form, but visible in the dump/list.
			mf.RelationKind = RelationHasMany
			mf.Multiple = true
			mf.ReadOnly = true
			mf.DisplayValue = relationMultipleDisplay
			mf.GenFormElement = nil // never rendered (ReadOnly)
			mf.BindStrings = relationHasManyBindStrings
		}
		mf.Sortable = false
		mf.Searchable = false
	}
	return mf
}

func isSortableKind(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeOf(time.Time{}) {
		return true
	}
	switch t.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

func isSearchableKind(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Kind() == reflect.String
}

func inputTypeFor(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeOf(time.Time{}) {
		return "datetime-local"
	}
	switch t.Kind() {
	case reflect.Bool:
		return "checkbox"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	default:
		return "text"
	}
}

// ──────────────────────────────────────────────────────────────────────────
// Model-level defaults.
// ──────────────────────────────────────────────────────────────────────────

// DefaultDisplayValues walks the fields and extracts each value via
// reflection, then calls each field's DisplayValue hook.
func DefaultDisplayValues[T any](mm MetaModel[T], instance T) []templ.Component {
	rv := reflect.ValueOf(instance)
	for rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	out := make([]templ.Component, len(mm.Fields))
	for i, mf := range mm.Fields {
		if mf.Hidden {
			continue
		}
		fv := rv.FieldByName(mf.Name)
		if !fv.IsValid() {
			continue
		}
		out[i] = mf.DisplayValue(mf, fv.Interface())
	}
	return out
}

// DefaultGenFormElements is the form analogue of DefaultDisplayValues.
// For RelationSingle fields, the renderer needs the FK uint rather than
// the embedded struct value, so this walks the sibling FKFieldName when
// present and passes the uint to the hook.
func DefaultGenFormElements[T any](mm MetaModel[T], instance T) []templ.Component {
	rv := reflect.ValueOf(instance)
	for rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	out := make([]templ.Component, len(mm.Fields))
	for i, mf := range mm.Fields {
		if mf.Hidden {
			continue
		}
		if mf.GenFormElement == nil {
			continue
		}
		var argVal any
		if mf.RelationKind == RelationSingle && mf.FKFieldName != "" {
			fk := rv.FieldByName(mf.FKFieldName)
			if fk.IsValid() {
				argVal = fk.Interface()
			}
		} else {
			fv := rv.FieldByName(mf.Name)
			if !fv.IsValid() {
				continue
			}
			argVal = fv.Interface()
		}
		out[i] = mf.GenFormElement(mf, argVal)
	}
	return out
}

// DefaultBindForm walks the fields, parses each via BindStrings, runs
// FieldValidate on the parsed *value*, and finally calls mm.Validate
// (if set) for cross-field rules. Errors accumulate into
// ValidationErrors keyed by field name (or ModelLevelKey for the
// cross-field hook). Returns nil on success.
//
// Validation pipeline (per field, then once for the model):
//
//  1. BindStrings parses the wire value. On failure, record under the
//     field name and skip step 2 — no Go value to feed it.
//  2. FieldValidate receives the field's value (not the whole struct).
//     On failure, record under the field name.
//  3. After all fields done, if every field passed and mm.Validate is
//     set, run it. On failure, record under ModelLevelKey ("").
func DefaultBindForm[T any](mm MetaModel[T], form map[string][]string, out *T) error {
	verrs := ValidationErrors{}
	rv := reflect.ValueOf(out).Elem()
	for _, mf := range mm.Fields {
		if mf.Hidden || mf.ReadOnly {
			continue
		}
		// Relations carry IDs under FormFieldName (the FK for single, the
		// relation name for many2many). Scalars use the bare field name.
		key := mf.FormFieldName
		if key == "" {
			key = mf.Name
		}
		strs := form[key]
		if err := mf.BindStrings(mf, strs, out); err != nil {
			verrs[mf.Name] = err.Error()
			continue
		}
		if mf.FieldValidate != nil {
			// Extract just this field's value — validators see only
			// what they're validating, not the whole struct.
			fv := rv.FieldByName(mf.Name)
			if !fv.IsValid() {
				continue
			}
			if err := mf.FieldValidate(fv.Interface()); err != nil {
				verrs[mf.Name] = err.Error()
			}
		}
	}
	// Model-level cross-field validation runs only if every field
	// passed — otherwise its preconditions may not hold.
	if len(verrs) == 0 && mm.Validate != nil {
		if err := mm.Validate(*out); err != nil {
			verrs[ModelLevelKey] = err.Error()
		}
	}
	if len(verrs) > 0 {
		return verrs
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────────────
// Field-level defaults. These return templ.Component built via templ.Raw
// over carefully escaped HTML — avoids pulling templ codegen into this
// package while keeping output safe.
// ──────────────────────────────────────────────────────────────────────────

// DefaultDisplayValue renders the value for a table cell or detail row:
//   - a bool as a coloured yes/no badge (green = yes, red = no);
//   - a time.Time in UTC with an explicit "UTC" suffix (blank when zero);
//   - everything else as the escaped formatValue text.
//
// This is display-only. The form pre-fill (DefaultGenFormElement) uses
// formatValue directly, so a datetime-local input keeps its timezone-less
// "2006-01-02T15:04" value and a checkbox stays a checkbox.
func DefaultDisplayValue(mf MetaField, value any) templ.Component {
	switch v := value.(type) {
	case bool:
		return boolBadge(v)
	case time.Time:
		return timeCell(site.DefaultTimeFormatter{}, v)
	}
	return templ.Raw(html.EscapeString(formatValue(mf, value)))
}

// timeDisplayHook builds a DisplayValue hook that renders a time.Time through
// tf, resolving the session zone from the render context.
func timeDisplayHook(tf site.TimeFormatter) func(MetaField, any) templ.Component {
	if tf == nil {
		tf = site.DefaultTimeFormatter{}
	}
	return func(_ MetaField, value any) templ.Component {
		t, _ := value.(time.Time)
		return timeCell(tf, t)
	}
}

// timeCell renders t through tf at the context's session zone, deferred to
// templ render time so site.Timezone(ctx) reflects the request.
func timeCell(tf site.TimeFormatter, t time.Time) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		_, err := io.WriteString(w, html.EscapeString(tf.FormatTime(site.Timezone(ctx), t)))
		return err
	})
}

// boolBadge renders a DaisyUI yes/no badge — green for true, red for false.
func boolBadge(b bool) templ.Component {
	if b {
		return templ.Raw(`<span class="badge badge-success">yes</span>`)
	}
	return templ.Raw(`<span class="badge badge-error">no</span>`)
}

// DefaultGenFormElement renders an HTML form element appropriate for
// mf.FormInputType, pre-filled with value. Outputs DaisyUI classes
// (input input-bordered, checkbox, …) and assumes Tailwind+DaisyUI are
// loaded by the caller's page shell.
func DefaultGenFormElement(mf MetaField, value any) templ.Component {
	name := html.EscapeString(mf.Name)
	switch mf.FormInputType {
	case "checkbox":
		b, _ := value.(bool)
		checked := ""
		if b {
			checked = " checked"
		}
		// Pair the checkbox with a hidden field carrying "off", so an
		// unchecked checkbox still sends a value the server can detect.
		return templ.Raw(fmt.Sprintf(
			`<input type="hidden" name=%q value="off"/>`+
				`<input type="checkbox" name=%q value="on"%s class="checkbox"/>`,
			mf.Name, mf.Name, checked))
	case "number":
		step := ""
		if isFloatKind(reflect.ValueOf(value).Kind()) {
			step = ` step="any"`
		}
		return templ.Raw(fmt.Sprintf(
			`<input type="number" name=%q value="%s"%s class="input"/>`,
			name, html.EscapeString(formatValue(mf, value)), step))
	case "datetime-local":
		t, _ := value.(time.Time)
		return timeInput(mf.Name, t)
	default:
		return templ.Raw(fmt.Sprintf(
			`<input type=%q name=%q value="%s" class="input"/>`,
			html.EscapeString(mf.FormInputType), name,
			html.EscapeString(formatValue(mf, value))))
	}
}

// timeInput renders a datetime-local input pre-filled with t in the session
// zone, plus an adjacent label naming that zone (e.g. "CEST (+02:00)") so the
// user never has to guess which zone they're entering. Deferred to render time
// so site.Timezone(ctx) reflects the request; the input value stays the
// zone-less "2006-01-02T15:04" the widget requires. Bind reinterprets that
// wall clock in the same zone (see TryBindForm).
func timeInput(fieldName string, t time.Time) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		loc := site.Timezone(ctx)
		val := ""
		if !t.IsZero() {
			val = t.In(loc).Format("2006-01-02T15:04")
		}
		_, err := io.WriteString(w, fmt.Sprintf(
			`<div class="flex items-center gap-2">`+
				`<input type="datetime-local" name=%q value="%s" class="input"/>`+
				`<span class="text-xs opacity-60 whitespace-nowrap">%s</span>`+
				`</div>`,
			html.EscapeString(fieldName),
			html.EscapeString(val),
			html.EscapeString(site.ZoneLabel(loc, t)),
		))
		return err
	})
}

// DefaultBindStrings parses strs[0] into the field's Go type and writes
// it via reflection. Returns an error if parsing fails or the field is
// not settable.
func DefaultBindStrings(mf MetaField, strs []string, instance any) error {
	rv := reflect.ValueOf(instance)
	for rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	field := rv.FieldByName(mf.Name)
	if !field.IsValid() {
		return fmt.Errorf("no such field")
	}
	if !field.CanSet() {
		return fmt.Errorf("field not settable (pass a pointer to a struct)")
	}

	// time.Time first (it's a struct, would otherwise miss the switch).
	if field.Type() == reflect.TypeOf(time.Time{}) {
		if len(strs) == 0 || strs[0] == "" {
			field.Set(reflect.ValueOf(time.Time{}))
			return nil
		}
		t, err := time.Parse("2006-01-02T15:04", strs[0])
		if err != nil {
			return err
		}
		field.Set(reflect.ValueOf(t))
		return nil
	}

	// Checkbox: paired with a hidden "off" sentinel, so strs may be
	// ["off"] (unchecked), ["off","on"] (checked — browser sends both),
	// or ["on"] (checked, no hidden field). Treat any "on" as true.
	if field.Kind() == reflect.Bool {
		field.SetBool(containsOn(strs))
		return nil
	}

	s := ""
	if len(strs) > 0 {
		s = strs[0]
	}

	switch field.Kind() {
	case reflect.String:
		field.SetString(s)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if s == "" {
			field.SetInt(0)
			return nil
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return err
		}
		field.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if s == "" {
			field.SetUint(0)
			return nil
		}
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return err
		}
		field.SetUint(n)
	case reflect.Float32, reflect.Float64:
		if s == "" {
			field.SetFloat(0)
			return nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		field.SetFloat(f)
	case reflect.Slice:
		// []byte (and any byte-slice type) binds the submitted string as
		// its UTF-8 bytes. This keeps BLOB columns (e.g. an opaque
		// WebAuthn handle) bindable instead of erroring as "unsupported
		// kind slice". An empty string yields empty bytes. Non-byte
		// slices are relations and never reach DefaultBindStrings (they
		// carry relation-specific BindStrings hooks).
		if field.Type().Elem().Kind() == reflect.Uint8 {
			field.SetBytes([]byte(s))
			return nil
		}
		return fmt.Errorf("unsupported slice element kind %s", field.Type().Elem().Kind())
	default:
		return fmt.Errorf("unsupported kind %s", field.Kind())
	}
	return nil
}

// formatValue stringifies value for display / form pre-fill. time.Time
// is formatted as the HTML datetime-local-compatible layout; a byte slice
// is shown as its UTF-8 string (the caller's DisplayValue HTML-escapes it).
func formatValue(mf MetaField, value any) string {
	if t, ok := value.(time.Time); ok {
		if t.IsZero() {
			return ""
		}
		return t.Format("2006-01-02T15:04")
	}
	if b, ok := value.(bool); ok {
		if b {
			return "true"
		}
		return "false"
	}
	if b, ok := value.([]byte); ok {
		return string(b)
	}
	return fmt.Sprintf("%v", value)
}

func isFloatKind(k reflect.Kind) bool {
	return k == reflect.Float32 || k == reflect.Float64
}

func containsOn(strs []string) bool {
	for _, s := range strs {
		if strings.EqualFold(s, "on") || strings.EqualFold(s, "true") || s == "1" {
			return true
		}
	}
	return false
}
