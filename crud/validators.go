package crud

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

// ValidationErrors maps field name → error message. Returned by
// MetaModel.BindForm on bind or per-field validation failures.
//
// A nil or empty ValidationErrors signals success; handlers should test
// for that explicitly (don't compare to nil — Go's nil-interface gotcha
// would mistake an empty ValidationErrors for a non-nil error).
type ValidationErrors map[string]string

func (e ValidationErrors) Error() string {
	if len(e) == 0 {
		return "<no errors>"
	}
	parts := make([]string, 0, len(e))
	for f, msg := range e {
		parts = append(parts, f+": "+msg)
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}

// Validator is the per-field validation hook type. Use directly as
// MetaField.FieldValidate, or compose with All.
type Validator = func(mf MetaField, instance any) error

// NotEmpty fails if the field's string value is empty after trimming
// (also covers slice/array fields of length 0).
func NotEmpty(mf MetaField, instance any) error {
	v := lookupField(instance, mf.Name)
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.String:
		if strings.TrimSpace(v.String()) == "" {
			return fmt.Errorf("required")
		}
	case reflect.Slice, reflect.Array, reflect.Map:
		if v.Len() == 0 {
			return fmt.Errorf("required")
		}
	}
	return nil
}

// MinLen fails if a string field is shorter than n characters.
func MinLen(n int) Validator {
	return func(mf MetaField, instance any) error {
		v := lookupField(instance, mf.Name)
		if v.IsValid() && v.Kind() == reflect.String && len(v.String()) < n {
			return fmt.Errorf("must be at least %d characters", n)
		}
		return nil
	}
}

// MaxLen fails if a string field is longer than n characters.
func MaxLen(n int) Validator {
	return func(mf MetaField, instance any) error {
		v := lookupField(instance, mf.Name)
		if v.IsValid() && v.Kind() == reflect.String && len(v.String()) > n {
			return fmt.Errorf("must be at most %d characters", n)
		}
		return nil
	}
}

// IntRange fails if a signed/unsigned int field is outside [min, max].
func IntRange(min, max int64) Validator {
	return func(mf MetaField, instance any) error {
		v := lookupField(instance, mf.Name)
		if !v.IsValid() {
			return nil
		}
		var n int64
		switch v.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			n = v.Int()
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			n = int64(v.Uint())
		default:
			return nil
		}
		if n < min || n > max {
			return fmt.Errorf("must be between %d and %d", min, max)
		}
		return nil
	}
}

// FloatRange fails if a float field is outside [min, max].
func FloatRange(min, max float64) Validator {
	return func(mf MetaField, instance any) error {
		v := lookupField(instance, mf.Name)
		if v.IsValid() && (v.Kind() == reflect.Float32 || v.Kind() == reflect.Float64) {
			f := v.Float()
			if f < min || f > max {
				return fmt.Errorf("must be between %g and %g", min, max)
			}
		}
		return nil
	}
}

var emailRE = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// Email fails if the field's string value doesn't look like an email
// (a permissive regex, not full RFC 5322).
func Email(mf MetaField, instance any) error {
	v := lookupField(instance, mf.Name)
	if v.IsValid() && v.Kind() == reflect.String && v.String() != "" {
		if !emailRE.MatchString(v.String()) {
			return fmt.Errorf("must be a valid email address")
		}
	}
	return nil
}

// Pattern fails if a string field doesn't match the supplied regex.
// reason is used in the error message ("must match <reason>") and
// should describe the format in user-friendly terms.
func Pattern(re *regexp.Regexp, reason string) Validator {
	return func(mf MetaField, instance any) error {
		v := lookupField(instance, mf.Name)
		if v.IsValid() && v.Kind() == reflect.String && v.String() != "" {
			if !re.MatchString(v.String()) {
				if reason != "" {
					return fmt.Errorf("must be %s", reason)
				}
				return fmt.Errorf("invalid format")
			}
		}
		return nil
	}
}

// All composes validators in order. First failure short-circuits.
func All(vs ...Validator) Validator {
	return func(mf MetaField, instance any) error {
		for _, v := range vs {
			if v == nil {
				continue
			}
			if err := v(mf, instance); err != nil {
				return err
			}
		}
		return nil
	}
}

func lookupField(instance any, name string) reflect.Value {
	rv := reflect.ValueOf(instance)
	for rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return reflect.Value{}
	}
	return rv.FieldByName(name)
}
