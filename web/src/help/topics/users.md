<!-- operations: First-run setup and access -->

# Users and API tokens

People sign in with a local password or through an identity provider; automation uses API tokens.
Every change is recorded in the audit log.

## Roles

- **viewer** reads everything except users, API tokens and the audit log.
- **operator** also changes DNS configuration.
- **admin** can do everything, including managing users and tokens.

The first admin is created on the setup page with the one-time token that the management plane logs
on first start.

## API tokens

API clients send a bearer token (`Authorization: Bearer nxt_...`). A token has a name, a role that never
exceeds its creator's, and an optional expiry. It is shown only once, when created. Revoking a token
refuses its requests from then on. Mutating requests must send `Content-Type: application/json`.

## OIDC

Single sign-on uses an OpenID Connect identity provider configured on the management plane, with
optional admin and operator group names that map to roles. Users from the identity provider appear
after their first sign-in.

## Account

Your profile shows your username, role and sign-in source, and lets you set a theme, time zone, clock
format and the default query log live mode. Local users can change their email, display name and
password; passwords need at least 12 characters, and changing yours can sign out your other sessions.
Users from an identity provider manage these there. More than 10 failed sign-ins or password changes
in 15 minutes pause further attempts.
