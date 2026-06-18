package auth

import (
	"context"
	"errors"
	"github.com/go-chi/chi/v5"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alexedwards/scs/v2"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newTestAuthGORM builds an AuthGORM bound to a fresh in-memory
// sqlite database, with the "admin" group seeded and an admin/secret
// user added to it.
func newTestAuthGORM(t *testing.T) (*AuthGORM, *scs.SessionManager) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm open: %v", err)
	}
	sm := scs.New()
	ag, err := NewAuthGORM(sm, db)
	if err != nil {
		t.Fatalf("NewAuthGORM: %v", err)
	}
	if err := ag.GroupAdd("admin"); err != nil {
		t.Fatalf("GroupAdd: %v", err)
	}
	if err := ag.UserAdd("admin", "admin@local", "secret"); err != nil {
		t.Fatalf("UserAdd: %v", err)
	}
	if err := ag.UserMod("admin", []string{"admin"}); err != nil {
		t.Fatalf("UserMod: %v", err)
	}
	return ag, sm
}

func TestAuthGORMUserAddDuplicateRejected(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	if err := ag.UserAdd("admin", "other@local", "x"); !errors.Is(err, ErrUserExists) {
		t.Errorf("UserAdd duplicate → %v, want ErrUserExists", err)
	}
}

func TestAuthGORMUserAddEmptyUsername(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	if err := ag.UserAdd("", "x@y", "x"); !errors.Is(err, ErrEmptyUsername) {
		t.Errorf("UserAdd empty username → %v, want ErrEmptyUsername", err)
	}
}

func TestAuthGORMUserDel(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	if err := ag.UserDel("nope"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("UserDel(missing) → %v, want ErrUserNotFound", err)
	}
	if err := ag.UserDel("admin"); err != nil {
		t.Fatalf("UserDel: %v", err)
	}
	if _, err := ag.Authenticate("admin", "secret"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("after UserDel, Authenticate → %v, want ErrUserNotFound", err)
	}
	// Group still exists (delete cleared the join, not the group).
	var g GroupGORM
	if err := ag.DB.Where("name = ?", "admin").First(&g).Error; err != nil {
		t.Errorf("UserDel collateral-deleted the group: %v", err)
	}
}

func TestAuthGORMPasswdRoundTrip(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	if err := ag.Passwd("admin", "newsecret"); err != nil {
		t.Fatalf("Passwd: %v", err)
	}
	if _, err := ag.Authenticate("admin", "secret"); !errors.Is(err, ErrInvalidPassword) {
		t.Errorf("old password after Passwd → %v, want ErrInvalidPassword", err)
	}
	if _, err := ag.Authenticate("admin", "newsecret"); err != nil {
		t.Errorf("new password rejected: %v", err)
	}
	if err := ag.Passwd("nope", "x"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("Passwd(missing) → %v, want ErrUserNotFound", err)
	}
}

func TestAuthGORMGroupAddDuplicateRejected(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	if err := ag.GroupAdd("admin"); !errors.Is(err, ErrGroupExists) {
		t.Errorf("GroupAdd duplicate → %v, want ErrGroupExists", err)
	}
}

func TestAuthGORMGroupAddEmptyName(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	if err := ag.GroupAdd(""); !errors.Is(err, ErrEmptyGroupName) {
		t.Errorf("GroupAdd empty name → %v, want ErrEmptyGroupName", err)
	}
}

func TestAuthGORMGroupDel(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	// Sanity: admin is in admin group.
	u, _ := ag.Authenticate("admin", "secret")
	if !u.HasGroup("admin") {
		t.Fatal("admin user not in admin group after seed")
	}
	if err := ag.GroupDel("nope"); !errors.Is(err, ErrGroupNotFound) {
		t.Errorf("GroupDel(missing) → %v, want ErrGroupNotFound", err)
	}
	if err := ag.GroupDel("admin"); err != nil {
		t.Fatalf("GroupDel: %v", err)
	}
	// User still exists; the association is gone.
	u, err := ag.Authenticate("admin", "secret")
	if err != nil {
		t.Fatalf("Authenticate after GroupDel: %v", err)
	}
	if u.HasGroup("admin") {
		t.Error("user still in admin group after GroupDel — join row leak")
	}
}

func TestAuthGORMUserModReplacesGroups(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	if err := ag.GroupAdd("editors"); err != nil {
		t.Fatal(err)
	}
	if err := ag.GroupAdd("viewers"); err != nil {
		t.Fatal(err)
	}

	// Replace admin's [admin] memberships with [editors, viewers].
	if err := ag.UserMod("admin", []string{"editors", "viewers"}); err != nil {
		t.Fatalf("UserMod: %v", err)
	}
	u, _ := ag.Authenticate("admin", "secret")
	if u.HasGroup("admin") {
		t.Error("UserMod didn't remove 'admin' membership")
	}
	if !u.HasGroup("editors") || !u.HasGroup("viewers") {
		t.Errorf("UserMod didn't add expected groups; got %v",
			groupNames(u.Groups()))
	}

	// Replace with [] → no memberships.
	if err := ag.UserMod("admin", []string{}); err != nil {
		t.Fatalf("UserMod empty: %v", err)
	}
	u, _ = ag.Authenticate("admin", "secret")
	if len(u.Groups()) != 0 {
		t.Errorf("UserMod([]) didn't clear: %v", groupNames(u.Groups()))
	}
}

func TestAuthGORMUserModMissingGroupRejected(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	err := ag.UserMod("admin", []string{"admin", "ghost"})
	if !errors.Is(err, ErrGroupNotFound) {
		t.Errorf("UserMod(unknown) → %v, want ErrGroupNotFound", err)
	}
	// Membership unchanged after the failed call.
	u, _ := ag.Authenticate("admin", "secret")
	if !u.HasGroup("admin") {
		t.Error("failed UserMod mutated memberships")
	}
}

func TestAuthGORMUserModMissingUser(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	err := ag.UserMod("ghost", []string{"admin"})
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("UserMod(missing user) → %v, want ErrUserNotFound", err)
	}
}

func TestAuthGORMAuthenticateWrongPassword(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	_, err := ag.Authenticate("admin", "wrong")
	if !errors.Is(err, ErrInvalidPassword) {
		t.Errorf("wrong password → %v, want ErrInvalidPassword", err)
	}
	if errors.Is(err, ErrUserNotFound) {
		t.Error("wrong-password and unknown-user should not share an error class")
	}
}

func TestAuthGORMDisabledUserHidden(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	// Disable the admin user directly through the DB.
	if err := ag.DB.Model(&UserGORM{}).
		Where("username = ?", "admin").
		Update("disabled", true).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ag.Authenticate("admin", "secret"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("disabled user → %v, want ErrUserNotFound", err)
	}
}

func TestAuthGORMUserGORMSatisfiesUser(t *testing.T) {
	var _ User = UserGORMAdapter{}
	var _ Group = GroupGORMAdapter{}

	ag, _ := newTestAuthGORM(t)
	u, err := ag.Authenticate("admin", "secret")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if u.Username() != "admin" {
		t.Errorf("Username = %q, want admin", u.Username())
	}
	if u.Email() != "admin@local" {
		t.Errorf("Email = %q, want admin@local", u.Email())
	}
	gs := u.Groups()
	if len(gs) != 1 || gs[0].Name() != "admin" {
		t.Errorf("Groups = %v, want [admin]", groupNames(gs))
	}

	// Type-assert back to the adapter so app-level Authz can reach
	// the raw row.
	adapter, ok := u.(UserGORMAdapter)
	if !ok {
		t.Fatalf("type-assert UserGORMAdapter failed: %T", u)
	}
	if adapter.U == nil || adapter.U.Username != "admin" {
		t.Errorf("adapter.U = %+v, want admin row", adapter.U)
	}
}

func TestAuthGORMCurrentUserAnonymous(t *testing.T) {
	ag, sm := newTestAuthGORM(t)
	withSession(t, sm, func(ctx context.Context, r *http.Request) {
		if u := ag.CurrentUser(r.Context()); u != nil {
			t.Errorf("CurrentUser anonymous = %v, want nil", u)
		}
	})
}

func TestAuthGORMCurrentUserAfterLogin(t *testing.T) {
	ag, sm := newTestAuthGORM(t)
	withSession(t, sm, func(ctx context.Context, r *http.Request) {
		u, err := ag.Authenticate("admin", "secret")
		if err != nil {
			t.Fatal(err)
		}
		if err := ag.Login(ctx, u); err != nil {
			t.Fatal(err)
		}
		got := ag.CurrentUser(r.Context())
		if got == nil || got.Username() != "admin" {
			t.Errorf("CurrentUser after Login = %v, want admin", got)
		}
	})
}

func TestAuthGORMCurrentUserAfterUserDel(t *testing.T) {
	ag, sm := newTestAuthGORM(t)
	withSession(t, sm, func(ctx context.Context, r *http.Request) {
		u, _ := ag.Authenticate("admin", "secret")
		_ = ag.Login(ctx, u)
		if err := ag.UserDel("admin"); err != nil {
			t.Fatal(err)
		}
		if got := ag.CurrentUser(r.Context()); got != nil {
			t.Errorf("CurrentUser after UserDel = %v, want nil", got)
		}
	})
}

func TestAuthGORMLogoutDestroysSession(t *testing.T) {
	ag, sm := newTestAuthGORM(t)
	withSession(t, sm, func(ctx context.Context, r *http.Request) {
		u, _ := ag.Authenticate("admin", "secret")
		_ = ag.Login(ctx, u)
		if ag.CurrentUser(r.Context()) == nil {
			t.Fatal("login didn't take effect")
		}
		if err := ag.Logout(ctx); err != nil {
			t.Fatal(err)
		}
		if ag.CurrentUser(r.Context()) != nil {
			t.Error("Logout didn't clear the session")
		}
	})
}

func TestAuthGORMNilConstructorArgs(t *testing.T) {
	if _, err := NewAuthGORM(nil, nil); err == nil {
		t.Error("NewAuthGORM(nil, nil) should error")
	}
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if _, err := NewAuthGORM(nil, db); err == nil {
		t.Error("NewAuthGORM(nil, db) should error")
	}
	if _, err := NewAuthGORM(scs.New(), nil); err == nil {
		t.Error("NewAuthGORM(sm, nil) should error")
	}
}

func TestAuthGORMRouteRegistersHandlers(t *testing.T) {
	ag, sm := newTestAuthGORM(t)
	mux := chi.NewRouter()
	if err := ag.RegisterRoutes(mux, "", nil); err != nil {
		t.Fatalf("RegisterRoutes: %v", err)
	}
	if ag.urlBase != "" {
		t.Errorf("urlBase = %q, want \"\"", ag.urlBase)
	}
	// GET /login should render (200) — wrap in LoadAndSave for the
	// session to populate.
	handler := sm.LoadAndSave(CSRFWrap(sm)(mux))
	req := httptest.NewRequest("GET", "/login", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("GET /login status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

// TestAuthGORMRegisterRoutesUnderPrefix verifies the relative-registration
// model: mounted under a stripping chi.Route("/auth"), the handlers serve at
// /auth/login etc., and the absolute helpers (LoginURL / IsAuthPath) reflect
// the mountBase so links and the page-shell gate stay correct behind a prefix.
func TestAuthGORMRegisterRoutesUnderPrefix(t *testing.T) {
	ag, sm := newTestAuthGORM(t)
	mux := chi.NewRouter()
	mux.Route("/auth", func(r chi.Router) {
		if err := ag.RegisterRoutes(r, "/auth", nil); err != nil {
			t.Fatalf("RegisterRoutes: %v", err)
		}
	})
	if got := ag.LoginURL(""); got != "/auth/login" {
		t.Errorf("LoginURL() = %q, want /auth/login", got)
	}
	if !ag.IsAuthPath("/auth/login") {
		t.Error("IsAuthPath(/auth/login) = false, want true")
	}
	handler := sm.LoadAndSave(CSRFWrap(sm)(mux))

	// The prefixed path serves; the unprefixed one 404s.
	for path, want := range map[string]int{
		"/auth/login": http.StatusOK,
		"/login":      http.StatusNotFound,
	} {
		req := httptest.NewRequest("GET", path, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Errorf("GET %s status = %d, want %d", path, rr.Code, want)
		}
	}
}

// groupNames pulls the name out of each Group for ergonomic test
// diagnostics.
func groupNames(gs []Group) []string {
	out := make([]string, len(gs))
	for i, g := range gs {
		out[i] = g.Name()
	}
	return out
}
