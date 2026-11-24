#!/usr/bin/env bash
# Репетиция переноса нативного PostgreSQL 12 в контейнер 16.16 (ТЗ §116.5) на ПК
# разработчика: scripts/deploy/pg_migrate_native.sh проходит check → dump →
# restore → fence → verify против «нативного» сервера, каким он стоит на бою.
#
# Источник — postgres:12 на Debian (glibc, как нативный сервер): peer-аутентификация
# на unix-сокете (сокет — в docker-томе вместо /var/run/postgresql хоста), роли с
# хешами md5, у суперпользователя postgres пароля нет, часовой пояс сервера —
# Europe/Moscow в конфигурации. Данные: база nexus — копия базы стенда из стека
# services (схема и строки); чужая база с кириллицей, matview, расширением,
# large object, unlogged-таблицей, ролью без входа и настройками базы и роли в
# базе; база с пробелом в имени; база с запретом подключения; свойства баз
# postgres/template1 (настройки, права, лимит соединений, комментарий); роль с
# VALID UNTIL 'infinity'.
#
# Проверяется и то, что скрипт ОТКАЗЫВАЕТ: dump при подключённом клиенте, fence
# после записи в источник, restore в непустой том.
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
ENVF="$ARC/rehearsal.env"
DUMP="$ARC/cluster.sql"

export COMPOSE_PROJECT_NAME="$P"
export COMPOSE_PATH_SEPARATOR=';'
export COMPOSE_FILE="docker-compose.yml;$OVR"
# Debian-образ: системный пользователь postgres — 999:999.
export NEXUS_PG_SRC_SOCKET="$SOCK" NEXUS_PG_SRC_UID=999 NEXUS_PG_SRC_GID=999 NEXUS_PG_SRC_PORT=5432
export NEXUS_PG_MIGRATE_NAME="$P-migrate" NEXUS_ENV_FILE="$ENVF"
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

# write_files — порт цели на 127.0.0.1:55432 (5432 занят стеком services) и .env
# с реквизитами приложения для проверки входа. !override: без него списки ports
# сливаются и 0.0.0.0:5432 остался бы (DEPLOYMENT §5.3.1).
write_files() {
	mkdir -p "$ARC"
	cat >"$OVR" <<'YAML'
services:
  postgres:
    ports: !override
      - "127.0.0.1:55432:5432"
YAML
	printf 'PG_USER=nexus\nPG_DATABASE=nexus\nPG_PASSWORD=nexus-pw\n' >"$ENVF"
}

# src_sql DB SQL — запрос к источнику от postgres по сокету (peer: exec от postgres).
src_sql() { docker exec -u postgres "$SRC" psql -X -q -v ON_ERROR_STOP=1 -d "$1" -c "$2"; }

# expect_fail ЧТО КОМАНДА… — команда обязана завершиться ошибкой.
expect_fail() {
	what="$1"
	shift
	if "$@" >"$ARC/expect.out" 2>&1; then
		cat "$ARC/expect.out"
		die "$what: команда прошла, а должна была отказать"
	fi
	echo "$what — отказ, верно: $(grep -m1 'ОШИБКА' "$ARC/expect.out" || tail -n 1 "$ARC/expect.out")"
}

start_source() {
	step "Источник: $SRC_IMAGE, peer на сокете, md5 по сети, TimeZone=Europe/Moscow"
	write_files
	remove_all
	write_files
	docker run -d --name "$SRC" -v "$SOCK:/var/run/postgresql" -v "$SRC_DATA:/var/lib/postgresql/data" \
		-e POSTGRES_PASSWORD=bootstrap -e POSTGRES_INITDB_ARGS="--auth-local=peer --auth-host=md5" \
		"$SRC_IMAGE" >/dev/null
	for _ in $(seq 1 60); do
		docker exec "$SRC" pg_isready -q -h 127.0.0.1 && break
		sleep 1
	done
	docker exec "$SRC" pg_isready -q -h 127.0.0.1 || die "источник не поднялся"
	src_sql postgres "alter system set timezone = 'Europe/Moscow'"
	src_sql postgres "select pg_reload_conf()"
}

seed_roles() {
	step "Роли: md5-пароли, членство, VALID UNTIL; у postgres пароля нет"
	src_sql postgres "alter role postgres password null;
		create role nexus login password 'nexus-pw';
		create role app2 login password 'app2-pw' valid until '2030-01-01 00:00+03';
		create role forever login password 'forever-pw' valid until 'infinity';
		create role readers nologin;
		grant readers to app2 with admin option;"
	src_sql postgres "select rolname, left(rolpassword, 3), rolvaliduntil from pg_authid where rolname in ('nexus', 'app2', 'forever')"
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
	step "Чужие базы: кириллица, matview, расширение, large object, unlogged, настройки"
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
		select lo_from_bytea(0, 'large object');
		grant select on items to readers;"
	src_sql postgres "alter database app2 set timezone = 'Asia/Yekaterinburg';
		alter role nexus in database app2 set statement_timeout = '5s';"
	src_sql postgres 'create database "report data" owner app2'
	src_sql "report data" "set role app2; create table t (x int); insert into t select generate_series(1, 42);"
	src_sql postgres "create database frozen"
	src_sql postgres "alter database frozen allow_connections false"
	step "Свойства баз postgres и template1 (pg_dumpall без --clean их не переносит)"
	src_sql postgres "alter database postgres set work_mem = '8MB';
		alter role app2 in database postgres set search_path = public, app;
		comment on database postgres is 'Главная база кластера';
		alter database template1 connection limit 50;
		revoke connect on database template1 from public;
		grant connect on database template1 to app2;"
}

run_migration() {
	step "check"
	"$MIGRATE" check
	step "dump при подключённом клиенте — должен отказать"
	docker exec -d -u postgres "$SRC" psql -X -d nexus -c "select pg_sleep(120)"
	sleep 2
	expect_fail "dump с клиентом" "$MIGRATE" dump "$DUMP"
	src_sql postgres "select pg_terminate_backend(pid) from pg_stat_activity where query like '%pg_sleep(120)%' and pid <> pg_backend_pid()" >/dev/null
	sleep 1
	step "dump"
	"$MIGRATE" dump "$DUMP"
	step "restore (нативный ещё работает: временный контейнер без порта)"
	"$MIGRATE" restore "$DUMP"
	step "повторный restore в непустой том — должен отказать"
	expect_fail "restore в непустой том" "$MIGRATE" restore "$DUMP"
	step "fence: после дампа не писали"
	"$MIGRATE" fence "$DUMP"
	step "fence после записи в источник — должен отказать"
	src_sql app2 "set role app2; insert into items (name) values ('после дампа'); delete from items where name = 'после дампа';"
	expect_fail "fence после записи" "$MIGRATE" fence "$DUMP"
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
	step "Вход по прежним паролям (md5) по сети compose и перенесённые настройки"
	net="${P}_nexus_net"
	login() { docker run --rm --network "$net" -e PGPASSWORD="$3" postgres:16.16-alpine \
		psql -X -A -t -h postgres -U "$1" -d "$2" -c "$4"; }
	login nexus nexus nexus-pw "select 'nexus: схема ' || version from schema_migrations"
	login app2 app2 app2-pw "select 'app2: строк ' || count(*) || ', large objects ' || (select count(*) from pg_largeobject_metadata) from items"
	login app2 "report data" app2-pw "select 'report data: строк ' || count(*) from t"
	login forever postgres forever-pw "select 'forever (valid until infinity): вход есть'"
	login nexus nexus nexus-pw "select 'TimeZone сервера: ' || current_setting('TimeZone')" | grep -q Europe/Moscow ||
		die "часовой пояс сервера не перенесён"
	login app2 app2 app2-pw "select 'TimeZone базы app2: ' || current_setting('TimeZone')"
	login nexus app2 nexus-pw "select 'statement_timeout nexus в app2: ' || current_setting('statement_timeout')"
	login app2 postgres app2-pw "select 'app2 в postgres: work_mem ' || current_setting('work_mem') || ', search_path ' || current_setting('search_path')"
	login app2 template1 app2-pw "select 'app2 в template1 (CONNECT выдан): вход есть'"
	if login nexus template1 nexus-pw "select 1" >/dev/null 2>&1; then die "права template1 не перенесены: nexus вошёл"; fi
	echo "nexus в template1 (CONNECT отозван у PUBLIC) — отказ, верно"
	if login nexus nexus wrong "select 1" >/dev/null 2>&1; then die "вошли с неверным паролем"; fi
	echo "неверный пароль отклонён — верно"
	login app2 app2 app2-pw "select 'порядок в musl (побайтный): ' || string_agg(name, ', ' order by name) from item_names"
	docker compose exec -T postgres sh -c 'grep -E "^host all all" "$PGDATA/pg_hba.conf"'
	docker compose exec -T postgres postgres --version
}

start_source
seed_roles
seed_nexus
seed_others
run_migration
switch_and_login
step "РЕПЕТИЦИЯ ПРОЙДЕНА"
