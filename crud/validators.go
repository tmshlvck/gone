package crud

import (
	"fmt"
	"net/netip"
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

// Validator is the per-field validation hook. It receives only the
// field's value — no MetaField, no surrounding struct. This keeps
// validators trivially reusable across fields and easy to unit-test.
//
// If a custom validator needs context (e.g. the field's DisplayName
// for an error message), close over it at the point you assign it:
//
//	mm.Fields[i].FieldValidate = func(v any) error {
//	    if v == "" {
//	        return fmt.Errorf("%s is required", mm.Fields[i].DisplayName)
//	    }
//	    return nil
//	}
//
// Cross-field rules belong on MetaModel.Validate instead.
type Validator = func(value any) error

// NotEmpty fails for an empty string (after trimming) or zero-length
// slice/array/map.
func NotEmpty(value any) error {
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
	return func(value any) error {
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
	return func(value any) error {
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
	return func(value any) error {
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
	return func(value any) error {
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
func Email(value any) error {
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
	return func(value any) error {
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
	return func(value any) error {
		for _, v := range vs {
			if v == nil {
				continue
			}
			if err := v(value); err != nil {
				return err
			}
		}
		return nil
	}
}

// Any composes validators with OR semantics: passes if any validator
// passes, fails only if every validator fails. The failure message
// joins the individual messages with " or " so the user sees what each
// alternative expected.
//
// Typical use: accept either IPv4 or IPv6:
//
//	f.FieldValidate = crud.Any(crud.IPv4Addr, crud.IPv6Addr)
//
// Empty vs returns nil (no constraint), mirroring All.
func Any(vs ...Validator) Validator {
	return func(value any) error {
		var msgs []string
		for _, v := range vs {
			if v == nil {
				continue
			}
			if err := v(value); err == nil {
				return nil
			} else {
				msgs = append(msgs, err.Error())
			}
		}
		if len(msgs) == 0 {
			return nil
		}
		return fmt.Errorf("%s", strings.Join(msgs, " or "))
	}
}

// IPv4Addr fails if value is a non-empty string that doesn't parse as
// an IPv4 address. v4-mapped IPv6 ("::ffff:1.2.3.4") is rejected —
// strict v4 only. Empty strings pass; pair with NotEmpty when required.
func IPv4Addr(value any) error {
	s, ok := value.(string)
	if !ok || s == "" {
		return nil
	}
	addr, err := netip.ParseAddr(s)
	if err != nil || !addr.Is4() {
		return fmt.Errorf("must be a valid IPv4 address")
	}
	return nil
}

// IPv6Addr fails if value is a non-empty string that doesn't parse as
// an IPv6 address. Plain IPv4 (4-byte form) is rejected; v4-mapped IPv6
// ("::ffff:1.2.3.4") passes — it's a valid IPv6 representation.
// Empty strings pass.
func IPv6Addr(value any) error {
	s, ok := value.(string)
	if !ok || s == "" {
		return nil
	}
	addr, err := netip.ParseAddr(s)
	if err != nil || addr.Is4() {
		return fmt.Errorf("must be a valid IPv6 address")
	}
	return nil
}

// IPv4Net fails if value is a non-empty string that doesn't parse as
// an IPv4 CIDR (e.g. "10.0.0.0/24"). The address must be IPv4 and the
// prefix length must be 0–32. Empty strings pass.
func IPv4Net(value any) error {
	s, ok := value.(string)
	if !ok || s == "" {
		return nil
	}
	prefix, err := netip.ParsePrefix(s)
	if err != nil || !prefix.Addr().Is4() {
		return fmt.Errorf("must be a valid IPv4 network (e.g. 10.0.0.0/24)")
	}
	return nil
}

// IPv6Net fails if value is a non-empty string that doesn't parse as
// an IPv6 CIDR (e.g. "2001:db8::/32"). The address must be IPv6 and the
// prefix length must be 0–128. Empty strings pass.
func IPv6Net(value any) error {
	s, ok := value.(string)
	if !ok || s == "" {
		return nil
	}
	prefix, err := netip.ParsePrefix(s)
	if err != nil || prefix.Addr().Is4() {
		return fmt.Errorf("must be a valid IPv6 network (e.g. 2001:db8::/32)")
	}
	return nil
}
