package auth

import (
	"context"
	"errors"
	"github.com/go-chi/chi/v5"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/alexedwards/scs/v2"
)

func newTestAuth(t *testing.T) (*AuthSimple, *scs.SessionManager) {
	t.Helper()
	sm := scs.New()
	sa := NewAuthSimple(sm)
	if err := sa.UserAdd("admin", "admin@local", "secret"); err != nil {
		t.Fatalf("UserAdd: %v", err)
	}
	return sa, sm
}

func TestUserAddDuplicateRejected(t *testing.T) {
	sa, _ := newTestAuth(t)
	if err := sa.UserAdd("admin", "x", "x"); !errors.Is(err, ErrUserExists) {
		t.Errorf("expected ErrUserExists, got %v", err)
	}
}

func TestUserAddEmptyUsername(t *testing.T) {
	sm := scs.New()
	sa := NewAuthSimple(sm)
	if err := sa.UserAdd("", "x", "x"); !errors.Is(err, ErrEmptyUsername) {
		t.Errorf("expected ErrEmptyUsername, got %v", err)
	}
}

func TestUserDelRoundTrip(t *testing.T) {
	sa, _ := newTestAuth(t)
	if err := sa.UserDel("nope"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("UserDel(missing) → %v, want ErrUserNotFound", err)
	}
	if err := sa.UserDel("admin"); err != nil {
		t.Fatalf("UserDel: %v", err)
	}
	if _, err := sa.Authenticate("admin", "secret"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("after UserDel, Authenticate → %v, want ErrUserNotFound", err)
	}
}

func TestPasswdSwitchesHash(t *testing.T) {
	sa, _ := newTestAuth(t)
	if err := sa.Passwd("admin", "newsecret"); err != nil {
		t.Fatalf("Passwd: %v", err)
	}
	if _, err := sa.Authenticate("admin", "secret"); err == nil {
		t.Error("old password still works after Passwd")
	}
	if _, err := sa.Authenticate("admin", "newsecret"); err != nil {
		t.Errorf("new password rejected: %v", err)
	}
	if err := sa.Passwd("nope", "x"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("Passwd(missing) → %v, want ErrUserNotFound", err)
	}
}

func TestAuthenticateWrongPassword(t *testing.T) {
	sa, _ := newTestAuth(t)
	_, err := sa.Authenticate("admin", "wrong")
	if !errors.Is(err, ErrInvalidPassword) {
		t.Errorf("wrong password → %v, want ErrInvalidPassword", err)
	}
	if errors.Is(err, ErrUserNotFound) {
		t.Error("wrong-password and unknown-user should not share an error class (enumeration check at the HTTP layer)")
	}
}

func TestUserSimpleSatisfiesUser(t *testing.T) {
	var _ User = (*UserSimple)(nil)
	var _ Group = (*GroupSimple)(nil)

	sa, _ := newTestAuth(t)
	u, err := sa.Authenticate("admin", "secret")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if u.Username() != "admin" {
		t.Errorf("Username = %q, want admin", u.Username())
	}
	if u.Email() != "admin@local" {
		t.Errorf("Email = %q, want admin@local", u.Email())
	}
	if !u.HasGroup("admin") {
		t.Error("admin user should be in admin group")
	}
	if u.HasGroup("editor") {
		t.Error("admin user should not be in editor group")
	}
	groups := u.Groups()
	if len(groups) != 1 || groups[0].Name() != "admin" {
		t.Errorf("Groups = %+v, want [admin]", groups)
	}
}

func TestLoginURLEncodesNext(t *testing.T) {
	sm := scs.New()
	sa := NewAuthSimple(sm)
	if got, want := sa.LoginURL(""), "/login"; got != want {
		t.Errorf("LoginURL(\"\") = %q, want %q", got, want)
	}
	if got, want := sa.LoginURL("/heroes?page=2"), "/login?next=%2Fheroes%3Fpage%3D2"; got != want {
		t.Errorf("LoginURL(/heroes?page=2) = %q, want %q", got, want)
	}
	if got, want := sa.LogoutURL(""), "/logout"; got != want {
		t.Errorf("LogoutURL(\"\") = %q, want %q", got, want)
	}
}

func TestSafeNext(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"", ""},
		{"/foo", "/foo"},
		{"/foo?x=1", "/foo?x=1"},
		{"//evil.example/path", ""},       // protocol-relative
		{"https://evil.example/path", ""}, // absolute
		{"javascript:alert(1)", ""},       // not "/"
		{"x", ""},                         // not "/"
	} {
		if got := safeNext(tc.in); got != tc.want {
			t.Errorf("safeNext(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCurrentUserAnonymous(t *testing.T) {
	sa, sm := newTestAuth(t)
	withSession(t, sm, func(ctx context.Context, r *http.Request) {
		if u := sa.CurrentUser(r.Context()); u != nil {
			t.Errorf("CurrentUser on anonymous session = %v, want nil", u)
		}
	})
}

func TestCurrentUserAfterLogin(t *testing.T) {
	sa, sm := newTestAuth(t)
	withSession(t, sm, func(ctx context.Context, r *http.Request) {
		u, err := sa.Authenticate("admin", "secret")
		if err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
		if err := sa.Login(ctx, u); err != nil {
			t.Fatalf("Login: %v", err)
		}
		got := sa.CurrentUser(r.Context())
		if got == nil || got.Username() != "admin" {
			t.Errorf("CurrentUser after Login = %v, want admin", got)
		}
	})
}

func TestCurrentUserAfterUserDel(t *testing.T) {
	sa, sm := newTestAuth(t)
	withSession(t, sm, func(ctx context.Context, r *http.Request) {
		u, _ := sa.Authenticate("admin", "secret")
		_ = sa.Login(ctx, u)
		// Now delete the user. The session still points at "admin" but
		// no such user is registered.
		if err := sa.UserDel("admin"); err != nil {
			t.Fatalf("UserDel: %v", err)
		}
		if got := sa.CurrentUser(r.Context()); got != nil {
			t.Errorf("CurrentUser after UserDel = %v, want nil", got)
		}
	})
}

func TestCurrentUserForgedSessionCookie(t *testing.T) {
	sa, sm := newTestAuth(t)
	// LoadAndSave inspects the cookie; an unknown session ID is treated
	// the same as no cookie (anonymous), so CurrentUser → nil.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "this-is-not-a-real-session-id"})
	sm.LoadAndSave(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := sa.CurrentUser(r.Context()); u != nil {
			t.Errorf("CurrentUser with forged cookie = %v, want nil", u)
		}
	})).ServeHTTP(rr, req)
}

func TestLogoutDestroysSession(t *testing.T) {
	sa, sm := newTestAuth(t)
	withSession(t, sm, func(ctx context.Context, r *http.Request) {
		u, _ := sa.Authenticate("admin", "secret")
		_ = sa.Login(ctx, u)
		if sa.CurrentUser(r.Context()) == nil {
			t.Fatal("Login didn't take effect")
		}
		if err := sa.Logout(ctx); err != nil {
			t.Fatalf("Logout: %v", err)
		}
		if sa.CurrentUser(r.Context()) != nil {
			t.Error("Logout didn't clear the session")
		}
	})
}

// ──────────────────────────────────────────────────────────────────
// HTTP-level tests via the full Route() handler chain.

// newRoutedAuth builds an AuthSimple, runs Route() against an
// http.ServeMux, and returns a handler wired with scs LoadAndSave +
// CSRFWrap so requests round-trip session cookies + CSRF properly.
func newRoutedAuth(t *testing.T) (*AuthSimple, *scs.SessionManager, http.Handler) {
	t.Helper()
	sm := scs.New()
	sa := NewAuthSimple(sm)
	if err := sa.UserAdd("admin", "admin@local", "secret"); err != nil {
		t.Fatalf("UserAdd: %v", err)
	}
	mux := chi.NewRouter()
	if err := sa.RegisterRoutes(mux, "", nil); err != nil {
		t.Fatalf("Route: %v", err)
	}
	handler := sm.LoadAndSave(CSRFWrap(sm)(mux))
	return sa, sm, handler
}

func TestRouteLoginPOSTSuccess(t *testing.T) {
	_, _, handler := newRoutedAuth(t)

	// GET /login to grab a CSRF token + session cookie.
	tok, cookie := primeCSRF(t, handler, "/login")

	form := url.Values{
		"csrf_token": {tok},
		"username":   {"admin"},
		"password":   {"secret"},
		"next":       {"/heroes"},
	}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("POST /login status = %d, want 303; body: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Location"); got != "/heroes" {
		t.Errorf("Location = %q, want /heroes", got)
	}
}

func TestRouteLoginPOSTWrongCSRFRejected(t *testing.T) {
	_, _, handler := newRoutedAuth(t)
	_, cookie := primeCSRF(t, handler, "/login")

	form := url.Values{
		"csrf_token": {"wrong-token"},
		"username":   {"admin"},
		"password":   {"secret"},
	}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("POST /login with wrong CSRF status = %d, want 403", rr.Code)
	}
}

func TestRouteLoginPOSTWrongPasswordReRenders(t *testing.T) {
	_, _, handler := newRoutedAuth(t)
	tok, cookie := primeCSRF(t, handler, "/login")

	form := url.Values{
		"csrf_token": {tok},
		"username":   {"admin"},
		"password":   {"WRONG"},
	}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("POST /login with wrong password status = %d, want 401", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Invalid username or password") {
		t.Errorf("response body missing error message; got: %s", rr.Body.String())
	}
}

func TestRouteLoginNextUnsafeFallsBack(t *testing.T) {
	sa, _, handler := newRoutedAuth(t)
	sa.AfterLogin = "/dashboard"

	tok, cookie := primeCSRF(t, handler, "/login")
	form := url.Values{
		"csrf_token": {tok},
		"username":   {"admin"},
		"password":   {"secret"},
		"next":       {"//evil.example/x"},
	}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rr.Code)
	}
	if got := rr.Header().Get("Location"); got != "/dashboard" {
		t.Errorf("unsafe next not stripped: Location = %q, want /dashboard", got)
	}
}

// ──────────────────────────────────────────────────────────────────
// Test helpers.

// withSession runs body inside a session that scs has loaded for the
// supplied request. Mirrors scs.SessionManager.LoadAndSave handler
// behaviour for direct ctx access in tests.
func withSession(t *testing.T, sm *scs.SessionManager, body func(ctx context.Context, r *http.Request)) {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	sm.LoadAndSave(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body(r.Context(), r)
	})).ServeHTTP(rr, req)
}

// primeCSRF runs a GET against path to mint a CSRF token + session
// cookie, then extracts both. Subsequent POSTs need both to satisfy
// CSRFWrap.
func primeCSRF(t *testing.T, handler http.Handler, path string) (token string, cookie *http.Cookie) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", path, rr.Code)
	}
	for _, c := range rr.Result().Cookies() {
		if c.Name == "session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatalf("no session cookie set on GET %s", path)
	}

	// Extract the token from the hidden input the templ emits. We
	// don't parse the HTML — a substring scan is enough for the form
	// shape we own.
	body, _ := io.ReadAll(rr.Result().Body)
	const marker = `name="csrf_token" value="`
	i := strings.Index(string(body), marker)
	if i < 0 {
		t.Fatalf("no csrf_token field in /login body: %s", body)
	}
	start := i + len(marker)
	end := strings.Index(string(body[start:]), `"`)
	if end < 0 {
		t.Fatalf("malformed csrf_token field")
	}
	token = string(body[start : start+end])
	if token == "" {
		t.Fatalf("empty csrf_token rendered")
	}
	return token, cookie
}
