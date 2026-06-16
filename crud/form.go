package crud

import (
	"net/http"
	"reflect"
	"time"

	"github.com/a-h/templ"
	"github.com/tmshlvck/gone/site"
)

// ──────────────────────────────────────────────────────────────────────────
// Form render + bind primitives on MetaModel.
//
// These are what an application uses when it wants the library to render a
// form and bind a POST, but owns the routing, authz, and data accessors
// itself. CRUDTable composes the very same primitives for the table case
// (see createForm/editForm); MetaModel itself stays pure (no routes, no
// data, no authz).
// ──────────────────────────────────────────────────────────────────────────

// FormOpts carries the per-render parameters for RenderForm. Callers pass
// action URL + HTMX target + label + optional title + validation errors +
// success message in one struct, so the signature stays stable as we add
// more rendering concerns.
type FormOpts struct {
	ActionURL   string           // form's POST target and hx-post URL
	HXTarget    string           // hx-target (CSS selector); empty = browser-only submit
	SubmitLabel string           // submit button label; "Save" if empty
	Title       string           // optional <h3> above the form
	Errors      ValidationErrors // empty/nil = fresh form; ModelLevelKey("") = banner above
	SuccessMsg  string           // optional green banner above the form
}

// RenderForm returns the bare form for an instance. opts.ActionURL is the
// POST target; opts.HXTarget enables HTMX submission. opts.Errors is the raw
// ValidationErrors from a previous BindForm — the form template picks
// ModelLevelKey ("") for the alert banner and the remaining entries for
// per-field messages; no upstream splitting required. SuccessMsg renders a
// green alert above the form.
func (mm *MetaModel[T]) RenderForm(instance T, opts FormOpts) templ.Component {
	submit := opts.SubmitLabel
	if submit == "" {
		submit = "Save"
	}
	return FormView(FormViewData{
		DisplayName: opts.Title,
		ActionURL:   opts.ActionURL,
		SubmitText:  submit,
		Fields:      mm.Fields,
		Inputs:      mm.GenFormElements(instance),
		Errors:      opts.Errors,
		SuccessMsg:  opts.SuccessMsg,
		HXTarget:    opts.HXTarget,
	})
}

// TryBindForm wraps ParseForm + BindForm into one call. Returns nil on
// success or the ValidationErrors from BindForm. Callers feed the returned
// error straight back into RenderForm via FormOpts.Errors — no upstream
// splitting needed (errors.As to ValidationErrors works because TryBindForm
// returns the value directly).
func (mm *MetaModel[T]) TryBindForm(r *http.Request, out *T) error {
	if err := r.ParseForm(); err != nil {
		return err
	}
	if err := mm.BindForm(r.PostForm, out); err != nil {
		return err
	}
	// Zone-aware reinterpretation: BindForm parsed each datetime-local wall
	// clock as UTC; if the session has a non-UTC zone, reinterpret the same
	// wall clock as being in that zone (what the user saw when editing).
	// Storage stays UTC (site.ForceUTC). Only fields actually submitted by
	// the form are touched, so read-only times (e.g. a loaded CreatedAt on
	// an update) keep their real instant.
	if loc := site.Timezone(r.Context()); loc != time.UTC {
		reinterpretSubmittedTimes(mm, r.PostForm, out, loc)
	}
	return nil
}

var bindTimeType = reflect.TypeOf(time.Time{})

// reinterpretSubmittedTimes rebuilds each submitted time.Time / *time.Time
// field's wall clock in loc. "Submitted" = a non-hidden, non-readonly field
// whose key is present in the posted form — exactly the rendered
// datetime-local inputs. Zero values are left as-is.
func reinterpretSubmittedTimes[T any](mm *MetaModel[T], form map[string][]string, out *T, loc *time.Location) {
	rv := reflect.ValueOf(out).Elem()
	for _, mf := range mm.Fields {
		if mf.Hidden || mf.ReadOnly {
			continue
		}
		key := mf.FormFieldName
		if key == "" {
			key = mf.Name
		}
		if _, ok := form[key]; !ok {
			continue
		}
		f := rv.FieldByName(mf.Name)
		if !f.IsValid() || !f.CanSet() {
			continue
		}
		switch f.Type() {
		case bindTimeType:
			if t := f.Interface().(time.Time); !t.IsZero() {
				f.Set(reflect.ValueOf(reinterpretWallClock(t, loc)))
			}
		case reflect.PointerTo(bindTimeType):
			if !f.IsNil() {
				if t := f.Interface().(*time.Time); !t.IsZero() {
					rt := reinterpretWallClock(*t, loc)
					f.Set(reflect.ValueOf(&rt))
				}
			}
		}
	}
}

// reinterpretWallClock takes t's wall-clock components and rebuilds them in
// loc — turning "14:30 parsed as UTC" into "14:30 in loc".
func reinterpretWallClock(t time.Time, loc *time.Location) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), loc)
}
