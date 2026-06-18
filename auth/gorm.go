package auth

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-chi/chi/v5"
	"net/url"
	"strings"
	"time"

	"github.com/alexedwards/argon2id"
	"github.com/alexedwards/scs/v2"
	"github.com/tmshlvck/gone/site"
	"gorm.io/gorm"
)

// fmtTime renders t through the configured TimeFormatter (default
// site.DefaultTimeFormatter) in the request's session zone. A zero time
// yields "". Used for account-page "last used" timestamps so auth's time
// output matches CRUD's.
func (a *AuthGORM) fmtTime(ctx context.Context, t time.Time) string {
	tf := a.TimeFormatter
	if tf == nil {
		tf = site.DefaultTimeFormatter{}
	}
	return tf.FormatTime(site.Timezone(ctx), t)
}

// ──────────────────────────────────────────────────────────────────
// Models.
//
// UserGORM and GroupGORM are plain GORM models with exported fields,
// designed so the CRUD library can derive a MetaModel from them via
// reflection. They DO NOT satisfy auth.User / auth.Group directly —
// Go won't allow a field named Username AND a method Username() on
// the same type. AuthGORM wraps each row in a small adapter
// (userGORMAdapter / groupGORMAdapter) when handing it back through
// the auth.User interface. Apps that need the raw record type-assert
// the adapter's exported U / G pointer.

// UserGORM is the GORM-backed user row. Exposed so apps can derive
// CRUDTable[UserGORM] over it.
type UserGORM struct {
	ID           uint   `gorm:"primaryKey"`
	Username     string `gorm:"uniqueIndex;size:64"`
	Email        string `gorm:"uniqueIndex;size:255"`
	PasswordHash string `gorm:"size:255"`
	// TOTPSecret holds the base32 shared secret for time-based one-
	// time passwords. Empty = TOTP not enrolled. Non-empty = the
	// user must enter a 6-digit code after the password step.
	TOTPSecret string `gorm:"size:64"`
	// WebAuthnHandle is the opaque per-user identifier the WebAuthn
	// spec wants every authenticator to bind credentials to (NOT the
	// numeric ID, which is account-derived). Generated on demand —
	// stays empty until the first passkey enrolment. 32 bytes of
	// crypto/rand.
	WebAuthnHandle []byte `gorm:"size:32"`
	Disabled       bool
	// SSOOnly: when true the user can sign in only via a linked SSO
	// identity (or an enrolled TOTP code on top of it). Self-service
	// password change and passkey enrolment are blocked at the
	// account-page handlers; the admin can clear this flag to give
	// the user back local-credential access. Set automatically when
	// first-login SSO auto-creates a user.
	SSOOnly       bool
	Groups        []GroupGORM       `gorm:"many2many:auth_user_groups"`
	Passkeys      []PasskeyGORM     `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE"`
	SSOIdentities []SSOIdentityGORM `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// PasskeyGORM is one WebAuthn credential bound to one UserGORM. A
// user may have many (different devices). Fields mirror
// webauthn.Credential plus the user-supplied label and last-used
// timestamp for the account-page UI.
type PasskeyGORM struct {
	ID              uint   `gorm:"primaryKey"`
	UserID          uint   `gorm:"index;not null"`
	CredentialID    []byte `gorm:"uniqueIndex;size:255"`
	PublicKey       []byte
	SignCount       uint32
	Transports      string `gorm:"size:128"` // CSV ("internal,usb,nfc,ble,hybrid")
	AttestationType string `gorm:"size:32"`
	AAGUID          []byte `gorm:"size:16"`
	BackupEligible  bool
	BackupState     bool
	Name            string `gorm:"size:64"` // user-visible label
	CreatedAt       time.Time
	LastUsedAt      time.Time
}

func (PasskeyGORM) TableName() string { return "auth_passkeys" }

// TableName overrides GORM's default pluralisation ("user_gorms").
func (UserGORM) TableName() string { return "auth_users" }

// GroupGORM is the GORM-backed group row.
type GroupGORM struct {
	ID        uint       `gorm:"primaryKey"`
	Name      string     `gorm:"uniqueIndex;size:64"`
	Users     []UserGORM `gorm:"many2many:auth_user_groups"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (GroupGORM) TableName() string { return "auth_groups" }

// UserGORMAdapter wraps *UserGORM to satisfy auth.User. Exported so
// app-level Authz impls can type-assert it and reach the underlying
// row:
//
//	if a, ok := u.(auth.UserGORMAdapter); ok {
//	    row := a.U  // *UserGORM
//	}
type UserGORMAdapter struct {
	U *UserGORM
}

func (a UserGORMAdapter) Username() string { return a.U.Username }
func (a UserGORMAdapter) Email() string    { return a.U.Email }
func (a UserGORMAdapter) Groups() []Group {
	out := make([]Group, len(a.U.Groups))
	for i := range a.U.Groups {
		out[i] = GroupGORMAdapter{G: &a.U.Groups[i]}
	}
	return out
}
func (a UserGORMAdapter) HasGroup(name string) bool {
	for _, g := range a.U.Groups {
		if g.Name == name {
			return true
		}
	}
	return false
}

// GroupGORMAdapter wraps *GroupGORM to satisfy auth.Group.
type GroupGORMAdapter struct {
	G *GroupGORM
}

func (a GroupGORMAdapter) Name() string { return a.G.Name }

// ──────────────────────────────────────────────────────────────────
// AuthGORM struct.

// AuthGORM is the v2 Auth implementation. Users + groups + password
// hashes live in a GORM-backed store. Satisfies auth.Auth.
//
// Future scope: passkey credentials, OIDC subjects, account
// management page. V1 covers username+password (argon2id) only —
// same single-method login UX as AuthSimple, so it shares the login
// templ for now.
type AuthGORM struct {
	Sessions   *scs.SessionManager
	DB         *gorm.DB
	AfterLogin string // default "/"

	// TOTPIssuer is the issuer string embedded in the otpauth URLs
	// generated for TOTP enrolment. Authenticator apps display it
	// alongside the username so users can tell accounts apart.
	// Defaults to "gone" when empty.
	TOTPIssuer string

	// TimeFormatter renders the account page's timestamps ("last used")
	// — the same app-global policy CRUD uses, so all of gone's time
	// output is consistent. nil → site.DefaultTimeFormatter. The session
	// zone comes from the request context (site.Timezone).
	TimeFormatter site.TimeFormatter

	// WebAuthn relying-party info. Required iff passkey routes are
	// in play (any user enrols a passkey, OR the login page is
	// served on a host that supports them — which today is "all").
	// RPID is the bare host the browser sees (no scheme, no port);
	// RPOrigins are the full schemed+ported origins the browser may
	// be loaded from (a list because dev typically wants both
	// "http://localhost:8080" and "http://127.0.0.1:8080").
	RPDisplayName string
	RPID          string
	RPOrigins     []string

	// ssoProviders are the registered SSO (OIDC + OAuth2) providers,
	// in registration order. Empty = SSO not in use; the login form
	// renders no "Sign in with …" buttons and IsAuthPath excludes
	// /login/sso/*. See sso.go for the provider types and AddOIDCProvider
	// / AddOAuth2Provider for registration.
	ssoProviders []ssoProvider

	urlBase            string
	loginPath          string
	logoutPath         string
	totpPath           string // {base}/login/totp
	passkeyOptionsPath string // {base}/login/passkey/options
	passkeyFinishPath  string // {base}/login/passkey/finish
	ssoStartPath       string // {base}/login/sso (prefix; route uses {name})
	ssoCallbackPath    string // {base}/login/sso (prefix; route uses {name}/callback)
}

// NewAuthGORM constructs an AuthGORM and runs db.AutoMigrate for
// UserGORM + GroupGORM. Idempotent — calling on an already-migrated
// schema is a no-op.
func NewAuthGORM(sm *scs.SessionManager, db *gorm.DB) (*AuthGORM, error) {
	if sm == nil {
		return nil, errors.New("auth.NewAuthGORM: nil session manager")
	}
	if db == nil {
		return nil, errors.New("auth.NewAuthGORM: nil DB")
	}
	if err := db.AutoMigrate(&UserGORM{}, &GroupGORM{}, &PasskeyGORM{}, &SSOIdentityGORM{}); err != nil {
		return nil, fmt.Errorf("auth.NewAuthGORM: migrate: %w", err)
	}
	return &AuthGORM{
		Sessions:   sm,
		DB:         db,
		AfterLogin: "/",
	}, nil
}

// ──────────────────────────────────────────────────────────────────
// Errors.

// ErrGroupExists is returned by GroupAdd when the name is taken.
var ErrGroupExists = errors.New("auth: group already exists")

// ErrGroupNotFound is returned by GroupDel / UserMod when a group
// reference can't be resolved.
var ErrGroupNotFound = errors.New("auth: group not found")

// ErrEmptyGroupName is returned by GroupAdd when called with "".
var ErrEmptyGroupName = errors.New("auth: empty group name")

// ──────────────────────────────────────────────────────────────────
// Config methods — not on the Auth interface; each impl exposes
// its own surface for user / group management.

// UserAdd creates a user with the given email and password. The
// password is argon2id-hashed before storage.
//
// Returns ErrUserExists if username (or email) is taken,
// ErrEmptyUsername if username == "".
func (a *AuthGORM) UserAdd(username, email, password string) error {
	if username == "" {
		return ErrEmptyUsername
	}
	hash, err := argon2id.CreateHash(password, argon2id.DefaultParams)
	if err != nil {
		return err
	}
	row := UserGORM{Username: username, Email: email, PasswordHash: hash}
	if err := a.DB.Create(&row).Error; err != nil {
		if isUniqueConstraintError(err) {
			return ErrUserExists
		}
		return err
	}
	return nil
}

// UserDel removes the named user and clears its group memberships
// (the m2m join rows are deleted). Returns ErrUserNotFound if absent.
func (a *AuthGORM) UserDel(username string) error {
	var user UserGORM
	if err := a.DB.Where("username = ?", username).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrUserNotFound
		}
		return err
	}
	// Select("Groups") tells Delete to also remove the join-table
	// rows that point at this user. Without it the row is deleted
	// but the m2m table keeps orphan references.
	return a.DB.Select("Groups").Delete(&user).Error
}

// HashPassword hashes a plaintext password with argon2id (DefaultParams),
// producing the PHC-encoded string stored in UserGORM.PasswordHash. Exported
// so an app hashing passwords outside the login path — e.g. an admin CRUD
// table using crud.Field{Hash: auth.HashPassword} — produces hashes the
// login path verifies. Returns the hash, or an error from the KDF.
func HashPassword(plaintext string) (string, error) {
	return argon2id.CreateHash(plaintext, argon2id.DefaultParams)
}

// Passwd replaces the named user's password (argon2id re-hashed).
// Returns ErrUserNotFound if absent.
func (a *AuthGORM) Passwd(username, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	res := a.DB.Model(&UserGORM{}).
		Where("username = ?", username).
		Update("password_hash", hash)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrUserNotFound
	}
	return nil
}

// GroupAdd creates a group with the given name. Returns ErrGroupExists
// if the name is taken, ErrEmptyGroupName if name == "".
func (a *AuthGORM) GroupAdd(name string) error {
	if name == "" {
		return ErrEmptyGroupName
	}
	row := GroupGORM{Name: name}
	if err := a.DB.Create(&row).Error; err != nil {
		if isUniqueConstraintError(err) {
			return ErrGroupExists
		}
		return err
	}
	return nil
}

// GroupDel removes the named group and clears its user memberships.
// Returns ErrGroupNotFound if absent.
func (a *AuthGORM) GroupDel(name string) error {
	var group GroupGORM
	if err := a.DB.Where("name = ?", name).First(&group).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrGroupNotFound
		}
		return err
	}
	return a.DB.Select("Users").Delete(&group).Error
}

// UserMod sets the named user's group memberships to the supplied
// list of group names. Groups not in the list are unlinked (the
// m2m row is removed); groups in the list that don't exist yet are
// NOT auto-created — UserMod returns ErrGroupNotFound wrapping the
// first missing group name.
//
// Useful for "set Alice's groups to [admin, editors]" without the
// caller juggling GroupGORM IDs.
func (a *AuthGORM) UserMod(username string, groupNames []string) error {
	var user UserGORM
	if err := a.DB.Where("username = ?", username).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrUserNotFound
		}
		return err
	}
	groups := make([]GroupGORM, 0, len(groupNames))
	for _, name := range groupNames {
		var g GroupGORM
		if err := a.DB.Where("name = ?", name).First(&g).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: %q", ErrGroupNotFound, name)
			}
			return err
		}
		groups = append(groups, g)
	}
	return a.DB.Model(&user).Association("Groups").Replace(groups)
}

// ──────────────────────────────────────────────────────────────────
// Auth interface implementation.

// Authenticate looks up the user by username, checks Disabled, and
// verifies the argon2id hash. Disabled users return ErrUserNotFound
// so callers can't enumerate them as a separate class.
//
// Exported so apps can drive login programmatically (e.g. an API
// endpoint) without going through the form handler.
func (a *AuthGORM) Authenticate(username, password string) (User, error) {
	var user UserGORM
	err := a.DB.Preload("Groups").Where("username = ?", username).First(&user).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	if user.Disabled {
		return nil, ErrUserNotFound
	}
	match, err := argon2id.ComparePasswordAndHash(password, user.PasswordHash)
	if err != nil {
		return nil, err
	}
	if !match {
		return nil, ErrInvalidPassword
	}
	return UserGORMAdapter{U: &user}, nil
}

// CurrentUser reads the username from the session and looks up the
// user (preloading groups). Returns nil for anonymous AND for sessions
// whose user has since been deleted or disabled.
func (a *AuthGORM) CurrentUser(ctx context.Context) User {
	username := a.CurrentUsername(ctx)
	if username == "" {
		return nil
	}
	var user UserGORM
	if err := a.DB.Preload("Groups").Where("username = ?", username).First(&user).Error; err != nil {
		return nil
	}
	if user.Disabled {
		return nil
	}
	return UserGORMAdapter{U: &user}
}

// CurrentUsername returns the session's username verbatim ("" when
// anonymous), reading only the ctx — no DB lookup, no disabled check. See
// the Auth interface for when to prefer this over CurrentUser.
func (a *AuthGORM) CurrentUsername(ctx context.Context) string {
	return a.Sessions.GetString(ctx, userSessionKey)
}

// LoginURL / LogoutURL are the same shape as AuthSimple's.
func (a *AuthGORM) LoginURL(next string) string {
	path := a.loginPath
	if path == "" {
		path = "/login"
	}
	if next == "" {
		return path
	}
	return path + "?next=" + url.QueryEscape(next)
}

// IsAuthPath: any path required by an auth ceremony that the user
// reaches before being fully signed-in. Password page, staged TOTP
// step, passkey JSON endpoints, SSO start + callback. Account pages
// are gated by impl authz, not by the page shell.
func (a *AuthGORM) IsAuthPath(path string) bool {
	if a.loginPath != "" {
		if path == a.loginPath {
			return true
		}
	} else if path == "/login" {
		return true
	}
	if a.totpPath != "" && path == a.totpPath {
		return true
	}
	if a.passkeyOptionsPath != "" && path == a.passkeyOptionsPath {
		return true
	}
	if a.passkeyFinishPath != "" && path == a.passkeyFinishPath {
		return true
	}
	// SSO start and callback both live under {base}/login/sso/ and
	// terminate at /{name} or /{name}/callback. Any path with that
	// prefix is treated as an auth path so the page shell doesn't
	// redirect anonymous browsers mid-ceremony.
	if a.ssoStartPath != "" && strings.HasPrefix(path, a.ssoStartPath+"/") {
		return true
	}
	return false
}

func (a *AuthGORM) LogoutURL(next string) string {
	path := a.logoutPath
	if path == "" {
		path = "/logout"
	}
	if next == "" {
		return path
	}
	return path + "?next=" + url.QueryEscape(next)
}

// Login rotates the session, stores the username, and rotates CSRF.
func (a *AuthGORM) Login(ctx context.Context, u User) error {
	if u == nil || u.Username() == "" {
		return ErrEmptyUsername
	}
	if err := a.Sessions.RenewToken(ctx); err != nil {
		return err
	}
	a.Sessions.Put(ctx, userSessionKey, u.Username())
	rotateCSRF(ctx, a.Sessions)
	return nil
}

// Logout destroys the session.
func (a *AuthGORM) Logout(ctx context.Context) error {
	return a.Sessions.Destroy(ctx)
}

// Route mounts:
//
//	GET/POST  /login       — username + password (stage 1)
//	GET/POST  /login/totp   — TOTP code (stage 2, only reachable after stage 1 for a TOTP-enrolled user)
//	POST      /logout
//	         + /account routes (see account.go)
//
// Stage 2 is skipped entirely for users without a TOTP secret —
// they go straight to AfterLogin from the password POST.
func (a *AuthGORM) RegisterRoutes(mux chi.Router, mountBase string, shell site.Shell) error {
	if mux == nil {
		return errors.New("auth.AuthGORM.RegisterRoutes: nil router")
	}
	base := normalizeAuthPrefix(mountBase)
	a.urlBase = base
	a.loginPath = base + pathLogin
	a.logoutPath = base + pathLogout
	a.totpPath = base + pathTOTPLogin
	a.passkeyOptionsPath = base + pathPasskeyOptions
	a.passkeyFinishPath = base + pathPasskeyFinish
	a.ssoStartPath = base + pathSSO
	a.ssoCallbackPath = base + pathSSO
	// Passkey paths are wired into the login form only when RP is
	// configured — gates the "Use passkey" button + conditional UI.
	var pkOpts, pkFin string
	if a.RPID != "" {
		pkOpts = a.passkeyOptionsPath
		pkFin = a.passkeyFinishPath
	}
	mountPasswordLogin(mux, passwordLoginOpts{
		LoginPath:          a.loginPath,
		LogoutPath:         a.logoutPath,
		AfterLogin:         func() string { return a.AfterLogin },
		Authenticate:       a.Authenticate,
		Login:              a.loginStage1, // staged: may detour through /login/totp
		Logout:             a.Logout,
		LoginURL:           a.LoginURL,
		Shell:              shell,
		PasskeyOptionsPath: pkOpts,
		PasskeyFinishPath:  pkFin,
		SSOButtons:         a.ssoLoginButtons,
	})
	a.mountTOTPLoginRoutes(mux, shell)
	a.mountAccountRoutes(mux, shell)
	// Passkey login is mounted only when RP fields are set; without
	// them the WebAuthn handler would panic on first call. Apps that
	// don't need passkeys leave RPID="" and the endpoints stay
	// unregistered — IsAuthPath also reports false, so nothing
	// references the empty paths.
	if a.RPID != "" {
		a.mountPasskeyLoginRoutes(mux)
	}
	// SSO is opt-in per provider. With zero providers configured the
	// mount is a no-op and IsAuthPath reports false for the sso
	// prefix.
	a.mountSSOLoginRoutes(mux, shell)
	return nil
}

// ──────────────────────────────────────────────────────────────────
// Helpers.

// isUniqueConstraintError returns true if err is the GORM-surfaced
// shape of a UNIQUE constraint violation. sqlite / postgres / mysql
// emit different strings — the substring match is what the popular
// wrapper libraries (gorm-helpers, etc.) settle on too.
func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "duplicate entry")
}
