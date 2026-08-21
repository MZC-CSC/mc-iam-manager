#!/usr/bin/env bash
# scripts/demo/setup-demo-rbac.sh가 만든 것을 전부 되돌린다:
#   - 데모 사용자 15명 삭제(Keycloak 계정 + role/워크스페이스/조직 매핑까지 DeleteUser가 함께 정리)
#   - 팀(조직) 20개 노드 삭제(부모 5개를 ?cascade=true로 삭제하면 자식 15개까지 함께 정리됨)
#   - 신규 역할 13종 삭제(billviewer/billadmin은 이 스크립트가 만든 게 아니라 원래 있던 기본 역할이라
#     건드리지 않는다 — role-permission-backup-demo-rbac.yaml에도 이 둘은 애초에 없음)
#
# permission.yaml/온보딩과 무관 — setup-demo-rbac.sh와 마찬가지로 수동으로 한 번 실행하는 선택적 스크립트.
# 멱등 — 이미 지워진 대상은 조회 시 못 찾으면 조용히 건너뛴다(재실행 안전).
#
# 사용법: setup-demo-rbac.sh와 동일한 환경변수(MC_IAM_MANAGER_HOST, MC_IAM_MANAGER_PLATFORMADMIN_ID/PASSWORD).
#   MC_IAM_MANAGER_HOST=http://<host>:<port> \
#   MC_IAM_MANAGER_PLATFORMADMIN_ID=<platform admin id> \
#   MC_IAM_MANAGER_PLATFORMADMIN_PASSWORD=<platform admin password> \
#   bash scripts/demo/teardown-demo-rbac.sh

set -euo pipefail

MC_IAM_MANAGER_HOST="${MC_IAM_MANAGER_HOST:-http://localhost:5000}"

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

# ---- 사용자 삭제 ----
delete_user() {
  local username="$1"
  api_call GET "/api/users/name/$username"
  if [ "$HTTP_STATUS" != "200" ]; then
    echo "  user '$username' 없음 — 스킵"
    return
  fi
  local user_id
  user_id=$(echo "$API_BODY" | jq -r '.id')
  api_call DELETE "/api/users/id/$user_id"
  if [ "$HTTP_STATUS" = "204" ] || [ "$HTTP_STATUS" = "200" ]; then
    echo "  user '$username' 삭제(id=$user_id)"
  else
    echo "ERROR: user '$username' 삭제 실패 ($HTTP_STATUS): $API_BODY" >&2
    exit 1
  fi
}

# ---- 조직(팀) 삭제 ----
ORGS_JSON=""
fetch_orgs() { api_call GET "/api/groups"; ORGS_JSON="$API_BODY"; }

delete_org_cascade() {
  local name="$1"
  fetch_orgs
  # 부모 필터 없이 이름만으로 조회 — 팀 이름("Workflow Team" 등)은 전체 조직 트리에서 유일하다고 가정.
  local org_id
  org_id=$(echo "$ORGS_JSON" | jq -r --arg name "$name" \
    'map(select(.name == $name)) | (.[0].id // empty)')
  if [ -z "$org_id" ]; then
    echo "  org '$name' 없음 — 스킵"
    return
  fi
  api_call DELETE "/api/groups/id/$org_id?cascade=true"
  if [ "$HTTP_STATUS" = "204" ] || [ "$HTTP_STATUS" = "200" ]; then
    echo "  org '$name' 삭제(id=$org_id, 하위 조직 포함 cascade)"
  else
    echo "ERROR: org '$name' 삭제 실패 ($HTTP_STATUS): $API_BODY" >&2
    exit 1
  fi
}

# ---- 역할 삭제 ----
delete_role() {
  local name="$1"
  api_call GET "/api/roles/name/$name?roleType=platform"
  if [ "$HTTP_STATUS" != "200" ]; then
    echo "  role '$name' 없음 — 스킵"
    return
  fi
  local role_id
  role_id=$(echo "$API_BODY" | jq -r '.id')
  local body
  body=$(jq -n '{roleType: "platform"}')
  api_call DELETE "/api/roles/id/$role_id" "$body"
  if [ "$HTTP_STATUS" = "200" ] || [ "$HTTP_STATUS" = "204" ]; then
    echo "  role '$name' 삭제(id=$role_id)"
  else
    echo "ERROR: role '$name' 삭제 실패 ($HTTP_STATUS): $API_BODY" >&2
    exit 1
  fi
}

# ================= main =================

login

echo "== 데모 사용자 15명 삭제 =="
for m in "${MODULE_ORDER[@]}"; do
  for t in "${TIER_ORDER[@]}"; do
    role="${ROLE_NAME[$m.$t]}"
    delete_user "demo-$role"
  done
done

echo ""
echo "== 팀(조직) 20개 노드 삭제 (부모 5개를 cascade=true로) =="
for m in "${MODULE_ORDER[@]}"; do
  delete_org_cascade "${MODULE_TEAM[$m]}"
done

echo ""
echo "== 신규 역할 13종 삭제 (billviewer/billadmin은 기존 역할이라 제외) =="
for role in "${NEW_ROLES[@]}"; do
  delete_role "$role"
done

echo ""
echo "완료 — 데모 RBAC 셋업(역할 13종/팀 20개/사용자 15명)을 되돌렸다."
