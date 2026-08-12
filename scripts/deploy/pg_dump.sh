#!/bin/sh
# Дамп PostgreSQL перед обновлением — страховка отката (DEPLOYMENT §9.5-E, шаг 2).
#
# Раньше процедура выката давала ДВА варианта команды и предлагала оператору
# самому выбрать нужный: контейнерный `docker compose exec postgres pg_dump` и
# нативный `pg_dump` с реквизитами из `.env`. Выбор делался в окне обновления,
# по памяти, и каждая из веток несла свои грабли (см. ниже). Скрипт определяет
# режим сам и остаётся одной командой.
#
# Использование:
#   ./scripts/deploy/pg_dump.sh              # дамп в deploy/arc/nexus_<дата>.sql
#   ./scripts/deploy/pg_dump.sh <файл>       # ... в указанный файл
#   ./scripts/deploy/pg_dump.sh --check      # только показать выбранный режим, без дампа
#
# Переменные окружения:
#   NEXUS_ENV_FILE       путь к .env                    (по умолчанию ./.env)
#   NEXUS_COMPOSE_FILE   compose-файл                   (по умолчанию корневой, без -f)
#   NEXUS_PG_SERVICE     имя сервиса PostgreSQL в compose (по умолчанию postgres)
#   NEXUS_PG_MODE        docker|native — принудительный режим вместо автоопределения
#
# Каталог `deploy/arc` исключён из git и из контекста сборки образов, поэтому
# дамп не портит `git status` и не оседает в слое образа.

set -eu

ENV_FILE="${NEXUS_ENV_FILE:-.env}"
COMPOSE_FILE="${NEXUS_COMPOSE_FILE:-}"
PG_SERVICE="${NEXUS_PG_SERVICE:-postgres}"

die() {
	echo "ОШИБКА: $*" >&2
	exit 1
}

usage() {
	sed -n '2,21p' "$0" | sed 's/^# \{0,1\}//'
	exit "${1:-0}"
}

compose() {
	if [ -n "$COMPOSE_FILE" ]; then
		docker compose -f "$COMPOSE_FILE" "$@"
	else
		docker compose "$@"
	fi
}

# env_get читает значение ключа из .env БЕЗ `source` и БЕЗ `eval`.
#
# Обе привычные формы здесь ломаются, и каждая — тихо:
#   * `source .env` спотыкается о значения с пробелами без кавычек
#     (KAFKA_HEAP_OPTS=-Xmx1G -Xms1G) и обрывает чтение;
#   * `eval "$(grep …)"` съедает пароли со спецсимволами — `$`, кавычки,
#     обратные кавычки подставятся как код, и пароль молча окажется другим.
# sed отдаёт всё после первого `=` дословно, поэтому значение доезжает как есть.
env_get() {
	[ -f "$ENV_FILE" ] || return 0
	# Хвостовой CR: .env, отредактированный под Windows, иначе даёт пароль с \r
	# — postgres ответит «authentication failed», а глазами разницы не видно.
	sed -n "s/^$1=//p" "$ENV_FILE" | head -n1 | tr -d '\r'
}

# unquote снимает кавычки, если значение в .env записано в них целиком.
unquote() {
	v="$1"
	case "$v" in
	\"*\") v="${v#\"}"; v="${v%\"}" ;;
	\'*\') v="${v#\'}"; v="${v%\'}" ;;
	esac
	printf '%s' "$v"
}

# detect_mode — где на самом деле работает PostgreSQL.
#
# Признак docker-режима один и надёжный: контейнер сервиса ЗАПУЩЕН в этом
# compose-проекте. Наличия секции в compose-файле недостаточно — в варианте B
# сервис описан, но остановлен, а база работает нативно.
detect_mode() {
	if [ -n "${NEXUS_PG_MODE:-}" ]; then
		case "$NEXUS_PG_MODE" in
		docker | native) printf '%s' "$NEXUS_PG_MODE"; return 0 ;;
		*) die "NEXUS_PG_MODE должен быть docker или native, получено '$NEXUS_PG_MODE'" ;;
		esac
	fi
	if command -v docker >/dev/null 2>&1 &&
		compose ps --status running --services 2>/dev/null | grep -qx "$PG_SERVICE"; then
		printf 'docker'
	else
		printf 'native'
	fi
}

MODE="$(detect_mode)"

PG_USER="$(unquote "$(env_get PG_USER)")"
PG_DATABASE="$(unquote "$(env_get PG_DATABASE)")"
PG_PORT="$(unquote "$(env_get PG_PORT)")"
PG_PASSWORD="$(unquote "$(env_get PG_PASSWORD)")"
: "${PG_USER:=nexus}"
: "${PG_DATABASE:=nexus}"
: "${PG_PORT:=5432}"

if [ "${1:-}" = "--check" ]; then
	echo "режим:    $MODE"
	echo "сервис:   $PG_SERVICE (compose)"
	echo "база:     $PG_DATABASE, пользователь $PG_USER, порт $PG_PORT"
	[ "$MODE" = native ] && {
		[ -n "$PG_PASSWORD" ] || echo "ВНИМАНИЕ: PG_PASSWORD в $ENV_FILE не найден — pg_dump спросит пароль" >&2
	}
	exit 0
fi

case "${1:-}" in
-h | --help) usage 0 ;;
esac

OUT="${1:-deploy/arc/nexus_$(date +%F_%H%M).sql}"
mkdir -p "$(dirname "$OUT")" || die "не удалось создать каталог для '$OUT'"

echo "PostgreSQL: режим $MODE, база $PG_DATABASE"
echo "дамп → $OUT"

# Пишем во временный файл и переименовываем только после успеха: оборванный
# дамп с правильным именем — худший исход, его заметят лишь при откате.
TMP="$OUT.part"
trap 'rm -f "$TMP"' EXIT INT TERM

if [ "$MODE" = docker ]; then
	compose exec -T "$PG_SERVICE" pg_dump -U "$PG_USER" "$PG_DATABASE" >"$TMP" ||
		die "pg_dump в контейнере '$PG_SERVICE' завершился с ошибкой"
else
	command -v pg_dump >/dev/null 2>&1 ||
		die "pg_dump не найден в PATH, а контейнер '$PG_SERVICE' не запущен — поставьте postgresql-client либо укажите NEXUS_PG_MODE=docker"
	# Хост 127.0.0.1, а НЕ $PG_HOST из .env: там адрес для КОНТЕЙНЕРОВ
	# (host.docker.internal), с самого сервера он не резолвится.
	#
	# Пароль уходит через `env`, а не префиксом `PGPASSWORD=… pg_dump`: префикс
	# легко потерять при копировании, и сбой выходит тихим — вместо ошибки
	# просто запрос пароля.
	env PGPASSWORD="$PG_PASSWORD" \
		pg_dump -h 127.0.0.1 -p "$PG_PORT" -U "$PG_USER" "$PG_DATABASE" >"$TMP" ||
		die "pg_dump завершился с ошибкой (проверьте реквизиты PG_* в $ENV_FILE)"
fi

# Пустой или обрезанный дамп — тоже провал, а код возврата его не покажет:
# pg_dump успевает отдать 0, если соединение оборвалось на середине вывода.
[ -s "$TMP" ] || die "дамп пуст — база не отдала данные"
grep -q "PostgreSQL database dump complete" "$TMP" ||
	die "дамп оборван: нет завершающей строки pg_dump (файл оставлен как $TMP)"

mv "$TMP" "$OUT"
trap - EXIT INT TERM

SIZE="$(wc -c <"$OUT" | tr -d ' ')"
echo "готово: $OUT ($SIZE байт)"
echo
echo "Восстановление (в аварии, DEPLOYMENT §10):"
if [ "$MODE" = docker ]; then
	echo "  $(if [ -n "$COMPOSE_FILE" ]; then echo "docker compose -f $COMPOSE_FILE"; else echo "docker compose"; fi) exec -T $PG_SERVICE psql -U $PG_USER $PG_DATABASE < $OUT"
else
	echo "  env PGPASSWORD=\"\$PG_PASSWORD\" psql -h 127.0.0.1 -p $PG_PORT -U $PG_USER $PG_DATABASE < $OUT"
fi
