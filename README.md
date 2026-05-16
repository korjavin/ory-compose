# Ory Stack (Kratos + Hydra + Login UI) for Portainer

A git-ops Docker Compose deployment that gives you:

- **Ory Kratos** — identity management (registration, login, profile, social sign-in via Google + Pocket-ID).
- **Ory Hydra** — OAuth2 / OIDC provider you point your other apps at (Outline, Forgejo, etc.).
- **kratos-selfservice-ui-node** — reference Login / Registration / Consent UI that bridges Kratos and Hydra.

Both Kratos and Hydra run on **SQLite** with data persisted in named Docker volumes — lightweight, no Postgres dependency.

## Architecture

```
       Browser
          │
          ▼
  ┌─────────────────┐         ┌──────────────────┐
  │   Login UI      │ ◀──────▶│  Kratos (public) │
  │ auth.example.com│         │ kratos.example.. │
  └────────┬────────┘         └─────────┬────────┘
           │                            │
           │ consent / login challenge  │ identity
           ▼                            │
  ┌─────────────────┐                   │
  │  Hydra (public) │ ◀─── identity ───┘
  │ hydra.example.. │
  └────────┬────────┘
           │ OIDC discovery + tokens
           ▼
   Your apps (Outline, Forgejo, …) — configure them
   with hydra.example.com/.well-known/openid-configuration
```

Trust boundaries:

| Endpoint                   | Network           | Exposure                       |
|----------------------------|-------------------|--------------------------------|
| Kratos public (`:4433`)    | traefik + internal| `https://${KRATOS_PUBLIC_HOST}`|
| Kratos admin  (`:4434`)    | internal only     | never on the public internet   |
| Hydra public  (`:4444`)    | traefik + internal| `https://${HYDRA_PUBLIC_HOST}` |
| Hydra admin   (`:4445`)    | internal only     | never on the public internet   |
| Login UI      (`:3000`)    | traefik + internal| `https://${LOGIN_UI_HOST}`     |

Admin APIs are reachable from other containers on the `ory_internal` Docker network. To talk to them from your laptop, use `docker exec ory-hydra hydra ...` or open a temporary SSH tunnel.

## Files

```
.
├── docker-compose.yml
├── .env.example
├── config/
│   └── kratos/
│       ├── kratos.yml.tmpl          # rendered at startup with envsubst
│       ├── identity.schema.json     # user shape (incl. groups[])
│       ├── oidc.google.jsonnet      # Google → Kratos identity mapper
│       └── oidc.pocket-id.jsonnet   # Pocket-ID → Kratos identity mapper
└── .github/workflows/
    ├── deploy.yml                   # push deploy branch → Portainer webhook
    └── vendor-images.yml            # weekly: pull oryd/* → push to GHCR
```

## How environment variables are wired

You said you don't want a `.env` file on the server — Portainer holds the values. The compose file references env names with sensible defaults; only **hostnames** and **secrets** truly need to be set in Portainer's stack-env panel.

Kratos itself doesn't natively read `${VAR}` from its YAML config. We work around that with a tiny init container (`kratos-config`) that runs `envsubst` on `kratos.yml.tmpl` and writes the rendered `kratos.yml` into a shared volume that the Kratos service mounts read-only.

## Required secrets

Generate each one independently with `openssl rand -hex 32`:

| Variable                       | Purpose                                |
|--------------------------------|----------------------------------------|
| `KRATOS_COOKIE_SECRET`         | signs Kratos session cookies           |
| `KRATOS_CIPHER_SECRET`         | encrypts secrets at rest in Kratos     |
| `HYDRA_SECRETS_SYSTEM`         | encrypts Hydra DB rows                 |
| `HYDRA_SECRETS_COOKIE`         | signs Hydra cookies                    |
| `HYDRA_PAIRWISE_SALT`          | salt for pairwise OIDC subject IDs     |
| `LOGIN_UI_COOKIE_SECRET`       | signs Login UI cookies                 |
| `LOGIN_UI_CSRF_COOKIE_SECRET`  | signs Login UI CSRF cookies            |

## DNS & TLS

Set up three DNS A/AAAA records pointing to your Hetzner host:

```
kratos.example.com   → host
hydra.example.com    → host
auth.example.com     → host
```

Traefik handles certs via the `myresolver` (or whatever you set in `TRAEFIK_CERTRESOLVER`). The cookie domain must be a parent that covers all three — set `COOKIE_DOMAIN=.example.com`.

## Setting up the social providers

### Google
1. Create OAuth client at <https://console.cloud.google.com/apis/credentials>.
2. Application type: **Web application**.
3. Authorized redirect URI:
   `https://${KRATOS_PUBLIC_HOST}/self-service/methods/oidc/callback/google`
4. Copy the client ID + secret into `GOOGLE_CLIENT_ID` and `GOOGLE_CLIENT_SECRET`.

### Pocket-ID
1. In your Pocket-ID admin UI, create an OIDC client.
2. Redirect URI:
   `https://${KRATOS_PUBLIC_HOST}/self-service/methods/oidc/callback/pocket-id`
3. Set `POCKET_ID_ISSUER_URL=https://pocket-id.example.com/` (must serve `.well-known/openid-configuration`).
4. Copy the client ID + secret into `POCKET_ID_CLIENT_ID` and `POCKET_ID_CLIENT_SECRET`.

If a provider's client_id/secret is left blank, Kratos still starts — but that provider's button won't function. To add a third social provider (GitHub, Microsoft, Apple, …), add a new entry under `selfservice.methods.oidc.config.providers` in `config/kratos/kratos.yml.tmpl` and ship a corresponding mapper jsonnet.

## Registering your apps as Hydra OAuth2 clients

There is no Hydra admin UI — you create clients via its admin API. Easiest way is to `docker exec` into the Hydra container and use the bundled CLI.

Example: register Outline.

```bash
docker exec -it ory-hydra hydra create client \
  --endpoint http://localhost:4445 \
  --name "Outline" \
  --grant-type authorization_code,refresh_token \
  --response-type code,id_token \
  --scope openid,offline,profile,email,groups \
  --redirect-uri https://outline.example.com/auth/oidc.callback \
  --token-endpoint-auth-method client_secret_basic \
  --skip-consent
```

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

## Managing users & groups

Kratos has **no built-in admin UI**. You manage identities via the Admin API on `:4434` (internal-only). A few common ops:

```bash
# List identities
docker exec -it ory-kratos kratos list identities --endpoint http://localhost:4434

# Create an admin user
docker exec -it ory-kratos kratos import identities --endpoint http://localhost:4434 - <<'JSON'
{
  "schema_id": "default",
  "traits": {
    "email": "you@example.com",
    "name": { "first": "You", "last": "Admin" },
    "groups": ["admin"]
  }
}
JSON

# Add a user to a group (PATCH the identity's traits)
# Replace <id> with the identity's UUID from `list identities`.
docker exec -it ory-kratos kratos patch identity --endpoint http://localhost:4434 \
  <id> -p '{"op":"replace","path":"/traits/groups","value":["admin","editors"]}'
```

If you want a clickable UI later, drop in a community admin tool — e.g. <https://github.com/dfoxg/kratos-admin-ui> — pointed at the same internal Kratos admin URL. Keep it behind oauth2-proxy or your VPN; never expose the admin port publicly.

### Surfacing groups in tokens

`groups` is part of every identity's traits. To make Hydra include them in access/ID tokens, add a `--token-claim` mapping when you create the OAuth2 client, or write a Hydra **consent** webhook that copies `kratos_session.identity.traits.groups` into the token's `session.access_token.groups`. The reference Login UI does the consent step — extend it (or fork it) when you need richer claims.

## Deploy in Portainer

1. **Create the Traefik network** if it doesn't exist:
   ```bash
   docker network create traefik_default
   ```
2. **Create the stack in Portainer** → "Repository" mode, point at this repo, branch `deploy`.
3. **Paste the env vars** from `.env.example` into Portainer's env panel (replace placeholder values).
4. Hit **Deploy**. Watch logs for `kratos-config`, then `kratos-migrate`, then `kratos`, `hydra-migrate`, `hydra`, `login-ui` coming up in order.
5. Set up the Portainer **redeploy webhook**, copy its URL into the GitHub repo as the secret `PORTAINER_REDEPLOY_HOOK`. From then on, every push to `master` triggers a redeploy via the `deploy` branch.

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
curl -s https://kratos.example.com/health/ready

# 3. Browser: visit https://auth.example.com/  → log in via Google or Pocket-ID
```

## Switching to Postgres later

Both Kratos and Hydra accept a `DSN` env var. Change `KRATOS_DSN` and `HYDRA_DSN` to a `postgres://...` URL, add a Postgres service to the compose file (or point at an existing one), and restart. The `*-migrate` containers will run any new migrations automatically on the next start.
