#!/usr/bin/env bash
# Репетиция переноса нативного PostgreSQL 12 в контейнер 16.16 (ТЗ §116.5) на ПК
# разработчика: scripts/deploy/pg_migrate_native.sh проходит check → dump →
# restore → verify против «нативного» сервера, каким он стоит на бою.
#
# Источник — postgres:12 на Debian (glibc, как нативный сервер): peer-аутентификация
# на unix-сокете (сокет — в docker-томе вместо /var/run/postgresql хоста), роли с
# хешами md5, у суперпользователя postgres пароля нет. Данные: база nexus — копия
# базы стенда из стека services (схема и строки), чужая база с кириллицей, matview,
# расширением и ролью без входа, база с пробелом в имени.
#
# Цель — сервис postgres корневого docker-compose.yml в отдельном compose-проекте,
# порт опубликован на 127.0.0.1:55432 (5432 занят стеком services).
#
# Запуск из корня репозитория (Git Bash или Linux):
#   ./scripts/stand/pg_migrate_rehearsal.sh          # прогон и уборка
#   KEEP=1 ./scripts/stand/pg_migrate_rehearsal.sh   # оставить контейнеры и тома для разбора
#
# Нужен Docker и запущенный контейнер postgres стека services с базой nexus
# (SERVICES_PG=postgres; без него база nexus строится из одной схемы миграций).

set -euo pipefail
cd "$(dirname "$0")/../.."
# Git Bash иначе переписывает /var/run/... в аргументах docker в пути Windows.
export MSYS_NO_PATHCONV=1

P=nexus-pgmig
SRC="$P-src"
SOCK="$P-sock"
SRC_DATA="$P-srcdata"
SRC_IMAGE="${SRC_IMAGE:-postgres:12}"
SERVICES_PG="${SERVICES_PG:-postgres}"
ARC=deploy/arc/pgmig-rehearsal
OVR="$ARC/compose.override.yml"
DUMP="$ARC/cluster.sql"

export COMPOSE_PROJECT_NAME="$P"
export COMPOSE_PATH_SEPARATOR=';'
export COMPOSE_FILE="docker-compose.yml;$OVR"
# Debian-образ: системный пользователь postgres — 999:999.
export NEXUS_PG_SRC_SOCKET="$SOCK" NEXUS_PG_SRC_UID=999 NEXUS_PG_SRC_GID=999 NEXUS_PG_SRC_PORT=5432
export NEXUS_PG_MIGRATE_NAME="$P-migrate"
MIGRATE=./scripts/deploy/pg_migrate_native.sh

step() { printf '\n######## %s\n' "$*"; }
die() {
	echo "РЕПЕТИЦИЯ ПРОВАЛЕНА: $*" >&2
	exit 1
}

# remove_all — уборка ресурсов репетиции (контейнеры, тома, файлы в deploy/arc).
remove_all() {
	docker rm -f "$SRC" "$P-migrate" >/dev/null 2>&1 || true
	[ -f "$OVR" ] && docker compose down -v --remove-orphans >/dev/null 2>&1 || true
	docker volume rm -f "$SOCK" "$SRC_DATA" "${P}_postgres_data" >/dev/null 2>&1 || true
	rm -rf "$ARC"
}

on_exit() {
	if [ "${KEEP:-}" = 1 ]; then
		echo "KEEP=1: ресурсы $P-* и $ARC оставлены (уборка — повторный запуск или remove_all)"
		return
	fi
	remove_all
}
trap on_exit EXIT

# write_override — порт цели на 127.0.0.1:55432 (5432 занят стеком services).
# !override: без него списки ports сливаются и 5432 остался бы (DEPLOYMENT §5.3.1).
write_override() {
	mkdir -p "$ARC"
	cat >"$OVR" <<'YAML'
services:
  postgres:
    ports: !override
      - "127.0.0.1:55432:5432"
YAML
}

# src_sql DB SQL — запрос к источнику от postgres по сокету (peer: exec от postgres).
src_sql() { docker exec -u postgres "$SRC" psql -X -q -v ON_ERROR_STOP=1 -d "$1" -c "$2"; }

start_source() {
	step "Источник: $SRC_IMAGE, peer на сокете, md5 по сети"
	write_override
	remove_all
	write_override
	docker run -d --name "$SRC" -v "$SOCK:/var/run/postgresql" -v "$SRC_DATA:/var/lib/postgresql/data" \
		-e POSTGRES_PASSWORD=bootstrap -e POSTGRES_INITDB_ARGS="--auth-local=peer --auth-host=md5" \
		"$SRC_IMAGE" >/dev/null
	for _ in $(seq 1 60); do
		docker exec "$SRC" pg_isready -q -h 127.0.0.1 && return 0
		sleep 1
	done
	die "источник не поднялся"
}

seed_roles() {
	step "Роли: md5-пароли, членство, срок действия; у postgres пароля нет"
	src_sql postgres "alter role postgres password null;
		create role nexus login password 'nexus-pw';
		create role app2 login password 'app2-pw' valid until '2030-01-01 00:00+03';
		create role readers nologin;
		grant readers to app2 with admin option;"
	src_sql postgres "select rolname, left(rolpassword, 3) from pg_authid where rolname in ('nexus', 'app2')"
}

seed_nexus() {
	step "База nexus: копия базы стенда ($SERVICES_PG) от имени nexus"
	src_sql postgres "create database nexus owner nexus"
	src_sql nexus 'create extension if not exists "uuid-ossp"'
	if docker exec "$SERVICES_PG" true 2>/dev/null; then
		# Дамп 16 в сервер 12: \restrict и SET, которых 12 не знает, — ожидаемые ошибки.
		{
			echo "set role nexus;"
			docker exec "$SERVICES_PG" pg_dump -U postgres --no-owner --no-privileges -d nexus |
				grep -v -E '^\\(restrict|unrestrict) '
		} | docker exec -i -u postgres "$SRC" psql -X -q -d nexus -v ON_ERROR_STOP=0 >/dev/null 2>"$ARC/seed.err" || true
	else
		echo "контейнера $SERVICES_PG нет — только схема миграций"
		for f in migrations/*.up.sql; do
			{ echo "set role nexus;"; cat "$f"; } | docker exec -i -u postgres "$SRC" psql -X -q -d nexus >/dev/null
		done
	fi
	src_sql nexus "select 'schema_migrations', version, dirty from schema_migrations"
	src_sql nexus "select 'таблиц nexus', count(*) from pg_tables where schemaname = 'public'"
}

seed_others() {
	step "Чужие базы: кириллица, matview, расширение, unlogged, пробел в имени"
	src_sql postgres "create database app2 owner app2"
	src_sql app2 'create extension "uuid-ossp"'
	src_sql app2 "set role app2;
		create table items (id uuid primary key default uuid_generate_v4(), name text not null);
		insert into items (name) select unnest(array['Яблоко', 'apple', 'Banana', 'ёж', 'Жук', 'zebra']);
		insert into items (name) select 'row ' || g from generate_series(1, 5000) g;
		create materialized view item_names as select name from items where name !~ '^row';
		create materialized view empty_view as select 1 as x with no data;
		create unlogged table cache (k text primary key, v text);
		insert into cache values ('a', 'b');
		grant select on items to readers;"
	src_sql postgres 'create database "report data" owner app2'
	src_sql "report data" "set role app2; create table t (x int); insert into t select generate_series(1, 42);"
}

run_migration() {
	step "check"
	"$MIGRATE" check
	step "dump"
	"$MIGRATE" dump "$DUMP"
	step "restore (нативный ещё работает: временный контейнер без порта)"
	"$MIGRATE" restore "$DUMP"
	step "повторный restore в непустой том — должен отказать"
	if "$MIGRATE" restore "$DUMP" >/dev/null 2>&1; then die "restore записал в непустой том"; fi
	echo "отказал — верно"
}

switch_and_login() {
	step "Переключение: источник остановлен, docker compose up -d postgres"
	docker stop "$SRC" >/dev/null
	docker compose up -d postgres >/dev/null
	for _ in $(seq 1 60); do
		[ "$(docker inspect -f '{{.State.Health.Status}}' "$(docker compose ps -q postgres)")" = healthy ] && break
		sleep 1
	done
	step "verify на работающем сервисе"
	"$MIGRATE" verify "$DUMP"
	step "Вход по прежним паролям (md5) по сети compose"
	net="${P}_nexus_net"
	login() { docker run --rm --network "$net" -e PGPASSWORD="$3" postgres:16.16-alpine \
		psql -X -A -t -h postgres -U "$1" -d "$2" -c "$4"; }
	login nexus nexus nexus-pw "select 'nexus: схема ' || version from schema_migrations"
	login app2 app2 app2-pw "select 'app2: строк ' || count(*) from items"
	login app2 "report data" app2-pw "select 'report data: строк ' || count(*) from t"
	if login nexus nexus wrong "select 1" >/dev/null 2>&1; then die "вошли с неверным паролем"; fi
	echo "неверный пароль отклонён — верно"
	login app2 app2 app2-pw "select 'порядок в musl (побайтный): ' || string_agg(name, ', ' order by name) from item_names"
	docker compose exec -T postgres postgres --version
}

start_source
seed_roles
seed_nexus
seed_others
run_migration
switch_and_login
step "РЕПЕТИЦИЯ ПРОЙДЕНА"
