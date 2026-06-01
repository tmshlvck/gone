package auth

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"gorm.io/gorm"
)

// mountAccountRoutes registers the GET/POST /account/{ref} pair on
// the supplied mux. Called from AuthGORM.Route after the login + logout
// handlers are already mounted.
//
// "ref" is either the literal string "me" (resolves to the current
// user) or a numeric ID (resolves the named UserGORM row). Both
// routes share a single handler that branches on r.Method.
func (a *AuthGORM) mountAccountRoutes(mux Mux, base string, shell PageShellFunc) {
	mux.HandleFunc("GET "+base+"/account/{ref}", func(w http.ResponseWriter, r *http.Request) {
		a.serveAccountForm(w, r, shell, "", "")
	})
	mux.HandleFunc("POST "+base+"/account/{ref}", func(w http.ResponseWriter, r *http.Request) {
		a.handleAccountPost(w, r, shell)
	})
}

// serveAccountForm renders the form for the GET path AND from the
// POST handler when validation fails / on success.
//
// HTMX requests get the bare form fragment + HX-Trigger openModal
// so the caller's modal pops; plain GETs get the form wrapped in
// the page shell.
func (a *AuthGORM) serveAccountForm(w http.ResponseWriter, r *http.Request, shell PageShellFunc, errMsg, successMsg string) {
	current := a.CurrentUser(r)
	if current == nil {
		// Anonymous → bounce through login with a return target.
		if isHTMXAuthRequest(r) {
			w.Header().Set("HX-Redirect", a.LoginURL(r.URL.Path))
			return
		}
		http.Redirect(w, r, a.LoginURL(r.URL.Path), http.StatusSeeOther)
		return
	}

	target, ok := a.resolveAccountRef(r, current)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !accountAllowed(current, target) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	htmx := isHTMXAuthRequest(r)
	modalBodyID := r.Header.Get("HX-Target")
	actionURL := a.urlBase + "/account/" + strconv.FormatUint(uint64(target.ID), 10)
	form := accountForm(accountFormData{
		ActionURL:      actionURL,
		TargetUsername: target.Username,
		IsSelf:         current.Username() == target.Username,
		CSRFToken:      CSRFToken(r.Context()),
		Error:          errMsg,
		Success:        successMsg,
		Modal:          htmx,
		ModalBodyID:    modalBodyID,
	})

	if htmx {
		// HTMX path: return the fragment for the modal body. Fire
		// openModal only on the initial GET (no errMsg/successMsg) —
		// re-renders inside an already-open modal don't need to
		// re-open it.
		if modalID := modalIDFromHXTarget(r); modalID != "" && errMsg == "" && successMsg == "" {
			w.Header().Set("HX-Trigger", fmt.Sprintf(`{"openModal":%q}`, modalID))
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		renderOrLog(w, r, form)
		return
	}
	writeShell(w, r, "Change password — "+target.Username, form, shell)
}

// handleAccountPost validates the form and either updates the
// password or re-renders the form with an error.
func (a *AuthGORM) handleAccountPost(w http.ResponseWriter, r *http.Request, shell PageShellFunc) {
	current := a.CurrentUser(r)
	if current == nil {
		// CSRFWrap already guarded this; reaching here means the
		// session evaporated between page load and submit.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	target, ok := a.resolveAccountRef(r, current)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !accountAllowed(current, target) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	oldPw := r.PostFormValue("old_password")
	newPw := r.PostFormValue("new_password")
	confirmPw := r.PostFormValue("new_password_confirm")

	var errMsg string
	switch {
	case oldPw == "":
		errMsg = "Please enter your current password."
	case newPw == "":
		errMsg = "Please enter a new password."
	case newPw != confirmPw:
		errMsg = "The new passwords do not match."
	}
	if errMsg == "" {
		// Re-authenticate the *current* user — the actor proves they
		// still hold the account they're acting from. Admins don't get
		// a free pass to change other users' passwords without
		// re-typing their own.
		if _, err := a.Authenticate(current.Username(), oldPw); err != nil {
			errMsg = "Your current password is incorrect."
		}
	}
	if errMsg != "" {
		a.serveAccountForm(w, r, shell, errMsg, "")
		return
	}

	if err := a.Passwd(target.Username, newPw); err != nil {
		a.serveAccountForm(w, r, shell, "Could not update password: "+err.Error(), "")
		return
	}

	// Success.
	if isHTMXAuthRequest(r) {
		// Modal flow: close the modal and suppress the swap so the
		// admin lands back on the page they were on (the admin
		// table). No success banner needed — closing the modal is
		// itself the confirmation. The page reload that would have
		// shown a success message is reserved for the full-page
		// flow below.
		if modalID := modalIDFromHXTarget(r); modalID != "" {
			w.Header().Set("HX-Trigger", fmt.Sprintf(`{"closeModal":%q}`, modalID))
		}
		w.Header().Set("HX-Reswap", "none")
		return
	}
	a.serveAccountForm(w, r, shell, "", "Password changed.")
}

// resolveAccountRef maps the {ref} path segment to a UserGORM row.
// "me" → the current user; a numeric ID → the matching row. Returns
// false when ref is malformed or the user doesn't exist.
func (a *AuthGORM) resolveAccountRef(r *http.Request, current User) (*UserGORM, bool) {
	ref := r.PathValue("ref")
	if ref == "" {
		return nil, false
	}
	var target UserGORM
	if ref == "me" {
		if err := a.DB.Where("username = ?", current.Username()).First(&target).Error; err != nil {
			return nil, false
		}
		return &target, true
	}
	id, err := strconv.ParseUint(ref, 10, 64)
	if err != nil {
		return nil, false
	}
	if err := a.DB.First(&target, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false
		}
		return nil, false
	}
	return &target, true
}

// accountAllowed implements the password-change policy: a user may
// change their own password; an admin-group member may change
// anyone's. No other rule.
func accountAllowed(current User, target *UserGORM) bool {
	if current == nil || target == nil {
		return false
	}
	if current.Username() == target.Username {
		return true
	}
	return current.HasGroup(adminGroupName)
}

// ──────────────────────────────────────────────────────────────────
// HTMX helpers, inlined to avoid pulling crud's unexported helpers
// across the package boundary.

func isHTMXAuthRequest(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// modalIDFromHXTarget reads HX-Target ("users-modal-l1-body") and
// trims "-body" to get the modal id ("users-modal-l1"). Returns ""
// if HX-Target isn't set (non-HTMX request, or the target is
// something other than a modal body).
func modalIDFromHXTarget(r *http.Request) string {
	target := r.Header.Get("HX-Target")
	if target == "" {
		return ""
	}
	return strings.TrimSuffix(target, "-body")
}

func renderOrLog(w http.ResponseWriter, r *http.Request, c templ.Component) {
	if err := c.Render(r.Context(), w); err != nil {
		log.Printf("auth: render: %v", err)
	}
}
