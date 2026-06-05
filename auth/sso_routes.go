package auth

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
)

// ssoButtonInfo is one "Sign in with …" row on the login form. The
// template renders one per registered provider; with zero providers
// the list is empty and the entire SSO section drops out.
type ssoButtonInfo struct {
	DisplayName string
	URL         string
}

// ssoButtonList resolves the SSOButtons closure to a slice. nil
// closure or empty result both yield nil, which the login template
// treats as "no SSO section".
func ssoButtonList(fn func(string) []ssoButtonInfo, next string) []ssoButtonInfo {
	if fn == nil {
		return nil
	}
	return fn(next)
}

// ssoLoginButtons builds the per-provider login button list keyed
// off the supplied next URL — each button's URL already carries the
// ?next= so the callback can route back to where the user came from.
// Returns an empty slice when no providers are configured, which
// the template treats as "no SSO section".
func (a *AuthGORM) ssoLoginButtons(next string) []ssoButtonInfo {
	if len(a.ssoProviders) == 0 {
		return nil
	}
	out := make([]ssoButtonInfo, 0, len(a.ssoProviders))
	for _, p := range a.ssoProviders {
		u := a.ssoStartPath + "/" + p.name()
		if next != "" {
			u += "?next=" + url.QueryEscape(next)
		}
		out = append(out, ssoButtonInfo{
			DisplayName: p.displayName(),
			URL:         u,
		})
	}
	return out
}

// Session keys for the in-flight SSO ceremony. All values are short-
// lived strings — cleared on a successful callback or on a new
// ceremony start. scs handles the storage; we just key-prefix.
const (
	ssoStateKey    = "auth:sso_state"
	ssoPKCEKey     = "auth:sso_pkce"
	ssoNonceKey    = "auth:sso_nonce"
	ssoProviderKey = "auth:sso_provider"
	ssoNextKey     = "auth:sso_next"
)

// mountSSOLoginRoutes wires the two SSO endpoints onto mux. Called
// from AuthGORM.Route() after the password / TOTP / passkey mounts.
// No-op when no providers are registered — keeps the URL space clean
// in the common case.
//
// Routes registered (paths shown for the default baseUrl=""):
//
//	GET  /login/sso/{name}             — start ceremony
//	GET  /login/sso/{name}/callback    — handle redirect-back
//
// The {name} segment is matched against registered providers; an
// unknown name returns 404 (deliberate — no leakage of which
// providers exist).
func (a *AuthGORM) mountSSOLoginRoutes(mux Mux, shell PageShellFunc) {
	if len(a.ssoProviders) == 0 {
		return
	}
	mux.HandleFunc("GET "+a.ssoStartPath+"/{name}", func(w http.ResponseWriter, r *http.Request) {
		a.ssoStartHandler(w, r)
	})
	mux.HandleFunc("GET "+a.ssoCallbackPath+"/{name}/callback", func(w http.ResponseWriter, r *http.Request) {
		a.ssoCallbackHandler(w, r, shell)
	})
}

// ssoStartHandler generates state + PKCE verifier + nonce, stashes
// them in the session keyed against this provider name, and 303-
// redirects the user to the provider's authorize endpoint.
//
// The ?next= query parameter (set by the page shell when an
// anonymous user tries to reach a gated path) survives the round-
// trip via the ssoNextKey session value.
func (a *AuthGORM) ssoStartHandler(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p := a.findSSOProvider(name)
	if p == nil {
		http.NotFound(w, r)
		return
	}
	state := newSSOSecret()
	pkce := newPKCEVerifier()
	nonce := newSSOSecret()
	next := safeNext(r.URL.Query().Get("next"))

	ctx := r.Context()
	a.Sessions.Put(ctx, ssoStateKey, state)
	a.Sessions.Put(ctx, ssoPKCEKey, pkce)
	a.Sessions.Put(ctx, ssoNonceKey, nonce)
	a.Sessions.Put(ctx, ssoProviderKey, name)
	a.Sessions.Put(ctx, ssoNextKey, next)

	http.Redirect(w, r, p.authCodeURL(state, nonce, pkce), http.StatusSeeOther)
}

// ssoCallbackHandler validates the state, exchanges the code for an
// identity, maps it to a UserGORM via resolveSSOLogin, and finalizes
// the session through loginStage1 — which detours through /login/totp
// if the user has TOTP enrolled. Failure paths render plain-text
// errors via http.Error (callers don't get to choose an error page
// for now; SSO failures are rare and the messages are diagnostic).
func (a *AuthGORM) ssoCallbackHandler(w http.ResponseWriter, r *http.Request, shell PageShellFunc) {
	name := r.PathValue("name")
	p := a.findSSOProvider(name)
	if p == nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()

	// Defensive: the same {name} the user started with?
	if started := a.Sessions.GetString(ctx, ssoProviderKey); started != name {
		http.Error(w, "SSO ceremony mismatch — restart from the login page", http.StatusBadRequest)
		return
	}
	expectedState := a.Sessions.GetString(ctx, ssoStateKey)
	if expectedState == "" || r.URL.Query().Get("state") != expectedState {
		http.Error(w, "SSO state mismatch — restart from the login page", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		// Provider redirected back with an error — surface the message.
		errParam := r.URL.Query().Get("error_description")
		if errParam == "" {
			errParam = r.URL.Query().Get("error")
		}
		if errParam == "" {
			errParam = "missing code"
		}
		http.Error(w, "SSO error: "+errParam, http.StatusBadRequest)
		return
	}
	pkce := a.Sessions.GetString(ctx, ssoPKCEKey)
	nonce := a.Sessions.GetString(ctx, ssoNonceKey)
	next := a.Sessions.GetString(ctx, ssoNextKey)

	// Clear ceremony state now so a refresh of the callback URL can't
	// be replayed.
	a.clearSSOCeremony(ctx)

	id, err := p.exchange(ctx, code, pkce, nonce)
	if err != nil {
		log.Printf("auth: SSO exchange failed for provider %q: %v", name, err)
		http.Error(w, "SSO sign-in failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	user, err := a.resolveSSOLogin(ctx, id, p)
	if err != nil {
		if errors.Is(err, ErrSSONoAccount) {
			// 403 with the friendly message — the user is who they say
			// they are, but the local policy refuses to map them.
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		log.Printf("auth: SSO resolve failed for provider %q: %v", name, err)
		http.Error(w, "SSO sign-in failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Finalize via the same staged-login path the password POST uses,
	// so TOTP-enrolled SSO users still get prompted at /login/totp.
	override, err := a.loginStage1(ctx, UserGORMAdapter{U: user}, next)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	dest := override
	if dest == "" {
		dest = next
	}
	if dest == "" {
		dest = a.AfterLogin
	}
	if dest == "" {
		dest = "/"
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func (a *AuthGORM) clearSSOCeremony(ctx context.Context) {
	a.Sessions.Remove(ctx, ssoStateKey)
	a.Sessions.Remove(ctx, ssoPKCEKey)
	a.Sessions.Remove(ctx, ssoNonceKey)
	a.Sessions.Remove(ctx, ssoProviderKey)
	a.Sessions.Remove(ctx, ssoNextKey)
}
