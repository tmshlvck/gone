package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"gorm.io/gorm"
)

// ──────────────────────────────────────────────────────────────────
// Schema.

// SSOIdentityGORM links one provider+subject tuple to one UserGORM.
// A user may have many (one Google, one GitHub, one corporate Okta
// — all the same person). The unique index on (Provider, Subject)
// guarantees an IdP-issued identity maps to at most one local user.
//
// Email + DisplayName are snapshots taken at link time / refreshed
// on every successful sign-in; convenient for the account page's
// "Linked accounts" list without re-querying the provider.
type SSOIdentityGORM struct {
	ID          uint   `gorm:"primaryKey"`
	UserID      uint   `gorm:"index;not null"`
	Provider    string `gorm:"size:64;uniqueIndex:idx_sso_provider_subject"`
	Subject     string `gorm:"size:255;uniqueIndex:idx_sso_provider_subject"`
	Email       string `gorm:"size:255"`
	DisplayName string `gorm:"size:255"`
	CreatedAt   time.Time
	LastUsedAt  time.Time
}

func (SSOIdentityGORM) TableName() string { return "auth_sso_identities" }

// ──────────────────────────────────────────────────────────────────
// Errors.

// ErrSSONoAccount: the callback identified a user the policy refused
// to map to a local account (no existing identity link, no allowed
// email auto-link, auto-create disabled, etc.). Surfaced to the
// caller as a 403; not leaked as a redirect.
var ErrSSONoAccount = errors.New("auth: no account matches SSO identity")

// ErrSSOProviderExists: AddOIDCProvider / AddOAuth2Provider called
// twice with the same Name.
var ErrSSOProviderExists = errors.New("auth: SSO provider name already registered")

// ErrSSOProviderUnknown: callback handler received a {name} segment
// that doesn't match any registered provider.
var ErrSSOProviderUnknown = errors.New("auth: SSO provider not registered")

// ──────────────────────────────────────────────────────────────────
// Internal interface + identity payload.

// ssoIdentity is the provider-agnostic representation of "who the
// user is" — produced by Exchange, consumed by the user-mapping
// policy. Claims carries the full ID-token / UserInfo payload so a
// GroupMapper hook can pull arbitrary fields without the library
// hard-coding which ones.
type ssoIdentity struct {
	Provider    string
	Subject     string // stable IdP-side ID
	Email       string
	DisplayName string
	Claims      map[string]any // raw claims for hooks; may be nil
}

// ssoProviderConfig holds the policy fields shared by every provider
// type. Embedded in OIDCProvider and OAuth2Provider; surfaced via
// the internal ssoProvider.config() so resolveSSOLogin can read it
// uniformly.
type ssoProviderConfig struct {
	// DefaultGroups: every user provisioned via this provider gets
	// added to these groups on first login. Groups must already exist
	// or CreateGroups must be true.
	DefaultGroups []string

	// GroupsClaim: name of a claim in the ID token (OIDC) or UserInfo
	// payload (OAuth2) that holds an array of group names. Empty =
	// ignore claims. Typical: "groups" for Keycloak/Okta.
	GroupsClaim string

	// CreateGroups: if true, group names mentioned by the IdP but not
	// in the DB are auto-created. If false they're silently skipped
	// (logged at info level). Default false — admins keep control of
	// what groups exist.
	CreateGroups bool

	// GroupMapper: optional hook for full custom logic. Receives the
	// raw claims map, returns group names to add. Composes with
	// DefaultGroups + GroupsClaim (result is the union, deduped).
	GroupMapper func(claims map[string]any) []string

	// AutoLinkByEmail: when an SSO callback returns an email matching
	// an existing local user (with no prior identity link), should
	// the library auto-create the link?
	//   false (default) → reject with ErrSSONoAccount. Admin must
	//                     manually link via UserMod / manual DB write.
	//   true            → create the link, log the user in.
	// Only flip on for IdPs whose email verification you trust
	// (corporate Okta, your own on-prem Keycloak). Public providers
	// (Google, generic GitHub) leave off — otherwise anyone able to
	// get an ID token claiming alice@example.com can take over the
	// local alice@example.com account.
	AutoLinkByEmail bool

	// DisableAutoCreate: by default the first SSO login from an
	// unknown identity creates a new local UserGORM (SSOOnly=true).
	// Set this to true to require admin pre-provisioning instead.
	DisableAutoCreate bool
}

// ssoProvider is the runtime interface every concrete provider
// satisfies. Kept unexported — apps configure providers by filling
// the OIDCProvider / OAuth2Provider structs and calling AddOIDCProvider
// / AddOAuth2Provider, which converts them into this internal form.
type ssoProvider interface {
	name() string
	displayName() string
	authCodeURL(state, nonce, pkceVerifier string) string
	exchange(ctx context.Context, code, pkceVerifier, expectedNonce string) (ssoIdentity, error)
	config() ssoProviderConfig
}

// ──────────────────────────────────────────────────────────────────
// OIDCProvider — for OIDC-compliant IdPs (Google, Okta, Keycloak,
// Authentik, Dex, …). Performs ID-token verification + nonce check.

// OIDCProvider configures one OpenID Connect provider. Fields up to
// the // group/policy comment are wire config; the rest are policy
// (ssoProviderConfig).
type OIDCProvider struct {
	// Name is the URL segment under /login/sso/{name} and the
	// SSOIdentityGORM.Provider column. Must be unique across all
	// providers registered on one AuthGORM. Lowercase recommended.
	Name string
	// DisplayName is the label on the "Sign in with X" button.
	DisplayName string
	// IssuerURL is the OIDC discovery base, e.g.
	// "https://accounts.google.com". The library fetches
	// {IssuerURL}/.well-known/openid-configuration during
	// AddOIDCProvider.
	IssuerURL string
	// ClientID + ClientSecret are the OAuth client credentials
	// registered with the IdP.
	ClientID     string
	ClientSecret string
	// RedirectURL is the absolute callback URL registered with the
	// IdP. Must equal {base}/login/sso/{Name}/callback for the
	// caller's deployed public URL. The library can't compute this
	// itself — it doesn't know the public origin.
	RedirectURL string
	// Scopes default to ["openid", "email", "profile"] when empty.
	// Add provider-specific scopes (e.g. "groups" on some Okta
	// configs) here.
	Scopes []string

	// Policy. See ssoProviderConfig for field docs.
	DefaultGroups     []string
	GroupsClaim       string
	CreateGroups      bool
	GroupMapper       func(claims map[string]any) []string
	AutoLinkByEmail   bool
	DisableAutoCreate bool

	// Internal — filled by initOIDC.
	oauth2Cfg *oauth2.Config
	verifier  *oidc.IDTokenVerifier
}

// GoogleProvider is OIDCProvider preset for accounts.google.com.
// Caller supplies their OAuth client credentials and redirect URL.
// Google does not emit a groups claim; configure DefaultGroups (or
// a GroupMapper that maps the hd "hosted domain" claim) post-hoc.
func GoogleProvider(clientID, clientSecret, redirectURL string) OIDCProvider {
	return OIDCProvider{
		Name:         "google",
		DisplayName:  "Google",
		IssuerURL:    "https://accounts.google.com",
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
	}
}

// OktaProvider is OIDCProvider preset for an Okta tenant. `domain`
// is the bare host like "dev-12345.okta.com" — the library prepends
// https:// for IssuerURL. To read Okta's groups claim set
// GroupsClaim="groups" on the returned struct (and configure your
// Okta authorization server to emit one).
func OktaProvider(domain, clientID, clientSecret, redirectURL string) OIDCProvider {
	return OIDCProvider{
		Name:         "okta",
		DisplayName:  "Okta",
		IssuerURL:    "https://" + strings.TrimPrefix(domain, "https://"),
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
	}
}

func (p *OIDCProvider) name() string        { return p.Name }
func (p *OIDCProvider) displayName() string { return p.DisplayName }

func (p *OIDCProvider) config() ssoProviderConfig {
	return ssoProviderConfig{
		DefaultGroups:     p.DefaultGroups,
		GroupsClaim:       p.GroupsClaim,
		CreateGroups:      p.CreateGroups,
		GroupMapper:       p.GroupMapper,
		AutoLinkByEmail:   p.AutoLinkByEmail,
		DisableAutoCreate: p.DisableAutoCreate,
	}
}

func (p *OIDCProvider) authCodeURL(state, nonce, pkceVerifier string) string {
	return p.oauth2Cfg.AuthCodeURL(
		state,
		oidc.Nonce(nonce),
		oauth2.S256ChallengeOption(pkceVerifier),
	)
}

func (p *OIDCProvider) exchange(ctx context.Context, code, pkceVerifier, expectedNonce string) (ssoIdentity, error) {
	token, err := p.oauth2Cfg.Exchange(ctx, code, oauth2.VerifierOption(pkceVerifier))
	if err != nil {
		return ssoIdentity{}, fmt.Errorf("oidc exchange: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return ssoIdentity{}, errors.New("oidc exchange: response missing id_token")
	}
	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return ssoIdentity{}, fmt.Errorf("oidc verify: %w", err)
	}
	if idToken.Nonce != expectedNonce {
		return ssoIdentity{}, errors.New("oidc verify: nonce mismatch")
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return ssoIdentity{}, fmt.Errorf("oidc claims: %w", err)
	}
	return ssoIdentity{
		Provider:    p.Name,
		Subject:     idToken.Subject,
		Email:       stringClaim(claims, "email"),
		DisplayName: firstNonEmpty(stringClaim(claims, "name"), stringClaim(claims, "preferred_username")),
		Claims:      claims,
	}, nil
}

// initOIDC runs discovery + builds the oauth2 + verifier. Called
// from AddOIDCProvider. Idempotent: returns the same provider value
// each call (within a single AuthGORM, AddOIDCProvider blocks
// duplicates by Name).
func (p *OIDCProvider) initOIDC(ctx context.Context) error {
	if p.Name == "" || p.IssuerURL == "" || p.ClientID == "" || p.RedirectURL == "" {
		return errors.New("OIDCProvider: Name, IssuerURL, ClientID, RedirectURL required")
	}
	prov, err := oidc.NewProvider(ctx, p.IssuerURL)
	if err != nil {
		return fmt.Errorf("oidc discovery %q: %w", p.IssuerURL, err)
	}
	scopes := p.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "email", "profile"}
	}
	p.oauth2Cfg = &oauth2.Config{
		ClientID:     p.ClientID,
		ClientSecret: p.ClientSecret,
		Endpoint:     prov.Endpoint(),
		RedirectURL:  p.RedirectURL,
		Scopes:       scopes,
	}
	p.verifier = prov.Verifier(&oidc.Config{ClientID: p.ClientID})
	return nil
}

// ──────────────────────────────────────────────────────────────────
// OAuth2Provider — for non-OIDC OAuth2 IdPs (GitHub today, custom
// providers in future). Caller supplies AuthURL / TokenURL + a
// UserInfo func that turns an access token into an ssoIdentity.

// OAuth2Provider configures one OAuth2-only provider. Used when the
// IdP doesn't speak OIDC (notably GitHub). No ID token verification
// — the access token + provider-specific REST call is what carries
// identity.
type OAuth2Provider struct {
	Name        string
	DisplayName string

	AuthURL  string // e.g. "https://github.com/login/oauth/authorize"
	TokenURL string // e.g. "https://github.com/login/oauth/access_token"

	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string // caller supplies — no sensible default across providers

	// UserInfo is REQUIRED. The library calls it with the freshly-
	// exchanged access token; the func is responsible for the
	// provider-specific REST fetch that yields (subject, email,
	// display name, claims). Set Subject / Email / DisplayName on
	// the returned struct — Provider is overwritten by the caller.
	UserInfo func(ctx context.Context, accessToken string) (ssoIdentity, error)

	// Policy.
	DefaultGroups     []string
	GroupsClaim       string
	CreateGroups      bool
	GroupMapper       func(claims map[string]any) []string
	AutoLinkByEmail   bool
	DisableAutoCreate bool

	// Internal.
	oauth2Cfg *oauth2.Config
}

func (p *OAuth2Provider) name() string        { return p.Name }
func (p *OAuth2Provider) displayName() string { return p.DisplayName }

func (p *OAuth2Provider) config() ssoProviderConfig {
	return ssoProviderConfig{
		DefaultGroups:     p.DefaultGroups,
		GroupsClaim:       p.GroupsClaim,
		CreateGroups:      p.CreateGroups,
		GroupMapper:       p.GroupMapper,
		AutoLinkByEmail:   p.AutoLinkByEmail,
		DisableAutoCreate: p.DisableAutoCreate,
	}
}

func (p *OAuth2Provider) authCodeURL(state, _nonce, pkceVerifier string) string {
	// OAuth2-only — no nonce param. PKCE still applied; harmless if
	// the IdP ignores it.
	return p.oauth2Cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(pkceVerifier))
}

func (p *OAuth2Provider) exchange(ctx context.Context, code, pkceVerifier, _expectedNonce string) (ssoIdentity, error) {
	token, err := p.oauth2Cfg.Exchange(ctx, code, oauth2.VerifierOption(pkceVerifier))
	if err != nil {
		return ssoIdentity{}, fmt.Errorf("oauth2 exchange: %w", err)
	}
	id, err := p.UserInfo(ctx, token.AccessToken)
	if err != nil {
		return ssoIdentity{}, fmt.Errorf("oauth2 userinfo: %w", err)
	}
	id.Provider = p.Name
	return id, nil
}

func (p *OAuth2Provider) initOAuth2() error {
	if p.Name == "" || p.AuthURL == "" || p.TokenURL == "" || p.ClientID == "" || p.RedirectURL == "" || p.UserInfo == nil {
		return errors.New("OAuth2Provider: Name, AuthURL, TokenURL, ClientID, RedirectURL, UserInfo required")
	}
	p.oauth2Cfg = &oauth2.Config{
		ClientID:     p.ClientID,
		ClientSecret: p.ClientSecret,
		Endpoint:     oauth2.Endpoint{AuthURL: p.AuthURL, TokenURL: p.TokenURL},
		RedirectURL:  p.RedirectURL,
		Scopes:       p.Scopes,
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────
// GitHub preset.

// GitHubProvider is the OAuth2Provider preset for github.com. Pulls
// /user for id+login+name+email, falls back to /user/emails for the
// verified primary email when /user returns no public email.
func GitHubProvider(clientID, clientSecret, redirectURL string) OAuth2Provider {
	return OAuth2Provider{
		Name:         "github",
		DisplayName:  "GitHub",
		AuthURL:      "https://github.com/login/oauth/authorize",
		TokenURL:     "https://github.com/login/oauth/access_token",
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Scopes:       []string{"read:user", "user:email"},
		UserInfo:     fetchGitHubUser,
	}
}

func fetchGitHubUser(ctx context.Context, accessToken string) (ssoIdentity, error) {
	var u struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := getJSONBearer(ctx, "https://api.github.com/user", accessToken, &u); err != nil {
		return ssoIdentity{}, fmt.Errorf("GET /user: %w", err)
	}
	email := u.Email
	if email == "" {
		var emails []struct {
			Email    string `json:"email"`
			Primary  bool   `json:"primary"`
			Verified bool   `json:"verified"`
		}
		if err := getJSONBearer(ctx, "https://api.github.com/user/emails", accessToken, &emails); err != nil {
			return ssoIdentity{}, fmt.Errorf("GET /user/emails: %w", err)
		}
		for _, e := range emails {
			if e.Primary && e.Verified {
				email = e.Email
				break
			}
		}
	}
	name := u.Name
	if name == "" {
		name = u.Login
	}
	return ssoIdentity{
		Subject:     strconv.FormatInt(u.ID, 10),
		Email:       email,
		DisplayName: name,
		Claims: map[string]any{
			"id":    u.ID,
			"login": u.Login,
			"email": email,
			"name":  name,
		},
	}, nil
}

func getJSONBearer(ctx context.Context, url, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ──────────────────────────────────────────────────────────────────
// AuthGORM provider registration.

// AddOIDCProvider registers one OIDC provider. Performs OIDC
// discovery against IssuerURL — call sites should expect a network
// round-trip and time the configuration accordingly. Returns
// ErrSSOProviderExists if the Name is already registered (case-
// sensitive).
//
// Call BEFORE Route() — Route reads the provider list to render
// "Sign in with X" buttons on the login form.
func (a *AuthGORM) AddOIDCProvider(p OIDCProvider) error {
	if err := a.ensureProviderNameFree(p.Name); err != nil {
		return err
	}
	if err := (&p).initOIDC(context.Background()); err != nil {
		return err
	}
	a.ssoProviders = append(a.ssoProviders, &p)
	return nil
}

// AddOAuth2Provider registers one OAuth2 (non-OIDC) provider. No
// network roundtrip — just validates the config and stores. Returns
// ErrSSOProviderExists if the Name collides.
//
// Call BEFORE Route().
func (a *AuthGORM) AddOAuth2Provider(p OAuth2Provider) error {
	if err := a.ensureProviderNameFree(p.Name); err != nil {
		return err
	}
	if err := (&p).initOAuth2(); err != nil {
		return err
	}
	a.ssoProviders = append(a.ssoProviders, &p)
	return nil
}

func (a *AuthGORM) ensureProviderNameFree(name string) error {
	for _, q := range a.ssoProviders {
		if q.name() == name {
			return fmt.Errorf("%w: %q", ErrSSOProviderExists, name)
		}
	}
	return nil
}

func (a *AuthGORM) findSSOProvider(name string) ssoProvider {
	for _, p := range a.ssoProviders {
		if p.name() == name {
			return p
		}
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────
// User mapping policy.

// resolveSSOLogin runs the (Provider, Subject) → UserGORM decision
// described in docs/AUTH.md (SSO section). Returns ErrSSONoAccount when the
// configured policy refuses the identity (no existing link, email
// auto-link disabled, auto-create disabled, etc.). Updates
// LastUsedAt + Email/DisplayName snapshots on every successful
// match.
func (a *AuthGORM) resolveSSOLogin(ctx context.Context, id ssoIdentity, p ssoProvider) (*UserGORM, error) {
	// Step 1: existing identity? A miss here is the common first-login
	// path, so probe with Limit(1).Find rather than First — Find
	// reports "no rows" via RowsAffected instead of returning
	// gorm.ErrRecordNotFound, which GORM's default logger prints as a
	// scary (but harmless) "record not found" line.
	var ident SSOIdentityGORM
	res := a.DB.Where("provider = ? AND subject = ?", id.Provider, id.Subject).Limit(1).Find(&ident)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected > 0 {
		var user UserGORM
		if err := a.DB.Preload("Groups").Where("id = ?", ident.UserID).First(&user).Error; err != nil {
			return nil, err
		}
		if user.Disabled {
			return nil, ErrSSONoAccount
		}
		ident.LastUsedAt = time.Now()
		if id.Email != "" {
			ident.Email = id.Email
		}
		if id.DisplayName != "" {
			ident.DisplayName = id.DisplayName
		}
		a.DB.Save(&ident) // best-effort; don't fail the login on this
		return &user, nil
	}

	cfg := p.config()

	// Step 2: email auto-link? Same optional-probe pattern as step 1.
	if cfg.AutoLinkByEmail && id.Email != "" {
		var user UserGORM
		res := a.DB.Preload("Groups").Where("email = ? AND disabled = ?", id.Email, false).Limit(1).Find(&user)
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected > 0 {
			link := SSOIdentityGORM{
				UserID:      user.ID,
				Provider:    id.Provider,
				Subject:     id.Subject,
				Email:       id.Email,
				DisplayName: id.DisplayName,
				LastUsedAt:  time.Now(),
			}
			if err := a.DB.Create(&link).Error; err != nil {
				return nil, err
			}
			return &user, nil
		}
	}

	// Step 3: auto-create?
	if cfg.DisableAutoCreate {
		return nil, ErrSSONoAccount
	}
	if id.Email == "" {
		return nil, fmt.Errorf("%w: provider returned no email; cannot auto-create", ErrSSONoAccount)
	}
	groups, err := a.resolveSSOGroups(id.Claims, p)
	if err != nil {
		return nil, err
	}
	user := UserGORM{
		Username: id.Email,
		Email:    id.Email,
		SSOOnly:  true,
		Groups:   groups,
	}
	if err := a.DB.Create(&user).Error; err != nil {
		if isUniqueConstraintError(err) {
			return nil, fmt.Errorf("%w: username/email %q already in use locally", ErrSSONoAccount, id.Email)
		}
		return nil, err
	}
	link := SSOIdentityGORM{
		UserID:      user.ID,
		Provider:    id.Provider,
		Subject:     id.Subject,
		Email:       id.Email,
		DisplayName: id.DisplayName,
		LastUsedAt:  time.Now(),
	}
	if err := a.DB.Create(&link).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// resolveSSOGroups builds the GroupGORM slice for the union of
// DefaultGroups + GroupsClaim-derived + GroupMapper-derived names
// (deduped). Missing groups are auto-created when CreateGroups is
// true, otherwise logged + skipped.
func (a *AuthGORM) resolveSSOGroups(claims map[string]any, p ssoProvider) ([]GroupGORM, error) {
	cfg := p.config()
	var names []string
	names = append(names, cfg.DefaultGroups...)
	if cfg.GroupsClaim != "" && claims != nil {
		if raw, ok := claims[cfg.GroupsClaim]; ok {
			names = append(names, coerceStringSlice(raw)...)
		}
	}
	if cfg.GroupMapper != nil {
		names = append(names, cfg.GroupMapper(claims)...)
	}
	seen := map[string]bool{}
	unique := names[:0:0]
	for _, n := range names {
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		unique = append(unique, n)
	}
	var out []GroupGORM
	for _, n := range unique {
		var g GroupGORM
		err := a.DB.Where("name = ?", n).First(&g).Error
		switch {
		case err == nil:
			out = append(out, g)
		case errors.Is(err, gorm.ErrRecordNotFound):
			if !cfg.CreateGroups {
				log.Printf("auth: SSO group %q from provider %q not in DB; CreateGroups=false, skipping", n, p.name())
				continue
			}
			g = GroupGORM{Name: n}
			if err := a.DB.Create(&g).Error; err != nil {
				return nil, err
			}
			out = append(out, g)
		default:
			return nil, err
		}
	}
	return out, nil
}

// ──────────────────────────────────────────────────────────────────
// PKCE + state + nonce generators. base64.RawURLEncoding so the
// values are URL-safe and unpadded — fit straight into the
// authorize URL or session value without further escaping.

const ssoStateBytes = 32

func newSSOSecret() string {
	b := make([]byte, ssoStateBytes)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand on Linux is /dev/urandom-backed; failure means
		// the OS is broken in a way that's already unrecoverable.
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// newPKCEVerifier returns a PKCE code_verifier per RFC 7636 §4.1
// (43–128 chars from the unreserved set). We use a 32-byte random
// → 43-char base64url string, which sits at the minimum length and
// is widely accepted.
func newPKCEVerifier() string {
	return newSSOSecret()
}

// ──────────────────────────────────────────────────────────────────
// Claim helpers.

func stringClaim(claims map[string]any, key string) string {
	if v, ok := claims[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// coerceStringSlice accepts the various JSON-decoded shapes a
// "groups" claim might arrive as: []string, []any of strings, or
// a single string (some IdPs).
func coerceStringSlice(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return []string{x}
	default:
		return nil
	}
}
