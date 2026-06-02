#!/usr/bin/env bash
# Полное «погружение» в тестирование стенда Nexus (bash-версия PowerShell-скрипта
# seed_and_test.ps1). Часть docs/STAND_TESTING.md.
#
# Создаёт узлы всех типов/авторизаций, гоняет по $COUNT запросов на каждый
# HTTP-узел через единый вход Web и печатает сводку. Узлы НЕ удаляются.
#
# Предпосылки: поднят стек (make docker-up), запущен echosrv
# (go run ./cmd/echosrv), для RabbitMQAsync — RabbitMQ под профилем stand.
#
# Переменные окружения:
#   WEB_URL         базовый адрес Web (по умолчанию http://localhost:8000)
#   ECHO_URL        адрес echosrv, видимый Receiver'у (в docker:
#                   http://host.docker.internal:9999)
#   ADMIN_PASSWORD  пароль admin (обязательно)
#   ADMIN_LOGIN     логин (по умолчанию admin)
#   COUNT           запросов на узел (по умолчанию 500)
#   TEAM_SLUG       slug команды в URL (по умолчанию default)
#   RMQ_HOST        если задан — создаётся узел RabbitMQAsync
set -euo pipefail

WEB_URL="${WEB_URL:-http://localhost:8000}"
ECHO_URL="${ECHO_URL:-http://host.docker.internal:9999}"
ADMIN_LOGIN="${ADMIN_LOGIN:-admin}"
COUNT="${COUNT:-500}"
TEAM_SLUG="${TEAM_SLUG:-default}"
: "${ADMIN_PASSWORD:?нужен ADMIN_PASSWORD}"

API="$WEB_URL/api"
JAR="$(mktemp)"
trap 'rm -f "$JAR"' EXIT

echo "==> Логин в Web"
curl -fsS -c "$JAR" -H 'Content-Type: application/json' \
  -d "{\"login\":\"$ADMIN_LOGIN\",\"password\":\"$ADMIN_PASSWORD\"}" \
  "$API/auth/login" >/dev/null

# create_node <json> — POST /api/nodes; 409 (уже есть) не считается ошибкой.
create_node() {
  local json="$1" code
  code="$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -c "$JAR" \
    -H 'Content-Type: application/json' -d "$json" "$API/nodes")"
  case "$code" in
    20*) echo "  + создан ($code)";;
    409) echo "  = уже существует";;
    *)   echo "  ! ошибка создания: HTTP $code"; return 1;;
  esac
}

# node JSON-шаблоны (минимальные; SetDefaults на бэке дополняет).
nodes_json=(
  "{\"path\":\"stand/req-noauth-post\",\"root_method\":\"request\",\"incoming_method\":\"POST\",\"outgoing_method\":\"POST\",\"url_mode\":\"static\",\"target_url\":\"$ECHO_URL/noauth/echo\",\"auth_type\":\"none\",\"clickhouse_table\":\"stand_req_noauth_post\"}"
  "{\"path\":\"stand/req-get\",\"root_method\":\"request\",\"incoming_method\":\"GET\",\"outgoing_method\":\"GET\",\"url_mode\":\"static\",\"target_url\":\"$ECHO_URL/noauth/echo\",\"auth_type\":\"none\",\"clickhouse_table\":\"stand_req_get\"}"
  "{\"path\":\"stand/req-put\",\"root_method\":\"request\",\"incoming_method\":\"PUT\",\"outgoing_method\":\"PUT\",\"url_mode\":\"static\",\"target_url\":\"$ECHO_URL/noauth/echo\",\"auth_type\":\"none\",\"clickhouse_table\":\"stand_req_put\"}"
  "{\"path\":\"stand/req-basic\",\"root_method\":\"request\",\"incoming_method\":\"POST\",\"outgoing_method\":\"POST\",\"url_mode\":\"static\",\"target_url\":\"$ECHO_URL/basic/echo\",\"auth_type\":\"basic\",\"auth_credentials\":\"user:pass\",\"clickhouse_table\":\"stand_req_basic\"}"
  "{\"path\":\"stand/req-token\",\"root_method\":\"request\",\"incoming_method\":\"POST\",\"outgoing_method\":\"POST\",\"url_mode\":\"static\",\"target_url\":\"$ECHO_URL/token/echo\",\"auth_type\":\"token\",\"auth_credentials\":\"secret-token\",\"clickhouse_table\":\"stand_req_token\"}"
  "{\"path\":\"stand/req-fwd-headers\",\"root_method\":\"request\",\"incoming_method\":\"POST\",\"outgoing_method\":\"POST\",\"url_mode\":\"static\",\"target_url\":\"$ECHO_URL/noauth/echo\",\"auth_type\":\"none\",\"forward_headers\":[\"X-Request-Id\",\"X-Custom\"],\"log_headers\":true,\"clickhouse_table\":\"stand_req_fwd\"}"
  "{\"path\":\"stand/req-empty\",\"root_method\":\"request\",\"incoming_method\":\"POST\",\"outgoing_method\":\"POST\",\"url_mode\":\"static\",\"target_url\":\"$ECHO_URL/empty/x\",\"auth_type\":\"none\",\"clickhouse_table\":\"stand_req_empty\"}"
  "{\"path\":\"stand/async-noauth\",\"root_method\":\"requestAsync\",\"incoming_method\":\"POST\",\"outgoing_method\":\"POST\",\"url_mode\":\"static\",\"target_url\":\"$ECHO_URL/noauth/echo\",\"auth_type\":\"none\",\"clickhouse_table\":\"stand_async_noauth\"}"
)
node_paths=(stand/req-noauth-post stand/req-get stand/req-put stand/req-basic stand/req-token stand/req-fwd-headers stand/req-empty stand/async-noauth)
node_methods=(POST GET PUT POST POST POST POST POST)
node_verbs=(request request request request request request request requestAsync)

echo "==> Создание узлов"
for j in "${nodes_json[@]}"; do create_node "$j"; done

echo "==> Нагрузка: по $COUNT запросов на узел"
for idx in "${!node_paths[@]}"; do
  path="${node_paths[$idx]}"; method="${node_methods[$idx]}"; verb="${node_verbs[$idx]}"
  url="$WEB_URL/api/v1/$verb/$TEAM_SLUG/$path"
  ok=0; bad=0
  for ((i=0; i<COUNT; i++)); do
    if [[ "$method" == "GET" ]]; then
      code="$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -X GET \
        -H "X-Request-Id: stand-$i" -H "X-Custom: v$i" "$url")"
    else
      code="$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -X "$method" \
        -H 'Content-Type: application/json' -H "X-Request-Id: stand-$i" -H "X-Custom: v$i" \
        -d "{\"n\":$i}" "$url")"
    fi
    if [[ "$code" =~ ^2 ]]; then ok=$((ok+1)); else bad=$((bad+1)); fi
  done
  printf '  %-26s method=%-6s ok=%-5s bad=%s\n' "$path" "$method" "$ok" "$bad"
done

echo
echo "Готово. Узлы НЕ удалены — проверьте UI ($WEB_URL): счётчики, графики,"
echo "логи (метод/тело/заголовки/IPv4), /metrics, полный адрес+копирование, dry-run, адаптивность."
