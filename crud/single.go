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
// These return templ.Component fragments — no <html>/<body>/<style>. The
// app embeds them inside its own page shell however it wants.
//
// All rendering config (URLs, HXTarget) lives on MetaModel. The
// renderers pull from there and never accept ViewData overrides — that
// way the CRUDTable case and the form_mem case both work off the same
// declarative model. The rendered fragments are BAREBONE: no card,
// no Edit / Cancel buttons, no page title. The caller (app pageShell
// or modal wrapper) provides chrome.
//
// *http.Request is accepted on every render path for future
// authz-driven rendering (hide Edit when CanUpdate is false, locale
// extraction, …); today most callers ignore r.
// ──────────────────────────────────────────────────────────────────────────

// RenderDisplayComponent returns the dump fragment for an instance.
// Pulls Fields + Cells from mm and the instance value. Output is just
// the field/value table — the caller wraps in card / modal-box / page
// shell as needed.
func (mm *MetaModel[T]) RenderDisplayComponent(r *http.Request, instance T) templ.Component {
	return DisplayView(DisplayViewData{
		Fields: mm.Fields,
		Cells:  mm.DisplayValues(*mm, instance),
	})
}

// RenderFormComponent returns the form fragment for an instance. URLs
// come from mm.FormURL / mm.HXTarget; the form's title is
// "Edit <DisplayName>" (intrinsic to a form's purpose). fieldErrors
// and modelErr drive the validation feedback — empty/nil means a
// fresh form.
func (mm *MetaModel[T]) RenderFormComponent(
	r *http.Request,
	instance T,
	fieldErrors map[string]string,
	modelErr string,
) templ.Component {
	return FormView(FormViewData{
		DisplayName: "Edit " + mm.DisplayName,
		ActionURL:   mm.FormURL,
		SubmitText:  "Save",
		Fields:      mm.Fields,
		Inputs:      mm.GenFormElements(*mm, instance),
		ErrMsg:      modelErr,
		FieldErrors: fieldErrors,
		HXTarget:    mm.HXTarget,
	})
}

// ──────────────────────────────────────────────────────────────────────────
// Partial-endpoint mounters
//
// These register fragment-only HTTP handlers at mm.FormURL / mm.DisplayURL.
// The application owns the main page route(s) that embed the renderers
// inside the app's own page shell.
// ──────────────────────────────────────────────────────────────────────────

// RouteDisplay mounts GET mm.DisplayURL → the dump fragment. The Edit
// button (if any) is the caller's chrome — RouteDisplay never adds one.
// Useful when the app wants to HTMX-refresh the dump (e.g. after an
// external state change).
//
// Returns an error if mux is nil, mm.DisplayURL is empty, or Authz
// denies all reads at registration time (no — authz is per-request).
func (mm *MetaModel[T]) RouteDisplay(
	mux *http.ServeMux,
	getter func() (T, error),
) error {
	if mux == nil {
		return errors.New("nil mux")
	}
	if mm.DisplayURL == "" {
		return errors.New("MetaModel.DisplayURL must be set before RouteDisplay")
	}
	authz := authzOrAllow(mm.Authz)
	mux.HandleFunc("GET "+mm.DisplayURL, func(w http.ResponseWriter, r *http.Request) {
		if !authz.CanRead(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		instance, err := getter()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeFragment(w, r, http.StatusOK, mm.RenderDisplayComponent(r, instance))
	})
	return nil
}

// RouteForm mounts:
//
//	GET  mm.FormURL  → form fragment populated from getter
//	POST mm.FormURL  → bind + validate + setter; on success returns the
//	                   dump fragment; on validation error returns the
//	                   form with per-field error messages.
//
// nil getter falls back to the zero value (useful for "create new"-style
// forms). The Authz check is CanRead for GET, CanUpdate for POST (or
// CanCreate when getter is nil — there's no prior row to "update").
func (mm *MetaModel[T]) RouteForm(
	mux *http.ServeMux,
	getter func() (T, error),
	setter func(data T) error,
) error {
	if mux == nil {
		return errors.New("nil mux")
	}
	if mm.FormURL == "" {
		return errors.New("MetaModel.FormURL must be set before RouteForm")
	}
	authz := authzOrAllow(mm.Authz)

	loadInstance := func() (T, error) {
		if getter == nil {
			var zero T
			return zero, nil
		}
		return getter()
	}

	// POST is CanCreate when there's no prior row to load (getter==nil
	// → "create new" form), CanUpdate otherwise.
	canWrite := func(r *http.Request) bool {
		if getter == nil {
			return authz.CanCreate(r)
		}
		return authz.CanUpdate(r)
	}

	mux.HandleFunc("GET "+mm.FormURL, func(w http.ResponseWriter, r *http.Request) {
		if !authz.CanRead(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		instance, err := loadInstance()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeFragment(w, r, http.StatusOK,
			mm.RenderFormComponent(r, instance, nil, ""))
	})

	mux.HandleFunc("POST "+mm.FormURL, func(w http.ResponseWriter, r *http.Request) {
		if !canWrite(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
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
				mm.RenderFormComponent(r, instance, fieldErrs, modelErr))
			return
		}
		if setter != nil {
			if err := setter(instance); err != nil {
				writeFragment(w, r, http.StatusOK,
					mm.RenderFormComponent(r, instance, nil, err.Error()))
				return
			}
		}
		// Success: swap back to the dump fragment.
		writeFragment(w, r, http.StatusOK, mm.RenderDisplayComponent(r, instance))
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
