package crud

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ValidationErrors maps field name → error message. Returned by
// MetaModel.BindForm on bind or per-field validation failures.
//
// The empty string "" is reserved for model-level errors emitted by
// MetaModel.Validate (cross-field rules).
//
// A nil or empty ValidationErrors signals success; handlers should test
// for that explicitly (don't compare to nil — Go's nil-interface gotcha
// would mistake an empty ValidationErrors for a non-nil error).
type ValidationErrors map[string]string

// ModelLevelKey is the key under which MetaModel.Validate's error is
// stored inside a ValidationErrors map.
const ModelLevelKey = ""

func (e ValidationErrors) Error() string {
	if len(e) == 0 {
		return "<no errors>"
	}
	parts := make([]string, 0, len(e))
	for f, msg := range e {
		if f == ModelLevelKey {
			parts = append(parts, msg)
		} else {
			parts = append(parts, f+": "+msg)
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}

// Validator is the per-field validation hook. It receives the field's
// metadata and the already-extracted Go-typed value (NOT the whole
// struct), enforcing one-field-at-a-time separation of concerns.
//
// Use it directly as MetaField.FieldValidate, or compose with All.
// Cross-field rules belong on MetaModel.Validate instead.
type Validator = func(mf MetaField, value any) error

// NotEmpty fails for an empty string (after trimming) or zero-length
// slice/array/map.
func NotEmpty(mf MetaField, value any) error {
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("required")
		}
	case []string:
		if len(v) == 0 {
			return fmt.Errorf("required")
		}
	}
	return nil
}

// MinLen fails if a string value is shorter than n characters.
func MinLen(n int) Validator {
	return func(mf MetaField, value any) error {
		s, ok := value.(string)
		if !ok {
			return nil
		}
		if len(s) < n {
			return fmt.Errorf("must be at least %d characters", n)
		}
		return nil
	}
}

// MaxLen fails if a string value is longer than n characters.
func MaxLen(n int) Validator {
	return func(mf MetaField, value any) error {
		s, ok := value.(string)
		if !ok {
			return nil
		}
		if len(s) > n {
			return fmt.Errorf("must be at most %d characters", n)
		}
		return nil
	}
}

// IntRange fails if an integer value is outside [min, max]. Accepts any
// of Go's signed or unsigned integer types.
func IntRange(min, max int64) Validator {
	return func(mf MetaField, value any) error {
		var n int64
		switch v := value.(type) {
		case int:
			n = int64(v)
		case int8:
			n = int64(v)
		case int16:
			n = int64(v)
		case int32:
			n = int64(v)
		case int64:
			n = v
		case uint:
			n = int64(v)
		case uint8:
			n = int64(v)
		case uint16:
			n = int64(v)
		case uint32:
			n = int64(v)
		case uint64:
			n = int64(v)
		default:
			return nil
		}
		if n < min || n > max {
			return fmt.Errorf("must be between %d and %d", min, max)
		}
		return nil
	}
}

// FloatRange fails if a float value is outside [min, max].
func FloatRange(min, max float64) Validator {
	return func(mf MetaField, value any) error {
		var f float64
		switch v := value.(type) {
		case float32:
			f = float64(v)
		case float64:
			f = v
		default:
			return nil
		}
		if f < min || f > max {
			return fmt.Errorf("must be between %g and %g", min, max)
		}
		return nil
	}
}

var emailRE = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// Email fails for a non-empty string that doesn't match a permissive
// email regex. Empty strings pass — use NotEmpty separately when
// required.
func Email(mf MetaField, value any) error {
	s, ok := value.(string)
	if !ok || s == "" {
		return nil
	}
	if !emailRE.MatchString(s) {
		return fmt.Errorf("must be a valid email address")
	}
	return nil
}

// Pattern fails for a non-empty string that doesn't match re. reason
// is used in the error message ("must be <reason>") and should describe
// the format in user-friendly terms. Empty strings pass.
func Pattern(re *regexp.Regexp, reason string) Validator {
	return func(mf MetaField, value any) error {
		s, ok := value.(string)
		if !ok || s == "" {
			return nil
		}
		if !re.MatchString(s) {
			if reason != "" {
				return fmt.Errorf("must be %s", reason)
			}
			return fmt.Errorf("invalid format")
		}
		return nil
	}
}

// All composes validators in order. First failure short-circuits.
func All(vs ...Validator) Validator {
	return func(mf MetaField, value any) error {
		for _, v := range vs {
			if v == nil {
				continue
			}
			if err := v(mf, value); err != nil {
				return err
			}
		}
		return nil
	}
}
