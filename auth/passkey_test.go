package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// newRoutedAuthGORMWithRP wraps newRoutedAuthGORM with the RP config
// passkey routes require. Used by passkey-related tests.
func newRoutedAuthGORMWithRP(t *testing.T) (http.Handler, *AuthGORM) {
	t.Helper()
	ag, sm := newTestAuthGORM(t)
	if err := ag.UserAdd("bob", "bob@local", "bobpass"); err != nil {
		t.Fatalf("UserAdd bob: %v", err)
	}
	if err := ag.Passwd("admin", "adminpass"); err != nil {
		t.Fatalf("Passwd admin: %v", err)
	}
	ag.RPDisplayName = "test"
	ag.RPID = "localhost"
	ag.RPOrigins = []string{"http://localhost"}
	mux := http.NewServeMux()
	if _, err := ag.Route(mux, "", nil); err != nil {
		t.Fatalf("Route: %v", err)
	}
	return sm.LoadAndSave(CSRFWrap(sm)(mux)), ag
}

func TestPasskeyGORM_Migrate(t *testing.T) {
	_, ag := newRoutedAuthGORMWithRP(t)
	// AutoMigrate ran in NewAuthGORM — table should be queryable.
	var n int64
	if err := ag.DB.Model(&PasskeyGORM{}).Count(&n).Error; err != nil {
		t.Fatalf("count auth_passkeys: %v", err)
	}
	if n != 0 {
		t.Errorf("fresh DB should have 0 passkeys, got %d", n)
	}
}

func TestAuthGORM_IsAuthPath_PasskeyEndpoints(t *testing.T) {
	_, ag := newRoutedAuthGORMWithRP(t)
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/login/passkey/options", true},
		{"/login/passkey/finish", true},
		{"/login/passkey/begin", false},   // not a real endpoint
		{"/account/1/passkey/begin", false}, // account-side: gated by handlers, not shell
		{"/admin/users", false},
	} {
		if got := ag.IsAuthPath(tc.path); got != tc.want {
			t.Errorf("IsAuthPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestAuthGORM_PasskeyRoutesNotMountedWithoutRP(t *testing.T) {
	ag, sm := newTestAuthGORM(t)
	// Deliberately don't set RP fields. Route() should succeed but
	// skip the passkey endpoints.
	mux := http.NewServeMux()
	if _, err := ag.Route(mux, "", nil); err != nil {
		t.Fatalf("Route: %v", err)
	}
	handler := sm.LoadAndSave(CSRFWrap(sm)(mux))

	// Need a valid CSRF token to get past the middleware so we can
	// observe the mux's own 404 (passkey routes weren't registered).
	tok, cookie := primeCSRFLogin(t, handler)
	form := url.Values{"csrf_token": {tok}}
	req := httptest.NewRequest("POST", "/login/passkey/options", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("passkey route without RP config = %d, want 404", rr.Code)
	}
}

func TestPasskeyBegin_LoginOptionsReturnsChallenge(t *testing.T) {
	handler, _ := newRoutedAuthGORMWithRP(t)
	// /login/passkey/options is reachable anonymously — it's part
	// of the discoverable-login ceremony.
	tok, cookie := primeCSRFLogin(t, handler)
	form := url.Values{"csrf_token": {tok}}
	req := httptest.NewRequest("POST", "/login/passkey/options", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", tok)
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	// JSON shape: { publicKey: { challenge, ... } }
	var out struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			RPID      string `json:"rpId"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("json: %v, body: %s", err, rr.Body.String())
	}
	if out.PublicKey.Challenge == "" {
		t.Error("challenge missing from /login/passkey/options")
	}
	if out.PublicKey.RPID != "localhost" {
		t.Errorf("rpId = %q, want localhost", out.PublicKey.RPID)
	}
}

func TestPasskeyEnrolBegin_RequiresSelf(t *testing.T) {
	handler, ag := newRoutedAuthGORMWithRP(t)
	cookie := loginVia(t, handler, "bob", "bobpass") // bob is non-admin

	// Bob tries to enrol a passkey for admin (user id 1).
	var admin UserGORM
	_ = ag.DB.Where("username = ?", "admin").First(&admin).Error

	tok := csrfTokenFor(handler, "/account/me", cookie)
	form := url.Values{"csrf_token": {tok}, "name": {"iPhone"}}
	req := httptest.NewRequest("POST", "/account/"+itoa(int(admin.ID))+"/passkey/begin",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("bob enrolling for admin = %d, want 403", rr.Code)
	}
}

func TestPasskeyEnrolBegin_SelfReturnsCreationOptions(t *testing.T) {
	handler, ag := newRoutedAuthGORMWithRP(t)
	cookie := loginVia(t, handler, "admin", "adminpass")
	tok := csrfTokenFor(handler, "/account/me", cookie)

	form := url.Values{"csrf_token": {tok}, "name": {"YubiKey"}}
	req := httptest.NewRequest("POST", "/account/1/passkey/begin", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			User      struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				DisplayName string `json:"displayName"`
			} `json:"user"`
			RP struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"rp"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("json: %v, body: %s", err, rr.Body.String())
	}
	if out.PublicKey.User.Name != "admin" {
		t.Errorf("user.name = %q, want admin", out.PublicKey.User.Name)
	}
	if out.PublicKey.User.ID == "" {
		t.Error("user.id missing — WebAuthnHandle not set?")
	}
	if out.PublicKey.RP.ID != "localhost" {
		t.Errorf("rp.id = %q, want localhost", out.PublicKey.RP.ID)
	}

	// Side-effect: the user's WebAuthnHandle is now persisted.
	var u UserGORM
	_ = ag.DB.Where("username = ?", "admin").First(&u).Error
	if len(u.WebAuthnHandle) == 0 {
		t.Error("WebAuthnHandle not persisted after begin")
	}
}

func TestPasskeyEnrolFinish_NoPendingSession_400(t *testing.T) {
	handler, _ := newRoutedAuthGORMWithRP(t)
	cookie := loginVia(t, handler, "admin", "adminpass")
	tok := csrfTokenFor(handler, "/account/me", cookie)

	// No /begin call → no session data. Finish should reject.
	req := httptest.NewRequest("POST", "/account/1/passkey/finish",
		strings.NewReader(`{"id":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", tok)
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (no enrolment in progress)", rr.Code)
	}
}

func TestPasskeyDelete_FromDBDirectly(t *testing.T) {
	handler, ag := newRoutedAuthGORMWithRP(t)
	cookie := loginVia(t, handler, "admin", "adminpass")
	tok := csrfTokenFor(handler, "/account/me", cookie)

	// Hand-insert a passkey row (we can't easily run a real WebAuthn
	// ceremony from a test). The delete handler shouldn't care
	// about the credential's cryptographic state — it just removes
	// the row.
	row := PasskeyGORM{
		UserID:       1,
		CredentialID: []byte{1, 2, 3, 4},
		Name:         "Test key",
	}
	if err := ag.DB.Create(&row).Error; err != nil {
		t.Fatalf("create passkey: %v", err)
	}

	form := url.Values{"csrf_token": {tok}}
	req := httptest.NewRequest("POST", "/account/1/passkey/"+itoa(int(row.ID))+"/delete",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var n int64
	_ = ag.DB.Model(&PasskeyGORM{}).Where("id = ?", row.ID).Count(&n).Error
	if n != 0 {
		t.Error("passkey row not deleted")
	}
	// Response is the refreshed passkey card — should mention the
	// "No passkeys enrolled" empty state.
	if !strings.Contains(rr.Body.String(), "No passkeys enrolled") {
		t.Errorf("response missing empty-state copy: %s", rr.Body.String())
	}
}

func TestPasskeyDelete_OtherUser_404(t *testing.T) {
	handler, ag := newRoutedAuthGORMWithRP(t)
	cookie := loginVia(t, handler, "admin", "adminpass")
	tok := csrfTokenFor(handler, "/account/me", cookie)

	// A passkey row belonging to bob.
	var bob UserGORM
	_ = ag.DB.Where("username = ?", "bob").First(&bob).Error
	row := PasskeyGORM{
		UserID:       bob.ID,
		CredentialID: []byte{9, 9, 9},
		Name:         "Bob's",
	}
	if err := ag.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}

	// Admin tries to delete bob's passkey via admin's own /account/1
	// scope — handler should not find row id under user 1.
	form := url.Values{"csrf_token": {tok}}
	req := httptest.NewRequest("POST", "/account/1/passkey/"+itoa(int(row.ID))+"/delete",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (admin can't reach bob's passkey through admin's account)", rr.Code)
	}
}

func TestPasskeyAccountCardRendersInForm(t *testing.T) {
	handler, _ := newRoutedAuthGORMWithRP(t)
	cookie := loginVia(t, handler, "admin", "adminpass")

	req := httptest.NewRequest("GET", "/account/me", nil)
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{`id="auth-passkey-card"`, "Passkeys", "Add passkey"} {
		if !strings.Contains(body, want) {
			t.Errorf("account page missing %q", want)
		}
	}
}

func TestLoginForm_PasskeyButtonRendersWhenRPSet(t *testing.T) {
	handler, _ := newRoutedAuthGORMWithRP(t)
	req := httptest.NewRequest("GET", "/login", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{`id="auth-passkey-login-btn"`, "Use passkey", "isConditionalMediationAvailable"} {
		if !strings.Contains(body, want) {
			t.Errorf("login form missing %q", want)
		}
	}
	// autocomplete="username webauthn" enables conditional autofill.
	if !strings.Contains(body, `autocomplete="username webauthn"`) {
		t.Error("username field missing webauthn autocomplete hint")
	}
}

func TestLoginForm_PasskeyButtonAbsentWithoutRP(t *testing.T) {
	ag, sm := newTestAuthGORM(t)
	mux := http.NewServeMux()
	if _, err := ag.Route(mux, "", nil); err != nil {
		t.Fatalf("Route: %v", err)
	}
	handler := sm.LoadAndSave(CSRFWrap(sm)(mux))

	req := httptest.NewRequest("GET", "/login", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	body := rr.Body.String()
	if strings.Contains(body, "Use passkey") {
		t.Error("Use passkey button rendered despite no RP config")
	}
	if strings.Contains(body, `autocomplete="username webauthn"`) {
		t.Error("username autocomplete still mentions webauthn")
	}
}

// primeCSRFLogin: GET /login to seed a CSRF token + session cookie
// for anonymous flows (like passkey discoverable login).
func primeCSRFLogin(t *testing.T, handler http.Handler) (string, *http.Cookie) {
	t.Helper()
	req := httptest.NewRequest("GET", "/login", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	cookie := pickCookie(rr.Result().Cookies(), "session")
	if cookie == nil {
		t.Fatal("no session cookie")
	}
	body := rr.Body.String()
	const m = `name="csrf_token" value="`
	i := strings.Index(body, m)
	if i < 0 {
		t.Fatalf("no csrf_token: %s", body)
	}
	tok := body[i+len(m):]
	tok = tok[:strings.Index(tok, `"`)]
	return tok, cookie
}
