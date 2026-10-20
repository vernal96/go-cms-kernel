# External authentication

`auth` is an optional built-in module. Users and external identity bindings belong
to Core and are shared by the application. Provider registrations, enabled
providers, callback origins and limits belong to each site runtime.

Declare the module after Core and bind `auth/adapters/postgres.DatabaseFactory`
through the application's normal module database selection. The Auth adapter owns
the `auth` schema; Core owns `core.external_identities`. Infrastructure creates
provider adapters from environment configuration and passes them in `Providers`.
Secrets never belong to profile fields or site settings.

```go
auth.New(auth.Config{
    Providers: []auth.Provider{configuredProvider},
    EnabledProvidersSetting: "auth_providers",
    AllowRegistration: true,
})
```

Declare `auth_providers` as a profile parameter, for example a typed
`field.Select` with `SelectOptions{Multiple: true, Choices: ...}`. Site updates use
the existing protected site settings API and rebuild the runtime. When
`EnabledProvidersSetting` is configured, an absent or empty list disables all
providers. Unknown selected providers reject runtime finalization. When the
setting option is omitted, every installed provider is enabled.

The default callback scheme is HTTPS; `Scheme: "http"` is an explicit local
development option. The callback hostname must match the site's configured
domain. HTTP flows preserve the actual request's port, including a nonstandard
local port; forwarded host headers are ignored. Origin validation requires the
configured scheme and exact request authority, with equivalent default ports
normalized. The durable flow also binds this callback authority. Register the
exact callback URL with each external provider.

## Public API

All paths are below `/api` and resolve the current published site runtime.

| Request | Behavior |
| --- | --- |
| `GET /identity/providers` | Enabled provider code/label list and registration policy |
| `GET /identity/me` | Current user's public identity; Bearer authentication required |
| `POST /identity/{provider}/start` | JSON `{}` for login or `{"link":true}` for authenticated linking; returns `authorization_url` |
| `GET /identity/{provider}/callback?state=...&code=...` | Provider callback; requires the browser binding cookie and returns the CMS Bearer token as JSON |

The browser first calls `start` and then navigates to `authorization_url`. Linking
requires the existing CMS Bearer token on the start request. The callback sets no
long-lived session cookie: clients use the returned token according to the
existing CMS Bearer contract. Tokens never appear in redirect URLs. Callback and
start responses use `Cache-Control: no-store`; callback responses also prohibit
referrer propagation. Global password login/logout remain `/api/auth/login` and
`/api/auth/logout`.

## Identity and registration rules

An external account is identified by provider code, provider namespace and stable
subject. A namespace distinguishes the provider's issuer/client subject space.
The provider adapter verifies external tokens and claims before returning an
identity. Only a verified provider email may populate the account email.

The first successful login may create a global user when registration is enabled.
It creates no group memberships and no password credential. A missing email
remains absent. The CMS generates a unique machine login independently of email.
A colliding email returns a conflict; it never merges or links existing accounts.
The person must authenticate their existing account and start an explicit link.
Linking checks that account's credential version and blocked state inside the
same database transaction as the binding. Blocking prevents every provider login.

Core's public `User.Email` and internal `Record.PasswordHash` remain strings;
empty values represent absent credentials. PostgreSQL stores absent values as
NULL. Administrative local-password creation still requires a real email and
password. Local-password login of a social-only account returns invalid
credentials. Changing its password through current-password verification is
rejected until a password credential is explicitly established administratively.

## Contributions and lifecycle

Project modules can implement `kernel.RuntimeContributionProvider` and contribute
to an interface embedding `kernel.ModuleRuntime` and `auth.ProviderRegistrar`.
Use `kernel.ContributeTo` and declare a dependency on `auth.ModuleCode`. Auth seals
the registry in `FinalizeRuntimeBuild`; duplicate codes and registrations after
publication fail. Create site-dependent provider objects during the contributing
module's runtime build. Immutable concurrency-safe adapters can be shared.

State is durable PostgreSQL data with atomic consumption. It binds the browser,
site, provider namespace and exact callback URL. Nonces and PKCE verifiers never
leave the trusted exchange path. One callback can consume a flow only once,
including across replicas. Authentication codes are not automatically retried
after provider failure; start a new login. Credential exchange has a configurable
timeout, and each runtime bounds concurrent exchanges. Flows expire after five
minutes by default (maximum ten), are capped at 10,000 pending entries per site,
and are pruned by the site's cancellable background task.

Removing a provider or disabling it in the rebuilt site runtime rejects pending
callbacks. Changing namespace or callback origin also rejects an old flow.
Provider outages produce generic client errors and recorded server diagnostics;
the provider adapter must never include secrets or tokens in its error text.

Expected provider rejection uses `auth.ErrRejected` and maps to HTTP 401. Network
failures, upstream 5xx and invalid client configuration remain server failures.
Built-in adapters omit request URLs from network diagnostics because some
provider protocols carry credentials in URL queries. VK callbacks additionally
carry the provider's `device_id`, forwarded unchanged to the code exchange.

`MaxConcurrentExchanges` configures each runtime's exchange capacity (default 8).
`ExchangeTimeout` defaults to ten seconds; individual built-in provider HTTP
requests have an additional eight-second bound and prohibit redirects.
