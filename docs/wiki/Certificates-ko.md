> 이 문서는 [Certificates](Certificates)의 한국어판입니다. 내용이 다르면 영문판이 기준입니다.

# 인증서

mc-iam-manager의 TLS 종료는 `mc-iam-manager-nginx`에서 처리합니다. 선택지는 세 가지이며, [설치 가이드](Installation-Guide-ko)에 설명한 대로 `installAll.sh`가 `-m`과 `-d` 값에 따라 하나를 고릅니다.

| 방식 | 선택 조건 | 스크립트 | nginx 템플릿 | 인증서 |
|---|---|---|---|---|
| 일반 HTTP | `-m dev -d localhost` (`127.0.0.1`, `mciam.local`도 동일) | [`0_preset_local.sh`](https://github.com/m-cmp/mc-iam-manager/blob/main/conf/mc-iam-manager/0_preset_local.sh) | [`nginx.template.local.conf`](https://github.com/m-cmp/mc-iam-manager/blob/main/conf/mc-iam-manager/nginx.template.local.conf) | 없음 |
| 자체 서명 | `-m dev -d <IP 또는 호스트명>` | [`0_preset_dev.sh`](https://github.com/m-cmp/mc-iam-manager/blob/main/conf/mc-iam-manager/0_preset_dev.sh) | [`nginx.template.conf`](https://github.com/m-cmp/mc-iam-manager/blob/main/conf/mc-iam-manager/nginx.template.conf) | OpenSSL로 로컬 생성 |
| CA (Let's Encrypt) | `-m prod -d <FQDN>` | [`docker-compose.cert.yaml`](https://github.com/m-cmp/mc-iam-manager/blob/main/docker-compose.cert.yaml) + [`0_preset_prod.sh`](https://github.com/m-cmp/mc-iam-manager/blob/main/conf/mc-iam-manager/0_preset_prod.sh) | [`nginx.template.conf`](https://github.com/m-cmp/mc-iam-manager/blob/main/conf/mc-iam-manager/nginx.template.conf) | certbot, HTTP-01 webroot |

> 이 문서는 기존 위키 문서 "자체 인증서 발급"(자체 서명)과 "CA 인증서 발급"(CA)을 대체합니다. 기존 문서는 `asset/setup/*.sh`, `.env_sample`, `DOMAIN_NAME`/`EMAIL` 변수, `dockerfiles/nginx/nginx.conf`를 참조했지만 지금은 모두 사용하지 않습니다. 현재 스크립트는 `conf/mc-iam-manager/`에 있고, 설정은 `.env.setup`에서 가져오며, 생성되는 설정 파일은 `container-volume/mc-iam-manager/nginx/nginx.conf`입니다.

## 어떤 방식이 필요한가요?

- **CSP 연동(Keycloak을 IdP로 사용하는 임시 CSP 자격 증명 발급)에는 CA 방식이 필요합니다.** CSP가 신뢰할 수 있는 공개 이름으로 mc-iam-manager의 Keycloak에 HTTPS로 접근할 수 있어야 하기 때문입니다. 자체 서명 인증서나 일반 HTTP에서도 M-CMP는 mc-infra-manager 자격 증명으로 인프라를 관리할 수 있지만, mc-iam-manager를 통한 CSP 제어는 불가능합니다.
- 자체 서명은 IP 또는 공인 인증서가 없는 호스트명으로 접속하는 원격 테스트 VM용입니다.
- 일반 HTTP는 개발자 PC용입니다.

## 파일과 경로

모든 경로는 레포지토리 루트 기준이며, 스크립트와 compose 파일로 확인한 내용입니다.

| 호스트 경로 | nginx 내부 마운트 위치 | 작성 주체 |
|---|---|---|
| `container-volume/mc-iam-manager/nginx/nginx.conf` | `/etc/nginx/nginx.conf` (읽기 전용) | 프리셋 스크립트. prod 설치 시에는 임시 HTTP 전용 버전도 설치 스크립트가 작성합니다. |
| `container-volume/mc-iam-manager/certs/live/<MC_IAM_MANAGER_PUBLIC_DOMAIN>/fullchain.pem`, `privkey.pem` | `/etc/nginx/certs/live/<domain>/...` | `0_preset_dev.sh`(자체 서명) 또는 certbot(CA) |
| `container-volume/certbot/www` | `/var/www/certbot` (읽기 전용) | ACME 챌린지용 certbot webroot |

nginx는 `/etc/nginx/certs/live/${MC_IAM_MANAGER_PUBLIC_DOMAIN}/`에서 인증서를 읽습니다(`nginx.template.conf`의 `ssl_certificate`). 자체 서명 스크립트도 Let's Encrypt와 같은 `live/<domain>` 구조를 사용하므로 템플릿 하나로 두 방식을 모두 처리합니다.

생성된 설정에서 nginx는 `/`를 `mc-iam-manager:${MC_IAM_MANAGER_PORT}`로, `/auth/`를 `mc-iam-manager-kc:${MC_IAM_MANAGER_KEYCLOAK_PORT}/auth/`로 라우팅합니다. Keycloak은 `KC_HTTP_RELATIVE_PATH=/auth`로 실행되므로 공개 URL은 항상 `/auth`로 끝납니다.

HTTPS 템플릿은 추가로 다음을 수행합니다.

- `/nginx-health`, `/health`, `/.well-known/acme-challenge/`를 제외한 80 포트 요청을 HTTPS로 리다이렉트
- TLS 1.2, 1.3과 ECDHE 암호 스위트만 허용
- `Strict-Transport-Security: max-age=31536000; includeSubDomains` 헤더 전송([HSTS](#모드-전환-시-hsts) 참고)
- TLS 포트로 들어온 일반 HTTP 요청(nginx 오류 497)을 HTTPS로 301 리다이렉트
- 전체 플랫폼 배포 시 iframe 프레임워크를 위해 3001(mc-web-console front), `MC_OBSERVABILITY_GRAFANA_PROXY_PORT`, `MC_COST_OPTIMIZER_FE_PROXY_PORT` 포트도 수신

## 자체 서명 인증서

```bash
./installAll.sh -m dev -d <VM_IP_or_hostname> -r background
```

프리셋을 직접 실행할 수도 있습니다(스크립트 자신의 디렉터리 기준으로 `../../.env`를 읽습니다).

```bash
cp .env.setup .env            # if not done yet
sed -i 's|^MC_IAM_MANAGER_PUBLIC_DOMAIN=.*|MC_IAM_MANAGER_PUBLIC_DOMAIN=203.0.113.10|' .env
./conf/mc-iam-manager/0_preset_dev.sh
```

`0_preset_dev.sh`가 하는 일:

1. `container-volume/mc-iam-manager/certs`와 `container-volume/mc-iam-manager/nginx`를 생성합니다. 쓰기 권한이 없으면 오류로 종료합니다([문제 해결](Troubleshooting-ko#프리셋-스크립트가-container-volume을-생성하거나-쓸-수-없음) 참고).
2. `MC_IAM_MANAGER_PUBLIC_DOMAIN`, `MC_IAM_MANAGER_KEYCLOAK_DOMAIN`, `MC_IAM_MANAGER_DATABASE_NAME`, `MC_IAM_MANAGER_DATABASE_USER`, `MC_IAM_MANAGER_DATABASE_PASSWORD`, `MC_IAM_MANAGER_DATABASE_HOST`, `MC_IAM_MANAGER_PORT`가 설정되어 있는지 확인합니다.
3. 호스트명이면 `/etc/hosts`에 `127.0.0.1 <hostname>` 추가를 시도합니다. IP면 건너뜁니다.
4. 기존 `privkey.pem`/`fullchain.pem`을 삭제하고, `CN=<domain>`, `subjectAltName=IP:<ip>` 또는 `DNS:<name>`으로 유효기간 365일의 RSA 2048 키와 인증서를 새로 생성합니다.
5. `nginx.template.conf`를 렌더링해 `nginx.conf`를 만듭니다.

그다음 nginx를 기동하거나 재시작합니다: `docker compose restart mc-iam-manager-nginx`.

클라이언트는 검증을 건너뛰거나(`curl -k`) 인증서를 명시적으로 신뢰해야 합니다. 스크립트를 다시 실행하면 인증서가 새로 발급되므로, 기존 인증서를 고정(pin)해 둔 클라이언트는 새 인증서를 다시 신뢰해야 합니다.

## CA 인증서 (Let's Encrypt)

요구 사항:

- `MC_IAM_MANAGER_PUBLIC_DOMAIN`이 DNS가 이 호스트를 가리키는 실제 FQDN일 것
- 80 포트가 인터넷에서 접근 가능할 것(HTTP-01 webroot 챌린지)
- `MC_IAM_MANAGER_CERT_EMAIL`이 유효한 주소일 것

### 설치 스크립트로 발급

```bash
./installAll.sh -m prod -d iam.example.com -r background
```

설치 스크립트는 임시 HTTP 전용 설정으로 nginx를 기동하고, certbot을 실행한 뒤, `0_preset_prod.sh`로 HTTPS 설정을 렌더링하고 nginx를 재시작합니다.

> **알려진 이슈:** `.env.setup`을 수정하지 않은 상태에서는 `MC_COST_OPTIMIZER_BE_PORT` 등 관련 변수 다섯 개가 없고 스크립트가 `set -euo pipefail`로 실행되므로, `0_preset_prod.sh`가 `nginx.conf`를 쓰기 전에 종료 코드 1로 끝납니다. [설치 가이드의 알려진 이슈 2](Installation-Guide-ko#시작하기-전에)대로 먼저 변수를 추가하세요.

### 수동 발급

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

Certbot은 `certbot certonly --webroot --webroot-path /var/www/certbot --email ${MC_IAM_MANAGER_CERT_EMAIL} --agree-tos --no-eff-email -d ${MC_IAM_MANAGER_PUBLIC_DOMAIN} --non-interactive`로 실행됩니다. 성공하면 `Successfully received certificate.`가 출력되고 `container-volume/mc-iam-manager/certs/live/<domain>/` 아래에 파일이 생깁니다.

certbot 컨테이너는 root로 실행되므로 생성된 파일도 root 소유입니다. `installAll.sh`는 이후 `sudo chown -R $USER:$USER container-volume`을 실행합니다. 수동으로 발급했다면 같은 작업을 직접 해 주세요.

### 갱신

Let's Encrypt 인증서는 90일 후 만료됩니다. 이 레포지토리는 갱신을 자동화하지 **않습니다**.

```bash
cd /path/to/mc-iam-manager
docker compose -f docker-compose.cert.yaml run --rm mcmp-certbot renew
docker compose -f docker-compose-standalone.yaml exec mc-iam-manager-nginx nginx -s reload
```

`renew`는 발급 시 기록된 webroot 방식을 그대로 사용합니다. HTTPS `nginx.conf`는 80 포트에서 계속 `/.well-known/acme-challenge/`를 제공하므로 nginx를 내리지 않아도 됩니다. 갱신 후에는 새 파일을 읽도록 nginx를 reload하세요.

cron 등록 예시(매일 12:00):

```cron
0 12 * * * cd /path/to/mc-iam-manager && docker compose -f docker-compose.cert.yaml run --rm mcmp-certbot renew && docker compose -f docker-compose-standalone.yaml exec -T mc-iam-manager-nginx nginx -s reload
```

## 모드 전환 시 HSTS

HTTPS 템플릿은 `includeSubDomains`와 함께 1년짜리 HSTS를 전송합니다. 브라우저가 어떤 **호스트명**에 HTTPS(자체 서명 또는 CA)로 한 번이라도 접속한 뒤 같은 호스트명을 일반 HTTP(`0_preset_local.sh`)로 전환하면, 브라우저가 계속 HTTPS를 강제하므로 사이트에 접속할 수 없는 것처럼 보입니다. 브라우저에서 해당 호스트의 HSTS 항목을 삭제하거나(Chromium: `chrome://net-internals/#hsts`의 "Delete domain security policies"), HTTP 모드에는 다른 호스트명을 사용하세요. [문제 해결](Troubleshooting-ko#http로-전환한-뒤-브라우저가-https를-강제함-hsts)을 참고하세요.

## 공개 도메인 변경

도메인은 여러 곳에 쓰입니다. `MC_IAM_MANAGER_PUBLIC_DOMAIN`을 바꾼 뒤에는 다음을 수행하세요.

1. 해당 프리셋을 다시 실행해 `nginx.conf`를 재생성합니다. 자체 서명이나 CA 방식이면 인증서도 다시 만듭니다.
2. 새 공개 URL을 반영하도록 Keycloak을 재생성합니다: `docker compose up -d --force-recreate mc-iam-manager-kc`. Keycloak의 `KC_HOSTNAME_URL`은 `${MC_IAM_MANAGER_PUBLIC_HOST}/auth`입니다.
3. Step 7에서 Keycloak 클라이언트 redirect URI를 다시 쓰도록 post-init 컨테이너를 재실행합니다([Initial Setup](Initial-Setup#re-running)).
4. `MC_IAM_MANAGER_KEYCLOAK_EXTERNAL_URL`, `MC_IAM_MANAGER_AWS_IDENTITY_PROVIDER_ARN`처럼 도메인이 포함된 `.env`의 다른 값도 확인합니다.
