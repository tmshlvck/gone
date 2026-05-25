package crud

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

type validatable struct {
	Name  string
	Age   int
	Score float64
	Email string
}

func mfFor(name string) MetaField { return MetaField{Name: name} }

func TestNotEmpty(t *testing.T) {
	v := validatable{Name: ""}
	if err := NotEmpty(mfFor("Name"), &v); err == nil {
		t.Error("empty string should fail")
	}
	v.Name = "   " // whitespace only
	if err := NotEmpty(mfFor("Name"), &v); err == nil {
		t.Error("whitespace-only string should fail")
	}
	v.Name = "Aragorn"
	if err := NotEmpty(mfFor("Name"), &v); err != nil {
		t.Errorf("non-empty string should pass: %v", err)
	}
}

func TestMinLen_MaxLen(t *testing.T) {
	v := validatable{Name: "Al"}
	if err := MinLen(3)(mfFor("Name"), &v); err == nil {
		t.Error("MinLen should fail for 'Al'")
	}
	v.Name = "Aragorn"
	if err := MinLen(3)(mfFor("Name"), &v); err != nil {
		t.Errorf("MinLen(3) should pass for 'Aragorn': %v", err)
	}
	v.Name = strings.Repeat("x", 50)
	if err := MaxLen(10)(mfFor("Name"), &v); err == nil {
		t.Error("MaxLen(10) should fail for 50 chars")
	}
}

func TestIntRange(t *testing.T) {
	v := validatable{Age: 5}
	if err := IntRange(10, 20)(mfFor("Age"), &v); err == nil {
		t.Error("5 should be out of [10,20]")
	}
	v.Age = 15
	if err := IntRange(10, 20)(mfFor("Age"), &v); err != nil {
		t.Errorf("15 should be in [10,20]: %v", err)
	}
	v.Age = 21
	if err := IntRange(10, 20)(mfFor("Age"), &v); err == nil {
		t.Error("21 should be out of [10,20]")
	}
}

func TestFloatRange(t *testing.T) {
	v := validatable{Score: 0.5}
	if err := FloatRange(0.0, 1.0)(mfFor("Score"), &v); err != nil {
		t.Errorf("0.5 should be in [0,1]: %v", err)
	}
	v.Score = 1.5
	if err := FloatRange(0.0, 1.0)(mfFor("Score"), &v); err == nil {
		t.Error("1.5 should be out of [0,1]")
	}
}

func TestEmail(t *testing.T) {
	v := validatable{Email: ""}
	// Empty passes (use NotEmpty separately if required).
	if err := Email(mfFor("Email"), &v); err != nil {
		t.Errorf("empty email should pass (use NotEmpty for required): %v", err)
	}
	v.Email = "notanemail"
	if err := Email(mfFor("Email"), &v); err == nil {
		t.Error("'notanemail' should fail")
	}
	v.Email = "user@example.com"
	if err := Email(mfFor("Email"), &v); err != nil {
		t.Errorf("valid email should pass: %v", err)
	}
}

func TestPattern(t *testing.T) {
	re := regexp.MustCompile(`^[A-Z][a-z]+$`)
	v := validatable{Name: "Aragorn"}
	if err := Pattern(re, "a capitalized word")(mfFor("Name"), &v); err != nil {
		t.Errorf("'Aragorn' should match: %v", err)
	}
	v.Name = "aragorn"
	if err := Pattern(re, "a capitalized word")(mfFor("Name"), &v); err == nil {
		t.Error("'aragorn' should fail")
	}
}

func TestAll_ShortCircuitsOnFirstFailure(t *testing.T) {
	v := validatable{Name: ""}
	combined := All(NotEmpty, MinLen(3), MaxLen(5))
	err := combined(mfFor("Name"), &v)
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("All should report the first failure (required), got: %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────
// End-to-end: BindForm collects bind + validation errors into ValidationErrors.
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

	// Both fail: Title too short, Count out of range.
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
	// Bad input → parse fails first, Validate is skipped.
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
	// Bind error mentions parse problem, NOT the IntRange message
	// (validation didn't run).
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
