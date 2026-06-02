package auth

import (
	"fmt"
	"net/http"
	"strconv"
)

// Session key for the in-flight TOTP enrolment secret. Per-session
// (not keyed by target user) because TOTP enrolment is always
// self-service — the current user is enrolling themselves. The value
// is the base32 secret returned by totp.Generate.
const totpSetupSecretKey = "auth:totp_setup_secret"

// mountTOTPAccountRoutes registers the enrol / verify / cancel /
// disable endpoints under base/account/{ref}/totp/<action>. Called
// from mountAccountRoutes after the password-change routes are
// already up.
func (a *AuthGORM) mountTOTPAccountRoutes(mux Mux, base string) {
	mux.HandleFunc("POST "+base+"/account/{ref}/totp/begin", func(w http.ResponseWriter, r *http.Request) {
		a.handleTOTPBegin(w, r)
	})
	mux.HandleFunc("POST "+base+"/account/{ref}/totp/verify", func(w http.ResponseWriter, r *http.Request) {
		a.handleTOTPVerify(w, r)
	})
	mux.HandleFunc("POST "+base+"/account/{ref}/totp/cancel", func(w http.ResponseWriter, r *http.Request) {
		a.handleTOTPCancel(w, r)
	})
	mux.HandleFunc("POST "+base+"/account/{ref}/totp/disable", func(w http.ResponseWriter, r *http.Request) {
		a.handleTOTPDisable(w, r)
	})
}

// handleTOTPBegin starts an enrolment: generates a fresh secret,
// stashes it in the session (NOT in the DB — only the verified
// secret lands there), and returns the totpSetupCard fragment so
// HTMX can swap it in place of the "Enable TOTP" button.
//
// Self only: an admin can't enrol TOTP for someone else (they'd need
// physical access to that user's authenticator).
func (a *AuthGORM) handleTOTPBegin(w http.ResponseWriter, r *http.Request) {
	current, target, ok := a.requireAccountSelf(w, r)
	if !ok {
		return
	}
	secret, otpauthURL, qr, err := totpGenerate(a.totpIssuer(), target.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.Sessions.Put(r.Context(), totpSetupSecretKey, secret)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	renderOrLog(w, r, totpSetupCard(totpSetupData{
		Secret:     secret,
		OTPAuthURL: otpauthURL,
		QRDataURL:  qr,
		VerifyURL:  a.totpEndpoint(target.ID, "verify"),
		CancelURL:  a.totpEndpoint(target.ID, "cancel"),
		CSRFToken:  CSRFToken(r.Context()),
	}))
	_ = current // marked as used (the requireAccountSelf signature is shared)
}

// handleTOTPVerify reads the pending secret + the user-supplied
// code, validates them, and on success writes the secret to the DB.
// Returns the totp card in its "enabled" state.
func (a *AuthGORM) handleTOTPVerify(w http.ResponseWriter, r *http.Request) {
	_, target, ok := a.requireAccountSelf(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	code := r.PostFormValue("code")
	secret := a.Sessions.GetString(r.Context(), totpSetupSecretKey)
	if secret == "" {
		// No enrolment in flight — render the empty state.
		a.renderTOTPCardForTarget(w, r, target, false)
		return
	}
	if !totpValidate(secret, code) {
		// Wrong code: re-render setup card with error. The pending
		// secret stays in the session so the user can try again
		// without rescanning the QR.
		_, otpauthURL, qr, err := totpRegenerateQR(secret, target.Username, a.totpIssuer())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		renderOrLog(w, r, totpSetupCard(totpSetupData{
			Secret:     secret,
			OTPAuthURL: otpauthURL,
			QRDataURL:  qr,
			VerifyURL:  a.totpEndpoint(target.ID, "verify"),
			CancelURL:  a.totpEndpoint(target.ID, "cancel"),
			CSRFToken:  CSRFToken(r.Context()),
			Error:      "Incorrect code. Try again.",
		}))
		return
	}
	// Verified: commit the secret to the DB, clear pending state.
	if err := a.DB.Model(&UserGORM{}).
		Where("username = ?", target.Username).
		Update("totp_secret", secret).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.Sessions.Remove(r.Context(), totpSetupSecretKey)
	a.renderTOTPCardForTarget(w, r, target, true)
}

// handleTOTPCancel drops the in-flight secret and swaps the card
// back to its empty state. Self only.
func (a *AuthGORM) handleTOTPCancel(w http.ResponseWriter, r *http.Request) {
	_, target, ok := a.requireAccountSelf(w, r)
	if !ok {
		return
	}
	a.Sessions.Remove(r.Context(), totpSetupSecretKey)
	a.renderTOTPCardForTarget(w, r, target, false)
}

// handleTOTPDisable clears the persisted TOTP secret for the target
// user. Self OR admin: the admin path covers the "user lost their
// phone" rescue case. The confirmation is on the client (hx-confirm
// on the button).
func (a *AuthGORM) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	current, target, ok := a.requireAccountAccess(w, r)
	if !ok {
		return
	}
	if err := a.DB.Model(&UserGORM{}).
		Where("username = ?", target.Username).
		Update("totp_secret", "").Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.Sessions.Remove(r.Context(), totpSetupSecretKey)
	a.renderTOTPCardForTarget(w, r, target, false)
	_ = current // unused by the success path
}

// renderTOTPCardForTarget writes the totp card fragment reflecting
// the supplied TOTPEnabled state. Used by every action handler to
// return to the steady-state view.
func (a *AuthGORM) renderTOTPCardForTarget(w http.ResponseWriter, r *http.Request, target *UserGORM, enabled bool) {
	current := a.CurrentUser(r)
	isSelf := current != nil && current.Username() == target.Username
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	renderOrLog(w, r, totpCard(accountFormData{
		TargetUsername: target.Username,
		IsSelf:         isSelf,
		CSRFToken:      CSRFToken(r.Context()),
		TOTPEnabled:    enabled,
		TOTPBaseURL:    a.totpEndpointBase(target.ID),
	}))
}

// requireAccountAccess is the self-or-admin gate (matches the
// password-change policy). Returns the current user + the target
// row when both checks pass; writes an HTTP error and returns
// (..., ..., false) otherwise.
func (a *AuthGORM) requireAccountAccess(w http.ResponseWriter, r *http.Request) (User, *UserGORM, bool) {
	current := a.CurrentUser(r)
	if current == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, nil, false
	}
	target, ok := a.resolveAccountRef(r, current)
	if !ok {
		http.NotFound(w, r)
		return nil, nil, false
	}
	if !accountAllowed(current, target) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, nil, false
	}
	return current, target, true
}

// requireAccountSelf is the stricter gate used by enrolment: the
// acting user MUST be the target. Admins can disable someone else's
// TOTP but they can't enrol it (they'd need physical access to that
// user's authenticator app).
func (a *AuthGORM) requireAccountSelf(w http.ResponseWriter, r *http.Request) (User, *UserGORM, bool) {
	current, target, ok := a.requireAccountAccess(w, r)
	if !ok {
		return nil, nil, false
	}
	if current.Username() != target.Username {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, nil, false
	}
	return current, target, true
}

// totpIssuer returns the configured issuer or the package default.
func (a *AuthGORM) totpIssuer() string {
	if a.TOTPIssuer != "" {
		return a.TOTPIssuer
	}
	return defaultTOTPIssuer
}

// totpEndpoint builds /account/{id}/totp/<action>.
func (a *AuthGORM) totpEndpoint(userID uint, action string) string {
	return fmt.Sprintf("%s/%s", a.totpEndpointBase(userID), action)
}

// totpEndpointBase builds /account/{id}/totp.
func (a *AuthGORM) totpEndpointBase(userID uint) string {
	return a.urlBase + "/account/" + strconv.FormatUint(uint64(userID), 10) + "/totp"
}

// totpRegenerateQR rebuilds the otpauth URL + QR for an existing
// secret (the verify-failure path). We don't re-call totp.Generate
// because that would mint a new secret — the user's authenticator
// already holds the original. Instead, hand-build the otpauth URL.
func totpRegenerateQR(secret, username, issuer string) (string, string, string, error) {
	// totp.Generate keeps the secret stable when given an explicit
	// Secret, but it requires a []byte. We use a small URL builder
	// directly to avoid that allocation dance: the URL fields are
	// fixed (TOTP, SHA1, 6 digits, 30s) and totp.Validate uses the
	// same defaults.
	url := "otpauth://totp/" + urlPathEscape(issuer+":"+username) +
		"?secret=" + secret +
		"&issuer=" + urlQueryEscape(issuer)
	qr, err := qrPNGDataURL(url)
	if err != nil {
		return "", "", "", err
	}
	return secret, url, qr, nil
}

// urlPathEscape / urlQueryEscape pull in a minimal escape helper so
// totp_account.go doesn't depend on net/url. (totpGenerate already
// does — keeping the second helper symmetric.)
func urlPathEscape(s string) string {
	// %-encode reserved chars in the path segment. Authenticator
	// apps want issuer:account here.
	var b []byte
	for _, c := range []byte(s) {
		if isURLPathSafe(c) {
			b = append(b, c)
			continue
		}
		const hex = "0123456789ABCDEF"
		b = append(b, '%', hex[c>>4], hex[c&0x0f])
	}
	return string(b)
}

func urlQueryEscape(s string) string {
	return urlPathEscape(s) // close enough; issuer is plain ASCII
}

func isURLPathSafe(c byte) bool {
	switch {
	case 'A' <= c && c <= 'Z',
		'a' <= c && c <= 'z',
		'0' <= c && c <= '9':
		return true
	}
	switch c {
	case '-', '_', '.', '~':
		return true
	}
	return false
}

