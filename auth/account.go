package auth

import (
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/tmshlvck/gone/site"
	"log"
	"net/http"
	"strconv"
	"time"

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
func (a *AuthGORM) mountAccountRoutes(mux chi.Router, shell site.Shell) {
	mux.Get("/account/{ref}", func(w http.ResponseWriter, r *http.Request) {
		a.serveAccountForm(w, r, shell, "", "")
	})
	mux.Post("/account/{ref}", func(w http.ResponseWriter, r *http.Request) {
		a.handleAccountPost(w, r, shell)
	})
	a.mountTOTPAccountRoutes(mux)
	// Passkey enrolment endpoints are mounted only when RP fields
	// are set (the WebAuthn library refuses to construct without
	// them). Apps that don't want passkeys skip both UI and routes.
	if a.RPID != "" {
		a.mountPasskeyAccountRoutes(mux)
	}
	// SSO unlink route. Available even when no providers are
	// registered — historical identities may exist on disk and the
	// user should still be able to clean them up.
	mux.Post("/account/{ref}/sso/{id}/delete", func(w http.ResponseWriter, r *http.Request) {
		a.handleSSOIdentityDelete(w, r)
	})
}

// serveAccountForm renders the form for the GET path AND from the
// POST handler when validation fails / on success.
//
// HTMX requests get the bare form fragment, which the crud modal bridge
// auto-opens when it lands in a modal body; plain GETs get the form wrapped
// in the page shell.
func (a *AuthGORM) serveAccountForm(w http.ResponseWriter, r *http.Request, shell site.Shell, errMsg, successMsg string) {
	current := a.CurrentUser(r.Context())
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
	data := a.buildAccountFormData(r, current, target, errMsg, successMsg)
	data.Modal = htmx
	data.ModalBodyID = r.Header.Get("HX-Target")
	form := accountForm(data)

	if htmx {
		// HTMX path: return the bare form fragment for the modal body. The
		// crud modal bridge (crud.PageModals) auto-opens the dialog a form is
		// swapped into — a GET landing in a .crud-modal-body — so no explicit
		// open event is needed here.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		renderOrLog(w, r, form)
		return
	}
	writeShell(w, r, "Change password — "+target.Username, form, shell)
}

// buildAccountFormData assembles the accountFormData for target —
// running the passkey + SSO queries and computing every endpoint URL —
// without deciding how it's rendered (modal vs. page). serveAccountForm
// and AccountSection share it; callers set Modal / ModalBodyID after.
func (a *AuthGORM) buildAccountFormData(r *http.Request, current User, target *UserGORM, errMsg, successMsg string) accountFormData {
	// Passkeys card data: load list only when feature is on (RP
	// configured). Sorted newest-first so the most recent enrolment
	// is the top row.
	passkeysEnabled := a.RPID != ""
	var pkItems []passkeyItem
	if passkeysEnabled {
		var ks []PasskeyGORM
		if err := a.DB.Where("user_id = ?", target.ID).Order("created_at DESC").Find(&ks).Error; err == nil {
			pkItems = passkeyItems(ks, func(t time.Time) string { return a.fmtTime(r.Context(), t) })
		}
	}

	// SSO identities: list whatever's linked to the target. Pre-mark
	// the "last one + SSOOnly" row so the templ can disable Unlink.
	var ssoItems []ssoIdentityItem
	var ssoRows []SSOIdentityGORM
	if err := a.DB.Where("user_id = ?", target.ID).Order("created_at ASC").Find(&ssoRows).Error; err == nil {
		ssoItems = make([]ssoIdentityItem, 0, len(ssoRows))
		for _, row := range ssoRows {
			item := ssoIdentityItem{
				ID:          row.ID,
				Provider:    row.Provider,
				DisplayName: row.DisplayName,
				Email:       row.Email,
			}
			item.LastUsed = a.fmtTime(r.Context(), row.LastUsedAt)
			ssoItems = append(ssoItems, item)
		}
		if target.SSOOnly && len(ssoItems) == 1 {
			ssoItems[0].IsLast = true
		}
	}

	return accountFormData{
		ActionURL:       a.urlBase + "/account/" + strconv.FormatUint(uint64(target.ID), 10),
		TargetUsername:  target.Username,
		IsSelf:          current.Username() == target.Username,
		CSRFToken:       CSRFToken(r.Context()),
		Error:           errMsg,
		Success:         successMsg,
		TOTPEnabled:     target.TOTPSecret != "",
		TOTPBaseURL:     a.totpEndpointBase(target.ID),
		PasskeyBaseURL:  a.passkeyEndpointBase(target.ID),
		PasskeyItems:    pkItems,
		PasskeysEnabled: passkeysEnabled,
		SSOOnly:         target.SSOOnly,
		SSOIdentities:   ssoItems,
		SSOBaseURL:      a.urlBase + "/account/" + strconv.FormatUint(uint64(target.ID), 10) + "/sso",
	}
}

// AccountAccess reports the outcome of the access checks AccountSection
// runs before producing the cards, mirroring the three branches the
// standalone /account/{ref} page distinguishes. The embedding app turns
// it into the appropriate HTTP response.
type AccountAccess int

const (
	// AccountOK: the cards component is non-nil and ready to render.
	AccountOK AccountAccess = iota
	// AccountAnonymous: no session user — the app should redirect to
	// LoginURL (303) or, for an HTMX request, set HX-Redirect.
	AccountAnonymous
	// AccountForbidden: a signed-in user asked for someone else's
	// account without admin rights — the app should respond 403.
	AccountForbidden
	// AccountNotFound: the ref didn't resolve to a user — 404.
	AccountNotFound
)

// AccountSection resolves ref (honoring "me", admin-edits-other, and the
// same anonymous / forbidden / not-found guards as the standalone
// /account/{ref} page), loads the account-security card data, and
// returns the library's card group as one component an app can drop into
// its own preferences page beneath its own cards.
//
// target is the resolved user (non-nil whenever res != AccountAnonymous
// and != AccountNotFound), so the app can load that user's app-specific
// preferences for the same ref — including in the admin-edits-other
// case. cards is non-nil only when res == AccountOK.
//
// Ref resolution: if the route carries a "{ref}" chi URL param it's
// honored exactly like the standalone page ("me" or a numeric ID, with
// admin rights required to view another user). When there's no "{ref}"
// param — the usual case for an app mounting this at a fixed path like
// "/preferences" — it resolves to the signed-in user (self-service).
//
// The component is the page-shaped (non-modal) layout: a responsive
// grid of cards. Wrap it in your own heading / section; the password
// + TOTP + passkey + SSO endpoints it points at are the same ones the
// library already mounts under /account/{ref}/..., so the cards stay
// fully functional embedded.
func (a *AuthGORM) AccountSection(r *http.Request) (cards templ.Component, target *UserGORM, res AccountAccess) {
	current := a.CurrentUser(r.Context())
	if current == nil {
		return nil, nil, AccountAnonymous
	}
	var t *UserGORM
	if chi.URLParam(r, "ref") == "" {
		// No {ref} in the route → self-service. Look the current user up
		// the same way resolveAccountRef does for "me".
		var u UserGORM
		if err := a.DB.Where("username = ?", current.Username()).First(&u).Error; err != nil {
			return nil, nil, AccountNotFound
		}
		t = &u
	} else {
		var ok bool
		if t, ok = a.resolveAccountRef(r, current); !ok {
			return nil, nil, AccountNotFound
		}
	}
	if !accountAllowed(current, t) {
		return nil, t, AccountForbidden
	}
	data := a.buildAccountFormData(r, current, t, "", "")
	return accountCards(data), t, AccountOK
}

// handleAccountPost validates the form and either updates the
// password or re-renders the form with an error.
func (a *AuthGORM) handleAccountPost(w http.ResponseWriter, r *http.Request, shell site.Shell) {
	current := a.CurrentUser(r.Context())
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
	if target.SSOOnly {
		// SSOOnly users can't have a local password set — the SSO
		// identity is the credential. Admin must clear the flag in
		// the admin UI first.
		http.Error(w, "this account is managed via SSO; clear the SSO-Only flag in the admin panel to set a local password.", http.StatusForbidden)
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
		// Modal flow: ask the crud bridge to close the topmost modal and
		// suppress the swap, so the admin lands back on the page they were on
		// (the admin table). Closing the modal is itself the confirmation; no
		// banner. The full-page flow below keeps the success message.
		w.Header().Set("HX-Trigger", `{"crud-close-modal":null}`)
		w.Header().Set("HX-Reswap", "none")
		return
	}
	a.serveAccountForm(w, r, shell, "", "Password changed.")
}

// resolveAccountRef maps the {ref} path segment to a UserGORM row.
// "me" → the current user; a numeric ID → the matching row. Returns
// false when ref is malformed or the user doesn't exist.
func (a *AuthGORM) resolveAccountRef(r *http.Request, current User) (*UserGORM, bool) {
	ref := chi.URLParam(r, "ref")
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

func renderOrLog(w http.ResponseWriter, r *http.Request, c templ.Component) {
	if err := c.Render(r.Context(), w); err != nil {
		log.Printf("auth: render: %v", err)
	}
}

// handleSSOIdentityDelete unlinks one SSO identity from the target
// user. Self-only — admins delete users wholesale through the admin
// UI, not their individual identity links.
//
// Refuses to delete the LAST identity on an SSOOnly account — that
// would lock the user out (no password to fall back on). The user
// must ask an admin to clear the SSOOnly flag first; the templ
// disables the Unlink button in that case, so the 403 here is a
// defence-in-depth path.
//
// Returns an HTMX fragment (the re-rendered linkedAccountsCard) on
// success; a plain text 403 / 404 on failure.
func (a *AuthGORM) handleSSOIdentityDelete(w http.ResponseWriter, r *http.Request) {
	_, target, ok := a.requireAccountSelf(w, r)
	if !ok {
		return
	}
	idStr := chi.URLParam(r, "id")
	identityID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var ident SSOIdentityGORM
	if err := a.DB.Where("id = ? AND user_id = ?", identityID, target.ID).First(&ident).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if target.SSOOnly {
		var count int64
		if err := a.DB.Model(&SSOIdentityGORM{}).Where("user_id = ?", target.ID).Count(&count).Error; err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if count <= 1 {
			http.Error(w, "cannot unlink the last SSO identity on an SSO-Only account; ask an admin to clear the SSO-Only flag first", http.StatusForbidden)
			return
		}
	}
	if err := a.DB.Delete(&ident).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Re-render the card.
	var remaining []SSOIdentityGORM
	_ = a.DB.Where("user_id = ?", target.ID).Order("created_at ASC").Find(&remaining).Error
	items := make([]ssoIdentityItem, 0, len(remaining))
	for _, row := range remaining {
		item := ssoIdentityItem{
			ID:          row.ID,
			Provider:    row.Provider,
			DisplayName: row.DisplayName,
			Email:       row.Email,
		}
		item.LastUsed = a.fmtTime(r.Context(), row.LastUsedAt)
		items = append(items, item)
	}
	if target.SSOOnly && len(items) == 1 {
		items[0].IsLast = true
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	renderOrLog(w, r, linkedAccountsCard(accountFormData{
		CSRFToken:     CSRFToken(r.Context()),
		SSOOnly:       target.SSOOnly,
		SSOIdentities: items,
		SSOBaseURL:    a.urlBase + "/account/" + strconv.FormatUint(uint64(target.ID), 10) + "/sso",
	}))
}
