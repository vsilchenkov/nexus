#!/bin/sh
# Перенос нативного кластера PostgreSQL в контейнер стека Nexus (ТЗ §116.5,
# DEPLOYMENT «Перенос нативного PostgreSQL в контейнер»).
#
# Переносится кластер ЦЕЛИКОМ: все базы и все роли с паролями, атрибутами и
# членством (pg_dumpall), а не только база nexus.
#
# Использование (из каталога проекта, на сервере, от root или пользователя docker):
#   ./scripts/deploy/pg_migrate_native.sh check            # осмотр источника и цели, ничего не меняет
#   ./scripts/deploy/pg_migrate_native.sh dump [файл]      # pg_dumpall + снимок для сверки → deploy/arc
#   ./scripts/deploy/pg_migrate_native.sh restore <дамп>   # том сервиса postgres из дампа (нативный не трогается)
#   ./scripts/deploy/pg_migrate_native.sh verify <дамп>    # сверка цели со снимком: базы, роли, строки
#
# Переменные окружения:
#   NEXUS_ENV_FILE        путь к .env                          (./.env)
#   NEXUS_COMPOSE_FILE    compose-файл                         (корневой, без -f)
#   NEXUS_PG_SERVICE      сервис PostgreSQL в compose          (postgres)
#   NEXUS_PG_IMAGE        образ клиента и пробного сервера     (postgres:16.16-alpine)
#   NEXUS_PG_SRC_SOCKET   каталог unix-сокета нативного сервера (/var/run/postgresql)
#   NEXUS_PG_SRC_PORT     порт нативного сервера               (PG_PORT из .env, иначе 5432)
#   NEXUS_PG_SRC_USER     суперпользователь нативного сервера  (postgres)
#   NEXUS_PG_SRC_UID      UID системного пользователя postgres (id -u postgres)
#   NEXUS_PG_SRC_GID      его GID                              (id -g postgres)
#   NEXUS_PG_MIGRATE_NAME имя временного контейнера            (nexus-pg-migrate)
#
# Дамп и снимки содержат хеши паролей и данные всех баз: файлы создаются с
# правами 600 в deploy/arc (каталог исключён из git и из контекста сборки).

set -eu

ENV_FILE="${NEXUS_ENV_FILE:-.env}"
# Не COMPOSE_FILE: это штатная переменная docker compose (см. pg_dump.sh).
NEXUS_COMPOSE="${NEXUS_COMPOSE_FILE:-}"
PG_SERVICE="${NEXUS_PG_SERVICE:-postgres}"
PG_IMAGE="${NEXUS_PG_IMAGE:-postgres:16.16-alpine}"
SRC_SOCKET="${NEXUS_PG_SRC_SOCKET:-/var/run/postgresql}"
SRC_USER="${NEXUS_PG_SRC_USER:-postgres}"
MIG="${NEXUS_PG_MIGRATE_NAME:-nexus-pg-migrate}"
# Суперпользователь тома в контейнере. Не PG_USER: при PG_USER=nexus дамп упёрся
# бы в существующие роль и базу nexus, а снять суперпользователя с
# bootstrap-роли PostgreSQL 16 не даёт — вместе с атрибутами не применился бы
# пароль (ТЗ §116.5).
TGT_SUPER=postgres
# Признак полного дампа pg_dumpall — последняя содержательная строка вывода.
DUMP_COMPLETE="PostgreSQL database cluster dump complete"

die() {
	echo "ОШИБКА: $*" >&2
	exit 1
}

say() { printf '%s\n' "$*"; }

section() { printf '\n== %s\n' "$*"; }

usage() {
	sed -n '2,28p' "$0" | sed 's/^# \{0,1\}//'
	exit "${1:-0}"
}

compose() {
	if [ -n "$NEXUS_COMPOSE" ]; then
		docker compose -f "$NEXUS_COMPOSE" "$@"
	else
		docker compose "$@"
	fi
}

# env_get читает значение ключа из .env без source и eval (см. pg_dump.sh).
env_get() {
	[ -f "$ENV_FILE" ] || return 0
	sed -n "s/^$1=//p" "$ENV_FILE" | head -n1 | tr -d '\r'
}

src_port() {
	if [ -n "${NEXUS_PG_SRC_PORT:-}" ]; then
		printf '%s' "$NEXUS_PG_SRC_PORT"
		return
	fi
	p="$(env_get PG_PORT | tr -d "\"'")"
	printf '%s' "${p:-5432}"
}

# resolve_src — UID:GID, под которым клиент из образа ходит к нативному сокету
# (SRC_IDS), и порт (SRC_PORT). Peer-аутентификация сверяет UID процесса с
# именем роли: сервер сам переводит UID в имя по своему /etc/passwd, поэтому
# клиенту в контейнере достаточно UID системного пользователя postgres хоста.
# Вызывается в основной оболочке: die внутри $(…) не остановил бы скрипт.
resolve_src() {
	uid="${NEXUS_PG_SRC_UID:-}"
	gid="${NEXUS_PG_SRC_GID:-}"
	[ -n "$uid" ] || uid="$(id -u "$SRC_USER" 2>/dev/null)" ||
		die "нет системного пользователя '$SRC_USER' — задайте NEXUS_PG_SRC_UID/NEXUS_PG_SRC_GID"
	[ -n "$gid" ] || gid="$(id -g "$SRC_USER" 2>/dev/null)" || gid="$uid"
	SRC_IDS="$uid:$gid"
	SRC_PORT="$(src_port)"
}

# src_run — команда клиента PostgreSQL 16 из образа против нативного сервера.
# Без -i: внутри циклов `while read` контейнер со stdin съел бы ввод цикла.
src_run() {
	docker run --rm --network none --user "$SRC_IDS" \
		-e PGHOST=/var/run/postgresql -e PGPORT="$SRC_PORT" -e PGUSER="$SRC_USER" \
		-v "$SRC_SOCKET:/var/run/postgresql" "$PG_IMAGE" "$@"
}

# src_psql SQL [база] — запрос к нативному серверу, вывод без оформления.
src_psql() {
	src_run psql -X -A -t -q -v ON_ERROR_STOP=1 -F '|' -d "${2:-postgres}" -c "$1"
}

# tgt_psql SQL [база] — запрос к цели (контейнер TGT_CTR) от суперпользователя
# тома по локальному сокету (в образе он trust).
tgt_psql() {
	docker exec "$TGT_CTR" psql -X -A -t -q -v ON_ERROR_STOP=1 -F '|' \
		-U "$TGT_SUPER" -d "${2:-postgres}" -c "$1"
}

# --- запросы снимка: одинаковые для источника и цели -----------------------

SQL_DBS="select datname from pg_database where datallowconn and datname <> 'template0' order by 1"

SQL_DB_PROPS="select datname, pg_encoding_to_char(encoding), datcollate, datctype, pg_get_userbyid(datdba)
from pg_database where datname <> 'template0' order by 1"

# Хеш пароля — md5 от хранимого значения: сверка без второй копии хешей.
# Срок — эпохой: текст timestamptz зависит от часового пояса сессии.
SQL_ROLES="select rolname, rolsuper, rolinherit, rolcreaterole, rolcreatedb, rolcanlogin,
rolreplication, rolbypassrls, rolconnlimit, coalesce(md5(rolpassword), '-'),
coalesce(extract(epoch from rolvaliduntil)::bigint::text, '-')
from pg_authid where rolname !~ '^pg_' order by 1"

SQL_MEMBERS="select r.rolname, m.rolname, a.admin_option from pg_auth_members a
join pg_roles r on r.oid = a.roleid join pg_roles m on m.oid = a.member
where r.rolname !~ '^pg_' order by 1, 2"

# Точное число строк каждой таблицы, партиционированной таблицы и matview.
SQL_COUNTS="select format('%I.%I', n.nspname, c.relname) || '|' ||
(xpath('/row/c/text()', query_to_xml(format('select count(*) as c from %I.%I', n.nspname, c.relname), false, true, '')))[1]::text
from pg_class c join pg_namespace n on n.oid = c.relnamespace
where c.relkind in ('r', 'p', 'm') and (c.relkind <> 'm' or c.relispopulated)
and n.nspname not in ('pg_catalog', 'information_schema') and n.nspname !~ '^pg_(toast|temp)'"

# snapshot RUNNER PREFIX — снимок для сверки: PREFIX.dbs, .roles, .members, .counts.
# Сортировка — здесь, с LC_ALL=C, а не ORDER BY: порядок строк в SQL зависит от
# локали базы, а glibc-источник и musl-цель сравнивают текст по-разному.
snapshot() {
	runner="$1"
	prefix="$2"
	(umask 077 && "$runner" "$SQL_DB_PROPS" >"$prefix.dbs")
	(umask 077 && "$runner" "$SQL_ROLES" >"$prefix.roles")
	(umask 077 && "$runner" "$SQL_MEMBERS" >"$prefix.members")
	(umask 077 && : >"$prefix.counts")
	"$runner" "$SQL_DBS" | while IFS= read -r db; do
		[ -n "$db" ] || continue
		"$runner" "$SQL_COUNTS" "$db" | sed "s/^/$db|/" >>"$prefix.counts"
	done
	for ext in dbs roles members counts; do
		LC_ALL=C sort -o "$prefix.$ext" "$prefix.$ext"
	done
}

# --- временный контейнер цели ----------------------------------------------

# start_target [DBS_SNAPSHOT] — поднимает сервис postgres временным контейнером
# `compose run` с томом сервиса и БЕЗ публикации портов: нативный сервер на 5432
# при этом продолжает работать. Со снимком баз источника пустой том
# инициализируется от TGT_SUPER, с методом md5 в pg_hba и с кодировкой и
# локалью template1 источника (postgres и template1 дамп не пересоздаёт).
start_target() {
	docker container inspect "$MIG" >/dev/null 2>&1 &&
		die "контейнер $MIG уже есть (прошлая попытка?) — разберите его: docker rm -f $MIG"
	if [ -n "${1:-}" ]; then
		t1="$(grep '^template1|' "$1" || true)"
		[ -n "$t1" ] || die "в снимке $1 нет template1"
		enc="$(printf '%s' "$t1" | cut -d'|' -f2)"
		coll="$(printf '%s' "$t1" | cut -d'|' -f3)"
		ctype="$(printf '%s' "$t1" | cut -d'|' -f4)"
		pass="$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
		# md5 в pg_hba принимает и хеши md5 (из PostgreSQL 12), и SCRAM; метод
		# scram-sha-256 по умолчанию образа хеш md5 не принимает.
		compose run -d --no-deps --name "$MIG" \
			-e POSTGRES_USER="$TGT_SUPER" -e POSTGRES_DB=postgres \
			-e POSTGRES_PASSWORD="$pass" -e POSTGRES_HOST_AUTH_METHOD=md5 \
			-e POSTGRES_INITDB_ARGS="--encoding=$enc --lc-collate=$coll --lc-ctype=$ctype" \
			"$PG_SERVICE" >/dev/null
	else
		compose run -d --no-deps --name "$MIG" "$PG_SERVICE" >/dev/null
	fi
	TGT_CTR="$MIG"
	wait_ready "$MIG"
}

# wait_ready CTR — готовность по TCP внутри контейнера. Не по сокету: при
# инициализации образ поднимает временный сервер только на сокете, и проверка
# по нему ответила бы «готов» раньше времени.
wait_ready() {
	i=0
	while [ "$i" -lt 120 ]; do
		[ "$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" = true ] || {
			docker logs --tail 30 "$1" >&2 || true
			die "контейнер $1 остановился при старте (лог выше)"
		}
		docker exec "$1" pg_isready -q -h 127.0.0.1 -U "$TGT_SUPER" >/dev/null 2>&1 && return 0
		i=$((i + 1))
		sleep 1
	done
	die "контейнер $1 не ответил за 120 с"
}

stop_target() {
	[ "${TGT_CTR:-}" = "$MIG" ] || return 0
	docker stop -t 60 "$MIG" >/dev/null && docker rm "$MIG" >/dev/null
	TGT_CTR=
}

# target_volume_empty — пуст ли каталог данных тома сервиса (через короткоживущий
# контейнер с томом; отсутствующий том compose создаст пустым).
target_volume_empty() {
	out="$(compose run --rm --no-deps --entrypoint sh "$PG_SERVICE" \
		-c 'ls -A /var/lib/postgresql/data | head -n 1')" ||
		die "не удалось проверить том сервиса $PG_SERVICE"
	[ -z "$out" ]
}

# --- check ------------------------------------------------------------------

# Провалы копятся в файле, а не в переменной: fail зовётся и из циклов в
# конвейерах, где переменная осталась бы в подоболочке.
FAIL_LOG=
fail() {
	echo "  ПРОВАЛ: $*"
	echo x >>"$FAIL_LOG"
}
warn() { echo "  ВНИМАНИЕ: $*"; }

check_source() {
	section "Источник: нативный PostgreSQL (сокет $SRC_SOCKET, порт $SRC_PORT, UID:GID $SRC_IDS)"
	src_psql "select version()" >/dev/null 2>&1 ||
		die "нативный сервер не отвечает по сокету — проверьте NEXUS_PG_SRC_SOCKET/PORT/UID и что он запущен"
	src_psql "select 'версия: ' || current_setting('server_version')"
	major="$(src_psql "select current_setting('server_version_num')::int / 10000")"
	[ "$major" -le 16 ] || fail "источник $major новее цели 16 — перенос дампом вниз не поддерживается"
	say "  базы (имя | размер | кодировка | collate | ctype):"
	src_psql "select datname, pg_size_pretty(pg_database_size(datname)), pg_encoding_to_char(encoding),
		datcollate, datctype from pg_database where datname <> 'template0' order by 1" | sed 's/^/    /'
	say "  всего: $(src_psql "select pg_size_pretty(sum(pg_database_size(datname))::bigint) from pg_database")"
	say "  роли (имя | super | login | пароль):"
	src_psql "select rolname, rolsuper, rolcanlogin, case when rolpassword is null then 'нет'
		when rolpassword like 'SCRAM-SHA-256%' then 'scram' when rolpassword like 'md5%' then 'md5'
		else 'другой' end from pg_authid where rolname !~ '^pg_' order by 1" | sed 's/^/    /'
}

check_unsupported() {
	section "Что дамп не переносит"
	ts="$(src_psql "select spcname || ' → ' || pg_tablespace_location(oid) from pg_tablespace
		where spcname not in ('pg_default', 'pg_global')")"
	[ -z "$ts" ] || fail "нестандартные табличные пространства (путей хоста в контейнере нет): $ts"
	slots="$(src_psql "select count(*) from pg_replication_slots")"
	[ "$slots" = 0 ] || warn "слотов репликации: $slots — репликация не переносится"
	src_psql "$SQL_DBS" | while IFS= read -r db; do
		[ -n "$db" ] || continue
		pubs="$(src_psql "select count(*) from pg_publication" "$db")"
		[ "$pubs" = 0 ] || warn "база $db: публикаций $pubs — логическая репликация не переносится"
	done
	subs="$(src_psql "select count(*) from pg_subscription")"
	[ "$subs" = 0 ] || warn "подписок: $subs — логическая репликация не переносится"
	say "  (табличные пространства, слоты, публикации, подписки — проверено)"
}

check_config() {
	section "Настройки нативного сервера (перенести в command: сервиса postgres, если нужны)"
	src_psql "select name || ' = ' || setting || coalesce(unit, '') from pg_settings
		where source = 'configuration file' and name !~ '(_file|_directory|_directories)$'
		order by 1" | sed 's/^/    /'
	section "Правила pg_hba (в контейнере будет одно: host all all all md5)"
	src_psql "select line_number, type, array_to_string(database, ','), array_to_string(user_name, ','),
		coalesce(address, ''), coalesce(netmask, ''), auth_method from pg_hba_file_rules
		order by 1" | sed 's/^/    /'
	section "Кто подключён сейчас (база | роль | откуда | приложение | соединений)"
	clients="$(src_psql "select datname, usename, coalesce(host(client_addr), 'unix-сокет'), application_name, count(*)
		from pg_stat_activity where backend_type = 'client backend' and pid <> pg_backend_pid()
		group by 1, 2, 3, 4 order by 5 desc")"
	if [ -n "$clients" ]; then
		printf '%s\n' "$clients" | sed 's/^/    /'
		say "  их строки подключения после переноса смотрят на контейнер (порт хоста или сеть compose)"
	else
		say "    (никого)"
	fi
}

# check_target_image — пробный сервер из образа без тома: есть ли там
# расширения и локали источника.
check_target_image() {
	section "Цель: образ $PG_IMAGE"
	probe="nexus-pg-probe-$$"
	docker run -d --rm --name "$probe" -e POSTGRES_PASSWORD=probe "$PG_IMAGE" >/dev/null
	trap 'docker stop "$probe" >/dev/null 2>&1 || true' EXIT INT TERM
	TGT_CTR="$probe"
	wait_ready "$probe"
	avail="$(tgt_psql "select name from pg_available_extensions")"
	src_psql "$SQL_DBS" | while IFS= read -r db; do
		[ -n "$db" ] || continue
		src_psql "select extname from pg_extension where extname <> 'plpgsql'" "$db" | while IFS= read -r ext; do
			[ -n "$ext" ] || continue
			printf '%s\n' "$avail" | grep -qx "$ext" || fail "база $db: расширения $ext нет в образе"
		done
	done
	src_psql "select distinct datcollate || '|' || datctype from pg_database where datname <> 'template0'" |
		while IFS='|' read -r coll ctype; do
			[ -n "$coll" ] || continue
			qc="$(printf '%s' "$coll" | sed "s/'/''/g")"
			qt="$(printf '%s' "$ctype" | sed "s/'/''/g")"
			if tgt_psql "create database nexus_locale_probe template template0 lc_collate '$qc' lc_ctype '$qt'" >/dev/null 2>&1; then
				tgt_psql "drop database nexus_locale_probe" >/dev/null
				say "  локаль $coll / $ctype — есть (в alpine сортировка текста побайтная, §116.5)"
			else
				fail "локали $coll / $ctype нет в образе — CREATE DATABASE из дампа упадёт"
			fi
		done
	docker stop "$probe" >/dev/null
	trap - EXIT INT TERM
	TGT_CTR=
}

check_target() {
	section "Цель: сервис $PG_SERVICE стека"
	if target_volume_empty; then
		say "  том данных пуст — восстанавливать можно"
	else
		fail "том данных сервиса $PG_SERVICE не пуст — restore пишет только в пустой том"
	fi
	say "  спецификация сервиса (образ, порты, память, команда, профили):"
	compose config "$PG_SERVICE" 2>/dev/null | awk '
		/^    [a-z_]+:/ { key = $1; sub(":", "", key)
			show = (key ~ /^(image|mem_limit|memswap_limit|shm_size|ports|command|profiles)$/) }
		show { print "    " $0 }'
	root="$(docker info -f '{{.DockerRootDir}}' 2>/dev/null || echo /var/lib/docker)"
	say "  место под данными Docker ($root):"
	df -Pk "$root" 2>/dev/null | sed 's/^/    /' || true
	say "  нужно примерно: размер кластера × 3 (дамп + данные + запас)"
}

cmd_check() {
	FAIL_LOG="$(mktemp)"
	check_source
	check_unsupported
	check_config
	check_target_image
	check_target
	echo
	fails="$(wc -l <"$FAIL_LOG" | tr -d ' ')"
	rm -f "$FAIL_LOG"
	[ "$fails" = 0 ] || die "провалов: $fails — переносить нельзя, пока они не устранены"
	say "check: препятствий нет"
}

# --- dump -------------------------------------------------------------------

cmd_dump() {
	out="${1:-deploy/arc/pg_cluster_$(date +%F_%H%M).sql}"
	mkdir -p "$(dirname "$out")" || die "не удалось создать каталог для '$out'"
	src_psql "select 1" >/dev/null 2>&1 || die "нативный сервер не отвечает — сначала check"
	say "pg_dumpall (клиент $PG_IMAGE) → $out"
	tmp="$out.part"
	trap 'rm -f "$tmp"' EXIT INT TERM
	(umask 077 && src_run pg_dumpall >"$tmp") || die "pg_dumpall завершился с ошибкой"
	grep -q "$DUMP_COMPLETE" "$tmp" || die "дамп оборван: нет строки «$DUMP_COMPLETE»"
	mv "$tmp" "$out"
	trap - EXIT INT TERM
	say "снимок для сверки → $out.{dbs,roles,members,counts}"
	snapshot src_psql "$out"
	say "готово: $(wc -c <"$out" | tr -d ' ') байт, баз $(wc -l <"$out.dbs" | tr -d ' '), ролей $(wc -l <"$out.roles" | tr -d ' '), таблиц $(wc -l <"$out.counts" | tr -d ' ')"
	say "дальше: $0 restore $out"
}

# --- restore ----------------------------------------------------------------

require_dump() {
	[ -n "${1:-}" ] || die "не указан файл дампа"
	[ -s "$1" ] || die "файл дампа '$1' не найден или пуст"
	grep -q "$DUMP_COMPLETE" "$1" || die "дамп '$1' оборван: нет строки «$DUMP_COMPLETE»"
	for ext in dbs roles members counts; do
		[ -f "$1.$ext" ] || die "нет снимка '$1.$ext' — дамп снимался не этим скриптом"
	done
}

# apply_dump DUMP LOG — дамп в цель; единственная допустимая ошибка — уже
# существующая роль суперпользователя тома (её создал initdb).
apply_dump() {
	(umask 077 && docker exec -i "$TGT_CTR" psql -X -q -U "$TGT_SUPER" -d postgres \
		-v ON_ERROR_STOP=0 <"$1" >"$2" 2>&1) || true
	unexpected="$(grep 'ERROR:' "$2" | grep -v "role \"$TGT_SUPER\" already exists" || true)"
	if [ -n "$unexpected" ]; then
		printf '%s\n' "$unexpected" | head -n 20 >&2
		die "восстановление с ошибками (выше первые 20, полный лог — $2). Том заполнен частично:
  docker rm -f $MIG; удалите том сервиса $PG_SERVICE (docker volume ls | grep postgres_data) и повторите restore"
	fi
}

cmd_restore() {
	require_dump "${1:-}"
	dump="$1"
	target_volume_empty || die "том данных сервиса $PG_SERVICE не пуст — restore пишет только в пустой том"
	say "временный контейнер $MIG (том сервиса $PG_SERVICE, порт не публикуется)"
	start_target "$dump.dbs"
	trap 'stop_target' EXIT INT TERM
	say "восстановление $dump"
	apply_dump "$dump" "$dump.restore.log"
	# Нет пароля у суперпользователя в источнике — нет и в цели (initdb дал ему
	# случайный): иначе сверка ролей разошлась бы, а вход по сокету и так trust.
	if grep -q "^$TGT_SUPER|.*|-|[^|]*$" "$dump.roles"; then
		tgt_psql "alter role $TGT_SUPER password null" >/dev/null
	fi
	say "статистика планировщика (vacuumdb --analyze-in-stages)"
	docker exec "$TGT_CTR" vacuumdb -q -U "$TGT_SUPER" --all --analyze-in-stages >/dev/null
	verify_against "$dump"
	stop_target
	trap - EXIT INT TERM
	say "restore: готово. Дальше — переключение (DEPLOYMENT): остановить нативный сервер, docker compose up -d $PG_SERVICE"
}

# --- verify -----------------------------------------------------------------

# verify_against DUMP — сверка текущей цели (TGT_CTR) со снимком источника.
verify_against() {
	got="$1.target"
	snapshot tgt_psql "$got"
	bad=0
	for ext in dbs roles members counts; do
		if cmp -s "$1.$ext" "$got.$ext"; then
			say "  $ext: совпадает ($(wc -l <"$1.$ext" | tr -d ' ') строк)"
		else
			say "  $ext: РАСХОЖДЕНИЕ (источник ← → цель):"
			diff "$1.$ext" "$got.$ext" | head -n 20 | sed 's/^/    /' || true
			bad=1
		fi
	done
	[ "$bad" = 0 ] || die "сверка не прошла — переключаться нельзя"
	say "verify: цель совпадает с источником"
}

cmd_verify() {
	require_dump "${1:-}"
	ctr="$(compose ps -q "$PG_SERVICE" 2>/dev/null || true)"
	if [ -n "$ctr" ] && [ "$(docker inspect -f '{{.State.Running}}' "$ctr" 2>/dev/null)" = true ]; then
		TGT_CTR="$ctr"
		verify_against "$1"
		return
	fi
	start_target
	trap 'stop_target' EXIT INT TERM
	verify_against "$1"
	stop_target
	trap - EXIT INT TERM
}

# --- main -------------------------------------------------------------------

TGT_CTR=

case "${1:-}" in
check | dump | restore | verify) ;;
-h | --help | help) usage 0 ;;
*) echo "неизвестная подкоманда: '${1:-}'" >&2; usage 2 ;;
esac
command -v docker >/dev/null 2>&1 || die "docker не найден в PATH"

case "$1" in
check) resolve_src; cmd_check ;;
dump) shift; resolve_src; cmd_dump "$@" ;;
restore) shift; cmd_restore "$@" ;;
verify) shift; cmd_verify "$@" ;;
esac
