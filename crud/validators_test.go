package crud

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

// Validators take only the field's value — no MetaField, no instance.

func TestNotEmpty(t *testing.T) {
	if err := NotEmpty(""); err == nil {
		t.Error("empty string should fail")
	}
	if err := NotEmpty("   "); err == nil {
		t.Error("whitespace-only string should fail")
	}
	if err := NotEmpty("Aragorn"); err != nil {
		t.Errorf("non-empty string should pass: %v", err)
	}
	if err := NotEmpty([]string{}); err == nil {
		t.Error("empty slice should fail")
	}
	if err := NotEmpty([]string{"x"}); err != nil {
		t.Errorf("non-empty slice should pass: %v", err)
	}
}

func TestMinLen_MaxLen(t *testing.T) {
	if err := MinLen(3)("Al"); err == nil {
		t.Error("MinLen(3) should fail for 'Al'")
	}
	if err := MinLen(3)("Aragorn"); err != nil {
		t.Errorf("MinLen(3) should pass for 'Aragorn': %v", err)
	}
	if err := MaxLen(10)(strings.Repeat("x", 50)); err == nil {
		t.Error("MaxLen(10) should fail for 50 chars")
	}
}

func TestIntRange(t *testing.T) {
	if err := IntRange(10, 20)(5); err == nil {
		t.Error("5 should be out of [10,20]")
	}
	if err := IntRange(10, 20)(15); err != nil {
		t.Errorf("15 should be in [10,20]: %v", err)
	}
	if err := IntRange(10, 20)(uint64(21)); err == nil {
		t.Error("uint64(21) should be out of [10,20]")
	}
}

func TestFloatRange(t *testing.T) {
	if err := FloatRange(0.0, 1.0)(0.5); err != nil {
		t.Errorf("0.5 should be in [0,1]: %v", err)
	}
	if err := FloatRange(0.0, 1.0)(1.5); err == nil {
		t.Error("1.5 should be out of [0,1]")
	}
	if err := FloatRange(0.0, 1.0)(float32(0.25)); err != nil {
		t.Errorf("float32 should be accepted: %v", err)
	}
}

func TestEmail(t *testing.T) {
	// Empty passes (use NotEmpty separately if required).
	if err := Email(""); err != nil {
		t.Errorf("empty email should pass: %v", err)
	}
	if err := Email("notanemail"); err == nil {
		t.Error("'notanemail' should fail")
	}
	if err := Email("user@example.com"); err != nil {
		t.Errorf("valid email should pass: %v", err)
	}
}

func TestPattern(t *testing.T) {
	re := regexp.MustCompile(`^[A-Z][a-z]+$`)
	if err := Pattern(re, "a capitalized word")("Aragorn"); err != nil {
		t.Errorf("'Aragorn' should match: %v", err)
	}
	if err := Pattern(re, "a capitalized word")("aragorn"); err == nil {
		t.Error("'aragorn' should fail")
	}
}

func TestAll_ShortCircuitsOnFirstFailure(t *testing.T) {
	combined := All(NotEmpty, MinLen(3), MaxLen(5))
	err := combined("")
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("All should report the first failure (required), got: %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────
// End-to-end: BindForm collects bind + per-field + model-level errors
// into ValidationErrors.
// ──────────────────────────────────────────────────────────────────────────

type formModel struct {
	Title string
	Count int
}

func TestDefaultBindForm_RunsFieldValidateAfterParse(t *testing.T) {
	mm, _ := DeriveMetaModel[formModel]()
	for i := range mm.Fields {
		switch mm.Fields[i].Name {
		case "Title":
			mm.Fields[i].FieldValidate = All(NotEmpty, MinLen(3))
		case "Count":
			mm.Fields[i].FieldValidate = IntRange(0, 10)
		}
	}

	form := map[string][]string{
		"Title": {"Hi"},
		"Count": {"42"},
	}
	var out formModel
	err := mm.BindForm(mm, form, &out)
	if err == nil {
		t.Fatal("expected ValidationErrors")
	}
	var verrs ValidationErrors
	if !errors.As(err, &verrs) {
		t.Fatalf("expected ValidationErrors, got %T: %v", err, err)
	}
	if msg := verrs["Title"]; !strings.Contains(msg, "at least 3") {
		t.Errorf("Title error = %q; want min-length message", msg)
	}
	if msg := verrs["Count"]; !strings.Contains(msg, "between 0 and 10") {
		t.Errorf("Count error = %q; want range message", msg)
	}
}

func TestDefaultBindForm_BindErrorSkipsValidate(t *testing.T) {
	mm, _ := DeriveMetaModel[formModel]()
	for i := range mm.Fields {
		if mm.Fields[i].Name == "Count" {
			mm.Fields[i].FieldValidate = IntRange(0, 10)
		}
	}
	form := map[string][]string{"Count": {"notnumber"}}
	var out formModel
	err := mm.BindForm(mm, form, &out)
	if err == nil {
		t.Fatal("expected error")
	}
	var verrs ValidationErrors
	if !errors.As(err, &verrs) {
		t.Fatalf("expected ValidationErrors, got %T", err)
	}
	msg := verrs["Count"]
	if msg == "" {
		t.Fatal("expected Count error")
	}
	if strings.Contains(msg, "between") {
		t.Errorf("FieldValidate should be skipped after bind error; got %q", msg)
	}
}

func TestDefaultBindForm_AllPassReturnsNil(t *testing.T) {
	mm, _ := DeriveMetaModel[formModel]()
	for i := range mm.Fields {
		switch mm.Fields[i].Name {
		case "Title":
			mm.Fields[i].FieldValidate = NotEmpty
		case "Count":
			mm.Fields[i].FieldValidate = IntRange(0, 100)
		}
	}
	form := map[string][]string{
		"Title": {"Ok"},
		"Count": {"50"},
	}
	var out formModel
	if err := mm.BindForm(mm, form, &out); err != nil {
		t.Errorf("expected nil error on valid input; got %v", err)
	}
	if out.Title != "Ok" || out.Count != 50 {
		t.Errorf("instance not populated: %+v", out)
	}
}

// ──────────────────────────────────────────────────────────────────────────
// Cross-field MetaModel.Validate hook — receives only the populated
// instance.
// ──────────────────────────────────────────────────────────────────────────

func TestModelValidate_RunsWhenSet_AndOnlyAfterFieldsPass(t *testing.T) {
	mm, _ := DeriveMetaModel[formModel]()
	for i := range mm.Fields {
		if mm.Fields[i].Name == "Count" {
			mm.Fields[i].FieldValidate = IntRange(0, 1000) // permissive
		}
	}
	mm.Validate = func(v formModel) error {
		digits := len(strings.TrimSpace(strings.TrimLeft(itoa(v.Count), "-")))
		if len(v.Title) != digits {
			return errors.New("Title length must equal digits in Count")
		}
		return nil
	}

	// All field-level validators pass; cross-field fails.
	form := map[string][]string{
		"Title": {"abcd"}, // 4 chars
		"Count": {"12"},   // 2 digits
	}
	var out formModel
	err := mm.BindForm(mm, form, &out)
	var verrs ValidationErrors
	if !errors.As(err, &verrs) {
		t.Fatalf("expected ValidationErrors, got %T: %v", err, err)
	}
	if msg, ok := verrs[ModelLevelKey]; !ok {
		t.Errorf("expected model-level error under ModelLevelKey, got %v", verrs)
	} else if !strings.Contains(msg, "Title length") {
		t.Errorf("model-level message = %q", msg)
	}

	// When fields fail, model-level Validate must NOT run.
	form["Count"] = []string{"notnumber"}
	out = formModel{}
	err = mm.BindForm(mm, form, &out)
	if !errors.As(err, &verrs) {
		t.Fatalf("expected ValidationErrors")
	}
	if _, ok := verrs[ModelLevelKey]; ok {
		t.Errorf("model-level Validate should be skipped when fields fail; got %v", verrs)
	}
}

func TestModelValidate_NilMeansNoCrossField(t *testing.T) {
	mm, _ := DeriveMetaModel[formModel]()
	// Validate is nil — must NOT run, and clean inputs pass cleanly.
	form := map[string][]string{"Title": {"x"}, "Count": {"5"}}
	var out formModel
	if err := mm.BindForm(mm, form, &out); err != nil {
		t.Errorf("nil Validate should not introduce errors; got %v", err)
	}
}

// itoa avoids strconv to keep this test file dependency-free of stdlib
// formatters that may themselves panic on weird input.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var out []byte
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}
