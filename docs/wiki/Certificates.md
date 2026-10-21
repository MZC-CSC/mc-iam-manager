# Certificates

mc-iam-manager terminates TLS in `mc-iam-manager-nginx`. Three options exist. `installAll.sh` picks one from `-m` and `-d`, as described in the [Installation Guide](Installation-Guide).

| Option | Selected by | Script | nginx template | Certificate |
|---|---|---|---|---|
| Plain HTTP | `-m dev -d localhost` (also `127.0.0.1`, `mciam.local`) | [`0_preset_local.sh`](https://github.com/m-cmp/mc-iam-manager/blob/main/conf/mc-iam-manager/0_preset_local.sh) | [`nginx.template.local.conf`](https://github.com/m-cmp/mc-iam-manager/blob/main/conf/mc-iam-manager/nginx.template.local.conf) | none |
| Self-signed | `-m dev -d <IP or hostname>` | [`0_preset_dev.sh`](https://github.com/m-cmp/mc-iam-manager/blob/main/conf/mc-iam-manager/0_preset_dev.sh) | [`nginx.template.conf`](https://github.com/m-cmp/mc-iam-manager/blob/main/conf/mc-iam-manager/nginx.template.conf) | OpenSSL, generated locally |
| CA (Let's Encrypt) | `-m prod -d <FQDN>` | [`docker-compose.cert.yaml`](https://github.com/m-cmp/mc-iam-manager/blob/main/docker-compose.cert.yaml) + [`0_preset_prod.sh`](https://github.com/m-cmp/mc-iam-manager/blob/main/conf/mc-iam-manager/0_preset_prod.sh) | [`nginx.template.conf`](https://github.com/m-cmp/mc-iam-manager/blob/main/conf/mc-iam-manager/nginx.template.conf) | certbot, HTTP-01 webroot |

> This page replaces the old wiki pages "자체 인증서 발급" (self-signed) and "CA 인증서 발급" (CA). Those pages referenced `asset/setup/*.sh`, `.env_sample`, `DOMAIN_NAME`/`EMAIL` variables and `dockerfiles/nginx/nginx.conf`, none of which is used any more. The scripts live in `conf/mc-iam-manager/`, the settings come from `.env.setup`, and the generated config is `container-volume/mc-iam-manager/nginx/nginx.conf`.

## Which one do I need?

- **CSP federation (temporary CSP credentials through Keycloak as IdP) requires the CA option.** The CSP has to reach mc-iam-manager's Keycloak over HTTPS at a public name it trusts. With self-signed certificates or plain HTTP, M-CMP still manages infrastructure through mc-infra-manager credentials, but CSP control through mc-iam-manager is not possible.
- Self-signed is for remote test VMs reached by IP or by a hostname without a public certificate.
- Plain HTTP is for a developer PC.

## Files and paths

All paths are relative to the repository root and verified against the scripts and compose files.

| Path on host | Mounted in nginx as | Written by |
|---|---|---|
| `container-volume/mc-iam-manager/nginx/nginx.conf` | `/etc/nginx/nginx.conf` (read-only) | the preset scripts. The prod installer also writes a temporary HTTP-only version. |
| `container-volume/mc-iam-manager/certs/live/<MC_IAM_MANAGER_PUBLIC_DOMAIN>/fullchain.pem` and `privkey.pem` | `/etc/nginx/certs/live/<domain>/...` | `0_preset_dev.sh` (self-signed) or certbot (CA) |
| `container-volume/certbot/www` | `/var/www/certbot` (read-only) | certbot webroot for ACME challenges |

nginx reads the certificate from `/etc/nginx/certs/live/${MC_IAM_MANAGER_PUBLIC_DOMAIN}/` (`ssl_certificate` in `nginx.template.conf`). The self-signed script uses the same `live/<domain>` layout as Let's Encrypt, so one template serves both.

In the generated config, nginx routes `/` to `mc-iam-manager:${MC_IAM_MANAGER_PORT}` and `/auth/` to `mc-iam-manager-kc:${MC_IAM_MANAGER_KEYCLOAK_PORT}/auth/`. Keycloak runs with `KC_HTTP_RELATIVE_PATH=/auth`, so its public URL always ends with `/auth`.

The HTTPS template additionally:

- redirects port 80 to HTTPS, except `/nginx-health`, `/health` and `/.well-known/acme-challenge/`
- accepts TLS 1.2 and 1.3 with ECDHE cipher suites only
- sends `Strict-Transport-Security: max-age=31536000; includeSubDomains` (see [HSTS](#hsts-when-switching-modes))
- turns plain HTTP sent to a TLS port (nginx error 497) into a 301 redirect to HTTPS
- also listens on 3001 (mc-web-console front), `MC_OBSERVABILITY_GRAFANA_PROXY_PORT` and `MC_COST_OPTIMIZER_FE_PROXY_PORT`, for iframe frameworks in a full-platform deployment

## Self-signed certificate

```bash
./installAll.sh -m dev -d <VM_IP_or_hostname> -r background
```

Or run the preset by hand (it reads `../../.env` relative to its own directory):

```bash
cp .env.setup .env            # if not done yet
sed -i 's|^MC_IAM_MANAGER_PUBLIC_DOMAIN=.*|MC_IAM_MANAGER_PUBLIC_DOMAIN=203.0.113.10|' .env
./conf/mc-iam-manager/0_preset_dev.sh
```

What `0_preset_dev.sh` does:

1. Creates `container-volume/mc-iam-manager/certs` and `container-volume/mc-iam-manager/nginx`. It exits with an error if they are not writable (see [Troubleshooting](Troubleshooting#preset-script-cannot-create-or-write-container-volume)).
2. Checks that `MC_IAM_MANAGER_PUBLIC_DOMAIN`, `MC_IAM_MANAGER_KEYCLOAK_DOMAIN`, `MC_IAM_MANAGER_DATABASE_NAME`, `MC_IAM_MANAGER_DATABASE_USER`, `MC_IAM_MANAGER_DATABASE_PASSWORD`, `MC_IAM_MANAGER_DATABASE_HOST` and `MC_IAM_MANAGER_PORT` are set.
3. For a hostname, tries to append `127.0.0.1 <hostname>` to `/etc/hosts`. For an IP, skips this.
4. Deletes any existing `privkey.pem`/`fullchain.pem` and generates a new RSA 2048 key and a certificate valid for 365 days, with `CN=<domain>` and `subjectAltName=IP:<ip>` or `DNS:<name>`.
5. Renders `nginx.template.conf` into `nginx.conf`.

Then start or restart nginx: `docker compose restart mc-iam-manager-nginx`.

Clients must skip verification (`curl -k`) or trust the certificate explicitly. Re-running the script issues a new certificate, so clients that pinned the old one must trust the new one again.

## CA certificate (Let's Encrypt)

Requirements:

- `MC_IAM_MANAGER_PUBLIC_DOMAIN` is a real FQDN whose DNS points to this host
- port 80 is reachable from the internet (HTTP-01 webroot challenge)
- `MC_IAM_MANAGER_CERT_EMAIL` is a valid address

### Issue with the installer

```bash
./installAll.sh -m prod -d iam.example.com -r background
```

The installer starts nginx with a temporary HTTP-only config, runs certbot, renders the HTTPS config with `0_preset_prod.sh`, and restarts nginx.

> **Known issue:** with an unmodified `.env.setup`, `0_preset_prod.sh` exits with status 1 before writing `nginx.conf`, because `MC_COST_OPTIMIZER_BE_PORT` and four related variables are missing and the script runs with `set -euo pipefail`. Add the variables first, as shown in [Installation Guide, Known issue 2](Installation-Guide#before-you-start).

### Issue by hand

```bash
# 1. nginx must be up and serve /.well-known/acme-challenge/ from container-volume/certbot/www on port 80.
#    The HTTPS nginx.conf does that on port 80. For a first issue, use an HTTP-only nginx.conf
#    like the one installAll.sh writes (see the prod branch of installAll.sh).
export COMPOSE_FILE=docker-compose-standalone.yaml
docker compose up -d mc-iam-manager-nginx

# 2. Request the certificate (one-shot container)
docker compose -f docker-compose.cert.yaml --env-file .env up

# 3. Render the HTTPS nginx.conf and reload nginx
./conf/mc-iam-manager/0_preset_prod.sh
docker compose restart mc-iam-manager-nginx
```

Certbot runs as `certbot certonly --webroot --webroot-path /var/www/certbot --email ${MC_IAM_MANAGER_CERT_EMAIL} --agree-tos --no-eff-email -d ${MC_IAM_MANAGER_PUBLIC_DOMAIN} --non-interactive`. On success it prints `Successfully received certificate.` and the files appear under `container-volume/mc-iam-manager/certs/live/<domain>/`.

The certbot container runs as root, so the files are root-owned. `installAll.sh` runs `sudo chown -R $USER:$USER container-volume` afterwards. Do the same after a manual run.

### Renewal

Let's Encrypt certificates expire after 90 days. Renewal is **not** automated by this repository.

```bash
cd /path/to/mc-iam-manager
docker compose -f docker-compose.cert.yaml run --rm mcmp-certbot renew
docker compose -f docker-compose-standalone.yaml exec mc-iam-manager-nginx nginx -s reload
```

`renew` reuses the webroot method recorded at issue time. The HTTPS `nginx.conf` keeps serving `/.well-known/acme-challenge/` on port 80, so nginx can stay up. Reload nginx afterwards so it picks up the new files.

Example cron entry (daily at 12:00):

```cron
0 12 * * * cd /path/to/mc-iam-manager && docker compose -f docker-compose.cert.yaml run --rm mcmp-certbot renew && docker compose -f docker-compose-standalone.yaml exec -T mc-iam-manager-nginx nginx -s reload
```

## HSTS when switching modes

The HTTPS template sends HSTS for one year with `includeSubDomains`. If a browser once reached a **hostname** over HTTPS (self-signed or CA) and you later switch that same hostname to plain HTTP (`0_preset_local.sh`), the browser keeps forcing HTTPS and the site appears unreachable. Fix it by clearing the HSTS entry for that host in the browser (in Chromium: `chrome://net-internals/#hsts`, "Delete domain security policies"), or use a different hostname for HTTP mode. See [Troubleshooting](Troubleshooting#browser-forces-https-after-switching-to-http-hsts).

## Changing the public domain later

The domain appears in several places. After changing `MC_IAM_MANAGER_PUBLIC_DOMAIN`:

1. Re-run the matching preset to regenerate `nginx.conf`, plus the certificate for self-signed or CA.
2. Recreate Keycloak so it picks up the new public URL: `docker compose up -d --force-recreate mc-iam-manager-kc`. Its `KC_HOSTNAME_URL` is `${MC_IAM_MANAGER_PUBLIC_HOST}/auth`.
3. Re-run the post-init container so Step 7 rewrites the Keycloak client redirect URIs ([Initial Setup](Initial-Setup#re-running)).
4. Check the other values in `.env` that embed the domain, such as `MC_IAM_MANAGER_KEYCLOAK_EXTERNAL_URL` and `MC_IAM_MANAGER_AWS_IDENTITY_PROVIDER_ARN`.
