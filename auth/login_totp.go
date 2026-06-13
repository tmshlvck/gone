package auth

import (
	"context"
	"errors"
	"github.com/go-chi/chi/v5"
	"net/http"

	"gorm.io/gorm"
)

// Session keys for the staged login flow. Set when password auth
// succeeded but the user has a TOTPSecret and must enter a code;
// cleared on stage-2 success (or when the user re-logs in as a
// different account whose TOTP is not enrolled).
const (
	totpPendingUserKey = "auth:totp_pending_user"
	totpPendingNextKey = "auth:totp_pending_next"
)

// loginStage1 is the Login callback wired into mountPasswordLogin.
// It branches on whether the just-authenticated user has TOTP:
//
//   - TOTPSecret == "" — call full Login(ctx, u) and let the helper
//     redirect to next / AfterLogin.
//   - TOTPSecret != "" — stash the pending username (and the user's
//     requested next URL) in the session, return the /login/totp
//     URL so the helper redirects there instead. The user is not
//     yet "signed in" — CurrentUser still returns nil.
//
// Either path first clears any stale TOTP-pending state so a second
// login attempt with a non-TOTP user doesn't carry over the
// previous user's pending flag.
func (a *AuthGORM) loginStage1(ctx context.Context, u User, formNext string) (string, error) {
	// Always clear stale pending state.
	a.Sessions.Remove(ctx, totpPendingUserKey)
	a.Sessions.Remove(ctx, totpPendingNextKey)

	username := u.Username()
	var row UserGORM
	if err := a.DB.Where("username = ?", username).First(&row).Error; err != nil {
		// Should not happen — Authenticate just found this user.
		// Treat as a real error.
		return "", err
	}
	if row.TOTPSecret == "" {
		// No TOTP enrolled: finish login normally.
		return "", a.Login(ctx, u)
	}
	// Stage 1 complete; stage 2 pending.
	a.Sessions.Put(ctx, totpPendingUserKey, username)
	if formNext != "" {
		a.Sessions.Put(ctx, totpPendingNextKey, formNext)
	}
	return a.totpPath, nil
}

// mountTOTPLoginRoutes registers GET/POST /login/totp. The handler
// is private to AuthGORM because AuthSimple has no TOTP path.
func (a *AuthGORM) mountTOTPLoginRoutes(mux chi.Router, shell PageShellFunc) {
	mux.Get(a.totpPath, func(w http.ResponseWriter, r *http.Request) {
		a.serveTOTPLoginForm(w, r, shell, "")
	})
	mux.Post(a.totpPath, func(w http.ResponseWriter, r *http.Request) {
		a.handleTOTPLoginPost(w, r, shell)
	})
}

func (a *AuthGORM) serveTOTPLoginForm(w http.ResponseWriter, r *http.Request, shell PageShellFunc, errMsg string) {
	username := a.Sessions.GetString(r.Context(), totpPendingUserKey)
	if username == "" {
		// No pending stage-1 state — bounce back to /login.
		http.Redirect(w, r, a.loginPath, http.StatusSeeOther)
		return
	}
	body := totpLoginForm(totpLoginFormData{
		Action:    a.totpPath,
		CSRFToken: CSRFToken(r.Context()),
		Error:     errMsg,
		Username:  username,
	})
	writeShell(w, r, "Two-factor authentication", body, shell)
}

func (a *AuthGORM) handleTOTPLoginPost(w http.ResponseWriter, r *http.Request, shell PageShellFunc) {
	username := a.Sessions.GetString(r.Context(), totpPendingUserKey)
	if username == "" {
		http.Redirect(w, r, a.loginPath, http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	code := r.PostFormValue("code")

	var row UserGORM
	if err := a.DB.Preload("Groups").Where("username = ?", username).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) || row.TOTPSecret == "" {
			// User deleted or TOTP disabled between stages — restart.
			a.Sessions.Remove(r.Context(), totpPendingUserKey)
			a.Sessions.Remove(r.Context(), totpPendingNextKey)
			http.Redirect(w, r, a.loginPath, http.StatusSeeOther)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if row.TOTPSecret == "" {
		// Sanity guard: shouldn't happen given the check above, but
		// race-safe.
		a.Sessions.Remove(r.Context(), totpPendingUserKey)
		http.Redirect(w, r, a.loginPath, http.StatusSeeOther)
		return
	}
	if !totpValidate(row.TOTPSecret, code) {
		a.serveTOTPLoginForm(w, r, shell, "Incorrect verification code. Try again.")
		return
	}

	// Stage 2 complete: read the saved next, clear pending state,
	// promote pending → fully logged in.
	next := a.Sessions.GetString(r.Context(), totpPendingNextKey)
	a.Sessions.Remove(r.Context(), totpPendingUserKey)
	a.Sessions.Remove(r.Context(), totpPendingNextKey)
	if err := a.Login(r.Context(), UserGORMAdapter{U: &row}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	dest := safeNext(next)
	if dest == "" {
		dest = a.AfterLogin
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}
