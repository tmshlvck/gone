// Package crud implements a simplified version of PRD §6.1–6.2.
//
// MetaField is non-generic; MetaModel[T] is generic over the model type.
// DeriveMetaModel[T]() walks T via reflection and installs reflect-based
// closures on each MetaField. Callers can post-mutate the returned model
// to override defaults (e.g. set FormInputType="email" on a particular
// field).
//
// Scope of this initial cut: scalar fields (string, signed/unsigned int,
// float, bool, time.Time). Relations, list-of-primitives, and validation
// hooks are stubbed and will land in later iterations.
package crud

import (
	"fmt"
	"html"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
)

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

	// DisplayValue renders the field's typed Go value as a templ.Component
	// (a single table cell or dump entry). value is the already-extracted
	// field value, not the whole instance.
	DisplayValue func(mf MetaField, value any) templ.Component

	// GenFormElement renders an <input> / <select> / etc. pre-filled with
	// value. Form name attribute is mf.Name.
	GenFormElement func(mf MetaField, value any) templ.Component

	// FromStrings parses wire form values into the field's Go type and
	// writes them into instance via reflection. strs is form[mf.Name];
	// an empty slice means the field was absent (e.g. unchecked checkbox).
	FromStrings func(mf MetaField, strs []string, instance any) error
}

// MetaModel is the per-type description used to render and bind. T is the
// model type. Hooks accept mm as their first argument so callers can
// post-mutate the model and the hooks see the current state.
type MetaModel[T any] struct {
	Fields []MetaField

	Name        string // type name (e.g. "ExampleConfig")
	DisplayName string

	DisplayValues   func(mm MetaModel[T], instance T) []templ.Component
	GenFormElements func(mm MetaModel[T], instance T) []templ.Component
	BindForm        func(mm MetaModel[T], form map[string][]string, out *T) error
}

// DeriveMetaModel reflects T, builds default MetaFields, and installs the
// model-level hooks. Caller may post-mutate the returned model — the
// hooks read mm at call time so changes are observed.
func DeriveMetaModel[T any]() (MetaModel[T], error) {
	var zero T
	rt := reflect.TypeOf(zero)
	for rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	if rt.Kind() != reflect.Struct {
		return MetaModel[T]{}, fmt.Errorf("DeriveMetaModel: %v is not a struct", rt)
	}

	mm := MetaModel[T]{
		Name:        rt.Name(),
		DisplayName: rt.Name(),
	}

	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		mm.Fields = append(mm.Fields, deriveField(f))
	}

	mm.DisplayValues = DefaultDisplayValues[T]
	mm.GenFormElements = DefaultGenFormElements[T]
	mm.BindForm = DefaultBindForm[T]

	return mm, nil
}

func deriveField(f reflect.StructField) MetaField {
	return MetaField{
		Name:           f.Name,
		DisplayName:    f.Name,
		FormInputType:  inputTypeFor(f.Type),
		Sortable:       isSortableKind(f.Type),
		Searchable:     isSearchableKind(f.Type),
		DisplayValue:   DefaultDisplayValue,
		GenFormElement: DefaultGenFormElement,
		FromStrings:    DefaultFromStrings,
	}
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
		fv := rv.FieldByName(mf.Name)
		if !fv.IsValid() {
			continue
		}
		out[i] = mf.GenFormElement(mf, fv.Interface())
	}
	return out
}

// DefaultBindForm walks the fields and calls each field's FromStrings to
// write the wire values into out.
func DefaultBindForm[T any](mm MetaModel[T], form map[string][]string, out *T) error {
	for _, mf := range mm.Fields {
		if mf.Hidden || mf.ReadOnly {
			continue
		}
		strs := form[mf.Name]
		if err := mf.FromStrings(mf, strs, out); err != nil {
			return fmt.Errorf("%s: %w", mf.Name, err)
		}
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────────────
// Field-level defaults. These return templ.Component built via templ.Raw
// over carefully escaped HTML — avoids pulling templ codegen into this
// package while keeping output safe.
// ──────────────────────────────────────────────────────────────────────────

// DefaultDisplayValue renders the value as a text node.
func DefaultDisplayValue(mf MetaField, value any) templ.Component {
	return templ.Raw(html.EscapeString(formatValue(mf, value)))
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
			`<input type="number" name=%q value="%s"%s class="input input-bordered"/>`,
			name, html.EscapeString(formatValue(mf, value)), step))
	default:
		return templ.Raw(fmt.Sprintf(
			`<input type=%q name=%q value="%s" class="input input-bordered"/>`,
			html.EscapeString(mf.FormInputType), name,
			html.EscapeString(formatValue(mf, value))))
	}
}

// DefaultFromStrings parses strs[0] into the field's Go type and writes
// it via reflection. Returns an error if parsing fails or the field is
// not settable.
func DefaultFromStrings(mf MetaField, strs []string, instance any) error {
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
	default:
		return fmt.Errorf("unsupported kind %s", field.Kind())
	}
	return nil
}

// formatValue stringifies value for display / form pre-fill. time.Time
// is formatted as the HTML datetime-local-compatible layout.
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
