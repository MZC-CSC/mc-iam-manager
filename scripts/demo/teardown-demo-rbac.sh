#!/usr/bin/env bash
# scripts/demo/setup-demo-rbac.sh가 만든 것을 전부 되돌린다:
#   - 데모 사용자 15명 삭제(Keycloak 계정 + role/워크스페이스/조직 매핑까지 DeleteUser가 함께 정리)
#   - 팀(조직) 20개 노드 삭제(부모 5개를 ?cascade=true로 삭제하면 자식 15개까지 함께 정리됨)
#   - 신규 역할 13종 삭제(billviewer/billadmin은 이 스크립트가 만든 게 아니라 원래 있던 기본 역할이라
#     건드리지 않는다 — role-permission-backup-demo-rbac.yaml에도 이 둘은 애초에 없음.
#     참고로 기본 5역할은 POST /api/roles로 생성돼 predefined=false라, 서버의 "predefined는 삭제 불가"
#     가드가 이들을 지켜주지 않는다 — 아래 NEW_ROLES 목록으로 직접 제외하는 것이 유일한 방어선이다)
#
# permission.yaml/온보딩과 무관 — setup-demo-rbac.sh와 마찬가지로 수동으로 한 번 실행하는 선택적 스크립트.
# 멱등 — 이미 지워진 대상은 조회 시 못 찾으면 조용히 건너뛴다(재실행 안전).
#
# 파괴적 작업이라 기본은 DRY-RUN이다. 무엇이 지워질지 먼저 보여주고, --apply를 줘야 실제로 지운다.
#
# 사용법: setup-demo-rbac.sh와 동일한 환경변수(MC_IAM_MANAGER_HOST, MC_IAM_MANAGER_PLATFORMADMIN_ID/PASSWORD).
#   MC_IAM_MANAGER_HOST=http://<host>:<port> \
#   MC_IAM_MANAGER_PLATFORMADMIN_ID=<platform admin id> \
#   MC_IAM_MANAGER_PLATFORMADMIN_PASSWORD=<platform admin password> \
#   bash scripts/demo/teardown-demo-rbac.sh            # 미리보기(삭제 안 함)
#   bash scripts/demo/teardown-demo-rbac.sh --apply    # 확인 후 삭제
#   bash scripts/demo/teardown-demo-rbac.sh --apply --yes   # 확인 없이 삭제
# WORKSPACE_NAME은 쓰지 않는다(사용자 삭제가 워크스페이스 role 매핑까지 함께 정리하므로).

set -euo pipefail

MC_IAM_MANAGER_HOST="${MC_IAM_MANAGER_HOST:-http://localhost:5000}"
APPLY=false
ASSUME_YES=false

usage() {
  sed -n '2,22p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit 0
}

while [ $# -gt 0 ]; do
  case "$1" in
    --apply) APPLY=true ;;
    --yes|-y) ASSUME_YES=true ;;
    -h|--help) usage ;;
    *) echo "ERROR: 알 수 없는 인자: $1 (사용법은 --help)" >&2; exit 1 ;;
  esac
  shift
done

command -v curl >/dev/null || { echo "ERROR: curl이 필요합니다." >&2; exit 1; }
command -v jq >/dev/null || { echo "ERROR: jq가 필요합니다." >&2; exit 1; }

# ---- 5모듈 x 3단계 데이터 — setup-demo-rbac.sh와 동일 ----
declare -A MODULE_TEAM=(
  [wf]="Workflow Team"
  [app]="Application Team"
  [data]="Data Team"
  [bill]="Cost Team"
  [mon]="Observability Team"
)
MODULE_ORDER=(wf app data bill mon)

declare -A ROLE_NAME=(
  [wf.viewer]="wfviewer"     [wf.operator]="wfoperator"     [wf.admin]="wfadmin"
  [app.viewer]="appviewer"   [app.operator]="appoperator"   [app.admin]="appadmin"
  [data.viewer]="dataviewer" [data.operator]="dataoperator" [data.admin]="dataadmin"
  [bill.viewer]="billviewer" [bill.operator]="billoperator" [bill.admin]="billadmin"
  [mon.viewer]="monviewer"   [mon.operator]="monoperator"   [mon.admin]="monadmin"
)
TIER_ORDER=(viewer operator admin)

# billviewer/billadmin은 이 스크립트가 만든 게 아니므로 삭제 대상에서 제외
NEW_ROLES=(wfviewer wfoperator wfadmin appviewer appoperator appadmin
           dataviewer dataoperator dataadmin billoperator monviewer monoperator monadmin)

# ---- HTTP 헬퍼 (setup-demo-rbac.sh와 동일) ----
HTTP_STATUS=""
API_BODY=""

api_call() {
  local method="$1" path="$2" body="${3:-}"
  local resp
  if [ -n "$body" ]; then
    resp=$(curl -s -w '\n%{http_code}' -X "$method" \
      --header "Authorization: Bearer $ACCESS_TOKEN" \
      --header 'Content-Type: application/json' \
      --data "$body" "$MC_IAM_MANAGER_HOST$path")
  else
    resp=$(curl -s -w '\n%{http_code}' -X "$method" \
      --header "Authorization: Bearer $ACCESS_TOKEN" \
      --header 'Content-Type: application/json' \
      "$MC_IAM_MANAGER_HOST$path")
  fi
  HTTP_STATUS=$(echo "$resp" | tail -n1)
  API_BODY=$(echo "$resp" | sed '$d')
}

login() {
  local id="${MC_IAM_MANAGER_PLATFORMADMIN_ID:-}"
  local pw="${MC_IAM_MANAGER_PLATFORMADMIN_PASSWORD:-}"
  [ -n "$id" ] || read -p "Platform admin ID: " id
  if [ -z "$pw" ]; then
    read -s -p "Platform admin password: " pw
    echo
  fi
  local body resp
  body=$(jq -n --arg id "$id" --arg pw "$pw" '{id: $id, password: $pw}')
  resp=$(curl -s --header 'Content-Type: application/json' --data "$body" "$MC_IAM_MANAGER_HOST/api/auth/login")
  ACCESS_TOKEN=$(echo "$resp" | jq -r '.access_token // empty')
  [ -n "$ACCESS_TOKEN" ] || { echo "ERROR: 로그인 실패: $resp" >&2; exit 1; }
  echo "로그인 성공"
}

# ---- 조직 조회 ----
ORGS_JSON=""
fetch_orgs() { api_call GET "/api/groups"; ORGS_JSON="$API_BODY"; }

# 이름 + 부모로 조직 id 조회. parent_id는 *uint + omitempty라 최상위 조직은 필드가 아예 없어서
# (.parent_id // null) 로 정규화해 비교한다 — setup-demo-rbac.sh의 ensure_org와 동일한 필터.
find_org_id() {
  local name="$1" parent_id="${2:-}"
  local pid_repr="${parent_id:-null}"
  echo "$ORGS_JSON" | jq -r --arg name "$name" --argjson pid "$pid_repr" \
    'map(select(.name == $name and ((.parent_id // null) == $pid))) | (.[0].id // empty)'
}

# ================= 1단계: 삭제 대상 수집 =================

login

echo ""
echo "== 삭제 대상 수집 (host=$MC_IAM_MANAGER_HOST) =="

# 팀 이름에 공백이 있으므로 "id|name" 형식으로 담고 파라미터 확장으로 분해한다
USER_TARGETS=()   # "id|username"
ORG_TARGETS=()    # "id|name"
ROLE_TARGETS=()   # "id|name"

for m in "${MODULE_ORDER[@]}"; do
  for t in "${TIER_ORDER[@]}"; do
    username="demo-${ROLE_NAME[$m.$t]}"
    api_call GET "/api/users/name/$username"
    if [ "$HTTP_STATUS" = "200" ]; then
      USER_TARGETS+=("$(echo "$API_BODY" | jq -r '.id')|$username")
    fi
  done
done

fetch_orgs
# 팀 조직은 MZC 하위에 만들어진다(setup-demo-rbac.sh). 트리 어딘가의 동명 조직을 잘못 지우지 않도록
# 반드시 MZC 하위로 스코프해서 찾는다.
MZC_ROOT_ID="$(find_org_id "MZC" "")"
if [ -z "$MZC_ROOT_ID" ]; then
  echo "  최상위 조직 'MZC' 없음 — 삭제할 팀 조직 없음"
else
  for m in "${MODULE_ORDER[@]}"; do
    team="${MODULE_TEAM[$m]}"
    org_id="$(find_org_id "$team" "$MZC_ROOT_ID")"
    [ -n "$org_id" ] && ORG_TARGETS+=("$org_id|$team")
  done
fi

for role in "${NEW_ROLES[@]}"; do
  api_call GET "/api/roles/name/$role?roleType=platform"
  if [ "$HTTP_STATUS" = "200" ]; then
    ROLE_TARGETS+=("$(echo "$API_BODY" | jq -r '.id')|$role")
  fi
done

echo ""
echo "-- 데모 사용자 (${#USER_TARGETS[@]}) --"
for e in ${USER_TARGETS[@]+"${USER_TARGETS[@]}"}; do echo "   ${e#*|} (id=${e%%|*})"; done
echo "-- 팀 조직 부모, cascade로 자식까지 (${#ORG_TARGETS[@]}) --"
for e in ${ORG_TARGETS[@]+"${ORG_TARGETS[@]}"}; do echo "   ${e#*|} (id=${e%%|*})"; done
echo "-- 신규 역할, billviewer/billadmin 제외 (${#ROLE_TARGETS[@]}) --"
for e in ${ROLE_TARGETS[@]+"${ROLE_TARGETS[@]}"}; do echo "   ${e#*|} (id=${e%%|*})"; done

TOTAL=$((${#USER_TARGETS[@]} + ${#ORG_TARGETS[@]} + ${#ROLE_TARGETS[@]}))
if [ "$TOTAL" -eq 0 ]; then
  echo ""
  echo "삭제할 대상이 없습니다 — 이미 정리된 상태입니다."
  exit 0
fi

if [ "$APPLY" != "true" ]; then
  echo ""
  echo "DRY-RUN — 아무것도 삭제하지 않았습니다. 실제 삭제하려면 --apply 를 붙이세요."
  exit 0
fi

if [ "$ASSUME_YES" != "true" ]; then
  echo ""
  echo "!! $MC_IAM_MANAGER_HOST 에서 위 ${TOTAL}건을 삭제합니다. 되돌리려면 setup-demo-rbac.sh를 다시 실행해야 합니다."
  read -p "계속하려면 'yes' 입력: " confirm
  [ "$confirm" = "yes" ] || { echo "취소했습니다."; exit 0; }
fi

# ================= 2단계: 삭제 =================
# 순서 주의 — 사용자를 먼저 지워야 조직에 소속 사용자가 남지 않는다.
# DELETE /api/users/id/:id 가 Keycloak 계정 + platform/workspace role 매핑 + 조직 소속까지 함께 정리한다.

echo ""
echo "== 데모 사용자 삭제 =="
for e in ${USER_TARGETS[@]+"${USER_TARGETS[@]}"}; do
  user_id="${e%%|*}"; username="${e#*|}"
  api_call DELETE "/api/users/id/$user_id"
  if [ "$HTTP_STATUS" = "204" ] || [ "$HTTP_STATUS" = "200" ]; then
    echo "  user '$username' 삭제(id=$user_id)"
  else
    echo "ERROR: user '$username' 삭제 실패 ($HTTP_STATUS): $API_BODY" >&2
    exit 1
  fi
done

echo ""
echo "== 팀(조직) 삭제 (부모를 cascade=true로 — 자식 3개씩 함께) =="
for e in ${ORG_TARGETS[@]+"${ORG_TARGETS[@]}"}; do
  org_id="${e%%|*}"; org_name="${e#*|}"
  api_call DELETE "/api/groups/id/$org_id?cascade=true"
  if [ "$HTTP_STATUS" = "204" ] || [ "$HTTP_STATUS" = "200" ]; then
    echo "  org '$org_name' 삭제(id=$org_id, 하위 조직 포함 cascade)"
  else
    echo "ERROR: org '$org_name' 삭제 실패 ($HTTP_STATUS): $API_BODY" >&2
    exit 1
  fi
done

echo ""
echo "== 신규 역할 삭제 (billviewer/billadmin은 기존 역할이라 제외) =="
for e in ${ROLE_TARGETS[@]+"${ROLE_TARGETS[@]}"}; do
  role_id="${e%%|*}"; role_name="${e#*|}"
  # DELETE인데도 핸들러가 c.Bind로 roleType을 읽으므로 바디가 필요하다.
  api_call DELETE "/api/roles/id/$role_id" "$(jq -n '{roleType: "platform"}')"
  if [ "$HTTP_STATUS" = "200" ] || [ "$HTTP_STATUS" = "204" ]; then
    echo "  role '$role_name' 삭제(id=$role_id)"
  else
    echo "ERROR: role '$role_name' 삭제 실패 ($HTTP_STATUS): $API_BODY" >&2
    exit 1
  fi
done

echo ""
echo "완료 — 데모 RBAC 셋업을 되돌렸다(사용자 ${#USER_TARGETS[@]} / 팀 부모 ${#ORG_TARGETS[@]} / 역할 ${#ROLE_TARGETS[@]})."
echo "참고: 역할 삭제는 Keycloak realm role 자체를 지우지 않는다(삭제 API 없음). 재실행 시 기존 realm role이 재사용된다."
