# Operations

Day-2 tasks for a deployment made from this repository. Commands assume the repository root and the standalone compose file:

```bash
export COMPOSE_FILE=docker-compose-standalone.yaml   # or docker-compose.yaml for the full set
```

> Without `COMPOSE_FILE`, a plain `docker compose` uses `docker-compose.yaml`, which also defines the mc-web-console services. `docker compose up -d` would then start them too. See [Deployment Modes](Deployment-Modes).

## Logs

| What | Command |
|---|---|
| mc-iam-manager (API, request log, Keycloak and DB errors) | `docker compose logs -f mc-iam-manager` |
| Keycloak | `docker compose logs -f mc-iam-manager-kc` |
| IAM PostgreSQL | `docker compose logs -f mc-iam-manager-db` |
| nginx | `docker compose logs -f mc-iam-manager-nginx` |
| Initial setup run | `docker logs mc-iam-manager-post-initial` |
| mc-infra-manager / mc-infra-connector | `docker compose logs -f mc-infra-manager mc-infra-connector` |

Notes:

- The mc-iam-manager container has no fixed name (`mc-iam-manager-mc-iam-manager-1` by default). Use `docker compose logs mc-iam-manager`. `docker logs mc-iam-manager` does not work.
- mc-iam-manager also appends its log to `../app.log` relative to its working directory ([`src/main.go`](https://github.com/m-cmp/mc-iam-manager/blob/main/src/main.go)). In the image (`WORKDIR /app`) that is `/app.log` inside the container, which is not persisted: `docker compose exec mc-iam-manager tail -f /app.log`.
- Set `MC_IAM_MANAGER_DEBUG=true` only temporarily: it puts internal error strings into HTTP error responses.
- mc-infra-manager and mc-infra-connector also write logs to `container-volume/mc-infra-manager/log/` and `container-volume/mc-infra-connector/log/`.

## Health checks

| Endpoint | Checks | Notes |
|---|---|---|
| `GET http://<host>:${MC_IAM_MANAGER_PORT}/readyz` | Keycloak realm and client exist, DB `SELECT 1`, `http://mc-iam-manager-nginx/nginx-health`, mc-infra-manager `/readyz` | 200 `{"status":"healthy"}`, or 503 with `unhealthy_components`. Used by the compose health check. |
| `GET /readyz?status=mc-iam-manager` | DB only | Liveness of mc-iam-manager itself |
| `GET /readyz?status=keycloak,db,nginx,mc-infra-manager` (or `status=all`) | the listed components | Returns one field per component |
| `GET http(s)://<PUBLIC_DOMAIN>/health` | proxied by nginx to mc-iam-manager `/readyz` | |
| `GET http://<PUBLIC_DOMAIN>/nginx-health` | nginx itself | Returns `nginx is healthy` |
| `GET http://localhost:${MC_IAM_MANAGER_KEYCLOAK_PORT}/auth/` | Keycloak (compose health check expects 200 or 302) | |
| `GET http://localhost:${MC_INFRA_MANAGER_PORT}/tumblebug/readyz` | mc-infra-manager | |
| `GET http://localhost:${MC_INFRA_CONNECTOR_PORT}/spider/readyz` | mc-infra-connector | |

Source: [`src/handler/health_handler.go`](https://github.com/m-cmp/mc-iam-manager/blob/main/src/handler/health_handler.go). The query parameter is `status`. The Swagger annotation names it `detail`, but that name is ignored.

The mc-infra-manager URL is the `mc-infra-manager` base URL from the service registry (`mcmp_api_services`) plus `/readyz`, or `MC_INFRA_MANAGER_HEALTH_URL` if set. On a fresh install the registry row exists only after [Initial Setup](Initial-Setup) Step 5-1. Until then `/readyz` reports mc-infra-manager as unhealthy.

Most services use a health check interval of `HEALTH_CHECK_INTERVAL` (default `1m`) with `HEALTH_CHECK_START_PERIOD` (default `60s`), so `docker compose ps` can lag a few minutes behind the real state.

## Data layout

All state is in bind mounts under `container-volume/`. The compose files declare no named volumes.

| Path | Content |
|---|---|
| `container-volume/mc-iam-manager/postgres/postgres_data` | PostgreSQL 14 data directory with **both** databases: `MC_IAM_MANAGER_DATABASE_NAME` (IAM, default `mc_iam_manager_db`) and `MC_IAM_MANAGER_KEYCLOAK_DATABASE_NAME` (Keycloak, default `mc_iam_keycloak_db`) |
| `container-volume/mc-iam-manager/keycloak/data` | Keycloak's local data directory. Realms, users and clients are **not** here: Keycloak uses `KC_DB: postgres` against `mc-iam-manager-db`. |
| `container-volume/mc-iam-manager/nginx/nginx.conf` | Generated nginx config |
| `container-volume/mc-iam-manager/certs/` | TLS certificates (`live/<domain>/`) |
| `container-volume/certbot/www` | ACME webroot |
| `container-volume/mc-infra-manager/`, `container-volume/mc-infra-connector/`, `container-volume/openbao-data` | Infra framework data (etcd, cb-tumblebug PostgreSQL, cb-spider meta DB, OpenBao) |

Both databases are created by [`conf/mc-iam-manager/init-db.sh`](https://github.com/m-cmp/mc-iam-manager/blob/main/conf/mc-iam-manager/init-db.sh). PostgreSQL runs that script only when the data directory is empty.

## Backup

Back up the two databases together, because Keycloak user IDs are referenced from the IAM DB. The commands run inside the DB container, where `POSTGRES_USER` and the `MC_IAM_MANAGER_*` variables are set from `.env`.

```bash
TS=$(date +%Y%m%d-%H%M%S)
docker compose exec -T mc-iam-manager-db sh -c \
  'pg_dump -U "$POSTGRES_USER" -Fc "$MC_IAM_MANAGER_DATABASE_NAME"' > "iam-db-$TS.dump"
docker compose exec -T mc-iam-manager-db sh -c \
  'pg_dump -U "$POSTGRES_USER" -Fc "$MC_IAM_MANAGER_KEYCLOAK_DATABASE_NAME"' > "keycloak-db-$TS.dump"
```

Also keep `.env` (secrets, domain) and `container-volume/mc-iam-manager/certs/`.

> The `tar container-volume/keycloak/` backup in [`readme.md`](https://github.com/m-cmp/mc-iam-manager/blob/main/readme.md) does not capture Keycloak's data, and the path does not exist. Use `pg_dump` of the Keycloak database.

For a cold, file-level backup, stop the stack and archive `container-volume/mc-iam-manager/postgres/` (use `sudo`, because the files are owned by the postgres UID 70).

## Restore

```bash
# 1. Stop the services that use the databases
docker compose stop mc-iam-manager mc-iam-manager-kc

# 2. Restore into the existing databases (drops and recreates the objects)
docker compose exec -T mc-iam-manager-db sh -c \
  'pg_restore -U "$POSTGRES_USER" -d "$MC_IAM_MANAGER_DATABASE_NAME" --clean --if-exists' < iam-db-<TS>.dump
docker compose exec -T mc-iam-manager-db sh -c \
  'pg_restore -U "$POSTGRES_USER" -d "$MC_IAM_MANAGER_KEYCLOAK_DATABASE_NAME" --clean --if-exists' < keycloak-db-<TS>.dump

# 3. Start again
docker compose start mc-iam-manager-kc mc-iam-manager
```

On a brand-new host, start `mc-iam-manager-db` alone first so `init-db.sh` creates the empty databases, then restore. Do not let the post-init container run against a restored system unless you want the [re-run effects](Initial-Setup#re-running).

## Upgrade

```bash
# 0. Back up (see above)

# 1. Get the new version
git pull

# 2. Compare env templates and add new variables to .env by hand
diff <(grep -oE '^[A-Z_][A-Z0-9_]*' .env.setup | sort -u) <(grep -oE '^[A-Z_][A-Z0-9_]*' .env | sort -u)

# 3. Get the new mc-iam-manager image (prebuilt edge) or build it locally
docker compose pull mc-iam-manager        # or: docker compose build mc-iam-manager

# 4. Recreate
docker compose up -d mc-iam-manager
docker compose logs -f mc-iam-manager
```

- Re-running `installAll.sh` also appends missing variables to `.env`, but it re-runs the preset too. In dev mode that regenerates the self-signed certificate, and in prod mode it requests a certificate again. Prefer the manual `diff`.
- If the nginx templates changed, re-run the matching `0_preset_*.sh` and `docker compose restart mc-iam-manager-nginx`.
- The compose files pin the infra framework image versions. Changing them is an mc-infra-manager or mc-infra-connector upgrade and follows their own procedures.
- New menu entries are not applied automatically, because `POST /api/setup/initial-menus` skips when menus exist. See [Menu and Role Permission](Menu-and-Role-Permission) for `?force=true` and the backup and restore endpoints.

## Database migrations

- **Automatic:** on every start, mc-iam-manager runs GORM `AutoMigrate` for all its models ([`src/main.go`](https://github.com/m-cmp/mc-iam-manager/blob/main/src/main.go)). That creates missing tables and adds missing columns. It does not drop or rename columns.
- **Manual SQL:** [`scripts/migration/`](https://github.com/m-cmp/mc-iam-manager/tree/main/scripts/migration) holds idempotent scripts (`ADD COLUMN IF NOT EXISTS`) for schema changes to `mcmp_menus`:
  - `20260713_mcmp_menus_menu_resource.sql`: `view_type`, `framework_service`, `path`
  - `20260826_mcmp_menus_icon.sql`: `icon`

  The same columns are declared in the `Menu` model, so `AutoMigrate` normally adds them. Apply the scripts when upgrading a database whose schema `AutoMigrate` could not change, or before starting a new binary against an old DB:

  ```bash
  docker compose exec -T mc-iam-manager-db sh -c \
    'psql -U "$POSTGRES_USER" -d "$MC_IAM_MANAGER_DATABASE_NAME" -v ON_ERROR_STOP=1' \
    < scripts/migration/20260826_mcmp_menus_icon.sql
  ```

## Stop, reset and clean up (`clearAll.sh`)

[`clearAll.sh`](https://github.com/m-cmp/mc-iam-manager/blob/main/clearAll.sh) operates on `docker-compose-standalone.yaml`.

| Mode | How | What happens |
|---|---|---|
| Stop only | `./clearAll.sh`, choose 1 | `docker compose down`: containers and networks are removed. `container-volume/` and `.env` are kept. Restart with `docker compose up -d`. |
| Full reset | `./clearAll.sh`, choose 2, or `./clearAll.sh --full` | Asks `Are you sure? (yes/N)` (also with `--full`). Then `docker compose down -v`, **`sudo rm -rf container-volume/`**, and deletes `.env` (or restores it with `git checkout` if `.env` is tracked). |

> **Full reset is destructive and cannot be undone.** It deletes:
> - the IAM database **and** the Keycloak database (all users, roles, realms)
> - certificates, including Let's Encrypt certificates (re-issuing counts against Let's Encrypt rate limits)
> - **all infra framework data** in the same folder: cb-tumblebug PostgreSQL and etcd, cb-spider meta DB (registered CSP connections and credentials), OpenBao data, and mc-web-console data if you used the full set
> - your `.env`, including every secret and customization
>
> Back up first. Note that `down -v` itself removes nothing extra: no named volumes are declared, and the data loss comes from `rm -rf container-volume/`.

Other caveats:

- Although the usage text calls `--full` non-interactive, it still prompts for `yes`.
- It only knows the standalone file. If you started the full set (`docker-compose.yaml`), the mc-web-console containers are not part of that file and keep running. Stop them with `docker compose -f docker-compose.yaml down`.
- The preset scripts' error messages mention `../../bin/cleanAll.sh`. That path exists only in mc-admin-cli. In this repository the script is `./clearAll.sh`.

## Certificates

Renewal and domain changes are described in [Certificates](Certificates#renewal).
