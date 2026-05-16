# Ory Stack (Kratos + Hydra + Login UI) for Portainer

A git-ops Docker Compose deployment that gives you:

- **Ory Kratos** — identity management. Sign-in surface is locked to **OIDC providers** (Google, GitHub, GitLab, Pocket-ID) and **passkeys** (WebAuthn passwordless). Password and email-magic-code login are off by default. TOTP and WebAuthn-2FA can be layered on top.
- **Ory Hydra** — OAuth2 / OIDC provider you point your other apps at (Outline, Forgejo, etc.). One Hydra client per app.
- **kratos-selfservice-ui-node** — reference Login / Registration / Settings / Recovery UI.
- **Custom consent service** (`consent/`) — replaces the Login UI's consent handler. Enforces per-client `required_groups` and copies `groups` into ID + access tokens, so each app's access is gated centrally.
- **Invite CLI** (`invite/`) — pre-creates a Kratos identity with the right group memberships and prints a 1h recovery link. Send the link, recipient picks whichever auth method(s) they want and links them all to the same identity.

Both Kratos and Hydra run on **SQLite** with data persisted in named Docker volumes — lightweight, no Postgres dependency.

## Architecture

```
       Browser
          │
          ▼
  ┌──────────────────────────────────────────────────┐
  │           auth.example.com (one origin)          │
  │  ┌──────────────┐  ┌──────────────┐  ┌────────┐  │
  │  │   Login UI   │  │   Kratos     │  │Consent │  │
  │  │   (catch-all)│  │   (public,   │  │service │  │
  │  │              │  │   path-routed│  │/consent│  │
  │  │  /login etc. │  │  /self-      │  │        │  │
  │  │              │  │   service/*) │  │        │  │
  │  └──────────────┘  └──────┬───────┘  └────────┘  │
  └─────────────────────────────│────────────────────┘
                                │ identity
                                ▼
                       ┌──────────────────┐
                       │  Hydra (public)  │
                       │ hydra.example.com│
                       └────────┬─────────┘
                                │ OIDC discovery + tokens
                                ▼
                Your apps (Outline, Forgejo, …) — configure them
                with hydra.example.com/.well-known/openid-configuration
```

Trust boundaries:

| Endpoint                   | Network           | Exposure                       |
|----------------------------|-------------------|--------------------------------|
| Login UI      (`:3000`)    | traefik + internal| `https://${LOGIN_UI_HOST}` (catch-all paths) |
| Kratos public (`:4433`)    | traefik + internal| `https://${LOGIN_UI_HOST}/self-service/…`, `/.well-known/…`, `/sessions/…`, `/schemas/…`, `/health/…` (path-routed, priority 90) |
| Consent       (`:3001`)    | traefik + internal| `https://${LOGIN_UI_HOST}/consent` (priority 100) |
| Kratos admin  (`:4434`)    | internal only     | never on the public internet   |
| Hydra public  (`:4444`)    | traefik + internal| `https://${HYDRA_PUBLIC_HOST}` |
| Hydra admin   (`:4445`)    | internal only     | never on the public internet   |

Login UI, Kratos public, and Consent all live on the **same origin** (`LOGIN_UI_HOST`) so CSRF and session cookies just work. Kratos's per-flow CSRF cookies don't honor `serve.public.cookies.domain` in v1.3.x, so a separate-subdomain layout produces an infinite redirect loop on every flow — same-origin sidesteps the bug entirely.

Admin APIs are reachable from other containers on the `ory_internal` Docker network. To talk to them from your laptop, use `docker exec ory-hydra hydra ...` or open a temporary SSH tunnel.

## Files

```
.
├── docker-compose.yml
├── .env.example
├── config/                           # built into ory-kratos-config image
│   ├── Dockerfile                    # alpine + gettext, baked config files
│   ├── render.sh                     # entrypoint: envsubst + copy
│   └── kratos/
│       ├── kratos.yml.tmpl           # rendered at startup with envsubst
│       ├── identity.schema.json      # user shape (incl. groups[])
│       ├── oidc.google.jsonnet       # Google → Kratos identity mapper
│       ├── oidc.pocket-id.jsonnet
│       ├── oidc.github.jsonnet
│       └── oidc.gitlab.jsonnet
├── consent/                          # our Go consent service
│   ├── main.go
│   ├── go.mod
│   └── Dockerfile                    # → ghcr.io/<owner>/ory-consent:latest
├── invite/                           # our Go invite CLI
│   ├── main.go
│   ├── go.mod
│   └── Dockerfile                    # → ghcr.io/<owner>/ory-invite:latest
└── .github/
    ├── scripts/
    │   └── build-deploy-branch.sh    # shared: rebuild deploy from master + tag pins
    └── workflows/
        ├── deploy.yml                # master push (non-image) → deploy branch
        ├── vendor-images.yml         # weekly: pull oryd/* → push vendored to GHCR
        └── build-services.yml        # build & push consent + invite + kratos-config
```

## How deploys are pinned

`master` always references images as `:latest` (e.g. `ghcr.io/korjavin/ory-consent:latest`). The `deploy` branch — what Portainer actually pulls — is auto-generated with each image pinned to a concrete tag.

| Image | Pinned to |
|---|---|
| `ory-consent`, `ory-invite`, `ory-kratos-config` | the **master commit SHA** that last built it (built by `build-services.yml`) |
| `kratos-vendor`, `hydra-vendor`, `kratos-selfservice-ui-node-vendor` | `d-<first-12-of-upstream-digest>` (built by `vendor-images.yml`) |

Each deploy-branch commit also carries `image-tags.env`, recording the exact tags for that revision. To inspect what's currently deployed:

```bash
git show origin/deploy:image-tags.env
```

To roll back one image (e.g. revert ory-consent to its previous SHA):

```bash
# Find the SHA you want to roll back to
git log --oneline master -- consent/
# Force the deploy branch to that pinned tag
ORY_CONSENT_TAG=<sha> bash .github/scripts/build-deploy-branch.sh
```

Or just `git revert` the offending master commit; the next `Deploy Ory Stack` run repins automatically.

The pinning model means Portainer always sees a tag it hasn't pulled before → it pulls every redeploy → no more "Portainer cached `:latest`" surprises.

## How environment variables are wired

You said you don't want a `.env` file on the server — Portainer holds the values. The compose file references env names with sensible defaults; only **hostnames** and **secrets** truly need to be set in Portainer's stack-env panel.

Kratos itself doesn't natively read `${VAR}` from its YAML config. We work around that with a tiny init container `kratos-config` built from `./config/`. The image bakes in `gettext` (for `envsubst`), the `kratos.yml.tmpl` template, all OIDC mapper jsonnets, and the identity schema. On every restart its entrypoint renders the template against the current Portainer env vars and drops the result into a shared volume that Kratos mounts read-only at `/etc/config/kratos`.

Edit-and-deploy loop:

1. Edit `config/kratos/kratos.yml.tmpl` (or any other file in `config/`) on master.
2. The `Build Custom Services` workflow rebuilds `ghcr.io/<owner>/ory-kratos-config:latest` and force-pushes the `deploy` branch.
3. Portainer pulls the new image on redeploy; the next `kratos-config` run renders the updated template.

## Required secrets

| Variable                       | Purpose                                | Generator                              |
|--------------------------------|----------------------------------------|----------------------------------------|
| `KRATOS_COOKIE_SECRET`         | signs Kratos session cookies           | `openssl rand -hex 32`                 |
| `KRATOS_CIPHER_SECRET`         | encrypts secrets at rest in Kratos     | **`openssl rand -hex 16`** (must be exactly 32 chars — xchacha20-poly1305 key length) |
| `HYDRA_SECRETS_SYSTEM`         | encrypts Hydra DB rows                 | `openssl rand -hex 32`                 |
| `HYDRA_SECRETS_COOKIE`         | signs Hydra cookies                    | `openssl rand -hex 32`                 |
| `HYDRA_PAIRWISE_SALT`          | salt for pairwise OIDC subject IDs     | `openssl rand -hex 32`                 |
| `LOGIN_UI_COOKIE_SECRET`       | signs Login UI cookies                 | `openssl rand -hex 32`                 |
| `LOGIN_UI_CSRF_COOKIE_SECRET`  | signs Login UI CSRF cookies            | `openssl rand -hex 32`                 |

## DNS & TLS

Set up **two** DNS A/AAAA records pointing to your Hetzner host:

```
auth.example.com    → host    (Login UI + Kratos public + Consent, all same-origin)
hydra.example.com   → host    (Hydra public OAuth2/OIDC)
```

Traefik handles certs via the `myresolver` (or whatever you set in `TRAEFIK_CERTRESOLVER`). The cookie domain must be a parent that covers all three — set `COOKIE_DOMAIN=.example.com`.

## Setting up the social providers

The redirect URI in every provider's console is always:

```
https://${LOGIN_UI_HOST}/self-service/methods/oidc/callback/<provider-id>
```

`<provider-id>` is `google`, `pocket-id`, `github`, or `gitlab`. Kratos rejects providers with null/empty `client_id` or `client_secret` at startup, so every configured provider must have valid credentials. To temporarily disable one, remove its block from `config/kratos/kratos.yml.tmpl` (and the corresponding env vars from `docker-compose.yml`'s `kratos-config` env block).

| Provider | Console | Notes |
|---|---|---|
| **Google** | <https://console.cloud.google.com/apis/credentials> → OAuth client (Web) | Set `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET`. |
| **Pocket-ID** | Your Pocket-ID admin UI → create OIDC client | Set `POCKET_ID_ISSUER_URL` to your Pocket-ID base URL (must serve `.well-known/openid-configuration`), plus client id/secret. |
| **GitHub** | <https://github.com/settings/developers> → New OAuth App | Set `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET`. GitHub doesn't issue `email_verified`; we trust the email returned by `user:email` scope. |
| **GitLab** | <https://gitlab.com/-/profile/applications> (or your self-hosted instance) | Set `GITLAB_ISSUER_URL`, `GITLAB_CLIENT_ID`, `GITLAB_CLIENT_SECRET`. Scopes: `openid profile email`. |

To add another provider (Microsoft/Entra, Apple, Discord, …), add an entry under `selfservice.methods.oidc.config.providers` in `config/kratos/kratos.yml.tmpl`, ship a matching mapper jsonnet under `config/kratos/`, list it in `config/render.sh`, and add the env vars to the `kratos-config` init container in `docker-compose.yml`.

### Passkeys (WebAuthn)

Both passkey-passwordless and WebAuthn-2FA methods are enabled by default. `WEBAUTHN_RP_ID` must be a registrable suffix of every origin where you'll use passkeys — typically your cookie domain *without* the leading dot (e.g. `example.com` when `COOKIE_DOMAIN=.example.com`). Once a user logs in and clicks "Add passkey" in the settings UI, they can use it for subsequent logins on any subdomain under `WEBAUTHN_RP_ID`.

## Invitations: pre-create identities + send a 1h link

The default sign-in surface is OIDC + passkey only — no password or email-magic-code login. So practically the only way someone gets in is either (a) you invite them, or (b) they already have a Google/GitHub/etc. account that maps to an existing Kratos identity (which doesn't happen by default — see below). Either way, invitation is the primary onboarding path.

Use the `invite` CLI. It hits Kratos's admin API on the internal Docker network — run it on the host:

```bash
docker run --rm --network=ory_internal \
  -e KRATOS_ADMIN_URL=http://kratos:4434 \
  ghcr.io/korjavin/ory-invite:latest \
  alice@example.com outline notan
```

What happens:

1. The CLI creates a Kratos identity with `traits.email = alice@example.com` and `metadata_admin.groups = ["outline-users", "notan-users"]`. Groups deliberately live under `metadata_admin` (not `traits`) so the user can NOT self-edit them via the Settings page.
2. It generates a Kratos recovery link valid for `KRATOS_RECOVERY_LIFESPAN` (default 1h).
3. It prints the link.

You forward the link to Alice (Telegram, email, however). When she clicks it:

1. Kratos validates the recovery token → creates a session.
2. She lands on `/settings`, where she can pick any combination of: link Google / GitHub / GitLab / Microsoft / Pocket-ID, add a passkey, set a password.
3. All of those credentials attach to the **same** identity, with `groups = ["outline-users", "notan-users"]` already set. Subsequent logins via any of those methods land on the same record.

If she doesn't click within an hour, the link expires — re-run the `invite` command to mint a fresh one (the identity is still there, just dormant).

Flags:

```
invite [flags] <email> <app1> [app2 ...]
  --expires-in 1h         # how long the link is valid
  --first "Alice"         # optional first name
  --last "Doe"            # optional last name
  --extra-groups admins   # additional groups beyond <app>-users
```

## Registering your apps as Hydra OAuth2 clients

There is no Hydra admin UI — you create clients via its admin API. Easiest way is to `docker exec` into the Hydra container and use the bundled CLI.

Each app gets its own Hydra client with `metadata.required_groups` listing the Kratos groups whose members may use it. The custom consent service enforces this: anyone not in at least one of those groups is rejected at consent time, before they ever see the app.

Example: Outline, gated on `outline-users`:

```bash
docker exec -it ory-hydra hydra create client \
  --endpoint http://localhost:4445 \
  --name "Outline" \
  --grant-type authorization_code,refresh_token \
  --response-type code,id_token \
  --scope openid,offline,profile,email,groups \
  --redirect-uri https://outline.example.com/auth/oidc.callback \
  --token-endpoint-auth-method client_secret_basic \
  --metadata '{"required_groups":["outline-users","admins"]}'
```

> Don't pass `--skip-consent`. The consent service must run on every request so it can enforce `required_groups` and inject `groups` into the token.

Hydra prints the generated `client_id` and `client_secret` — paste them into Outline's env vars.

In Outline (or any app), the OIDC discovery URL is:

```
https://hydra.example.com/.well-known/openid-configuration
```

Outline-specific env mapping:

```
OIDC_AUTH_URI=https://hydra.example.com/oauth2/auth
OIDC_TOKEN_URI=https://hydra.example.com/oauth2/token
OIDC_USERINFO_URI=https://hydra.example.com/userinfo
OIDC_DISPLAY_NAME=Sign in
OIDC_SCOPES=openid profile email
```

### Changing required_groups later

You can update a client's metadata without recreating it:

```bash
docker exec -it ory-hydra hydra update client <client-id> \
  --endpoint http://localhost:4445 \
  --metadata '{"required_groups":["outline-users","admins","editors"]}'
```

Changes take effect on the next consent (i.e. the next time a user logs in fresh). Users who already have a refresh token keep working until it expires; revoke their tokens via `hydra revoke token` if you need an immediate cutoff.

## The consent service in one paragraph

`consent/` is a ~250-line Go service that owns the `/consent` URL on `auth.example.com`. On every consent request it:

1. Asks Hydra for the consent challenge details (`/admin/oauth2/auth/requests/consent`).
2. Asks Kratos for the identity (`/admin/identities/<subject>`) — pulls `metadata_admin.groups` (admin-only, not user-editable), plus `traits.email`, `traits.name`.
3. Reads `client.metadata.required_groups`. If non-empty and the user is in none of them → reject. Otherwise → accept.
4. On accept, copies `groups` into both `id_token.groups` and `access_token.groups`, plus standard email/name claims.

Auto-accept (no consent screen) because every Hydra client is first-party (your apps). If you ever expose Hydra to third-party apps, add a confirmation page here.

## Managing users & groups

Kratos has **no built-in admin UI**. For day-to-day work use the `invite` CLI above. For other operations, talk to the admin API on `:4434` (internal-only) directly:

```bash
# List identities
docker exec -it ory-kratos kratos list identities --endpoint http://localhost:4434

# Change someone's groups (replaces the list).
# Groups live under metadata_admin — admin-only, not user-editable from Settings.
docker exec -it ory-kratos kratos patch identity --endpoint http://localhost:4434 \
  <id> -p '[{"op":"replace","path":"/metadata_admin/groups","value":["admin","outline-users","forgejo-users"]}]'

# Revoke all of someone's sessions immediately (e.g. after offboarding)
docker exec -it ory-kratos kratos delete identity --endpoint http://localhost:4434 <id>
```

The `groups` array is the only thing the consent service consults — change it and the next consent (after re-login or token refresh) reflects the new permissions. To force an immediate cutoff, also revoke their Hydra refresh tokens: `docker exec -it ory-hydra hydra revoke token --endpoint http://localhost:4445 <token>`.

If you want a clickable UI later, drop in a community admin tool — e.g. <https://github.com/dfoxg/kratos-admin-ui> — pointed at the same internal Kratos admin URL. Keep it behind oauth2-proxy or your VPN; never expose the admin port publicly.

### Group naming convention

The `invite` CLI assigns groups as `<app>-users` (e.g. `outline-users`, `notan-users`). Match that in each Hydra client's `metadata.required_groups`. You can also add cross-cutting groups (`admins`, `editors`) — `--extra-groups admins` on the invite CLI, then list `admins` in `required_groups` for any app admins should be able to use.

## Deploy in Portainer

1. **Create the Traefik network** if it doesn't exist:
   ```bash
   docker network create traefik_default
   ```
2. **Before first deploy, run the image-building workflows once manually:**
   - `Vendor Ory Images to GHCR` — mirrors `oryd/kratos`, `oryd/hydra`, `oryd/kratos-selfservice-ui-node` to your GHCR namespace.
   - `Build Custom Services` — builds `ory-consent`, `ory-invite`, and `ory-kratos-config` from this repo into GHCR.

   After both succeed, five images exist under `ghcr.io/<owner>/`: `kratos-vendor`, `hydra-vendor`, `kratos-selfservice-ui-node-vendor`, `ory-consent`, `ory-kratos-config`. (`ory-invite` exists too but is only used via `docker run` on demand.)
3. **Create the stack in Portainer** → "Repository" mode, point at this repo, branch `deploy`.
4. **Paste the env vars** from `.env.example` into Portainer's env panel (replace placeholder values).
5. Hit **Deploy**. Watch logs for `kratos-config`, then `kratos-migrate`, then `kratos`, `hydra-migrate`, `hydra`, `login-ui`, `consent` coming up in order.
6. Set up the Portainer **redeploy webhook**, copy its URL into the GitHub repo as the secret `PORTAINER_REDEPLOY_HOOK`. From then on, every push to `master` triggers a redeploy via the `deploy` branch.

## Vendoring images

`.github/workflows/vendor-images.yml` runs weekly (Mondays 04:00 UTC) and:

1. Pulls `oryd/kratos:v1.3.1`, `oryd/hydra:v2.3.0`, and `oryd/kratos-selfservice-ui-node:v1.3.1` from Docker Hub.
2. Re-pushes them as `ghcr.io/<owner>/<name>-vendor:latest`.
3. Logs the upstream digest, force-pushes the `deploy` branch, and pings the Portainer webhook.

Bump the upstream tags in `.github/workflows/vendor-images.yml` when new Ory releases come out, then run the workflow manually with **Run workflow**.

## First-boot smoke test

```bash
# 1. Discovery doc (public)
curl -s https://hydra.example.com/.well-known/openid-configuration | jq .issuer

# 2. Kratos health
curl -s https://auth.example.com/health/ready

# 3. Browser: visit https://auth.example.com/  → log in via Google or Pocket-ID
```

## Switching to Postgres later

Both Kratos and Hydra accept a `DSN` env var. Change `KRATOS_DSN` and `HYDRA_DSN` to a `postgres://...` URL, add a Postgres service to the compose file (or point at an existing one), and restart. The `*-migrate` containers will run any new migrations automatically on the next start.
