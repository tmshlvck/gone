package auth

import (
	"github.com/go-chi/chi/v5"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// newRoutedAuthGORM builds an AuthGORM with two users:
//
//	admin / adminpass — member of "admin" group
//	bob   / bobpass   — non-admin
//
// Returns the wired http.Handler (LoadAndSave → CSRFWrap → mux) and
// the AuthGORM so tests can inspect the DB directly when needed.
func newRoutedAuthGORM(t *testing.T) (http.Handler, *AuthGORM) {
	t.Helper()
	ag, sm := newTestAuthGORM(t)
	if err := ag.UserAdd("bob", "bob@local", "bobpass"); err != nil {
		t.Fatalf("UserAdd bob: %v", err)
	}
	// Reset admin's password to a known one (newTestAuthGORM uses "secret").
	if err := ag.Passwd("admin", "adminpass"); err != nil {
		t.Fatalf("Passwd admin: %v", err)
	}
	mux := chi.NewRouter()
	if err := ag.RegisterRoutes(mux, "", nil); err != nil {
		t.Fatalf("Route: %v", err)
	}
	return sm.LoadAndSave(CSRFWrap(sm)(mux)), ag
}

// loginVia POSTs the login form and returns the post-login session
// cookie. Wraps the prime/CSRF dance.
func loginVia(t *testing.T, handler http.Handler, username, password string) *http.Cookie {
	t.Helper()
	// GET /login to seed the CSRF token + session cookie.
	req := httptest.NewRequest("GET", "/login", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	cookie := pickCookie(rr.Result().Cookies(), "session")
	if cookie == nil {
		t.Fatal("no session cookie after GET /login")
	}
	body, _ := io.ReadAll(rr.Result().Body)
	const m = `name="csrf_token" value="`
	i := strings.Index(string(body), m)
	if i < 0 {
		t.Fatalf("no csrf_token: %s", body)
	}
	start := i + len(m)
	end := strings.Index(string(body[start:]), `"`)
	tok := string(body[start : start+end])

	// POST /login
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
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("login %s status = %d, want 303; body: %s", username, rr.Code, rr.Body.String())
	}
	return pickCookie(rr.Result().Cookies(), "session")
}

func pickCookie(cs []*http.Cookie, name string) *http.Cookie {
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// csrfTokenFor pulls the CSRF token out of a rendered form fragment.
func csrfTokenFor(handler http.Handler, path string, cookie *http.Cookie) string {
	req := httptest.NewRequest("GET", path, nil)
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	body, _ := io.ReadAll(rr.Result().Body)
	const m = `name="csrf_token" value="`
	i := strings.Index(string(body), m)
	if i < 0 {
		return ""
	}
	start := i + len(m)
	end := strings.Index(string(body[start:]), `"`)
	return string(body[start : start+end])
}

func TestAccountForm_AnonymousRedirected(t *testing.T) {
	handler, _ := newRoutedAuthGORM(t)
	req := httptest.NewRequest("GET", "/account/me", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Errorf("GET /account/me anon status = %d, want 303", rr.Code)
	}
	if !strings.HasPrefix(rr.Header().Get("Location"), "/login") {
		t.Errorf("Location = %q, want /login...", rr.Header().Get("Location"))
	}
}

func TestAccountForm_MeRendersOwnUsername(t *testing.T) {
	handler, _ := newRoutedAuthGORM(t)
	cookie := loginVia(t, handler, "admin", "adminpass")

	req := httptest.NewRequest("GET", "/account/me", nil)
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /account/me logged in status = %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Change your password") {
		t.Errorf("self-service heading missing; got: %s", body)
	}
}

func TestAccountForm_AdminViewsOther(t *testing.T) {
	handler, ag := newRoutedAuthGORM(t)
	cookie := loginVia(t, handler, "admin", "adminpass")

	// Look up bob's ID.
	var bob UserGORM
	if err := ag.DB.Where("username = ?", "bob").First(&bob).Error; err != nil {
		t.Fatalf("lookup bob: %v", err)
	}
	path := "/account/" + itoa(int(bob.ID))
	req := httptest.NewRequest("GET", path, nil)
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin GET %s = %d", path, rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Change password for bob") {
		t.Errorf("admin-view heading missing 'bob'; got: %s", body)
	}
}

func TestAccountForm_NonAdminBlockedFromOther(t *testing.T) {
	handler, ag := newRoutedAuthGORM(t)
	cookie := loginVia(t, handler, "bob", "bobpass")

	var admin UserGORM
	if err := ag.DB.Where("username = ?", "admin").First(&admin).Error; err != nil {
		t.Fatal(err)
	}
	path := "/account/" + itoa(int(admin.ID))
	req := httptest.NewRequest("GET", path, nil)
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("bob GET %s status = %d, want 403", path, rr.Code)
	}
}

func TestAccountForm_UnknownIDIs404(t *testing.T) {
	handler, _ := newRoutedAuthGORM(t)
	cookie := loginVia(t, handler, "admin", "adminpass")
	req := httptest.NewRequest("GET", "/account/9999", nil)
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("/account/9999 status = %d, want 404", rr.Code)
	}
}

func TestAccountPost_SelfChangesOwnPassword(t *testing.T) {
	handler, ag := newRoutedAuthGORM(t)
	cookie := loginVia(t, handler, "admin", "adminpass")

	tok := csrfTokenFor(handler, "/account/me", cookie)
	if tok == "" {
		t.Fatal("no csrf token on /account/me")
	}
	var admin UserGORM
	_ = ag.DB.Where("username = ?", "admin").First(&admin).Error

	form := url.Values{
		"csrf_token":           {tok},
		"old_password":         {"adminpass"},
		"new_password":         {"newadminpass"},
		"new_password_confirm": {"newadminpass"},
	}
	req := httptest.NewRequest("POST", "/account/"+itoa(int(admin.ID)),
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST self = %d, body: %s", rr.Code, rr.Body.String())
	}
	if _, err := ag.Authenticate("admin", "newadminpass"); err != nil {
		t.Errorf("new password rejected after self-change: %v", err)
	}
	if _, err := ag.Authenticate("admin", "adminpass"); err == nil {
		t.Error("old password still works after self-change")
	}
}

func TestAccountPost_WrongCurrentPasswordRejected(t *testing.T) {
	handler, ag := newRoutedAuthGORM(t)
	cookie := loginVia(t, handler, "admin", "adminpass")
	tok := csrfTokenFor(handler, "/account/me", cookie)

	form := url.Values{
		"csrf_token":           {tok},
		"old_password":         {"WRONG"},
		"new_password":         {"shouldntstick"},
		"new_password_confirm": {"shouldntstick"},
	}
	req := httptest.NewRequest("POST", "/account/me", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		// We re-render with the error inline (200 with the form body).
		t.Errorf("POST wrong-old status = %d, want 200 with error", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "current password is incorrect") {
		t.Errorf("error message missing; got: %s", rr.Body.String())
	}
	if _, err := ag.Authenticate("admin", "shouldntstick"); err == nil {
		t.Error("password was changed despite wrong-old failure")
	}
}

func TestAccountPost_MismatchedNewPasswordsRejected(t *testing.T) {
	handler, _ := newRoutedAuthGORM(t)
	cookie := loginVia(t, handler, "admin", "adminpass")
	tok := csrfTokenFor(handler, "/account/me", cookie)

	form := url.Values{
		"csrf_token":           {tok},
		"old_password":         {"adminpass"},
		"new_password":         {"aaa"},
		"new_password_confirm": {"bbb"},
	}
	req := httptest.NewRequest("POST", "/account/me", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if !strings.Contains(rr.Body.String(), "do not match") {
		t.Errorf("mismatch error missing; got: %s", rr.Body.String())
	}
}

func TestAccountPost_AdminChangesOthersPassword(t *testing.T) {
	handler, ag := newRoutedAuthGORM(t)
	cookie := loginVia(t, handler, "admin", "adminpass")
	var bob UserGORM
	_ = ag.DB.Where("username = ?", "bob").First(&bob).Error
	bobPath := "/account/" + itoa(int(bob.ID))
	tok := csrfTokenFor(handler, bobPath, cookie)

	form := url.Values{
		"csrf_token":           {tok},
		"old_password":         {"adminpass"}, // admin's own
		"new_password":         {"bobnewpass"},
		"new_password_confirm": {"bobnewpass"},
	}
	req := httptest.NewRequest("POST", bobPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin POST bob status = %d", rr.Code)
	}
	if _, err := ag.Authenticate("bob", "bobnewpass"); err != nil {
		t.Errorf("bob's new password rejected after admin-change: %v", err)
	}
	if _, err := ag.Authenticate("bob", "bobpass"); err == nil {
		t.Error("bob's old password still works after admin-change")
	}
}

// TestAccountPost_HTMXModalSuccessClosesModal: the bug the user
// reported. Admin opens the password modal from the User table,
// submits — server should send HX-Trigger:closeModal + HX-Reswap:none
// so the modal closes and the admin stays on the table. No success
// body should be swapped into the modal.
func TestAccountPost_HTMXModalSuccessClosesModal(t *testing.T) {
	handler, ag := newRoutedAuthGORM(t)
	cookie := loginVia(t, handler, "admin", "adminpass")
	var bob UserGORM
	_ = ag.DB.Where("username = ?", "bob").First(&bob).Error
	bobPath := "/account/" + itoa(int(bob.ID))
	tok := csrfTokenFor(handler, bobPath, cookie)

	form := url.Values{
		"csrf_token":           {tok},
		"old_password":         {"adminpass"},
		"new_password":         {"newbobpass"},
		"new_password_confirm": {"newbobpass"},
	}
	req := httptest.NewRequest("POST", bobPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "users-modal-l1-body")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("HTMX modal POST status = %d", rr.Code)
	}
	if trig := rr.Header().Get("HX-Trigger"); !strings.Contains(trig, "closeModal") {
		t.Errorf("HX-Trigger should request closeModal; got %q", trig)
	}
	if reswap := rr.Header().Get("HX-Reswap"); reswap != "none" {
		t.Errorf("HX-Reswap = %q, want \"none\" (so no body swaps into the closed modal)", reswap)
	}
	if body := rr.Body.String(); strings.Contains(body, "Password changed.") {
		t.Errorf("modal success response must not embed a 'Password changed.' banner; got: %s", body)
	}
}

// TestAccountFormHasHTMXAttrsInModal: the form rendered for an HTMX
// request must carry hx-post / hx-target / hx-swap so submission
// stays in the modal instead of doing a browser navigation.
func TestAccountFormHasHTMXAttrsInModal(t *testing.T) {
	handler, _ := newRoutedAuthGORM(t)
	cookie := loginVia(t, handler, "admin", "adminpass")

	req := httptest.NewRequest("GET", "/account/me", nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "users-modal-l1-body")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, `hx-post="/account/`) {
		t.Errorf("modal form missing hx-post: %s", body)
	}
	if !strings.Contains(body, `hx-target="#users-modal-l1-body"`) {
		t.Errorf("modal form missing matching hx-target: %s", body)
	}
}

// TestAccountFormPlainSubmitOutsideModal: a plain GET (no HX-Request)
// must NOT add hx-post on the *password* form — that form submits via
// standard browser POST so the success page renders normally. The
// TOTP card legitimately has hx-post on its own buttons.
func TestAccountFormPlainSubmitOutsideModal(t *testing.T) {
	handler, _ := newRoutedAuthGORM(t)
	cookie := loginVia(t, handler, "admin", "adminpass")

	req := httptest.NewRequest("GET", "/account/me", nil)
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	body := rr.Body.String()
	// Find the password form (its action ends with /account/<id>).
	const marker = `action="/account/1"`
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("password form action not found: %s", body)
	}
	// Slice the password <form> tag (between the previous <form and
	// the next > after the marker) and check it has no hx-post.
	open := strings.LastIndex(body[:i], "<form")
	closeTag := strings.Index(body[i:], ">") + i
	formOpen := body[open : closeTag+1]
	if strings.Contains(formOpen, "hx-post") {
		t.Errorf("page-flow password form should not have hx-post: %s", formOpen)
	}
}

func TestAccountPost_AdminMustStillUseOwnCurrentPassword(t *testing.T) {
	handler, ag := newRoutedAuthGORM(t)
	cookie := loginVia(t, handler, "admin", "adminpass")
	var bob UserGORM
	_ = ag.DB.Where("username = ?", "bob").First(&bob).Error
	bobPath := "/account/" + itoa(int(bob.ID))
	tok := csrfTokenFor(handler, bobPath, cookie)

	// Admin types BOB'S password instead of their own — should fail
	// even though admin has the privilege to change bob's password.
	form := url.Values{
		"csrf_token":           {tok},
		"old_password":         {"bobpass"}, // bob's, not admin's
		"new_password":         {"hijacked"},
		"new_password_confirm": {"hijacked"},
	}
	req := httptest.NewRequest("POST", bobPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if !strings.Contains(rr.Body.String(), "current password is incorrect") {
		t.Errorf("expected re-auth failure; got: %s", rr.Body.String())
	}
	if _, err := ag.Authenticate("bob", "hijacked"); err == nil {
		t.Error("bob's password was changed despite admin re-auth failing")
	}
}

func TestAccountPost_NonAdminBlockedFromOther(t *testing.T) {
	handler, ag := newRoutedAuthGORM(t)
	cookie := loginVia(t, handler, "bob", "bobpass")
	var admin UserGORM
	_ = ag.DB.Where("username = ?", "admin").First(&admin).Error
	adminPath := "/account/" + itoa(int(admin.ID))

	// bob shouldn't even be able to GET admin's account page;
	// for completeness, also verify POST is 403.
	form := url.Values{
		"old_password":         {"bobpass"},
		"new_password":         {"hijacked"},
		"new_password_confirm": {"hijacked"},
	}
	req := httptest.NewRequest("POST", adminPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("bob POST admin status = %d, want 403", rr.Code)
	}
}

// itoa is a thin wrapper so the call sites read better.
func itoa(n int) string {
	// strconv would do it but its import sits in many places — keep
	// the cluster of integer formatting local to the test file.
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
