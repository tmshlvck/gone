package crud

import (
	"net/http"

	"github.com/a-h/templ"
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
	return mm.BindForm(r.PostForm, out)
}
