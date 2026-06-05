# auth_sso — AuthGORM with OIDC + OAuth2 SSO

Same as [`auth_gorm`](../auth_gorm/) but reads SSO provider credentials
from environment variables. The login page renders a **"Sign in with
X"** button for every provider whose env vars are set. With no env
vars set the example is identical to `auth_gorm` — useful to confirm
the baseline before flipping SSO on.

A successful SSO sign-in auto-creates a local `UserGORM` row with:

- `Username` = the email returned by the provider
- `Email`    = same
- `SSOOnly`  = `true`  ← gates the account page so the user can't
  enrol a local password / passkey
- group memberships per the provider's `DefaultGroups` + any
  `GroupsClaim`/`GroupMapper` you configure

The admin can clear the `SSOOnly` flag in the admin panel
(`/admin/users`), after which the user can also enrol a local
password and/or passkey alongside their SSO sign-in.

## Run with no SSO providers

```sh
go run ./examples/auth_sso
```

Open <http://localhost:8080>, log in as `admin / admin`. No SSO
buttons; everything else is `auth_gorm`.

## Quickstart — add Google SSO

1. **Register an OAuth client** at
   <https://console.cloud.google.com/apis/credentials> →
   *Create credentials → OAuth client ID → Web application*.
2. Set **Authorized redirect URI** to:
   `http://localhost:8080/login/sso/google/callback`
3. Copy the client ID + secret into env vars:
   ```sh
   export GOOGLE_CLIENT_ID=...apps.googleusercontent.com
   export GOOGLE_CLIENT_SECRET=...
   go run ./examples/auth_sso
   ```
4. Open <http://localhost:8080/login> — "Sign in with Google" button
   appears under the password form. Click it, sign in with a Google
   account, you'll land on `/admin` as a freshly-created SSO-only
   user in the `users` group.

For production set `BASE_URL` to your real public origin (e.g.
`https://app.example.com`) so the redirect URL the library hands to
Google matches what you registered:

```sh
export BASE_URL=https://app.example.com
```

The library does not auto-detect the public origin — OAuth2 redirect
URLs must match exactly, so you tell it.

## Adding GitHub

1. <https://github.com/settings/developers> → **New OAuth App**.
2. **Authorization callback URL**:
   `http://localhost:8080/login/sso/github/callback`
3. Generate a client secret in the app settings.
4. Set env vars:
   ```sh
   export GITHUB_CLIENT_ID=Iv1.xxxxxxxxxxxxxxxx
   export GITHUB_CLIENT_SECRET=...
   ```

GitHub does not speak OIDC — `auth.GitHubProvider` uses the
`OAuth2Provider` type, which calls `/user` + `/user/emails` to find
the verified primary email. If the user has no verified primary email
on GitHub the sign-in fails with `provider returned no email`.

## Adding Okta

1. In the Okta admin console: *Applications → Create App Integration*
   → **OIDC – OpenID Connect / Web Application**.
2. **Sign-in redirect URIs**:
   `http://localhost:8080/login/sso/okta/callback`
3. Copy the **client ID** + **client secret**. The **Okta domain** is
   the bare host of your tenant, e.g. `dev-12345.okta.com`.
4. (Optional) To use group-based mapping enable the `groups` claim on
   your authorization server (Security → API → Authorization Servers
   → default → Claims → Add Claim → name `groups`, value
   `Groups()`).
5. Env vars:
   ```sh
   export OKTA_DOMAIN=dev-12345.okta.com
   export OKTA_CLIENT_ID=...
   export OKTA_CLIENT_SECRET=...
   ```

The example registers Okta with `AutoLinkByEmail=true` and
`GroupsClaim="groups"` — corporate Okta is typically trusted enough
for both. With `CreateGroups` left off (default), groups the Okta
token names but the DB doesn't know about are silently skipped (a
log line per skipped name).

## What the SSO-only flag does

For an `SSOOnly=true` user:

- **`/account/{id}` page**: password card replaced by a notice
  "this account signs in via SSO"; passkey card hidden; TOTP card
  still rendered (TOTP layers on top of every sign-in method).
- **`POST /account/{id}`** (password change): returns 403.
- **`POST /account/{id}/passkey/begin`** + **`/passkey/finish`**:
  return 403.
- **`POST /account/{id}/sso/{identityID}/delete`** (unlink): allowed
  except for the **last** linked identity — unlinking it would lock
  the user out (no password to fall back on). The unlink button is
  disabled in the UI for that row; the handler also defends.

Admin can clear the flag in `/admin/users` → edit user → uncheck
SSO-Only → save. After that the user's account page re-renders the
password and passkey cards.

## Provider config knobs

Whether you use a preset constructor or build `OIDCProvider` /
`OAuth2Provider` literally, these fields apply uniformly:

| Field               | Behaviour                                                                                       |
|---------------------|-------------------------------------------------------------------------------------------------|
| `DefaultGroups`     | Group names added to every new sign-in.                                                          |
| `GroupsClaim`       | Name of a claim in the ID token (OIDC) / UserInfo payload (OAuth2) carrying an array of group names. |
| `CreateGroups`      | When true, group names from the claim that aren't in the DB are auto-created.                    |
| `GroupMapper`       | Optional `func(claims) []string` for custom logic. Composes (union, dedup) with DefaultGroups + GroupsClaim. |
| `AutoLinkByEmail`   | When true and the callback's email matches an existing local user, create the identity link and log them in. |
| `DisableAutoCreate` | When true, the callback rejects new identities — admin must pre-provision the user and link.     |

Combining knobs:

- **Public IdPs (Google, generic GitHub)**: leave `AutoLinkByEmail`
  off. Otherwise anyone able to get an ID token claiming
  `alice@example.com` takes over the local `alice@example.com`
  account.
- **Trusted IdPs (corporate Okta, on-prem Keycloak, Authelia)**:
  `AutoLinkByEmail=true` is fine; the IdP vouches for the email.
- **Strict deployments**: set `DisableAutoCreate=true` everywhere
  and pre-provision users via the admin UI; the callback then
  refuses to mint accounts.

## Adding more OIDC providers

Any OIDC-compliant IdP (Keycloak, Authentik, Dex, ZITADEL, …) works
via `auth.OIDCProvider` directly:

```go
ag.AddOIDCProvider(auth.OIDCProvider{
    Name:         "keycloak",
    DisplayName:  "Keycloak",
    IssuerURL:    "https://keycloak.example.com/realms/myrealm",
    ClientID:     os.Getenv("KEYCLOAK_CLIENT_ID"),
    ClientSecret: os.Getenv("KEYCLOAK_CLIENT_SECRET"),
    RedirectURL:  baseURL + "/login/sso/keycloak/callback",
    DefaultGroups:  []string{"users"},
    GroupsClaim:    "groups",
    AutoLinkByEmail: true,
})
```

`OIDCProvider` performs discovery on `IssuerURL` during `AddOIDCProvider`
so misconfigured issuer URLs fail at startup, not at first sign-in.

## Troubleshooting

**`Error 400: redirect_uri_mismatch` (Google) / equivalent on other
IdPs.** The provider is rejecting the callback URL the app sent.
The fix is always on the *provider* side — register the exact URL:

- It goes in **Authorized redirect URIs**, not "Authorized JavaScript
  origins". (Google's most common mistake.)
- Byte-for-byte match: `http://` not `https://` for localhost,
  no trailing slash, the `:8080` port present, `localhost` not
  `127.0.0.1`.
- The OAuth client must be a **Web application** type — a "Desktop"
  or "TV/limited input" client won't accept a web redirect URI.
- Changes can take a minute or two to propagate on Google's side.

If your real origin differs from `http://localhost:8080`, set
`BASE_URL` so the app and the provider agree on the callback URL —
the app builds it as `{BASE_URL}/login/sso/{name}/callback` and does
**not** auto-detect the origin (a reverse proxy can rewrite request
URLs, and OAuth needs an exact match).

**`record not found` in the console on first SSO login.** Harmless.
That was GORM's default logger narrating the "is this identity
already linked?" probe missing — which is exactly what happens on a
first login, right before the user is auto-created. The library now
uses a quiet existence-probe for that lookup, so you shouldn't see
it anymore; if you do, it's informational, not an error.

**"no account matches this identity" (403).** The provider
authenticated the user, but the local policy refused to map them:
either the email matched an existing local user and
`AutoLinkByEmail` is off (the default for untrusted IdPs), or
`DisableAutoCreate` is on and no account was pre-provisioned. Create
or pre-link the user via `/admin/users`, or flip the relevant
provider knob (see the config table above).
