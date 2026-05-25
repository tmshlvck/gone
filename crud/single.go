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
// ──────────────────────────────────────────────────────────────────────────

// DisplayComponent renders the dump fragment for an instance. If
// editURL is non-empty, an Edit button is rendered that hx-gets editURL
// into hxTarget (typically "#some-id" wrapping this component on the
// page).
func (mm *MetaModel[T]) DisplayComponent(instance T, editURL, hxTarget string) templ.Component {
	cells := mm.DisplayValues(*mm, instance)
	return DumpView(DumpViewData{
		DisplayName:  mm.DisplayName,
		EditURL:      editURL,
		EditHXTarget: hxTarget,
		Fields:       mm.Fields,
		Cells:        cells,
	})
}

// FormComponent renders the form fragment for an instance. The form
// posts to actionURL and (when hxTarget != "") submits via HTMX with
// hx-target=hxTarget — successful submissions and validation errors
// both swap into that container.
//
// The returned fragment is wrapped in a DaisyUI card so it matches the
// DumpView styling. Modal callers (CRUDTable's edit/create flow) build
// FormView directly with no card wrapper instead.
func (mm *MetaModel[T]) FormComponent(
	instance T,
	actionURL, hxTarget string,
	fieldErrors map[string]string,
	modelErr string,
) templ.Component {
	inputs := mm.GenFormElements(*mm, instance)
	return cardWrap(FormView(FormViewData{
		DisplayName: "Edit " + mm.DisplayName,
		ActionURL:   actionURL,
		BackURL:     "",
		SubmitText:  "Save",
		Fields:      mm.Fields,
		Inputs:      inputs,
		ErrMsg:      modelErr,
		FieldErrors: fieldErrors,
		HXTarget:    hxTarget,
	}))
}

// ──────────────────────────────────────────────────────────────────────────
// Partial-endpoint mounters
//
// These register fragment-only HTTP handlers. The application is
// responsible for the main page route(s) that embed DisplayComponent /
// FormComponent inside the app's own page shell.
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
		writeFragment(w, r, http.StatusOK, mm.DisplayComponent(instance, editURL, hxTarget))
	})
	return nil
}

// RouteForm mounts:
//
//	GET  formURL  → form fragment populated from getter
//	POST formURL  → bind + validate + setter; on success returns the
//	                dump fragment (Edit button pointing back at formURL);
//	                on validation error returns the form fragment with
//	                per-field error messages.
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

	mux.HandleFunc("GET "+formURL, func(w http.ResponseWriter, r *http.Request) {
		instance, err := loadInstance()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeFragment(w, r, http.StatusOK, mm.FormComponent(instance, formURL, hxTarget, nil, ""))
	})

	mux.HandleFunc("POST "+formURL, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Start from the current instance so unsubmitted hidden /
		// read-only fields keep their value.
		instance, err := loadInstance()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := mm.BindForm(*mm, r.PostForm, &instance); err != nil {
			fieldErrs, modelErr := splitValidationErr(err)
			// Status 200: HTMX only swaps response bodies on 2xx by
			// default, so a 4xx here would hide the re-rendered form.
			// The invalid state is fully expressed in the HTML
			// (alert + per-field errors), which is the form-handling
			// convention used by most server-rendered frameworks.
			writeFragment(w, r, http.StatusOK,
				mm.FormComponent(instance, formURL, hxTarget, fieldErrs, modelErr))
			return
		}
		if setter != nil {
			if err := setter(instance); err != nil {
				writeFragment(w, r, http.StatusOK,
					mm.FormComponent(instance, formURL, hxTarget, nil, err.Error()))
				return
			}
		}
		// Success: swap back to the dump fragment.
		writeFragment(w, r, http.StatusOK, mm.DisplayComponent(instance, formURL, hxTarget))
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
