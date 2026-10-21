> 이 문서는 [Troubleshooting](Troubleshooting)의 한국어판입니다. 내용이 다르면 영문판이 기준입니다.

# 문제 해결

각 항목은 증상, 원인, 해결 방법 순으로 정리되어 있습니다. 명령은 레포지토리 루트에서 `export COMPOSE_FILE=docker-compose-standalone.yaml`을 설정했다고 가정합니다([Operations](Operations) 참고).

먼저 확인할 것:

```bash
docker compose ps
docker logs mc-iam-manager-post-initial | tail -n 50
curl -s http://localhost:5000/readyz                      # port = MC_IAM_MANAGER_PORT
curl -s 'http://localhost:5000/readyz?status=all'         # per component
docker compose logs --tail=100 mc-iam-manager
```

---

## 설치와 초기화

### 설치 후 mc-iam-manager가 계속 unhealthy 상태

**증상:** `docker compose ps`에서 mc-iam-manager가 `unhealthy`로 표시됩니다. `docker logs mc-iam-manager-post-initial`의 마지막이 `ERROR: 1_setup_auto.sh failed.` 또는 `Setup failed with errors!`입니다.

**원인:** `/readyz`(컨테이너 헬스 체크)는 [Initial Setup](Initial-Setup)이 Keycloak realm과 클라이언트(Step 1), `mc-infra-manager` 레지스트리 항목(Step 5-1)을 만든 뒤에야 healthy가 됩니다. post-init 실행이 실패하면 서비스는 계속 unhealthy 상태로 남습니다. 흔한 원인은 다음과 같습니다.

1. `MC_IAM_MANAGER_HOST`가 `http://localhost:5000`을 가리킴(다음 항목 참고).
2. 스크립트 실행 시점에 mc-iam-manager가 아직 준비되지 않음(콜드 스타트 경쟁 조건). 컨테이너는 mc-iam-manager가 *시작*되기만 기다린 뒤 60초 대기합니다.
3. mc-infra-manager가 정상이 아니어서 Step 6(`sync-projects`)이 실패함.

**해결:** 원인을 해결하고 의존 서비스가 healthy가 될 때까지 기다린 뒤 post-init 컨테이너를 재실행합니다. 반복 실행해도 안전하지만, [Initial Setup의 Re-running](Initial-Setup#re-running)에 정리된 부수 효과가 있습니다.

```bash
docker rm mc-iam-manager-post-initial 2>/dev/null
docker compose up -d mc-iam-manager-post-initial
docker logs -f mc-iam-manager-post-initial
curl -s http://localhost:5000/readyz        # expect {"status":"healthy"}
```

### post-init Step 1이 HTTP 상태 `000`으로 실패

**증상:** post-init 로그에 `Target URL: http://localhost:5000/api/initial-admin`과 `Platform admin init HTTP Status: 000`이 표시됩니다.

**원인:** [`.env.setup`](https://github.com/m-cmp/mc-iam-manager/blob/main/.env.setup)에 `MC_IAM_MANAGER_HOST`가 두 번 정의되어 있고, 마지막 줄 `MC_IAM_MANAGER_HOST=http://localhost:5000`이 적용됩니다. post-init 컨테이너 안에서 `localhost`는 컨테이너 자신입니다.

**해결:** `.env`에서 그 마지막 줄을 삭제하거나 `MC_IAM_MANAGER_HOST=http://mc-iam-manager:5000`으로 설정한 뒤 post-init 컨테이너를 재실행합니다(이전 항목 참고). 실제 적용값은 `docker compose config | grep MC_IAM_MANAGER_HOST`로 확인할 수 있습니다.

### nginx가 계속 재시작됨: `invalid number of arguments in "listen" directive`

**증상:** `./installAll.sh -m dev -d localhost` 실행 후 `mc-iam-manager-nginx`가 재시작을 반복합니다. `docker compose logs mc-iam-manager-nginx`에 `invalid number of arguments in "listen" directive`가 출력됩니다.

**원인:** `nginx.template.local.conf`는 `MC_COST_OPTIMIZER_BE_PORT`, `MC_COST_OPTIMIZER_ALARM_PORT`, `MC_WORKFLOW_MANAGER_PROXY_PORT`, `MC_DATA_MANAGER_PROXY_PORT`, `MC_APPLICATION_MANAGER_PROXY_PORT`를 사용합니다. 이 변수들이 `.env.setup`에 없어서 `0_preset_local.sh`가 `listen ;`을 생성합니다.

**해결:** 다섯 변수를 `.env`에 추가하고(값은 [설치 가이드의 알려진 이슈 2](Installation-Guide-ko#시작하기-전에) 참고) 다음을 실행합니다.

```bash
./conf/mc-iam-manager/0_preset_local.sh
docker compose restart mc-iam-manager-nginx
docker compose exec mc-iam-manager-nginx nginx -t
```

### `installAll.sh -m prod`가 "Error occurred during production mode configuration"으로 중단

**증상:** 인증서는 발급되었지만(`Certificate generation completed`) Step 3에서 실패합니다. `0_preset_prod.sh`가 `환경변수를 로드합니다...` 출력 직후 멈춥니다.

**원인:** `0_preset_prod.sh`는 `set -euo pipefail`로 실행되며 `MC_COST_OPTIMIZER_BE_PORT` 등 관련 변수 다섯 개를 `grep`으로 읽습니다. `.env`에 키가 없으면 `grep`이 1을 반환하고 스크립트는 `nginx.conf`를 쓰기 전에 종료됩니다. nginx는 임시 HTTP 전용 설정(`cert pending`)으로 계속 서비스하는 상태로 남습니다.

**해결:** 다섯 변수를 `.env`에 추가한 뒤(이전 항목 참고) `./conf/mc-iam-manager/0_preset_prod.sh`와 `docker compose restart mc-iam-manager-nginx`를 실행합니다. 인증서를 다시 발급할 필요는 없습니다.

### 프리셋 스크립트가 `container-volume`을 생성하거나 쓸 수 없음

**증상:** `0_preset_dev.sh` 또는 `0_preset_local.sh`가 `Cannot create .../container-volume/mc-iam-manager/...` 또는 `exists but is not writable`을 출력합니다.

**원인:** 이전 Docker 실행(또는 root로 실행되는 certbot)이 root 소유 디렉터리를 남겼습니다.

**해결:** 디렉터리 소유권을 현재 사용자에게 돌려준 뒤 프리셋을 다시 실행합니다.

```bash
sudo chown "$USER:$USER" container-volume container-volume/mc-iam-manager
sudo chown -R "$USER:$USER" container-volume/mc-iam-manager/certs container-volume/mc-iam-manager/nginx 2>/dev/null
./conf/mc-iam-manager/0_preset_dev.sh
```

스크립트가 안내하는 `../../bin/cleanAll.sh`는 mc-admin-cli에만 있습니다. [`readme.md`](https://github.com/m-cmp/mc-iam-manager/blob/main/readme.md)는 `container-volume/mc-iam-manager/postgres`와 `.../keycloak` 삭제를 안내하지만, 이렇게 하면 **IAM 데이터베이스와 Keycloak 데이터베이스가 모두 삭제됩니다**. 버려도 되는 설치 환경에서만 사용하세요.

### 설치 스크립트가 마지막에 출력한 URL이 동작하지 않음

**증상:** 모니터링 결과로 `mc-iam-manager : http://localhost:5005/readyz`와 `Keycloak admin : http://localhost:8080/admin/`이 출력되지만 둘 다 접속되지 않습니다.

**원인:** `installAll.sh`가 자기 셸에 `.env`를 로드하지 않아 포트가 `5005`로 대체됩니다. 또 Keycloak은 `/auth` 아래에서 서비스됩니다.

**해결:** `http://localhost:${MC_IAM_MANAGER_PORT}/readyz`(기본값 5000)와 `http://localhost:8080/auth/admin/`을 사용합니다.

### `docker compose up -d`로 mc-web-console 컨테이너까지 기동됨

**원인:** `installAll.sh`는 `COMPOSE_FILE=docker-compose-standalone.yaml`을 자기 프로세스 안에서만 export합니다. 사용자 셸에서 `docker compose`는 기본적으로 `docker-compose.yaml`(전체 구성)을 사용합니다.

**해결:** 필요 없다면 `docker compose -f docker-compose.yaml stop mc-web-console-front mc-web-console-api mc-web-console-db`로 중지하고, 이후에는 `export COMPOSE_FILE=docker-compose-standalone.yaml`을 설정해 사용합니다.

---

## 서비스 의존성

### mc-iam-manager와 nginx가 서로를 기다림

**증상:** 둘 다 healthy가 되지 않고, `/readyz?status=all`에 `"nginx":"unhealthy"`가 표시됩니다.

**원인:** mc-iam-manager의 `/readyz`는 `http://mc-iam-manager-nginx/nginx-health`를 호출합니다([`src/handler/health_handler.go`](https://github.com/m-cmp/mc-iam-manager/blob/main/src/handler/health_handler.go)). compose 파일에서 nginx가 mc-iam-manager의 **healthy** 상태를 기다리도록 설정되어 있으면(`depends_on: mc-iam-manager: condition: service_healthy`) 두 서비스가 서로를 영원히 기다립니다. 일부 mc-admin-cli 버전을 포함한 예전 또는 사용자 수정 compose 파일에 이런 순환 대기가 있었습니다. 이 레포지토리의 compose 파일은 nginx가 `mc-iam-manager-kc`(`service_started`)에만 의존합니다.

**해결:** nginx 기동 조건을 mc-iam-manager의 health로 걸지 마세요. `service_started`를 쓰거나 의존성을 두지 않습니다. 다른 서비스에서 nginx를 제외한 mc-iam-manager 생존 확인이 필요하면 `/readyz?status=mc-iam-manager`(DB만 확인)를 사용합니다.

### nginx가 `host not found in upstream "mc-iam-manager"`로 재시작됨

**증상:** HTTPS 모드에서 기동 중에 nginx가 여러 번 재시작됩니다.

**원인:** `nginx.template.conf`는 정적 `proxy_pass http://mc-iam-manager:...`를 사용하므로 nginx가 설정을 로드할 때 이름이 해석되어야 합니다. mc-iam-manager는 mc-infra-manager가 healthy가 된 뒤에야 시작되며, 이 과정에 몇 분이 걸릴 수 있습니다.

**해결:** 별도 조치는 필요 없습니다. `restart: unless-stopped`에 의해 mc-iam-manager가 실행될 때까지 재시도합니다. 끝내 복구되지 않으면 mc-iam-manager나 mc-infra-manager가 실행되지 않는 이유를 확인합니다(`docker compose ps`, `docker compose logs mc-infra-manager`).

### mc-iam-manager가 아예 시작되지 않음

**증상:** `mc-iam-manager` 서비스가 `Created` 상태에서 `Up`으로 바뀌지 않습니다.

**원인:** mc-iam-manager는 `mc-iam-manager-db`와 `mc-iam-manager-kc`가 healthy가 되는 것**과 함께** `mc-infra-manager`가 healthy가 되는 것도 기다립니다. mc-infra-manager는 다시 자신의 PostgreSQL과 OpenBao를 기다립니다.

**해결:** `docker compose ps`로 healthy가 아닌 의존 서비스를 찾아 로그를 확인합니다. 헬스 체크 간격이 기본 1분이므로 몇 분 정도 여유를 두세요.

### mc-infra-manager가 실행 중인데 `/readyz`가 unhealthy로 보고함

**원인:** 헬스 체크 URL은 Initial Setup Step 5-1에서 생성되는 서비스 레지스트리(`mcmp_api_services`)의 `mc-infra-manager` 행에서 가져옵니다. 이 행이 생기기 전이거나 행의 base URL이 잘못되어 있으면 체크가 실패합니다.

**해결:** Initial Setup을 완료하거나 재실행합니다. 또는 `.env`에 `MC_INFRA_MANAGER_HEALTH_URL=http://mc-infra-manager:1323/tumblebug/readyz`를 설정하고 mc-iam-manager를 재생성합니다.

---

## Keycloak

### Keycloak이 준비되지 않음 / unhealthy

**증상:** `mc-iam-manager-kc`가 `starting` 또는 `unhealthy` 상태에 머뭅니다. post-init Step 1이 `Failed to login as admin`으로 실패합니다.

**원인과 해결:**

- Keycloak은 모든 데이터를 PostgreSQL에 저장합니다(`KC_DB: postgres`, `KC_DB_URL: jdbc:postgresql://mc-iam-manager-db:5432/${MC_IAM_MANAGER_KEYCLOAK_DATABASE_NAME}`). `docker compose logs mc-iam-manager-kc`에서 연결 오류를 확인하고, Keycloak 데이터베이스가 존재하는지 확인합니다. `init-db.sh`는 DB를 맨 처음 기동할 때만 이 데이터베이스를 생성합니다.

  ```bash
  docker compose exec mc-iam-manager-db sh -c 'psql -U "$POSTGRES_USER" -lqt' | cut -d'|' -f1
  ```

- 헬스 체크는 `GET /auth/`를 요청해 200 또는 302를 기대합니다. 최초 기동은 시간이 걸리고, 기본 체크 간격은 1분입니다.
- `Failed to login as admin`은 `MC_IAM_MANAGER_KEYCLOAK_ADMIN` 또는 `MC_IAM_MANAGER_KEYCLOAK_ADMIN_PASSWORD`가 Keycloak `master` 관리자와 일치하지 않는다는 뜻입니다. Keycloak은 최초 기동 시에만 `KEYCLOAK_ADMIN`/`KEYCLOAK_ADMIN_PASSWORD`로 관리자를 만들기 때문에, 나중에 `.env` 값을 바꿔도 반영되지 않습니다. Keycloak 관리 콘솔에서 비밀번호를 변경하거나 Keycloak 데이터베이스를 초기화하세요.

### mc-iam-manager가 기동 중 `Failed to initialize Keycloak`으로 종료됨

**원인:** `InitKeycloak`이 확인하는 변수 중 하나가 비어 있습니다: `MC_IAM_MANAGER_KEYCLOAK_HOST`, `MC_IAM_MANAGER_KEYCLOAK_REALM`, `MC_IAM_MANAGER_KEYCLOAK_CLIENT_NAME`, `MC_IAM_MANAGER_KEYCLOAK_CLIENT_SECRET`, `MC_IAM_MANAGER_KEYCLOAK_OIDC_CLIENT_ID`, `MC_IAM_MANAGER_KEYCLOAK_OIDC_CLIENT_NAME`, `MC_IAM_MANAGER_KEYCLOAK_OIDC_CLIENT_SECRET`.

**해결:** `.env`에 해당 값을 설정합니다. [Configuration Reference](Configuration-Reference#keycloak)를 참고하세요.

### `.env`에서 비밀번호 변경 후 플랫폼 관리자 로그인 실패

**원인:** `POST /api/initial-admin`은 사용자가 처음 생성될 때만 Keycloak 비밀번호를 설정합니다. 재실행 시에는 기존 비밀번호가 유지되므로, 새 `MC_IAM_MANAGER_PLATFORMADMIN_PASSWORD`로 로그인하는 Step 2가 실패합니다.

**해결:** Keycloak 관리 콘솔(`/auth/admin/`)에서 realm `MC_IAM_MANAGER_KEYCLOAK_REALM`의 해당 사용자 비밀번호를 새 값으로 설정하거나, `.env`를 이전 값으로 되돌립니다.

### 도메인 변경 후 로그인 리다이렉트나 토큰이 잘못된 호스트를 사용함

**원인:** Keycloak 공개 URL(`KC_HOSTNAME_URL=${MC_IAM_MANAGER_PUBLIC_HOST}/auth`)과 클라이언트 redirect URI(Initial Setup Step 7)가 여전히 이전 도메인을 사용합니다.

**해결:** [인증서의 공개 도메인 변경](Certificates-ko#공개-도메인-변경) 절차를 따릅니다.

---

## 인증서와 HTTPS

### Let's Encrypt 발급 실패

**확인 사항:**

- DNS: `MC_IAM_MANAGER_PUBLIC_DOMAIN`의 A 레코드가 이 호스트를 가리키는지.
- 80 포트가 인터넷에서 열려 있고, nginx가 `container-volume/certbot/www`에서 `/.well-known/acme-challenge/`를 제공하는지.
- `MC_IAM_MANAGER_CERT_EMAIL`이 유효한지.
- `mc-iam-manager_mc-iam-manager-network` 네트워크가 존재하는지. `docker-compose.cert.yaml`은 이 네트워크를 external network로 사용하므로 메인 스택이 최소 한 번은 기동되어 있어야 합니다. `docker network ls`로 확인하세요.
- 실패가 반복되면 Let's Encrypt rate limit에 걸릴 수 있습니다. 잠시 기다린 뒤 재시도하세요.

### 브라우저에 인증서 경고가 표시됨

자체 서명 인증서(`-m dev -d <IP or hostname>`)에서는 정상 동작입니다. 인증서를 수락하거나 명시적으로 신뢰하고, `curl`에는 `-k`를 사용합니다. 인증서는 `0_preset_dev.sh`를 실행할 때마다 재생성됩니다. CSP 연동에는 CA 인증서를 사용하세요([인증서](Certificates-ko)).

### nginx 기동 실패: 인증서 파일을 찾을 수 없음

**증상:** `cannot load certificate "/etc/nginx/certs/live/<domain>/fullchain.pem"`.

**원인:** HTTPS `nginx.conf`가 `container-volume/mc-iam-manager/certs/live/<domain>/` 아래에 인증서가 없는 도메인으로 렌더링되었습니다. `MC_IAM_MANAGER_PUBLIC_DOMAIN`을 바꿨거나, certbot이 성공하기 전에 `0_preset_prod.sh`를 실행한 경우에 발생합니다.

**해결:** 현재 도메인의 인증서를 발급한 뒤(자체 서명: `0_preset_dev.sh`, CA: [인증서](Certificates-ko)) nginx를 재시작합니다. `docker compose exec mc-iam-manager-nginx nginx -t`로 확인하세요.

### HTTP로 전환한 뒤 브라우저가 HTTPS를 강제함 (HSTS)

**증상:** 호스트를 HTTPS 모드에서 일반 HTTP(`0_preset_local.sh`)로 전환한 뒤에도 브라우저가 `https://`로 리다이렉트하고 접속에 실패합니다.

**원인:** HTTPS 템플릿은 `Strict-Transport-Security: max-age=31536000; includeSubDomains`를 전송합니다. 브라우저는 해당 호스트명에 대해 이 정책을 1년간 기억합니다.

**해결:** 브라우저에서 해당 호스트명의 HSTS 정책을 삭제하거나(Chromium: `chrome://net-internals/#hsts`의 "Delete domain security policies", Firefox: 방문 기록에서 해당 사이트 삭제), HTTP 모드에는 다른 호스트명을 사용합니다(예: `mciam.local` 대신 `localhost`).

### HTTPS 포트로 일반 HTTP 요청

nginx가 `https://`로 301 리다이렉트를 응답합니다(`error_page 497`). HTTPS 모드에서는 443, 3001, `MC_OBSERVABILITY_GRAFANA_PROXY_PORT`, `MC_COST_OPTIMIZER_FE_PROXY_PORT` 포트에 `https://`를 사용하세요.

---

## 데이터

### Initial Setup 재실행 후 프로젝트가 첫 번째 워크스페이스로 되돌아감

**원인:** Step 8은 모든 프로젝트를 첫 번째 워크스페이스에 할당하며, 프로젝트는 하나의 워크스페이스에만 속할 수 있습니다.

**해결:** 프로젝트를 다시 할당합니다(`POST /api/workspaces/assign/projects`). 앞으로는 사용 중인 시스템에서 post-init 컨테이너 전체를 재실행하지 마세요. [Initial Setup의 Re-running](Initial-Setup#re-running)을 참고하세요.

### YAML의 메뉴 변경이 반영되지 않음

**원인:** `POST /api/setup/initial-menus`는 메뉴가 이미 있으면 건너뜁니다. 최초 설치 이후에는 DB가 기준(source of truth)입니다.

**해결:** [Menu and Role Permission](Menu-and-Role-Permission)을 참고하세요(자동 백업과 함께 `?force=true` 사용, 또는 `/api/menus`로 수정).
