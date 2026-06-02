package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

// ──────────────────────────────────────────────────────────────────
// Pure helper tests.

func TestTOTPGenerateAndValidate(t *testing.T) {
	secret, otpauthURL, qr, err := totpGenerate("gone-test", "alice")
	if err != nil {
		t.Fatalf("totpGenerate: %v", err)
	}
	if secret == "" {
		t.Error("empty secret")
	}
	if !strings.HasPrefix(otpauthURL, "otpauth://totp/") {
		t.Errorf("malformed otpauth url: %s", otpauthURL)
	}
	if !strings.HasPrefix(qr, "data:image/png;base64,") {
		t.Errorf("qr should be a PNG data URL; got: %s", qr[:60])
	}

	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	if !totpValidate(secret, code) {
		t.Error("totpValidate rejected a freshly-generated code")
	}
	if totpValidate(secret, "000000") {
		t.Error("totpValidate accepted a zero code (extremely unlikely match)")
	}
}

// ──────────────────────────────────────────────────────────────────
// Two-stage login flow.

// enrolTOTP is a test-side helper: enrol the named user with TOTP
// and return the secret.
func enrolTOTP(t *testing.T, ag *AuthGORM, username string) string {
	t.Helper()
	secret, _, _, err := totpGenerate("gone-test", username)
	if err != nil {
		t.Fatalf("totpGenerate: %v", err)
	}
	if err := ag.DB.Model(&UserGORM{}).Where("username = ?", username).
		Update("totp_secret", secret).Error; err != nil {
		t.Fatalf("set totp_secret: %v", err)
	}
	return secret
}

// generateCurrentCode mints a fresh TOTP code from the secret.
func generateCurrentCode(t *testing.T, secret string) string {
	t.Helper()
	c, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	return c
}

// loginPasswordOnly does just the stage-1 POST. Returns the cookie
// (post-stage-1) and the last response's Location header.
func loginPasswordOnly(t *testing.T, handler http.Handler, username, password string) (*http.Cookie, string) {
	t.Helper()
	req := httptest.NewRequest("GET", "/login", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	cookie := pickCookie(rr.Result().Cookies(), "session")
	tok := csrfTokenFor(handler, "/login", cookie)

	form := url.Values{
		"csrf_token": {tok},
		"username":   {username},
		"password":   {password},
	}
	req = httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	loc := rr.Header().Get("Location")
	cookie = pickCookie(rr.Result().Cookies(), "session")
	return cookie, loc
}

func TestStagedLogin_NoTOTPGoesStraightThrough(t *testing.T) {
	handler, _ := newRoutedAuthGORM(t)
	_, loc := loginPasswordOnly(t, handler, "admin", "adminpass")
	if loc == "/login/totp" {
		t.Errorf("user without TOTP should not be redirected to TOTP step; got %s", loc)
	}
	// (Default redirect is to AfterLogin "/" — anything other than
	// /login/totp is fine.)
}

func TestStagedLogin_TOTPUserRedirectedToTOTPStep(t *testing.T) {
	handler, ag := newRoutedAuthGORM(t)
	enrolTOTP(t, ag, "bob")

	cookie, loc := loginPasswordOnly(t, handler, "bob", "bobpass")
	if loc != "/login/totp" {
		t.Errorf("stage-1 redirect = %q, want /login/totp", loc)
	}

	// Verify the session indeed holds the pending state and not the
	// fully-logged-in marker. CurrentUser must still return nil.
	req := httptest.NewRequest("GET", "/admin", nil)
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	withSession(t, ag.Sessions, func(ctx context.Context, _ *http.Request) {})
	// Use ag.CurrentUser via a wrapped handler so the session is loaded.
	ag.Sessions.LoadAndSave(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := ag.CurrentUser(r); u != nil {
			t.Errorf("CurrentUser between stages = %v, want nil", u)
		}
		// Pending marker present.
		if ag.Sessions.GetString(r.Context(), totpPendingUserKey) != "bob" {
			t.Error("totp_pending_user not set after stage 1")
		}
	})).ServeHTTP(rr, req)
}

func TestStagedLogin_TOTPSuccessCompletesLogin(t *testing.T) {
	handler, ag := newRoutedAuthGORM(t)
	secret := enrolTOTP(t, ag, "bob")

	cookie, _ := loginPasswordOnly(t, handler, "bob", "bobpass")
	tok := csrfTokenFor(handler, "/login/totp", cookie)
	code := generateCurrentCode(t, secret)

	form := url.Values{"csrf_token": {tok}, "code": {code}}
	req := httptest.NewRequest("POST", "/login/totp", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("stage-2 POST = %d, want 303; body: %s", rr.Code, rr.Body.String())
	}
	final := pickCookie(rr.Result().Cookies(), "session")

	// Verify the user is now fully logged in.
	req = httptest.NewRequest("GET", "/account/me", nil)
	req.AddCookie(final)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("GET /account/me after TOTP = %d, want 200", rr.Code)
	}
}

func TestStagedLogin_TOTPWrongCodeRejected(t *testing.T) {
	handler, ag := newRoutedAuthGORM(t)
	enrolTOTP(t, ag, "bob")

	cookie, _ := loginPasswordOnly(t, handler, "bob", "bobpass")
	tok := csrfTokenFor(handler, "/login/totp", cookie)

	form := url.Values{"csrf_token": {tok}, "code": {"000000"}}
	req := httptest.NewRequest("POST", "/login/totp", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		// Re-render with error stays at 200.
		t.Errorf("wrong code status = %d, want 200 (form re-render)", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Incorrect verification code") {
		t.Errorf("error message missing; got: %s", rr.Body.String())
	}
}

func TestStagedLogin_TOTPStepWithoutPendingRedirectsToLogin(t *testing.T) {
	handler, _ := newRoutedAuthGORM(t)
	req := httptest.NewRequest("GET", "/login/totp", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", rr.Code)
	}
	if rr.Header().Get("Location") != "/login" {
		t.Errorf("Location = %q, want /login", rr.Header().Get("Location"))
	}
}

func TestStagedLogin_NewLoginClearsStalePendingState(t *testing.T) {
	handler, ag := newRoutedAuthGORM(t)
	enrolTOTP(t, ag, "bob")

	// First login (bob → pending TOTP).
	cookie, _ := loginPasswordOnly(t, handler, "bob", "bobpass")

	// Re-submit /login with admin (no TOTP). The pending state from
	// bob must NOT leak into admin's session.
	tok := csrfTokenFor(handler, "/login", cookie)
	form := url.Values{
		"csrf_token": {tok},
		"username":   {"admin"},
		"password":   {"adminpass"},
	}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("admin POST = %d, want 303", rr.Code)
	}
	if rr.Header().Get("Location") == "/login/totp" {
		t.Error("admin login bounced to TOTP page — stale pending state leaked")
	}
}

// ──────────────────────────────────────────────────────────────────
// Account-page TOTP enrolment flow.

func TestTOTPBegin_StashesSecretAndRendersSetupCard(t *testing.T) {
	handler, _ := newRoutedAuthGORM(t)
	cookie := loginVia(t, handler, "admin", "adminpass")
	tok := csrfTokenFor(handler, "/account/me", cookie)

	form := url.Values{"csrf_token": {tok}}
	req := httptest.NewRequest("POST", "/account/1/totp/begin", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("totp/begin status = %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"Set up two-factor authentication", "data:image/png;base64,", "Verify & enable"} {
		if !strings.Contains(body, want) {
			t.Errorf("setup card missing %q; body: %s", want, body[:200])
		}
	}
}

func TestTOTPVerify_WrongCodeKeepsPendingSecret(t *testing.T) {
	handler, ag := newRoutedAuthGORM(t)
	cookie := loginVia(t, handler, "admin", "adminpass")
	tok := csrfTokenFor(handler, "/account/me", cookie)

	// Begin enrolment.
	form := url.Values{"csrf_token": {tok}}
	req := httptest.NewRequest("POST", "/account/1/totp/begin", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// Submit wrong code.
	form = url.Values{"csrf_token": {tok}, "code": {"000000"}}
	req = httptest.NewRequest("POST", "/account/1/totp/verify", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.AddCookie(cookie)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !strings.Contains(rr.Body.String(), "Incorrect code") {
		t.Errorf("expected 'Incorrect code' in body: %s", rr.Body.String())
	}
	// DB must not have a secret yet.
	var u UserGORM
	_ = ag.DB.Where("username = ?", "admin").First(&u).Error
	if u.TOTPSecret != "" {
		t.Errorf("DB has TOTP secret after wrong-code verify: %q", u.TOTPSecret)
	}
}

func TestTOTPVerify_CorrectCodeWritesSecret(t *testing.T) {
	handler, ag := newRoutedAuthGORM(t)
	cookie := loginVia(t, handler, "admin", "adminpass")
	tok := csrfTokenFor(handler, "/account/me", cookie)

	// Begin enrolment.
	form := url.Values{"csrf_token": {tok}}
	req := httptest.NewRequest("POST", "/account/1/totp/begin", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// Extract the secret from the response so we can mint a valid code.
	body := rr.Body.String()
	const m = `<code class="text-xs break-all">`
	i := strings.Index(body, m)
	if i < 0 {
		t.Fatalf("secret not found in setup card; body: %s", body)
	}
	end := strings.Index(body[i+len(m):], `</code>`)
	secret := body[i+len(m) : i+len(m)+end]
	code := generateCurrentCode(t, secret)

	// Submit correct code.
	form = url.Values{"csrf_token": {tok}, "code": {code}}
	req = httptest.NewRequest("POST", "/account/1/totp/verify", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.AddCookie(cookie)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("verify status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Two-factor authentication is enabled") {
		t.Errorf("expected 'enabled' card after verify; got: %s", rr.Body.String())
	}
	var u UserGORM
	_ = ag.DB.Where("username = ?", "admin").First(&u).Error
	if u.TOTPSecret == "" {
		t.Error("DB still has no TOTP secret after successful verify")
	}
	if u.TOTPSecret != secret {
		t.Errorf("DB secret %q ≠ enrolled secret %q", u.TOTPSecret, secret)
	}
}

func TestTOTPDisable_AdminCanDisableOthers(t *testing.T) {
	handler, ag := newRoutedAuthGORM(t)
	// Bob enrols.
	enrolTOTP(t, ag, "bob")

	// Admin logs in, disables bob's TOTP.
	cookie := loginVia(t, handler, "admin", "adminpass")
	var bob UserGORM
	_ = ag.DB.Where("username = ?", "bob").First(&bob).Error
	tok := csrfTokenFor(handler, "/account/"+itoa(int(bob.ID)), cookie)

	form := url.Values{"csrf_token": {tok}}
	req := httptest.NewRequest("POST", "/account/"+itoa(int(bob.ID))+"/totp/disable",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("disable = %d", rr.Code)
	}

	_ = ag.DB.Where("username = ?", "bob").First(&bob).Error
	if bob.TOTPSecret != "" {
		t.Errorf("bob's TOTP secret still set after admin disable: %q", bob.TOTPSecret)
	}
}

func TestTOTPDisable_NonAdminCannotDisableOthers(t *testing.T) {
	handler, ag := newRoutedAuthGORM(t)
	enrolTOTP(t, ag, "admin")

	cookie := loginVia(t, handler, "bob", "bobpass")
	tok := csrfTokenFor(handler, "/account/me", cookie)

	form := url.Values{"csrf_token": {tok}}
	req := httptest.NewRequest("POST", "/account/1/totp/disable",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("bob disable admin's TOTP = %d, want 403", rr.Code)
	}

	var admin UserGORM
	_ = ag.DB.Where("username = ?", "admin").First(&admin).Error
	if admin.TOTPSecret == "" {
		t.Error("admin's TOTP secret was cleared despite forbidden response")
	}
}

func TestTOTPBegin_AdminCannotEnrolOthers(t *testing.T) {
	handler, ag := newRoutedAuthGORM(t)
	cookie := loginVia(t, handler, "admin", "adminpass")
	var bob UserGORM
	_ = ag.DB.Where("username = ?", "bob").First(&bob).Error
	tok := csrfTokenFor(handler, "/account/"+itoa(int(bob.ID)), cookie)

	form := url.Values{"csrf_token": {tok}}
	req := httptest.NewRequest("POST", "/account/"+itoa(int(bob.ID))+"/totp/begin",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("admin /totp/begin for bob = %d, want 403", rr.Code)
	}
}

func TestTOTPCancel_DropsPendingSecret(t *testing.T) {
	handler, _ := newRoutedAuthGORM(t)
	cookie := loginVia(t, handler, "admin", "adminpass")
	tok := csrfTokenFor(handler, "/account/me", cookie)

	// Begin enrolment.
	form := url.Values{"csrf_token": {tok}}
	req := httptest.NewRequest("POST", "/account/1/totp/begin", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// Cancel.
	req = httptest.NewRequest("POST", "/account/1/totp/cancel", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.AddCookie(cookie)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("cancel = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Enable TOTP") {
		t.Errorf("after cancel the card should offer Enable TOTP; got: %s", rr.Body.String())
	}
}
