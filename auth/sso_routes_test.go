package auth

import (
	"github.com/go-chi/chi/v5"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// registerFakeSSOProvider inserts a fake provider into AuthGORM's
// internal list, bypassing the OIDC discovery that AddOIDCProvider
// would require. Test-only helper — production code can't see
// ssoProviders.
func registerFakeSSOProvider(ag *AuthGORM, name string, id ssoIdentity, cfg ssoProviderConfig) *fakeSSOProvider {
	p := &fakeSSOProvider{
		nameVal:    name,
		displayVal: strings.ToUpper(name[:1]) + name[1:],
		cfg:        cfg,
		identity:   id,
	}
	id.Provider = name
	p.identity = id
	ag.ssoProviders = append(ag.ssoProviders, p)
	return p
}

// ──────────────────────────────────────────────────────────────────
// Login form — SSO buttons render only when providers are configured.

func TestLoginForm_NoSSOButtonsByDefault(t *testing.T) {
	handler, _ := newRoutedAuthGORM(t)
	req := httptest.NewRequest("GET", "/login", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "Sign in with") {
		t.Errorf("login form contains SSO button text without any provider configured")
	}
}

func TestLoginForm_RendersOneButtonPerProvider(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	registerFakeSSOProvider(ag, "google", ssoIdentity{}, ssoProviderConfig{})
	registerFakeSSOProvider(ag, "github", ssoIdentity{}, ssoProviderConfig{})
	mux := chi.NewRouter()
	if err := ag.RegisterRoutes(mux, "", nil); err != nil {
		t.Fatal(err)
	}
	handler := ag.Sessions.LoadAndSave(CSRFWrap(ag.Sessions)(mux))

	req := httptest.NewRequest("GET", "/login", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	body := rr.Body.String()
	if !strings.Contains(body, "Sign in with Google") {
		t.Errorf("missing Google button: %s", body)
	}
	if !strings.Contains(body, "Sign in with Github") {
		t.Errorf("missing Github button: %s", body)
	}
}

// ──────────────────────────────────────────────────────────────────
// Start handler — stashes ceremony state, redirects to provider.

func TestSSOStart_StashesStateAndRedirects(t *testing.T) {
	ag, sm := newTestAuthGORM(t)
	registerFakeSSOProvider(ag, "google", ssoIdentity{}, ssoProviderConfig{})
	mux := chi.NewRouter()
	if err := ag.RegisterRoutes(mux, "", nil); err != nil {
		t.Fatal(err)
	}
	handler := sm.LoadAndSave(CSRFWrap(sm)(mux))

	req := httptest.NewRequest("GET", "/login/sso/google?next=/admin", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rr.Code)
	}
	loc := rr.Header().Get("Location")
	if loc != "https://idp.test/authorize" {
		t.Errorf("Location = %q, want fakeSSOProvider authorize URL", loc)
	}
	cookie := rr.Header().Get("Set-Cookie")
	if cookie == "" {
		t.Fatal("no session cookie set")
	}
	// Round-trip a second request with the same session cookie to
	// verify the ceremony keys actually landed in storage.
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Header.Set("Cookie", cookie)
	rr2 := httptest.NewRecorder()
	sm.LoadAndSave(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := sm.GetString(r.Context(), ssoStateKey); got == "" {
			t.Error("ssoStateKey not stashed")
		}
		if got := sm.GetString(r.Context(), ssoPKCEKey); got == "" {
			t.Error("ssoPKCEKey not stashed")
		}
		if got := sm.GetString(r.Context(), ssoNonceKey); got == "" {
			t.Error("ssoNonceKey not stashed")
		}
		if got := sm.GetString(r.Context(), ssoProviderKey); got != "google" {
			t.Errorf("ssoProviderKey = %q, want google", got)
		}
		if got := sm.GetString(r.Context(), ssoNextKey); got != "/admin" {
			t.Errorf("ssoNextKey = %q, want /admin", got)
		}
	})).ServeHTTP(rr2, req2)
}

func TestSSOStart_UnknownProvider404(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	registerFakeSSOProvider(ag, "google", ssoIdentity{}, ssoProviderConfig{})
	mux := chi.NewRouter()
	if err := ag.RegisterRoutes(mux, "", nil); err != nil {
		t.Fatal(err)
	}
	handler := ag.Sessions.LoadAndSave(CSRFWrap(ag.Sessions)(mux))
	req := httptest.NewRequest("GET", "/login/sso/twitter", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// ──────────────────────────────────────────────────────────────────
// Callback — state mismatch / unknown provider / missing code.

func TestSSOCallback_StateMismatch400(t *testing.T) {
	ag, sm := newTestAuthGORM(t)
	registerFakeSSOProvider(ag, "google", ssoIdentity{Subject: "s", Email: "x@y"}, ssoProviderConfig{})
	mux := chi.NewRouter()
	if err := ag.RegisterRoutes(mux, "", nil); err != nil {
		t.Fatal(err)
	}
	handler := sm.LoadAndSave(CSRFWrap(sm)(mux))

	// Start the ceremony to populate state.
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest("GET", "/login/sso/google", nil))
	cookie := rr.Header().Get("Set-Cookie")

	// Callback with a wrong state.
	req := httptest.NewRequest("GET", "/login/sso/google/callback?state=WRONG&code=abc", nil)
	req.Header.Set("Cookie", cookie)
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req)
	if rr2.Code != http.StatusBadRequest {
		t.Errorf("state mismatch → %d, want 400; body: %s", rr2.Code, rr2.Body.String())
	}
}

func TestSSOCallback_NoCeremonyStarted400(t *testing.T) {
	ag, sm := newTestAuthGORM(t)
	registerFakeSSOProvider(ag, "google", ssoIdentity{}, ssoProviderConfig{})
	mux := chi.NewRouter()
	if err := ag.RegisterRoutes(mux, "", nil); err != nil {
		t.Fatal(err)
	}
	handler := sm.LoadAndSave(CSRFWrap(sm)(mux))
	req := httptest.NewRequest("GET", "/login/sso/google/callback?state=anything&code=abc", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("no ceremony → %d, want 400", rr.Code)
	}
}

// Full happy-path callback exercise: we use the fake provider's
// exchange() to short-circuit the real OAuth2 round-trip and verify
// resolveSSOLogin → loginStage1 → session is set correctly.
func TestSSOCallback_HappyPath_AutoCreate(t *testing.T) {
	ag, sm := newTestAuthGORM(t)
	registerFakeSSOProvider(ag, "google",
		ssoIdentity{Subject: "sub-1", Email: "bob@example.com", DisplayName: "Bob"},
		ssoProviderConfig{},
	)
	mux := chi.NewRouter()
	if err := ag.RegisterRoutes(mux, "", nil); err != nil {
		t.Fatal(err)
	}
	handler := sm.LoadAndSave(CSRFWrap(sm)(mux))

	// Start ceremony → get the state that the IdP would echo back.
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest("GET", "/login/sso/google?next=/admin", nil))
	cookie := rr.Header().Get("Set-Cookie")

	// Read state from the live session for the callback.
	var state string
	sm.LoadAndSave(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state = sm.GetString(r.Context(), ssoStateKey)
	})).ServeHTTP(httptest.NewRecorder(), withCookie(httptest.NewRequest("GET", "/", nil), cookie))
	if state == "" {
		t.Fatal("no state in session after start")
	}

	// Now hit the callback.
	req := httptest.NewRequest("GET", "/login/sso/google/callback?state="+state+"&code=anything", nil)
	req.Header.Set("Cookie", cookie)
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req)
	if rr2.Code != http.StatusSeeOther {
		t.Fatalf("callback status = %d, want 303; body: %s", rr2.Code, rr2.Body.String())
	}
	if loc := rr2.Header().Get("Location"); loc != "/admin" {
		t.Errorf("redirect = %q, want /admin", loc)
	}

	// Verify bob was created with SSOOnly=true.
	var bob UserGORM
	if err := ag.DB.Where("email = ?", "bob@example.com").First(&bob).Error; err != nil {
		t.Fatalf("user not created: %v", err)
	}
	if !bob.SSOOnly {
		t.Error("auto-created user should be SSOOnly")
	}
	if bob.PasswordHash != "" {
		t.Error("auto-created user should have no password hash")
	}
	// And that the identity link landed.
	var link SSOIdentityGORM
	if err := ag.DB.Where("provider = ? AND subject = ?", "google", "sub-1").First(&link).Error; err != nil {
		t.Errorf("identity not linked: %v", err)
	}
}

func withCookie(r *http.Request, cookie string) *http.Request {
	r.Header.Set("Cookie", cookie)
	return r
}

// ──────────────────────────────────────────────────────────────────
// SSOOnly enforcement on account endpoints.

func TestAccountPost_BlockedForSSOOnly(t *testing.T) {
	handler, ag := newRoutedAuthGORM(t)
	// Mark bob as SSOOnly. bobpass is set by newRoutedAuthGORM.
	if err := ag.DB.Model(&UserGORM{}).Where("username = ?", "bob").Update("sso_only", true).Error; err != nil {
		t.Fatal(err)
	}
	cookie := loginVia(t, handler, "bob", "bobpass")
	csrf := csrfTokenFor(handler, "/account/me", cookie)
	if csrf == "" {
		t.Fatal("no csrf token from /account/me")
	}
	form := "csrf_token=" + csrf + "&old_password=bobpass&new_password=new&new_password_confirm=new"
	req := httptest.NewRequest("POST", "/account/me", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("SSOOnly password POST → %d, want 403; body: %s", rr.Code, rr.Body.String())
	}
}

func TestPasskeyEnrolBegin_BlockedForSSOOnly(t *testing.T) {
	handler, ag := newRoutedAuthGORM(t)
	// Need RP fields configured for the passkey routes to mount —
	// re-mount via direct field write because newRoutedAuthGORM
	// already called Route(). Cleanest: spin a fresh AuthGORM with
	// RP fields set BEFORE Route().
	_ = handler
	_ = ag

	// Build a fresh AuthGORM with RP fields configured.
	ag2, sm2 := newTestAuthGORM(t)
	ag2.RPDisplayName = "test"
	ag2.RPID = "localhost"
	ag2.RPOrigins = []string{"http://localhost"}
	if err := ag2.UserAdd("bob", "bob@local", "bobpass"); err != nil {
		t.Fatal(err)
	}
	if err := ag2.DB.Model(&UserGORM{}).Where("username = ?", "bob").Update("sso_only", true).Error; err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	if err := ag2.RegisterRoutes(mux, "", nil); err != nil {
		t.Fatal(err)
	}
	h2 := sm2.LoadAndSave(CSRFWrap(sm2)(mux))

	cookie := loginVia(t, h2, "bob", "bobpass")
	csrf := csrfTokenFor(h2, "/account/me", cookie)
	form := "csrf_token=" + csrf + "&name=mykey"
	// Bob is user ID 3 (admin=1, plus the newTestAuthGORM admin user — but
	// the seed inserts only one admin). Easier: look up bob's ID.
	var bob UserGORM
	if err := ag2.DB.Where("username = ?", "bob").First(&bob).Error; err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST",
		"/account/"+strconvUint(bob.ID)+"/passkey/begin",
		strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	h2.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("SSOOnly passkey begin → %d, want 403; body: %s", rr.Code, rr.Body.String())
	}
}

func strconvUint(u uint) string {
	if u == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for u > 0 {
		i--
		buf[i] = byte('0' + u%10)
		u /= 10
	}
	return string(buf[i:])
}
