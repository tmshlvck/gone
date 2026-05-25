package crud

import (
	"errors"
	"log"
	"net/http"

	"github.com/a-h/templ"
)

// ──────────────────────────────────────────────────────────────────────────
// Single-instance component renderers
//
// These return templ.Component fragments — no <html>/<body>/<style>.
// The application embeds them inside its own page shell however it
// wants, satisfying the "library renders components, app composes
// pages" separation.
//
// Each renderer accepts a partially-filled View Data struct: the
// MetaModel fills in Fields + Inputs/Cells from instance and its own
// schema, while the caller controls titles, URLs, hx-targets, error
// messages, and chrome flags (WrapInCard).
// ──────────────────────────────────────────────────────────────────────────

// RenderDisplayComponent returns the dump fragment for an instance.
// d.Fields and d.Cells are overwritten by the model; d.DisplayName
// defaults to mm.DisplayName when empty. The caller controls EditURL,
// EditHXTarget, and (eventually) any custom chrome.
func (mm *MetaModel[T]) RenderDisplayComponent(instance T, d DumpViewData) templ.Component {
	d.Fields = mm.Fields
	d.Cells = mm.DisplayValues(*mm, instance)
	if d.DisplayName == "" {
		d.DisplayName = mm.DisplayName
	}
	return DumpView(d)
}

// RenderFormComponent returns the form fragment for an instance.
// d.Fields and d.Inputs are overwritten by the model; d.DisplayName /
// d.SubmitText default to "Edit <ModelName>" / "Save" when empty. The
// caller controls ActionURL, HXTarget, error messages, and WrapInCard
// (true for inline use, false when the form lives inside a modal-box
// that already provides chrome).
func (mm *MetaModel[T]) RenderFormComponent(instance T, d FormViewData) templ.Component {
	d.Fields = mm.Fields
	d.Inputs = mm.GenFormElements(*mm, instance)
	if d.DisplayName == "" {
		d.DisplayName = "Edit " + mm.DisplayName
	}
	if d.SubmitText == "" {
		d.SubmitText = "Save"
	}
	return FormView(d)
}

// ──────────────────────────────────────────────────────────────────────────
// Partial-endpoint mounters
//
// These register fragment-only HTTP handlers. The application is
// responsible for the main page route(s) that embed the renderers
// inside the app's own page shell.
// ──────────────────────────────────────────────────────────────────────────

// RouteDisplay mounts GET displayURL → the dump fragment with an Edit
// button wired to editURL/hxTarget. Useful when the app wants to
// HTMX-refresh the dump (e.g. after an external state change).
//
// editURL may be empty to omit the Edit button.
func (mm *MetaModel[T]) RouteDisplay(
	mux *http.ServeMux,
	displayURL string,
	editURL, hxTarget string,
	getter func() (T, error),
) error {
	if mux == nil {
		return errors.New("nil mux")
	}
	mux.HandleFunc("GET "+displayURL, func(w http.ResponseWriter, r *http.Request) {
		instance, err := getter()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeFragment(w, r, http.StatusOK, mm.RenderDisplayComponent(instance, DumpViewData{
			EditURL:      editURL,
			EditHXTarget: hxTarget,
		}))
	})
	return nil
}

// RouteForm mounts:
//
//	GET  formURL  → form fragment populated from getter (WrapInCard=true)
//	POST formURL  → bind + validate + setter; on success returns the
//	                dump fragment with Edit pointing back at formURL;
//	                on validation error returns the form fragment with
//	                per-field error messages, still WrapInCard=true.
//
// hxTarget is the container both fragments target. nil getter falls
// back to the zero value (useful for "create new"-style forms).
func (mm *MetaModel[T]) RouteForm(
	mux *http.ServeMux,
	formURL, hxTarget string,
	getter func() (T, error),
	setter func(data T) error,
) error {
	if mux == nil {
		return errors.New("nil mux")
	}

	loadInstance := func() (T, error) {
		if getter == nil {
			var zero T
			return zero, nil
		}
		return getter()
	}

	formCfg := func(fieldErrs map[string]string, modelErr string) FormViewData {
		return FormViewData{
			ActionURL:   formURL,
			HXTarget:    hxTarget,
			WrapInCard:  true, // inline use — needs its own chrome
			FieldErrors: fieldErrs,
			ErrMsg:      modelErr,
		}
	}
	dumpCfg := DumpViewData{
		EditURL:      formURL,
		EditHXTarget: hxTarget,
	}

	mux.HandleFunc("GET "+formURL, func(w http.ResponseWriter, r *http.Request) {
		instance, err := loadInstance()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeFragment(w, r, http.StatusOK,
			mm.RenderFormComponent(instance, formCfg(nil, "")))
	})

	mux.HandleFunc("POST "+formURL, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		instance, err := loadInstance()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := mm.BindForm(*mm, r.PostForm, &instance); err != nil {
			fieldErrs, modelErr := splitValidationErr(err)
			// Status 200: HTMX only swaps response bodies on 2xx by
			// default, so a 4xx here would hide the re-rendered form.
			writeFragment(w, r, http.StatusOK,
				mm.RenderFormComponent(instance, formCfg(fieldErrs, modelErr)))
			return
		}
		if setter != nil {
			if err := setter(instance); err != nil {
				writeFragment(w, r, http.StatusOK,
					mm.RenderFormComponent(instance, formCfg(nil, err.Error())))
				return
			}
		}
		// Success: swap back to the dump fragment.
		writeFragment(w, r, http.StatusOK, mm.RenderDisplayComponent(instance, dumpCfg))
	})
	return nil
}

// writeFragment writes a templ.Component as the entire response body,
// no page chrome. Shared by Route* helpers.
func writeFragment(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := c.Render(r.Context(), w); err != nil {
		log.Printf("render: %v", err)
	}
}
