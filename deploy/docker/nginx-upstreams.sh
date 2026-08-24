#!/bin/sh
# Генерация списков реплик для upstream'ов nginx (§93).
#
# Кладётся в /docker-entrypoint.d/ официального образа nginx — тот выполняет
# оттуда все *.sh ДО запуска сервера, так что отдельный entrypoint не нужен.
#
# Списки генерируются при КАЖДОМ старте контейнера, а не только когда файлов
# нет. Причина: rolling.sh помечает выводимую реплику `down` прямо в этих
# файлах, и если выкат оборвётся на середине, файл останется с `down`.
# Перезапуск nginx обязан возвращать полный состав, а не наследовать чужое
# незавершённое состояние.
#
# Состав задаётся переменными окружения (см. deploy/docker-compose.ha.yml):
#   NEXUS_WEB_UPSTREAMS       "web-1:8000 web-2:8000"
#   NEXUS_RECEIVER_UPSTREAMS  "receiver-1:8080 receiver-2:8080"
#   NEXUS_UPSTREAM_MAX_FAILS  сколько подряд неудач выводят реплику (по умолчанию 2)
#   NEXUS_UPSTREAM_FAIL_TIMEOUT  на сколько выводят и за какое окно считают (10s)

set -eu

DYN_DIR=/etc/nginx/dyn
MAX_FAILS="${NEXUS_UPSTREAM_MAX_FAILS:-2}"
FAIL_TIMEOUT="${NEXUS_UPSTREAM_FAIL_TIMEOUT:-10s}"

WEB="${NEXUS_WEB_UPSTREAMS:-web-1:8000 web-2:8000}"
RECEIVER="${NEXUS_RECEIVER_UPSTREAMS:-receiver-1:8080 receiver-2:8080}"

mkdir -p "$DYN_DIR"

# resolve: nginx перерезолвивает адрес апстрима в рантайме через `resolver`
# (127.0.0.11 valid=10s в nginx.conf), а не только при старте/reload. Без него
# пересоздание web/receiver (`docker compose up -d --build …` даёт контейнеру
# НОВЫЙ IP) оставляло nginx с прежним адресом → 502 на весь `/api/v1/*`, пока
# кто-то не сделает `nginx -s reload` (инцидент kz 24.08.2026: одиночный
# nginx-профиль, rolling.sh нет, reload делать было некому). Требует `zone` в
# upstream-блоке (есть) и hostname (не IP) в адресе — состав всегда задаётся
# именами docker-сервисов, поэтому условие выполнено. Порядок параметров у
# `server` не значим.
write_upstreams() {
	file="$1"
	shift
	: >"$file"
	for target in "$@"; do
		[ -n "$target" ] || continue
		printf 'server %s resolve max_fails=%s fail_timeout=%s;\n' \
			"$target" "$MAX_FAILS" "$FAIL_TIMEOUT" >>"$file"
	done
	# Пустой include — это ошибка конфигурации nginx («no servers in upstream»),
	# и контейнер просто не поднимется. Явное сообщение экономит четверть часа.
	[ -s "$file" ] || {
		echo "nexus-upstreams: список реплик для $file пуст — проверьте NEXUS_*_UPSTREAMS" >&2
		exit 1
	}
}

# shellcheck disable=SC2086 # список адресов должен разбиться на слова
write_upstreams "$DYN_DIR/web.conf" $WEB
# shellcheck disable=SC2086
write_upstreams "$DYN_DIR/receiver.conf" $RECEIVER

echo "nexus-upstreams: web → $WEB"
echo "nexus-upstreams: receiver → $RECEIVER"
