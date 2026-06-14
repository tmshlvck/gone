package crud

import (
	"fmt"
	"html"
	"reflect"
	"strings"

	"github.com/a-h/templ"
)

// Ready-made hooks for the common secret / password fields. Each plugs into
// one of MetaField's generic transform hooks (DisplayValue / GenFormElement /
// BindStrings) — nothing is special-cased; you compose them on a field in a
// DeriveMetaModel preset.

// Redact is a DisplayValue hook for a sensitive or opaque field: it never
// renders the value, only whether one is present — "-hidden-" (italic) when
// non-empty (handles strings, []byte, etc.), "-empty-" otherwise. Pair with
// ReadOnly:true so the field shows redacted but is never sent to a form or
// bound (a round-trip can't corrupt it):
//
//	{Name: "TOTPSecret", ReadOnly: true, DisplayValue: crud.Redact}
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
//	{Name: "PasswordHash", DisplayName: "Password", FormInputType: "password",
//	    DisplayValue:   crud.Redact,
//	    GenFormElement: crud.PasswordInput,
//	    BindStrings:    crud.HashWith(auth.HashPassword)}
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
