# Troubleshooting

Each entry lists the symptom, the cause and the fix. Commands assume the repository root and `export COMPOSE_FILE=docker-compose-standalone.yaml` (see [Operations](Operations)).

First look at:

```bash
docker compose ps
docker logs mc-iam-manager-post-initial | tail -n 50
curl -s http://localhost:5000/readyz                      # port = MC_IAM_MANAGER_PORT
curl -s 'http://localhost:5000/readyz?status=all'         # per component
docker compose logs --tail=100 mc-iam-manager
```

---

## Installation and initialization

### mc-iam-manager stays unhealthy after install

**Symptom:** `docker compose ps` shows mc-iam-manager as `unhealthy`. `docker logs mc-iam-manager-post-initial` ends with `ERROR: 1_setup_auto.sh failed.` or `Setup failed with errors!`.

**Cause:** `/readyz` (the container health check) is healthy only after [Initial Setup](Initial-Setup) has created the Keycloak realm and client (Step 1) and the `mc-infra-manager` registry entry (Step 5-1). If the post-init run failed, the service stays unhealthy. Common reasons:

1. `MC_IAM_MANAGER_HOST` points to `http://localhost:5000` (see the next entry).
2. mc-iam-manager was not ready yet when the script ran (cold-start race). The container only waits for mc-iam-manager to be *started* and then sleeps 60 seconds.
3. mc-infra-manager is not healthy, so Step 6 (`sync-projects`) fails.

**Fix:** fix the cause, wait until the dependencies are healthy, then re-run the post-init container. It is safe to repeat, with the side effects listed in [Initial Setup, Re-running](Initial-Setup#re-running).

```bash
docker rm mc-iam-manager-post-initial 2>/dev/null
docker compose up -d mc-iam-manager-post-initial
docker logs -f mc-iam-manager-post-initial
curl -s http://localhost:5000/readyz        # expect {"status":"healthy"}
```

### Post-init Step 1 fails with HTTP status `000`

**Symptom:** the post-init log shows `Target URL: http://localhost:5000/api/initial-admin` and `Platform admin init HTTP Status: 000`.

**Cause:** [`.env.setup`](https://github.com/m-cmp/mc-iam-manager/blob/main/.env.setup) defines `MC_IAM_MANAGER_HOST` twice, and its last line `MC_IAM_MANAGER_HOST=http://localhost:5000` wins. Inside the post-init container, `localhost` is the container itself.

**Fix:** in `.env`, remove that last line, or set `MC_IAM_MANAGER_HOST=http://mc-iam-manager:5000`. Then re-run the post-init container (previous entry). Check the effective value with `docker compose config | grep MC_IAM_MANAGER_HOST`.

### nginx keeps restarting: `invalid number of arguments in "listen" directive`

**Symptom:** after `./installAll.sh -m dev -d localhost`, `mc-iam-manager-nginx` restarts in a loop. `docker compose logs mc-iam-manager-nginx` shows `invalid number of arguments in "listen" directive`.

**Cause:** `nginx.template.local.conf` uses `MC_COST_OPTIMIZER_BE_PORT`, `MC_COST_OPTIMIZER_ALARM_PORT`, `MC_WORKFLOW_MANAGER_PROXY_PORT`, `MC_DATA_MANAGER_PROXY_PORT` and `MC_APPLICATION_MANAGER_PROXY_PORT`. These are not in `.env.setup`, so `0_preset_local.sh` renders `listen ;`.

**Fix:** add the five variables to `.env` (values in [Installation Guide, Known issue 2](Installation-Guide#before-you-start)), then:

```bash
./conf/mc-iam-manager/0_preset_local.sh
docker compose restart mc-iam-manager-nginx
docker compose exec mc-iam-manager-nginx nginx -t
```

### `installAll.sh -m prod` stops with "Error occurred during production mode configuration"

**Symptom:** the certificate was issued (`Certificate generation completed`), but Step 3 fails. `0_preset_prod.sh` stops right after `환경변수를 로드합니다...` ("loading environment variables").

**Cause:** `0_preset_prod.sh` runs with `set -euo pipefail` and reads `MC_COST_OPTIMIZER_BE_PORT` and four related variables with `grep`. When a key is missing from `.env`, `grep` returns 1, and the script exits before writing `nginx.conf`. nginx is left serving the temporary HTTP-only config (`cert pending`).

**Fix:** add the five variables to `.env` (see the previous entry), then run `./conf/mc-iam-manager/0_preset_prod.sh` and `docker compose restart mc-iam-manager-nginx`. The certificate does not need to be issued again.

### Preset script cannot create or write `container-volume`

**Symptom:** `0_preset_dev.sh` or `0_preset_local.sh` prints `Cannot create .../container-volume/mc-iam-manager/...` or `exists but is not writable`.

**Cause:** a previous Docker run (or certbot, which runs as root) left root-owned directories.

**Fix:** give the directories back to your user, then run the preset again:

```bash
sudo chown "$USER:$USER" container-volume container-volume/mc-iam-manager
sudo chown -R "$USER:$USER" container-volume/mc-iam-manager/certs container-volume/mc-iam-manager/nginx 2>/dev/null
./conf/mc-iam-manager/0_preset_dev.sh
```

The scripts suggest `../../bin/cleanAll.sh`, which exists only in mc-admin-cli. [`readme.md`](https://github.com/m-cmp/mc-iam-manager/blob/main/readme.md) suggests deleting `container-volume/mc-iam-manager/postgres` and `.../keycloak`, which **wipes the IAM and Keycloak databases**. Do that only on a throw-away install.

### The installer's final URLs do not work

**Symptom:** the monitor prints `mc-iam-manager : http://localhost:5005/readyz` and `Keycloak admin : http://localhost:8080/admin/`, and both fail.

**Cause:** `installAll.sh` does not load `.env` into its shell, so it falls back to port `5005`. Keycloak serves under `/auth`.

**Fix:** use `http://localhost:${MC_IAM_MANAGER_PORT}/readyz` (default 5000) and `http://localhost:8080/auth/admin/`.

### `docker compose up -d` started mc-web-console containers

**Cause:** `installAll.sh` exports `COMPOSE_FILE=docker-compose-standalone.yaml` only for its own process. In your shell, `docker compose` defaults to `docker-compose.yaml` (the full set).

**Fix:** `docker compose -f docker-compose.yaml stop mc-web-console-front mc-web-console-api mc-web-console-db` if you do not want them, and use `export COMPOSE_FILE=docker-compose-standalone.yaml` from then on.

---

## Service dependencies

### mc-iam-manager and nginx wait on each other

**Symptom:** neither becomes healthy, and `/readyz?status=all` shows `"nginx":"unhealthy"`.

**Cause:** mc-iam-manager's `/readyz` calls `http://mc-iam-manager-nginx/nginx-health` ([`src/handler/health_handler.go`](https://github.com/m-cmp/mc-iam-manager/blob/main/src/handler/health_handler.go)). If a compose file makes nginx wait for mc-iam-manager to be **healthy** (`depends_on: mc-iam-manager: condition: service_healthy`), the two wait on each other forever. Older or customized compose files, including some mc-admin-cli versions, have had this circular wait. The compose files in this repository make nginx depend only on `mc-iam-manager-kc` (`service_started`).

**Fix:** never gate nginx on mc-iam-manager health. Use `service_started`, or no dependency. Use `/readyz?status=mc-iam-manager` (DB only) if another service needs an mc-iam-manager liveness check that does not include nginx.

### nginx restarts with `host not found in upstream "mc-iam-manager"`

**Symptom:** in HTTPS modes, nginx restarts several times during start-up.

**Cause:** `nginx.template.conf` uses static `proxy_pass http://mc-iam-manager:...`, which must resolve when nginx loads its config. mc-iam-manager starts only after mc-infra-manager is healthy, which can take minutes.

**Fix:** none needed. `restart: unless-stopped` retries until mc-iam-manager is running. If it never recovers, check why mc-iam-manager or mc-infra-manager is not running (`docker compose ps`, `docker compose logs mc-infra-manager`).

### mc-iam-manager does not start at all

**Symptom:** the `mc-iam-manager` service shows `Created` but never `Up`.

**Cause:** it waits for `mc-iam-manager-db` and `mc-iam-manager-kc` to be healthy **and** for `mc-infra-manager` to be healthy. mc-infra-manager in turn waits for its PostgreSQL and OpenBao.

**Fix:** `docker compose ps` shows which dependency is not healthy. Check its logs. Health check intervals are 1 minute by default, so allow a few minutes.

### `/readyz` reports `mc-infra-manager` unhealthy although it is running

**Cause:** the health URL comes from the `mc-infra-manager` row in the service registry (`mcmp_api_services`), created by Initial Setup Step 5-1. Before that, or with a wrong base URL in the row, the check fails.

**Fix:** complete or re-run Initial Setup, or set `MC_INFRA_MANAGER_HEALTH_URL=http://mc-infra-manager:1323/tumblebug/readyz` in `.env` and recreate mc-iam-manager.

---

## Keycloak

### Keycloak not ready / unhealthy

**Symptom:** `mc-iam-manager-kc` stays `starting` or `unhealthy`. Post-init Step 1 fails with `Failed to login as admin`.

**Cause and fix:**

- Keycloak stores everything in PostgreSQL (`KC_DB: postgres`, `KC_DB_URL: jdbc:postgresql://mc-iam-manager-db:5432/${MC_IAM_MANAGER_KEYCLOAK_DATABASE_NAME}`). Check `docker compose logs mc-iam-manager-kc` for connection errors, and check that the Keycloak database exists. `init-db.sh` creates it only on the very first DB start:

  ```bash
  docker compose exec mc-iam-manager-db sh -c 'psql -U "$POSTGRES_USER" -lqt' | cut -d'|' -f1
  ```

- The health check requests `GET /auth/` and expects 200 or 302. First starts take time, and the default interval is 1 minute.
- `Failed to login as admin` means `MC_IAM_MANAGER_KEYCLOAK_ADMIN` or `MC_IAM_MANAGER_KEYCLOAK_ADMIN_PASSWORD` does not match the Keycloak `master` admin. Keycloak creates that admin from `KEYCLOAK_ADMIN`/`KEYCLOAK_ADMIN_PASSWORD` only on its first start, so changing the values in `.env` later has no effect. Change the password in the Keycloak admin console, or reset Keycloak's database.

### mc-iam-manager exits at start-up with `Failed to initialize Keycloak`

**Cause:** one of the variables checked by `InitKeycloak` is empty: `MC_IAM_MANAGER_KEYCLOAK_HOST`, `MC_IAM_MANAGER_KEYCLOAK_REALM`, `MC_IAM_MANAGER_KEYCLOAK_CLIENT_NAME`, `MC_IAM_MANAGER_KEYCLOAK_CLIENT_SECRET`, `MC_IAM_MANAGER_KEYCLOAK_OIDC_CLIENT_ID`, `MC_IAM_MANAGER_KEYCLOAK_OIDC_CLIENT_NAME` or `MC_IAM_MANAGER_KEYCLOAK_OIDC_CLIENT_SECRET`.

**Fix:** set it in `.env` and see [Configuration Reference](Configuration-Reference#keycloak).

### Platform admin login fails after changing the password in `.env`

**Cause:** `POST /api/initial-admin` sets the Keycloak password only when the user is first created. A re-run keeps the existing password, so Step 2 (login with the new `MC_IAM_MANAGER_PLATFORMADMIN_PASSWORD`) fails.

**Fix:** set the new password for the user in realm `MC_IAM_MANAGER_KEYCLOAK_REALM` in the Keycloak admin console (`/auth/admin/`), or restore the old value in `.env`.

### Login redirects or tokens use the wrong host after a domain change

**Cause:** Keycloak's public URL (`KC_HOSTNAME_URL=${MC_IAM_MANAGER_PUBLIC_HOST}/auth`) and the client redirect URIs (Initial Setup Step 7) still use the old domain.

**Fix:** follow [Certificates, Changing the public domain later](Certificates#changing-the-public-domain-later).

---

## Certificates and HTTPS

### Let's Encrypt issuance fails

**Checks:**

- DNS: the A record of `MC_IAM_MANAGER_PUBLIC_DOMAIN` points to this host.
- Port 80 is open from the internet, and nginx serves `/.well-known/acme-challenge/` from `container-volume/certbot/www`.
- `MC_IAM_MANAGER_CERT_EMAIL` is valid.
- The network `mc-iam-manager_mc-iam-manager-network` exists. `docker-compose.cert.yaml` uses it as an external network, so the main stack must have been started at least once. Check with `docker network ls`.
- Repeated failures can hit Let's Encrypt rate limits. Wait before retrying.

### Browser shows a certificate warning

Expected with self-signed certificates (`-m dev -d <IP or hostname>`). Accept the certificate or trust it explicitly, and use `curl -k`. The certificate is regenerated on every `0_preset_dev.sh` run. For CSP federation, use a CA certificate ([Certificates](Certificates)).

### nginx fails to start: certificate file not found

**Symptom:** `cannot load certificate "/etc/nginx/certs/live/<domain>/fullchain.pem"`.

**Cause:** the HTTPS `nginx.conf` was rendered for a domain that has no certificate under `container-volume/mc-iam-manager/certs/live/<domain>/`. This happens when `MC_IAM_MANAGER_PUBLIC_DOMAIN` changed, or when `0_preset_prod.sh` ran before certbot succeeded.

**Fix:** issue the certificate for the current domain (self-signed: `0_preset_dev.sh`, CA: [Certificates](Certificates)), then restart nginx. Check with `docker compose exec mc-iam-manager-nginx nginx -t`.

### Browser forces HTTPS after switching to HTTP (HSTS)

**Symptom:** after switching a host from an HTTPS mode to plain HTTP (`0_preset_local.sh`), the browser still redirects to `https://` and fails.

**Cause:** the HTTPS template sends `Strict-Transport-Security: max-age=31536000; includeSubDomains`. The browser remembers it for that hostname for a year.

**Fix:** delete the HSTS policy for that hostname in the browser (Chromium: `chrome://net-internals/#hsts`, "Delete domain security policies"; Firefox: forget the site in history), or use a different hostname for HTTP mode (for example `localhost` instead of `mciam.local`).

### Plain HTTP request to an HTTPS port

nginx answers with a 301 redirect to `https://` (`error_page 497`). Use `https://` for ports 443, 3001, `MC_OBSERVABILITY_GRAFANA_PROXY_PORT` and `MC_COST_OPTIMIZER_FE_PROXY_PORT` in HTTPS modes.

---

## Data

### Initial setup re-run moved my projects back to the first workspace

**Cause:** Step 8 assigns all projects to the first workspace, and a project can belong to only one workspace.

**Fix:** re-assign them (`POST /api/workspaces/assign/projects`). In the future, do not re-run the whole post-init container on a system in use. See [Initial Setup, Re-running](Initial-Setup#re-running).

### Menu changes in YAML are not applied

**Cause:** `POST /api/setup/initial-menus` skips when menus already exist. The DB is the source of truth after the first install.

**Fix:** see [Menu and Role Permission](Menu-and-Role-Permission) (`?force=true` with automatic backup, or edit through `/api/menus`).
