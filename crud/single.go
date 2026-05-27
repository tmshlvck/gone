package crud

import (
	"net/http"

	"github.com/a-h/templ"
)

// ──────────────────────────────────────────────────────────────────────────
// Single-instance render + bind helpers on MetaModel.
//
// These are the primitives an application uses when it wants the
// library to render a form / dump and bind a POST, but wants to own
// the routing, the authz, and the data accessors itself.
//
// The library's CRUDTable composes these primitives for the table case;
// MetaModel itself stays pure (no routes, no data, no authz).
// ──────────────────────────────────────────────────────────────────────────

// FormOpts carries the per-render parameters for RenderForm. Callers
// pass action URL + HTMX target + label + optional title + validation
// errors + success message in one struct, so the signature stays stable
// as we add more rendering concerns.
type FormOpts struct {
	ActionURL   string           // form's POST target and hx-post URL
	HXTarget    string           // hx-target (CSS selector); empty = browser-only submit
	SubmitLabel string           // submit button label; "Save" if empty
	Title       string           // optional <h3> above the form
	Errors      ValidationErrors // empty/nil = fresh form; ModelLevelKey("") = banner above
	SuccessMsg  string           // optional green banner above the form
}

// RenderDisplay returns the bare key/value table for an instance — no
// chrome, no Edit button, no header. The caller's pageShell or modal
// wrapper supplies surrounding markup.
func (mm *MetaModel[T]) RenderDisplay(instance T) templ.Component {
	return DisplayView(DisplayViewData{
		Fields: mm.Fields,
		Cells:  mm.DisplayValues(*mm, instance),
	})
}

// RenderForm returns the bare form for an instance. opts.ActionURL is
// the POST target; opts.HXTarget enables HTMX submission. opts.Errors
// is the raw ValidationErrors from a previous BindForm — the form
// template picks ModelLevelKey ("") for the alert banner and the
// remaining entries for per-field messages; no upstream splitting
// required. SuccessMsg renders a green alert above the form.
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
		Inputs:      mm.GenFormElements(*mm, instance),
		Errors:      opts.Errors,
		SuccessMsg:  opts.SuccessMsg,
		HXTarget:    opts.HXTarget,
	})
}

// TryBindForm wraps ParseForm + BindForm into one call. Returns nil on
// success or the ValidationErrors from BindForm. Callers feed the
// returned error straight back into RenderForm via FormOpts.Errors —
// no upstream splitting needed (errors.As to ValidationErrors works
// because TryBindForm returns the value directly).
func (mm *MetaModel[T]) TryBindForm(r *http.Request, out *T) error {
	if err := r.ParseForm(); err != nil {
		return err
	}
	return mm.BindForm(*mm, r.PostForm, out)
}
