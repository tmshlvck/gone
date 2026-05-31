package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubAuth is a tiny Auth implementation for the authz tests — it
// returns the supplied user on every CurrentUser call. Saves the
// authz tests from spinning up scs + AuthSimple just to test
// "user is logged in" / "user is in admin group" branches.
type stubAuth struct{ user User }

func (s stubAuth) Route(Mux, string, PageShellFunc) (string, error) { return "", nil }
func (s stubAuth) CurrentUser(*http.Request) User                   { return s.user }
func (s stubAuth) LoginURL(string) string                           { return "/login" }
func (s stubAuth) LogoutURL(string) string                          { return "/logout" }
func (s stubAuth) Login(context.Context, User) error                { return nil }
func (s stubAuth) Logout(context.Context) error                     { return nil }

// stubUser is a minimal User; groups are a string slice converted to
// stubGroup on demand.
type stubUser struct {
	name   string
	groups []string
}

func (u stubUser) Username() string { return u.name }
func (u stubUser) Email() string    { return "" }
func (u stubUser) Groups() []Group {
	out := make([]Group, len(u.groups))
	for i, g := range u.groups {
		out[i] = stubGroup{name: g}
	}
	return out
}
func (u stubUser) HasGroup(name string) bool {
	for _, g := range u.groups {
		if g == name {
			return true
		}
	}
	return false
}

type stubGroup struct{ name string }

func (g stubGroup) Name() string { return g.name }

func TestAuthzAllowAll(t *testing.T) {
	a := AuthzAllowAll{}
	r := httptest.NewRequest("GET", "/", nil)
	for _, can := range []func() bool{
		func() bool { return a.CanList(r) },
		func() bool { return a.CanRead(r) },
		func() bool { return a.CanCreate(r) },
		func() bool { return a.CanUpdate(r) },
		func() bool { return a.CanDelete(r) },
	} {
		if !can() {
			t.Error("AuthzAllowAll should permit every action")
		}
	}
}

func TestAuthzDenyAll(t *testing.T) {
	a := AuthzDenyAll{}
	r := httptest.NewRequest("GET", "/", nil)
	for _, can := range []func() bool{
		func() bool { return a.CanList(r) },
		func() bool { return a.CanRead(r) },
		func() bool { return a.CanCreate(r) },
		func() bool { return a.CanUpdate(r) },
		func() bool { return a.CanDelete(r) },
	} {
		if can() {
			t.Error("AuthzDenyAll should reject every action")
		}
	}
}

func TestAuthzOrAllow(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	// nil → AuthzAllowAll
	if !AuthzOrAllow(nil).CanRead(r) {
		t.Error("AuthzOrAllow(nil) should permit")
	}
	// non-nil pass-through
	if AuthzOrAllow(AuthzDenyAll{}).CanRead(r) {
		t.Error("AuthzOrAllow(AuthzDenyAll) should pass AuthzDenyAll through")
	}
}

// allCanMethods returns one closure per Authz method so tests can
// assert the same expectation across all five at once.
func allCanMethods(a Authz, r *http.Request) []func() bool {
	return []func() bool{
		func() bool { return a.CanList(r) },
		func() bool { return a.CanRead(r) },
		func() bool { return a.CanCreate(r) },
		func() bool { return a.CanUpdate(r) },
		func() bool { return a.CanDelete(r) },
	}
}

func TestAuthzLoggedInAnonymous(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	a := AuthzLoggedIn{Auth: stubAuth{user: nil}}
	for i, can := range allCanMethods(a, r) {
		if can() {
			t.Errorf("AuthzLoggedIn[%d] permitted anonymous", i)
		}
	}
}

func TestAuthzLoggedInAuthenticated(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	a := AuthzLoggedIn{Auth: stubAuth{user: stubUser{name: "alice"}}}
	for i, can := range allCanMethods(a, r) {
		if !can() {
			t.Errorf("AuthzLoggedIn[%d] denied authenticated", i)
		}
	}
}

func TestAuthzLoggedInNilAuth(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	a := AuthzLoggedIn{Auth: nil}
	for i, can := range allCanMethods(a, r) {
		if can() {
			t.Errorf("AuthzLoggedIn[%d] with nil Auth permitted access (should fail closed)", i)
		}
	}
}

func TestAuthzLoggedInReadOnlyAuthenticated(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	a := AuthzLoggedInReadOnly{Auth: stubAuth{user: stubUser{name: "alice"}}}
	if !a.CanList(r) || !a.CanRead(r) {
		t.Error("AuthzLoggedInReadOnly denied reads to logged-in user")
	}
	if a.CanCreate(r) || a.CanUpdate(r) || a.CanDelete(r) {
		t.Error("AuthzLoggedInReadOnly permitted writes to logged-in user")
	}
}

func TestAuthzLoggedInReadOnlyAnonymous(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	a := AuthzLoggedInReadOnly{Auth: stubAuth{user: nil}}
	for i, can := range allCanMethods(a, r) {
		if can() {
			t.Errorf("AuthzLoggedInReadOnly[%d] permitted anonymous", i)
		}
	}
}

func TestAuthzLoggedInReadAdminWriteDefault(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	// Default AdminGroup ("") → "admin".
	a := AuthzLoggedInReadAdminWrite{Auth: stubAuth{user: stubUser{name: "alice", groups: []string{"admin"}}}}
	for i, can := range allCanMethods(a, r) {
		if !can() {
			t.Errorf("AuthzLoggedInReadAdminWrite[%d] denied admin user", i)
		}
	}
}

func TestAuthzLoggedInReadAdminWriteNonAdmin(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	a := AuthzLoggedInReadAdminWrite{Auth: stubAuth{user: stubUser{name: "bob", groups: []string{"viewer"}}}}
	// Reads pass — viewer is logged in.
	if !a.CanList(r) || !a.CanRead(r) {
		t.Error("AuthzLoggedInReadAdminWrite denied reads to logged-in non-admin")
	}
	// Writes denied — not in admin group.
	if a.CanCreate(r) || a.CanUpdate(r) || a.CanDelete(r) {
		t.Error("AuthzLoggedInReadAdminWrite permitted writes to non-admin")
	}
}

func TestAuthzLoggedInReadAdminWriteAnonymous(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	a := AuthzLoggedInReadAdminWrite{Auth: stubAuth{user: nil}}
	for i, can := range allCanMethods(a, r) {
		if can() {
			t.Errorf("AuthzLoggedInReadAdminWrite[%d] permitted anonymous", i)
		}
	}
}

func TestAuthzLoggedInReadAdminWriteCustomGroup(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	a := AuthzLoggedInReadAdminWrite{
		Auth:       stubAuth{user: stubUser{name: "editor", groups: []string{"editors"}}},
		AdminGroup: "editors",
	}
	for i, can := range allCanMethods(a, r) {
		if !can() {
			t.Errorf("AuthzLoggedInReadAdminWrite[%d] (AdminGroup=editors) denied editor", i)
		}
	}
	// Same user without the editors group → reads-only.
	a.Auth = stubAuth{user: stubUser{name: "viewer", groups: []string{"admin"}}}
	if a.CanCreate(r) || a.CanUpdate(r) || a.CanDelete(r) {
		t.Error("AuthzLoggedInReadAdminWrite (AdminGroup=editors) treated 'admin' as admin")
	}
	if !a.CanRead(r) || !a.CanList(r) {
		t.Error("AuthzLoggedInReadAdminWrite (AdminGroup=editors) denied reads to logged-in user")
	}
}

func TestAuthzLoggedInReadAdminWriteNilAuth(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	a := AuthzLoggedInReadAdminWrite{Auth: nil}
	for i, can := range allCanMethods(a, r) {
		if can() {
			t.Errorf("AuthzLoggedInReadAdminWrite[%d] with nil Auth permitted access", i)
		}
	}
}
