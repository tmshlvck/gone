package auth

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"gorm.io/gorm"
)

// Session keys for in-flight WebAuthn ceremonies. Each ceremony has
// one round-trip — the challenge goes out, the assertion/attestation
// comes back referencing it. We park the SessionData (JSON-serialised
// because gob-registering protocol.URLEncodedBase64 is more friction
// than it's worth) here while the user is talking to the browser.
const (
	passkeyLoginSessionKey = "auth:passkey_login_session"
	passkeySetupSessionKey = "auth:passkey_setup_session" // includes user-supplied label
)

// ──────────────────────────────────────────────────────────────────
// webauthn.User adapter.

// webauthnUser wraps the GORM row + its passkey credentials so the
// WebAuthn library can read identity / list of registered keys via
// its User interface. The library never sees AuthGORM directly.
type webauthnUser struct {
	u        *UserGORM
	passkeys []PasskeyGORM
}

// WebAuthnID returns the per-user opaque handle. The WebAuthn spec
// asks for a random byte sequence (NOT the user's numeric account ID)
// so authenticators can't correlate keys across the same account at
// different RPs. We lazy-generate WebAuthnHandle the first time it's
// needed and persist it; from then on it's stable for the user.
func (w webauthnUser) WebAuthnID() []byte { return w.u.WebAuthnHandle }

// WebAuthnName is what shows up in the browser's account picker.
func (w webauthnUser) WebAuthnName() string { return w.u.Username }

// WebAuthnDisplayName is the friendly name the browser may show
// alongside the account. Email is more recognisable when set;
// otherwise fall back to username.
func (w webauthnUser) WebAuthnDisplayName() string {
	if w.u.Email != "" {
		return w.u.Email
	}
	return w.u.Username
}

// WebAuthnCredentials lists every passkey already enrolled for this
// user. Used during registration (to exclude already-enrolled
// credentials via excludeCredentials) and during user-bound login.
// For discoverable login the library doesn't call this on the empty
// list — instead it finds the user via the credential handle.
func (w webauthnUser) WebAuthnCredentials() []webauthn.Credential {
	out := make([]webauthn.Credential, 0, len(w.passkeys))
	for _, p := range w.passkeys {
		out = append(out, passkeyToCredential(p))
	}
	return out
}

func passkeyToCredential(p PasskeyGORM) webauthn.Credential {
	transports := []protocol.AuthenticatorTransport{}
	for _, t := range strings.Split(p.Transports, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			transports = append(transports, protocol.AuthenticatorTransport(t))
		}
	}
	return webauthn.Credential{
		ID:              p.CredentialID,
		PublicKey:       p.PublicKey,
		AttestationType: p.AttestationType,
		Transport:       transports,
		Flags: webauthn.CredentialFlags{
			BackupEligible: p.BackupEligible,
			BackupState:    p.BackupState,
		},
		Authenticator: webauthn.Authenticator{
			AAGUID:    p.AAGUID,
			SignCount: p.SignCount,
		},
	}
}

// ──────────────────────────────────────────────────────────────────
// WebAuthn config.

// webauthnConfig returns a *webauthn.WebAuthn built from the
// AuthGORM RP fields. Built on each call (the underlying struct is
// just a config holder — no I/O, no caching benefit). If the RP
// fields are unset the library refuses to construct; we propagate
// that as a runtime 500.
func (a *AuthGORM) webauthnConfig() (*webauthn.WebAuthn, error) {
	if a.RPDisplayName == "" || a.RPID == "" || len(a.RPOrigins) == 0 {
		return nil, errors.New("auth: passkey routes require AuthGORM.RPDisplayName, RPID, RPOrigins")
	}
	return webauthn.New(&webauthn.Config{
		RPDisplayName: a.RPDisplayName,
		RPID:          a.RPID,
		RPOrigins:     a.RPOrigins,
	})
}

// loadUserWithPasskeys fetches a UserGORM + its PasskeyGORM rows. Used
// by both enrolment and user-bound login paths.
func (a *AuthGORM) loadUserWithPasskeys(username string) (*UserGORM, []PasskeyGORM, error) {
	var u UserGORM
	if err := a.DB.Where("username = ?", username).First(&u).Error; err != nil {
		return nil, nil, err
	}
	var ks []PasskeyGORM
	if err := a.DB.Where("user_id = ?", u.ID).Find(&ks).Error; err != nil {
		return nil, nil, err
	}
	return &u, ks, nil
}

// ensureWebAuthnHandle sets and persists WebAuthnHandle if empty.
// 32 bytes of crypto/rand — well within the spec's 64-byte ceiling.
func (a *AuthGORM) ensureWebAuthnHandle(u *UserGORM) error {
	if len(u.WebAuthnHandle) > 0 {
		return nil
	}
	h := make([]byte, 32)
	if _, err := rand.Read(h); err != nil {
		return err
	}
	if err := a.DB.Model(u).Update("web_authn_handle", h).Error; err != nil {
		return err
	}
	u.WebAuthnHandle = h
	return nil
}

// ──────────────────────────────────────────────────────────────────
// Enrolment routes.

// mountPasskeyAccountRoutes registers begin / finish / delete under
// base + "/account/{ref}/passkey/...". Called from mountAccountRoutes.
func (a *AuthGORM) mountPasskeyAccountRoutes(mux Mux, base string) {
	mux.HandleFunc("POST "+base+"/account/{ref}/passkey/begin", func(w http.ResponseWriter, r *http.Request) {
		a.handlePasskeyEnrolBegin(w, r)
	})
	mux.HandleFunc("POST "+base+"/account/{ref}/passkey/finish", func(w http.ResponseWriter, r *http.Request) {
		a.handlePasskeyEnrolFinish(w, r)
	})
	mux.HandleFunc("POST "+base+"/account/{ref}/passkey/{pkid}/delete", func(w http.ResponseWriter, r *http.Request) {
		a.handlePasskeyDelete(w, r)
	})
}

// passkeyEnrolPending is the JSON envelope we stash in the session
// while the browser is doing navigator.credentials.create(). We hold
// onto the user's chosen passkey label too — the WebAuthn ceremony
// itself doesn't carry one.
type passkeyEnrolPending struct {
	Name        string                `json:"name"`
	UserID      uint                  `json:"user_id"`
	SessionData *webauthn.SessionData `json:"session_data"`
}

// handlePasskeyEnrolBegin: self only. Reads the user-supplied label
// from the form, generates a WebAuthn challenge, and returns the
// PublicKeyCredentialCreationOptions JSON the browser will hand to
// navigator.credentials.create().
func (a *AuthGORM) handlePasskeyEnrolBegin(w http.ResponseWriter, r *http.Request) {
	_, target, ok := a.requireAccountSelf(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		name = "Passkey"
	}
	if err := a.ensureWebAuthnHandle(target); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	wa, err := a.webauthnConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, passkeys, err := a.loadUserWithPasskeys(target.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// ResidentKeyRequirementRequired forces a *discoverable* credential.
	// Without it the authenticator can choose to store the credential
	// server-side-only, which means discoverable-login (empty
	// allowCredentials) won't find it — Bitwarden in particular
	// reports "No passkey found for this application" in that case.
	// UserVerificationPreferred lines up with what the login side
	// asks for, so the authenticator picks UV-capable keys.
	creation, sessionData, err := wa.BeginRegistration(
		webauthnUser{u: target, passkeys: passkeys},
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationPreferred,
		}),
	)
	if err != nil {
		http.Error(w, "webauthn begin: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := a.putJSON(r, passkeySetupSessionKey, passkeyEnrolPending{
		Name:        name,
		UserID:      target.ID,
		SessionData: sessionData,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, creation)
}

// handlePasskeyEnrolFinish: parses the attestation, verifies it
// against the stashed challenge, and persists the credential.
func (a *AuthGORM) handlePasskeyEnrolFinish(w http.ResponseWriter, r *http.Request) {
	_, target, ok := a.requireAccountSelf(w, r)
	if !ok {
		return
	}
	var pend passkeyEnrolPending
	if err := a.getJSON(r, passkeySetupSessionKey, &pend); err != nil {
		http.Error(w, "no enrolment in progress", http.StatusBadRequest)
		return
	}
	if pend.UserID != target.ID {
		http.Error(w, "session/target user mismatch", http.StatusBadRequest)
		return
	}
	wa, err := a.webauthnConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, passkeys, err := a.loadUserWithPasskeys(target.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cred, err := wa.FinishRegistration(webauthnUser{u: target, passkeys: passkeys}, *pend.SessionData, r)
	if err != nil {
		http.Error(w, "webauthn finish: "+err.Error(), http.StatusUnauthorized)
		return
	}
	transports := make([]string, 0, len(cred.Transport))
	for _, t := range cred.Transport {
		transports = append(transports, string(t))
	}
	row := PasskeyGORM{
		UserID:          target.ID,
		CredentialID:    cred.ID,
		PublicKey:       cred.PublicKey,
		SignCount:       cred.Authenticator.SignCount,
		Transports:      strings.Join(transports, ","),
		AttestationType: cred.AttestationType,
		AAGUID:          cred.Authenticator.AAGUID,
		BackupEligible:  cred.Flags.BackupEligible,
		BackupState:     cred.Flags.BackupState,
		Name:            pend.Name,
		LastUsedAt:      time.Now(),
	}
	if err := a.DB.Create(&row).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.Sessions.Remove(r.Context(), passkeySetupSessionKey)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":   row.ID,
		"name": row.Name,
	})
}

// handlePasskeyDelete: removes one passkey. Self only. Doesn't ask
// for password re-auth (matches TOTP disable's UX); the click went
// through the user's authenticated session.
func (a *AuthGORM) handlePasskeyDelete(w http.ResponseWriter, r *http.Request) {
	_, target, ok := a.requireAccountSelf(w, r)
	if !ok {
		return
	}
	pkid := r.PathValue("pkid")
	res := a.DB.Where("id = ? AND user_id = ?", pkid, target.ID).Delete(&PasskeyGORM{})
	if res.Error != nil {
		http.Error(w, res.Error.Error(), http.StatusInternalServerError)
		return
	}
	if res.RowsAffected == 0 {
		http.NotFound(w, r)
		return
	}
	// Render the refreshed passkey list fragment so the account
	// page can swap it in place.
	a.renderPasskeyCard(w, r, target)
}

// ──────────────────────────────────────────────────────────────────
// Login routes.

// mountPasskeyLoginRoutes registers the two endpoints the login
// page's JS calls. Mounted directly from Route() (not from the
// account routes), because login is reachable to anonymous users.
func (a *AuthGORM) mountPasskeyLoginRoutes(mux Mux) {
	mux.HandleFunc("POST "+a.passkeyOptionsPath, func(w http.ResponseWriter, r *http.Request) {
		a.handlePasskeyLoginOptions(w, r)
	})
	mux.HandleFunc("POST "+a.passkeyFinishPath, func(w http.ResponseWriter, r *http.Request) {
		a.handlePasskeyLoginFinish(w, r)
	})
}

// passkeyLoginPending is what we stash in the session between the
// /options and /finish round-trip. SessionData carries the challenge
// the browser must sign.
type passkeyLoginPending struct {
	SessionData *webauthn.SessionData `json:"session_data"`
	Next        string                `json:"next"`
}

// handlePasskeyLoginOptions: generates a discoverable-login challenge.
// allowCredentials is empty so any passkey for this RP can match —
// the browser shows its native account picker.
func (a *AuthGORM) handlePasskeyLoginOptions(w http.ResponseWriter, r *http.Request) {
	wa, err := a.webauthnConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// VerificationPreferred matches what registration asks for —
	// nudges UV-capable authenticators to actually prompt for it.
	assertion, sessionData, err := wa.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationPreferred),
	)
	if err != nil {
		http.Error(w, "webauthn begin: "+err.Error(), http.StatusInternalServerError)
		return
	}
	next := safeNext(r.URL.Query().Get("next"))
	if next == "" {
		next = safeNext(r.PostFormValue("next"))
	}
	if err := a.putJSON(r, passkeyLoginSessionKey, passkeyLoginPending{
		SessionData: sessionData,
		Next:        next,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, assertion)
}

// handlePasskeyLoginFinish: verifies the assertion, looks up the
// passkey row by credential ID, and finalises the session. TOTP is
// bypassed — the device already verified the user.
func (a *AuthGORM) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	var pend passkeyLoginPending
	if err := a.getJSON(r, passkeyLoginSessionKey, &pend); err != nil {
		http.Error(w, "no passkey login in progress", http.StatusBadRequest)
		return
	}
	wa, err := a.webauthnConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// FinishPasskeyLogin's handler is called with the credential's
	// raw ID + user handle; it returns the webauthn.User that owns
	// the credential so the library can verify the signature.
	var matched *UserGORM
	var matchedPasskey PasskeyGORM
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		var pk PasskeyGORM
		if err := a.DB.Where("credential_id = ?", rawID).First(&pk).Error; err != nil {
			return nil, fmt.Errorf("credential not found: %w", err)
		}
		var u UserGORM
		if err := a.DB.First(&u, pk.UserID).Error; err != nil {
			return nil, err
		}
		// User-handle check: the browser includes it in the assertion;
		// it MUST match what we stored at enrolment time.
		if len(u.WebAuthnHandle) > 0 && !bytesEqual(u.WebAuthnHandle, userHandle) {
			return nil, errors.New("user handle mismatch")
		}
		_, passkeys, err := a.loadUserWithPasskeys(u.Username)
		if err != nil {
			return nil, err
		}
		matched = &u
		matchedPasskey = pk
		return webauthnUser{u: &u, passkeys: passkeys}, nil
	}
	_, cred, err := wa.FinishPasskeyLogin(handler, *pend.SessionData, r)
	if err != nil {
		http.Error(w, "webauthn finish: "+err.Error(), http.StatusUnauthorized)
		return
	}
	if matched == nil {
		http.Error(w, "user not resolved", http.StatusInternalServerError)
		return
	}
	if matched.Disabled {
		http.Error(w, "user disabled", http.StatusForbidden)
		return
	}
	// Persist updated sign counter + last-used time.
	updates := map[string]any{
		"sign_count":   cred.Authenticator.SignCount,
		"last_used_at": time.Now(),
		"backup_state": cred.Flags.BackupState,
	}
	if err := a.DB.Model(&PasskeyGORM{}).Where("id = ?", matchedPasskey.ID).Updates(updates).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Finalise session — bypasses TOTP because a.Login is the
	// terminal call, not loginStage1.
	if err := a.Login(r.Context(), UserGORMAdapter{U: matched}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.Sessions.Remove(r.Context(), passkeyLoginSessionKey)
	dest := pend.Next
	if dest == "" {
		dest = a.AfterLogin
	}
	writeJSON(w, http.StatusOK, map[string]any{"next": dest})
}

// ──────────────────────────────────────────────────────────────────
// Helpers.

// putJSON marshals v into the session under key as a string. Avoids
// gob-registering protocol.URLEncodedBase64 (which webauthn.SessionData
// embeds) by using JSON the way the rest of the package already does.
func (a *AuthGORM) putJSON(r *http.Request, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	a.Sessions.Put(r.Context(), key, string(b))
	return nil
}

func (a *AuthGORM) getJSON(r *http.Request, key string, dst any) error {
	s := a.Sessions.GetString(r.Context(), key)
	if s == "" {
		return errors.New("not set")
	}
	return json.Unmarshal([]byte(s), dst)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// passkeyIDStr is a templ-friendly stringifier for passkey IDs in the
// list view (uint → decimal). Inline in a templ via { passkeyIDStr(id) }.
func passkeyIDStr(id uint) string {
	return fmt.Sprintf("%d", id)
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// renderPasskeyCard re-renders the account-page passkey list. Used
// by the delete handler so the UI updates in place. Pulls a fresh
// list from the DB; same shape as serveAccountForm but only the
// passkey card.
func (a *AuthGORM) renderPasskeyCard(w http.ResponseWriter, r *http.Request, target *UserGORM) {
	current := a.CurrentUser(r)
	isSelf := current != nil && current.Username() == target.Username
	var ks []PasskeyGORM
	_ = a.DB.Where("user_id = ?", target.ID).Order("created_at DESC").Find(&ks).Error
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	renderOrLog(w, r, passkeyCard(accountFormData{
		TargetUsername:  target.Username,
		IsSelf:          isSelf,
		CSRFToken:       CSRFToken(r.Context()),
		PasskeyBaseURL:  a.passkeyEndpointBase(target.ID),
		PasskeyItems:    passkeyItems(ks),
		PasskeysEnabled: true,
	}))
}

// passkeyItems converts DB rows into the view-friendly shape.
func passkeyItems(rows []PasskeyGORM) []passkeyItem {
	out := make([]passkeyItem, len(rows))
	for i, r := range rows {
		out[i] = passkeyItem{
			ID:         r.ID,
			Name:       r.Name,
			CreatedAt:  r.CreatedAt.Format("2006-01-02"),
			LastUsedAt: r.LastUsedAt.Format("2006-01-02 15:04"),
		}
	}
	return out
}

// passkeyEndpointBase builds /account/{id}/passkey for buttons /
// JS to target.
func (a *AuthGORM) passkeyEndpointBase(userID uint) string {
	return fmt.Sprintf("%s/account/%d/passkey", a.urlBase, userID)
}

// loadOrInitWebAuthnUser ensures WebAuthnHandle is set then returns
// the adapter — used by tests + handlers that need a webauthn.User
// before BeginRegistration / BeginLogin.
//
// kept here for testability.
func (a *AuthGORM) loadOrInitWebAuthnUser(username string) (webauthnUser, error) {
	u, ks, err := a.loadUserWithPasskeys(username)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return webauthnUser{}, err
		}
		return webauthnUser{}, err
	}
	if err := a.ensureWebAuthnHandle(u); err != nil {
		return webauthnUser{}, err
	}
	return webauthnUser{u: u, passkeys: ks}, nil
}
