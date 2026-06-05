package auth

import (
	"context"
	"errors"
	"testing"
)

// fakeSSOProvider implements ssoProvider with a static identity +
// config. Test-only — the OIDC / OAuth2 wire paths are exercised
// against real libraries; what these tests verify is the user-
// mapping policy and group resolution.
type fakeSSOProvider struct {
	nameVal, displayVal string
	cfg                 ssoProviderConfig
	identity            ssoIdentity
}

func (f *fakeSSOProvider) name() string              { return f.nameVal }
func (f *fakeSSOProvider) displayName() string       { return f.displayVal }
func (f *fakeSSOProvider) config() ssoProviderConfig { return f.cfg }
func (f *fakeSSOProvider) authCodeURL(_, _, _ string) string {
	return "https://idp.test/authorize"
}
func (f *fakeSSOProvider) exchange(_ context.Context, _, _, _ string) (ssoIdentity, error) {
	return f.identity, nil
}

// ──────────────────────────────────────────────────────────────────
// resolveSSOGroups

func TestResolveSSOGroups_DefaultGroupsOnly(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	if err := ag.GroupAdd("editors"); err != nil {
		t.Fatalf("GroupAdd: %v", err)
	}
	p := &fakeSSOProvider{cfg: ssoProviderConfig{DefaultGroups: []string{"editors"}}}
	groups, err := ag.resolveSSOGroups(nil, p)
	if err != nil {
		t.Fatalf("resolveSSOGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].Name != "editors" {
		t.Errorf("got %+v, want [editors]", groups)
	}
}

func TestResolveSSOGroups_MissingGroupSkippedWhenNoCreate(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	p := &fakeSSOProvider{cfg: ssoProviderConfig{DefaultGroups: []string{"editors"}}}
	groups, err := ag.resolveSSOGroups(nil, p)
	if err != nil {
		t.Fatalf("resolveSSOGroups: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("unknown group not skipped: %+v", groups)
	}
}

func TestResolveSSOGroups_AutoCreateWhenEnabled(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	p := &fakeSSOProvider{cfg: ssoProviderConfig{
		DefaultGroups: []string{"editors"},
		CreateGroups:  true,
	}}
	groups, err := ag.resolveSSOGroups(nil, p)
	if err != nil {
		t.Fatalf("resolveSSOGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].Name != "editors" {
		t.Errorf("got %+v, want [editors]", groups)
	}
	// Was actually persisted?
	var g GroupGORM
	if err := ag.DB.Where("name = ?", "editors").First(&g).Error; err != nil {
		t.Errorf("group not persisted: %v", err)
	}
}

func TestResolveSSOGroups_LayeredFromClaimAndMapper(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	for _, n := range []string{"editors", "reviewers", "owners"} {
		if err := ag.GroupAdd(n); err != nil {
			t.Fatalf("GroupAdd %q: %v", n, err)
		}
	}
	p := &fakeSSOProvider{cfg: ssoProviderConfig{
		DefaultGroups: []string{"editors"},
		GroupsClaim:   "groups",
		GroupMapper: func(claims map[string]any) []string {
			return []string{"owners"}
		},
	}}
	claims := map[string]any{
		"groups": []any{"editors", "reviewers"}, // dupes with default; reviewers is new
	}
	groups, err := ag.resolveSSOGroups(claims, p)
	if err != nil {
		t.Fatalf("resolveSSOGroups: %v", err)
	}
	names := make(map[string]bool, len(groups))
	for _, g := range groups {
		names[g.Name] = true
	}
	for _, want := range []string{"editors", "reviewers", "owners"} {
		if !names[want] {
			t.Errorf("missing %q in result %v", want, names)
		}
	}
	if len(groups) != 3 {
		t.Errorf("expected 3 groups (dedupe), got %d: %v", len(groups), groups)
	}
}

// ──────────────────────────────────────────────────────────────────
// coerceStringSlice — handles the JSON-decoded shapes that claims
// might arrive in.

func TestCoerceStringSlice(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want []string
	}{
		{"nil", nil, nil},
		{"[]string", []string{"a", "b"}, []string{"a", "b"}},
		{"[]any of strings", []any{"a", "b"}, []string{"a", "b"}},
		{"[]any with non-string", []any{"a", 42, "b"}, []string{"a", "b"}},
		{"single string", "alone", []string{"alone"}},
		{"unrecognised", 42, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := coerceStringSlice(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("len got=%d want=%d (%v)", len(got), len(c.want), got)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("[%d] got=%q want=%q", i, got[i], c.want[i])
				}
			}
		})
	}
}

// ──────────────────────────────────────────────────────────────────
// resolveSSOLogin — the policy decision branches.

func newSSOProvider(name string, cfg ssoProviderConfig, id ssoIdentity) *fakeSSOProvider {
	id.Provider = name
	return &fakeSSOProvider{nameVal: name, displayVal: name, cfg: cfg, identity: id}
}

func TestResolveSSOLogin_ExistingIdentity(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	// Seed a user + linked identity.
	if err := ag.UserAdd("alice", "alice@example.com", "x"); err != nil {
		t.Fatalf("UserAdd: %v", err)
	}
	var alice UserGORM
	if err := ag.DB.Where("username = ?", "alice").First(&alice).Error; err != nil {
		t.Fatal(err)
	}
	link := SSOIdentityGORM{UserID: alice.ID, Provider: "google", Subject: "sub-123"}
	if err := ag.DB.Create(&link).Error; err != nil {
		t.Fatal(err)
	}
	p := newSSOProvider("google", ssoProviderConfig{}, ssoIdentity{
		Subject: "sub-123", Email: "alice@example.com", DisplayName: "Alice",
	})
	got, err := ag.resolveSSOLogin(context.Background(), p.identity, p)
	if err != nil {
		t.Fatalf("resolveSSOLogin: %v", err)
	}
	if got.Username != "alice" {
		t.Errorf("got username %q, want alice", got.Username)
	}
}

func TestResolveSSOLogin_DisabledUserRejected(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	if err := ag.UserAdd("alice", "alice@example.com", "x"); err != nil {
		t.Fatal(err)
	}
	if err := ag.DB.Model(&UserGORM{}).Where("username = ?", "alice").Update("disabled", true).Error; err != nil {
		t.Fatal(err)
	}
	var alice UserGORM
	if err := ag.DB.Where("username = ?", "alice").First(&alice).Error; err != nil {
		t.Fatal(err)
	}
	if err := ag.DB.Create(&SSOIdentityGORM{UserID: alice.ID, Provider: "google", Subject: "x"}).Error; err != nil {
		t.Fatal(err)
	}
	p := newSSOProvider("google", ssoProviderConfig{}, ssoIdentity{Subject: "x", Email: "alice@example.com"})
	_, err := ag.resolveSSOLogin(context.Background(), p.identity, p)
	if !errors.Is(err, ErrSSONoAccount) {
		t.Errorf("disabled user → %v, want ErrSSONoAccount", err)
	}
}

func TestResolveSSOLogin_AutoLinkByEmail(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	if err := ag.UserAdd("alice", "alice@example.com", "x"); err != nil {
		t.Fatal(err)
	}
	p := newSSOProvider("okta", ssoProviderConfig{AutoLinkByEmail: true}, ssoIdentity{
		Subject: "sub-99", Email: "alice@example.com", DisplayName: "Alice",
	})
	user, err := ag.resolveSSOLogin(context.Background(), p.identity, p)
	if err != nil {
		t.Fatalf("resolveSSOLogin: %v", err)
	}
	if user.Username != "alice" {
		t.Errorf("got %q, want alice", user.Username)
	}
	// Identity link created?
	var link SSOIdentityGORM
	if err := ag.DB.Where("provider = ? AND subject = ?", "okta", "sub-99").First(&link).Error; err != nil {
		t.Errorf("identity not linked: %v", err)
	}
	if link.UserID != user.ID {
		t.Errorf("link UserID=%d, want %d", link.UserID, user.ID)
	}
}

func TestResolveSSOLogin_AutoLinkOffRejects(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	if err := ag.UserAdd("alice", "alice@example.com", "x"); err != nil {
		t.Fatal(err)
	}
	p := newSSOProvider("google", ssoProviderConfig{AutoLinkByEmail: false, DisableAutoCreate: true}, ssoIdentity{
		Subject: "sub-99", Email: "alice@example.com",
	})
	_, err := ag.resolveSSOLogin(context.Background(), p.identity, p)
	if !errors.Is(err, ErrSSONoAccount) {
		t.Errorf("no auto-link + no auto-create → %v, want ErrSSONoAccount", err)
	}
}

func TestResolveSSOLogin_AutoCreate(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	if err := ag.GroupAdd("readers"); err != nil {
		t.Fatal(err)
	}
	p := newSSOProvider("google", ssoProviderConfig{
		DefaultGroups: []string{"readers"},
	}, ssoIdentity{Subject: "sub-1", Email: "bob@example.com", DisplayName: "Bob"})
	user, err := ag.resolveSSOLogin(context.Background(), p.identity, p)
	if err != nil {
		t.Fatalf("resolveSSOLogin: %v", err)
	}
	if user.Username != "bob@example.com" {
		t.Errorf("got %q, want bob@example.com", user.Username)
	}
	if !user.SSOOnly {
		t.Error("auto-created user is not SSOOnly")
	}
	if user.PasswordHash != "" {
		t.Error("auto-created user has a password hash")
	}
	// Reload with groups to verify membership (Create with embedded
	// Groups doesn't necessarily populate the m2m on the in-memory
	// struct after the insert).
	var fresh UserGORM
	if err := ag.DB.Preload("Groups").First(&fresh, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if len(fresh.Groups) != 1 || fresh.Groups[0].Name != "readers" {
		t.Errorf("groups = %+v, want [readers]", fresh.Groups)
	}
}

func TestResolveSSOLogin_DisableAutoCreateRejects(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	p := newSSOProvider("google", ssoProviderConfig{DisableAutoCreate: true}, ssoIdentity{
		Subject: "sub-1", Email: "bob@example.com",
	})
	_, err := ag.resolveSSOLogin(context.Background(), p.identity, p)
	if !errors.Is(err, ErrSSONoAccount) {
		t.Errorf("DisableAutoCreate → %v, want ErrSSONoAccount", err)
	}
}

func TestResolveSSOLogin_AutoCreateNoEmailRejects(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	p := newSSOProvider("github", ssoProviderConfig{}, ssoIdentity{Subject: "id-42"})
	_, err := ag.resolveSSOLogin(context.Background(), p.identity, p)
	if !errors.Is(err, ErrSSONoAccount) {
		t.Errorf("auto-create with no email → %v, want ErrSSONoAccount", err)
	}
}

// ──────────────────────────────────────────────────────────────────
// Provider registration.

func TestAddSSOProvider_DuplicateNameRejected(t *testing.T) {
	ag, _ := newTestAuthGORM(t)
	a := OAuth2Provider{
		Name: "x", AuthURL: "https://a", TokenURL: "https://b",
		ClientID: "c", RedirectURL: "https://r",
		UserInfo: func(context.Context, string) (ssoIdentity, error) { return ssoIdentity{}, nil },
	}
	if err := ag.AddOAuth2Provider(a); err != nil {
		t.Fatalf("first AddOAuth2Provider: %v", err)
	}
	b := a
	if err := ag.AddOAuth2Provider(b); !errors.Is(err, ErrSSOProviderExists) {
		t.Errorf("duplicate → %v, want ErrSSOProviderExists", err)
	}
}
