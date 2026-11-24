#!/bin/sh
# Перенос нативного кластера PostgreSQL в контейнер стека Nexus (ТЗ §116.5,
# DEPLOYMENT §5.4.4 «Перенос нативного PostgreSQL в контейнер стека»).
#
# Переносится кластер ЦЕЛИКОМ: все базы и все роли с паролями, атрибутами и
# членством (pg_dumpall), свойства баз postgres/template1 и сеансовые настройки
# сервера (часовой пояс, DateStyle, lc_*), а не только база nexus.
#
# Использование (из каталога проекта, на сервере, от root или пользователя docker):
#   pg_migrate_native.sh check            # осмотр источника и цели, ничего не меняет
#   pg_migrate_native.sh dump [файл]      # pg_dumpall + снимок для сверки → deploy/arc
#   pg_migrate_native.sh restore <дамп>   # том сервиса postgres из дампа (нативный не трогается)
#   pg_migrate_native.sh fence <дамп>     # после дампа в источник не писали? — перед его остановкой
#   pg_migrate_native.sh verify <дамп>    # сверка цели со снимком
#
# Переменные окружения:
#   NEXUS_ENV_FILE          путь к .env (PG_USER/PG_DATABASE/PG_PASSWORD — проверка входа)  (./.env)
#   NEXUS_COMPOSE_FILE      compose-файл                         (корневой, без -f)
#   NEXUS_PG_SERVICE        сервис PostgreSQL в compose          (postgres)
#   NEXUS_PG_IMAGE          образ клиента и пробного сервера     (postgres:16.16-alpine)
#   NEXUS_PG_SRC_SOCKET     каталог unix-сокета нативного сервера (/var/run/postgresql)
#   NEXUS_PG_SRC_PORT       порт нативного сервера               (PG_PORT из .env, иначе 5432)
#   NEXUS_PG_SRC_USER       суперпользователь нативного сервера  (postgres)
#   NEXUS_PG_SRC_UID/_GID   UID/GID системного пользователя postgres (id -u/-g postgres)
#   NEXUS_PG_MIGRATE_NAME   имя временного контейнера            (nexus-pg-migrate)
#   NEXUS_PG_ALLOW_CLIENTS  1 — dump при подключённых к источнику клиентах (только читающих!)
#   NEXUS_PG_ALLOW_PUBLIC   1 — разрешить публикацию 5432 на всех интерфейсах хоста
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
PGDATA_DIR=/var/lib/postgresql/data
# Признак полного дампа pg_dumpall — последняя содержательная строка вывода.
DUMP_COMPLETE="PostgreSQL database cluster dump complete"
SNAPSHOT_PARTS="dbs roles members settings conf counts schema"

TGT_CTR=
PROBE=
TGT_VOLUME=
VOLUME_TOUCHED=0

die() {
	echo "ОШИБКА: $*" >&2
	exit 1
}

say() { printf '%s\n' "$*"; }

section() { printf '\n== %s\n' "$*"; }

usage() {
	awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"
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

# unquote снимает кавычки, если значение в .env записано в них целиком.
unquote() {
	v="$1"
	case "$v" in
	\"*\") v="${v#\"}"; v="${v%\"}" ;;
	\'*\') v="${v#\'}"; v="${v%\'}" ;;
	esac
	printf '%s' "$v"
}

# on_exit — уборка временных контейнеров на любом выходе; если restore упал
# после инициализации тома — подсказка, как его очистить перед повтором.
on_exit() {
	rc=$?
	stop_target || true
	stop_probe || true
	if [ "$rc" != 0 ] && [ "$VOLUME_TOUCHED" = 1 ]; then
		echo "Том ${TGT_VOLUME:-сервиса $PG_SERVICE} заполнен частично — перед повтором restore удалите его:" >&2
		echo "  docker volume rm ${TGT_VOLUME:-<том postgres_data>}" >&2
	fi
	exit "$rc"
}

# --- источник ---------------------------------------------------------------

src_port() {
	if [ -n "${NEXUS_PG_SRC_PORT:-}" ]; then
		printf '%s' "$NEXUS_PG_SRC_PORT"
		return
	fi
	p="$(unquote "$(env_get PG_PORT)")"
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

# src_run DB CMD… — команда клиента PostgreSQL 16 из образа против нативного
# сервера. База — через PGDATABASE, а не -d: psql разбирает значение -d со
# знаком «=» как строку подключения. Без -i: внутри циклов `while read`
# контейнер со stdin съел бы ввод цикла.
src_run() {
	db="$1"
	shift
	docker run --rm --network none --user "$SRC_IDS" \
		-e PGHOST=/var/run/postgresql -e PGPORT="$SRC_PORT" -e PGUSER="$SRC_USER" -e PGDATABASE="$db" \
		-v "$SRC_SOCKET:/var/run/postgresql" "$PG_IMAGE" "$@"
}

# src_psql SQL [база] — запрос к нативному серверу, вывод без оформления.
src_psql() {
	src_run "${2:-postgres}" psql -X -A -t -q -v ON_ERROR_STOP=1 -F '|' -c "$1"
}

# tgt_psql SQL [база] — запрос к цели (контейнер TGT_CTR) от суперпользователя
# тома по локальному сокету (в образе он trust).
tgt_psql() {
	docker exec -e PGDATABASE="${2:-postgres}" "$TGT_CTR" \
		psql -X -A -t -q -v ON_ERROR_STOP=1 -F '|' -U "$TGT_SUPER" -c "$1"
}

# --- запросы снимка: одинаковые для источника (12) и цели (16) --------------

SQL_DBS="select datname from pg_database where datallowconn and datname <> 'template0'"

# ACL — разобранной и отсортированной (без grantor'а), NULL — как ACL по
# умолчанию: так одинаковые права совпадают при разном порядке записей.
SQL_ACL="coalesce((select string_agg(x, ',' order by x collate \"C\") from (
select case when a.grantee = 0 then 'PUBLIC' else pg_get_userbyid(a.grantee) end || '='
|| a.privilege_type || case when a.is_grantable then '*' else '' end as x
from aclexplode(coalesce(d.datacl, acldefault('d', d.datdba))) a) s), '-')"

SQL_DB_PROPS="select d.datname, pg_encoding_to_char(d.encoding), d.datcollate, d.datctype,
pg_get_userbyid(d.datdba), d.datconnlimit, d.datistemplate, $SQL_ACL,
coalesce(replace(shobj_description(d.oid, 'pg_database'), E'\n', ' '), '-')
from pg_database d where d.datallowconn and d.datname <> 'template0'"

# Хеш пароля — md5 от хранимого значения: сверка без второй копии хешей.
# Срок — эпохой (текст timestamptz зависит от часового пояса сессии), кроме
# infinity: её эпоха в bigint не помещается.
SQL_ROLES="select rolname, rolsuper, rolinherit, rolcreaterole, rolcreatedb, rolcanlogin,
rolreplication, rolbypassrls, rolconnlimit, coalesce(md5(rolpassword), '-'),
case when rolvaliduntil is null then '-' when isfinite(rolvaliduntil)
then extract(epoch from rolvaliduntil)::bigint::text else rolvaliduntil::text end
from pg_authid where rolname !~ '^pg_'"

SQL_MEMBERS="select r.rolname, m.rolname, a.admin_option from pg_auth_members a
join pg_roles r on r.oid = a.roleid join pg_roles m on m.oid = a.member
where r.rolname !~ '^pg_'"

# Настройки баз и ролей (ALTER DATABASE/ROLE … SET), элементы — отсортированными.
SQL_SETTINGS="select coalesce(d.datname, '*'), coalesce(r.rolname, '*'),
(select string_agg(x, ',' order by x collate \"C\") from unnest(s.setconfig) x)
from pg_db_role_setting s left join pg_database d on d.oid = s.setdatabase
left join pg_roles r on r.oid = s.setrole
where d.datname is null or (d.datallowconn and d.datname <> 'template0')"

# Сеансовые настройки сервера, от которых зависит поведение чужих баз (время
# now() без пояса, форматы дат и чисел). Переносятся ALTER SYSTEM; адреса,
# порты, SSL, пути и логирование относятся к хосту и не переносятся.
CARRY_GUCS="'TimeZone', 'log_timezone', 'DateStyle', 'IntervalStyle', 'default_text_search_config',
'lc_monetary', 'lc_numeric', 'lc_time', 'timezone_abbreviations', 'extra_float_digits',
'default_transaction_isolation', 'bytea_output', 'standard_conforming_strings'"

# Значение — из файлов конфигурации (pg_file_settings), а не текущее в сеансе:
# в сеансе его перекрыли бы ALTER DATABASE/ROLE … SET. Нет в файлах — boot_val
# (у timezone_abbreviations он NULL: значение сервер подставляет при старте).
SQL_CONF_VAL="coalesce((select f.setting from pg_file_settings f where f.applied
and lower(f.name) = lower(s.name) limit 1), s.boot_val)"

SQL_CONF="select s.name, coalesce($SQL_CONF_VAL, '-') from pg_settings s where s.name in ($CARRY_GUCS)"

# Точное число строк каждой таблицы, партиционированной таблицы и matview.
SQL_COUNTS="select format('%I.%I', n.nspname, c.relname) || '|' ||
(xpath('/row/c/text()', query_to_xml(format('select count(*) as c from %I.%I', n.nspname, c.relname), false, true, '')))[1]::text
from pg_class c join pg_namespace n on n.oid = c.relnamespace
where c.relkind in ('r', 'p', 'm') and (c.relkind <> 'm' or c.relispopulated)
and n.nspname not in ('pg_catalog', 'information_schema') and n.nspname !~ '^pg_(toast|temp)'"

# Схема базы числом объектов: обрыв восстановления на post-data (индексы,
# ограничения, триггеры, права) счётчики строк не видят. Объекты расширений
# не считаются: версия расширения в образе 16 может быть новее.
SQL_SCHEMA="select 'rel:' || c.relkind::text || '|' || count(*) from pg_class c
join pg_namespace n on n.oid = c.relnamespace
where n.nspname not in ('pg_catalog', 'information_schema') and n.nspname !~ '^pg_(toast|temp)'
and not exists (select 1 from pg_depend e where e.classid = 'pg_class'::regclass and e.objid = c.oid and e.deptype = 'e')
group by c.relkind
union all select 'con:' || o.contype::text || '|' || count(*) from pg_constraint o
join pg_namespace n on n.oid = o.connamespace where n.nspname not in ('pg_catalog', 'information_schema') group by o.contype
union all select 'trg|' || count(*) from pg_trigger t where not t.tgisinternal
union all select 'proc|' || count(*) from pg_proc p join pg_namespace n on n.oid = p.pronamespace
where n.nspname not in ('pg_catalog', 'information_schema')
and not exists (select 1 from pg_depend e where e.classid = 'pg_proc'::regclass and e.objid = p.oid and e.deptype = 'e')
union all select 'policy|' || count(*) from pg_policy
union all select 'lo|' || count(*) from pg_largeobject_metadata
union all select 'ext:' || extname || '|1' from pg_extension
union all select format('seq:%I.%I', schemaname, sequencename) || '|' || coalesce(last_value::text, '-') from pg_sequences"

# Записи в таблицы с момента запуска статистики — ограждение от потерянных
# записей между дампом и переключением. Только пользовательские таблицы:
# ANALYZE автовакуума пишет в каталоги и дал бы ложную тревогу.
SQL_WRITES="select coalesce(sum(n_tup_ins + n_tup_upd + n_tup_del), 0) from pg_stat_user_tables"

SQL_CLIENTS="select coalesce(datname, '-') || ' | ' || coalesce(usename, '-') || ' | '
|| coalesce(host(client_addr), 'unix-сокет') || ' | ' || coalesce(application_name, '')
from pg_stat_activity where backend_type = 'client backend' and pid <> pg_backend_pid()"

# Свойства баз postgres и template1, которые pg_dumpall без --clean не
# переносит (он восстанавливает только их содержимое): лимит соединений,
# комментарий, права, настройки базы и ролей в ней. Плюс ALTER SYSTEM для
# CARRY_GUCS. ord задаёт порядок: REVOKE раньше GRANT.
SQL_POST_UNION="select 1 as ord, format('ALTER DATABASE %I CONNECTION LIMIT %s;', datname, datconnlimit) as stmt
from pg_database where datname in ('postgres', 'template1')
union all select 1, format('COMMENT ON DATABASE %I IS %L;', datname, shobj_description(oid, 'pg_database'))
from pg_database where datname in ('postgres', 'template1') and shobj_description(oid, 'pg_database') is not null
union all select 2, format('REVOKE ALL ON DATABASE %I FROM PUBLIC; REVOKE ALL ON DATABASE %I FROM %I;',
datname, datname, pg_get_userbyid(datdba))
from pg_database where datname in ('postgres', 'template1') and datacl is not null
union all select 3, format('GRANT %s ON DATABASE %I TO %s%s;', a.privilege_type, d.datname,
case when a.grantee = 0 then 'PUBLIC' else quote_ident(pg_get_userbyid(a.grantee)) end,
case when a.is_grantable then ' WITH GRANT OPTION' else '' end)
from pg_database d, aclexplode(d.datacl) a where d.datname in ('postgres', 'template1') and d.datacl is not null
union all select 4, format('ALTER %s SET %s TO %s;',
case when s.setrole = 0 then format('DATABASE %I', d.datname)
else format('ROLE %I IN DATABASE %I', pg_get_userbyid(s.setrole), d.datname) end,
split_part(c, '=', 1),
case when split_part(c, '=', 1) in ('search_path', 'temp_tablespaces', 'local_preload_libraries', 'session_preload_libraries')
then substr(c, strpos(c, '=') + 1) else quote_literal(substr(c, strpos(c, '=') + 1)) end)
from pg_db_role_setting s join pg_database d on d.oid = s.setdatabase, unnest(s.setconfig) c
where d.datname in ('postgres', 'template1')
union all select 5, format('ALTER SYSTEM SET %s = %L;', s.name, $SQL_CONF_VAL)
from pg_settings s where s.name in ($CARRY_GUCS) and $SQL_CONF_VAL is not null"

SQL_POST="select stmt from ($SQL_POST_UNION) q order by ord"
SQL_POST_CONF="select stmt from ($SQL_POST_UNION) q where ord = 5"

# q_to FILE RUNNER SQL [база] — результат запроса в файл (600); сбой запроса
# останавливает скрипт, а не превращается в пустой снимок.
q_to() {
	(umask 077 && "$2" "$3" "${4:-postgres}" >"$1") || die "запрос снимка не выполнился (база ${4:-postgres}, файл $1)"
}

# q_db FILE RUNNER SQL DB — строки запроса к базе DB с префиксом «DB|» в FILE.
q_db() {
	q_to "$1.part" "$2" "$3" "$4"
	while IFS= read -r q_line; do
		printf '%s|%s\n' "$4" "$q_line"
	done <"$1.part" >>"$1"
	rm -f "$1.part"
}

# snapshot RUNNER PREFIX — снимок для сверки: PREFIX.<SNAPSHOT_PARTS>.
# Сортировка — здесь, с LC_ALL=C, а не ORDER BY: порядок строк в SQL зависит от
# локали базы, а glibc-источник и musl-цель сравнивают текст по-разному.
snapshot() {
	s_run="$1"
	s_pre="$2"
	q_to "$s_pre.dbs" "$s_run" "$SQL_DB_PROPS"
	q_to "$s_pre.roles" "$s_run" "$SQL_ROLES"
	q_to "$s_pre.members" "$s_run" "$SQL_MEMBERS"
	q_to "$s_pre.settings" "$s_run" "$SQL_SETTINGS"
	q_to "$s_pre.conf" "$s_run" "$SQL_CONF"
	s_dbs="$("$s_run" "$SQL_DBS" postgres)" || die "список баз не получен"
	(umask 077 && : >"$s_pre.counts" && : >"$s_pre.schema")
	while IFS= read -r s_db; do
		[ -n "$s_db" ] || continue
		q_db "$s_pre.counts" "$s_run" "$SQL_COUNTS" "$s_db"
		q_db "$s_pre.schema" "$s_run" "$SQL_SCHEMA" "$s_db"
	done <<EOF
$s_dbs
EOF
	for part in $SNAPSHOT_PARTS; do
		LC_ALL=C sort -o "$s_pre.$part" "$s_pre.$part"
	done
}

# writes_snapshot FILE — «база|записей» по всем базам источника (600).
writes_snapshot() {
	# Статистика PostgreSQL 12 доходит до коллектора с задержкой до ~0.5 с.
	# NEXUS_PG_STATS_DELAY — только для тестов (tests/deploy), на сервере не задавать.
	sleep "${NEXUS_PG_STATS_DELAY:-2}"
	w_dbs="$(src_psql "$SQL_DBS")" || die "список баз источника не получен"
	(umask 077 && : >"$1")
	while IFS= read -r w_db; do
		[ -n "$w_db" ] || continue
		q_db "$1" src_psql "$SQL_WRITES" "$w_db"
	done <<EOF
$w_dbs
EOF
	LC_ALL=C sort -o "$1" "$1"
}

# --- временный и пробный контейнеры цели ------------------------------------

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
	TGT_VOLUME="$(docker inspect -f "{{range .Mounts}}{{if eq .Destination \"$PGDATA_DIR\"}}{{.Name}}{{end}}{{end}}" "$MIG" 2>/dev/null || true)"
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
	[ -n "$TGT_CTR" ] && [ "$TGT_CTR" = "$MIG" ] || return 0
	TGT_CTR=
	docker stop -t 60 "$MIG" >/dev/null 2>&1 || true
	docker rm "$MIG" >/dev/null 2>&1 || true
}

stop_probe() {
	[ -n "$PROBE" ] || return 0
	docker stop "$PROBE" >/dev/null 2>&1 || true
	PROBE=
}

# target_volume_empty — пуст ли каталог данных тома сервиса (через короткоживущий
# контейнер с томом; отсутствующий том compose создаст пустым).
target_volume_empty() {
	out="$(compose run --rm --no-deps --entrypoint sh "$PG_SERVICE" \
		-c "ls -A $PGDATA_DIR | head -n 1")" ||
		die "не удалось проверить том сервиса $PG_SERVICE"
	[ -z "$out" ]
}

# port_public — сервис публикует 5432 на всех интерфейсах хоста. Нативный
# сервер на бою слушает и адрес шлюза docker-сети (иначе контейнеры до него не
# дошли бы), поэтому «как было» — не довод: Docker публикует порт в обход ufw.
port_public() {
	cfg="$(compose config "$PG_SERVICE" 2>/dev/null)" || die "docker compose config $PG_SERVICE не выполнился"
	counts="$(printf '%s\n' "$cfg" | awk '
		/^    ports:/ { p = 1; next }
		p && /^    [a-z_]+:/ { p = 0 }
		p && /- mode:/ { n++ }
		p && /host_ip:/ && $2 !~ /^"?(0\.0\.0\.0|::)"?$/ { safe++ }
		END { print n + 0, safe + 0 }')"
	n="${counts% *}"
	safe="${counts#* }"
	[ "$n" -gt "$safe" ]
}

port_public_message() {
	echo "сервис $PG_SERVICE публикует 5432 на всех интерфейсах хоста (корневой compose). В локальный"
	echo "  override: postgres: ports: !override [\"127.0.0.1:5432:5432\"] (+ \"172.17.0.1:5432:5432\","
	echo "  если к базе ходят контейнеры других стеков). Нужен доступ с других серверов —"
	echo "  NEXUS_PG_ALLOW_PUBLIC=1 (pg_hba тома пускает только частные сети) — DEPLOYMENT §5.4.4"
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
	closed="$(src_psql "select string_agg(datname, ', ') from pg_database where not datallowconn and datname <> 'template0'")"
	[ -z "$closed" ] || warn "базы с запретом подключения (datallowconn = false) pg_dumpall не переносит: $closed"
	say "  роли (имя | super | login | пароль):"
	src_psql "select rolname, rolsuper, rolcanlogin, case when rolpassword is null then 'нет'
		when rolpassword like 'SCRAM-SHA-256%' then 'scram' when rolpassword like 'md5%' then 'md5'
		else 'другой' end from pg_authid where rolname !~ '^pg_' order by 1" | sed 's/^/    /'
}

check_unsupported() {
	section "Что дамп не переносит"
	ts="$(src_psql "select string_agg(spcname || ' → ' || pg_tablespace_location(oid), ', ') from pg_tablespace
		where spcname not in ('pg_default', 'pg_global')")"
	[ -z "$ts" ] || fail "нестандартные табличные пространства (путей хоста в контейнере нет): $ts"
	slots="$(src_psql "select count(*) from pg_replication_slots")"
	[ "$slots" = 0 ] || warn "слотов репликации: $slots — репликация не переносится"
	dbs="$(src_psql "$SQL_DBS")"
	while IFS= read -r db; do
		[ -n "$db" ] || continue
		pubs="$(src_psql "select count(*) from pg_publication" "$db")"
		[ "$pubs" = 0 ] || warn "база $db: публикаций $pubs — логическая репликация не переносится"
	done <<EOF
$dbs
EOF
	subs="$(src_psql "select count(*) from pg_subscription")"
	[ "$subs" = 0 ] || warn "подписок: $subs — логическая репликация не переносится"
	say "  (табличные пространства, слоты, публикации, подписки — проверено)"
}

check_config() {
	section "Настройки нативного сервера из конфигурации (что | имя = значение)"
	# Значения, где может лежать секрет (строка подключения репликации с паролем,
	# команда пароля ключа SSL), не печатаются — только имя.
	src_psql "select case
		when name in ($CARRY_GUCS) then 'перенесётся (ALTER SYSTEM)  '
		when name ~ '^(listen_addresses|port|ssl.*|unix_socket_.*|external_pid_file|cluster_name|lc_messages|logging_collector|log_destination|log_directory|log_filename|log_file_mode|log_rotation_.*|log_truncate_on_rotation|syslog_.*|event_source|dynamic_shared_memory_type|archive_.*|restore_command|primary_.*|hot_standby.*|wal_keep_segments|recovery_.*|promote_trigger_file|stats_temp_directory)\$'
			then 'не переносить (это хост)  '
		else 'решите сами: command: / mem_limit  ' end
		|| '| ' || name || ' = ' || case when name ~ '(conninfo|passphrase|password)' then '<скрыто>'
		else setting || coalesce(unit, '') end from pg_settings
		where source = 'configuration file' and name !~ '(_file|_directory|_directories)\$'
		order by 1" | sed 's/^/    /'
	section "Правила pg_hba (в контейнере: host … md5 только для частных сетей 10/8, 172.16/12, 192.168/16, fc00::/7)"
	src_psql "select line_number, type, array_to_string(database, ','), array_to_string(user_name, ','),
		coalesce(address, ''), coalesce(netmask, ''), auth_method from pg_hba_file_rules
		order by 1" | sed 's/^/    /'
	section "Кто подключён сейчас (база | роль | откуда | приложение)"
	clients="$(src_psql "$SQL_CLIENTS")"
	if [ -n "$clients" ]; then
		printf '%s\n' "$clients" | sed 's/^/    /'
		say "  на время дампа их нужно остановить (dump с ними откажет); после переноса их строки"
		say "  подключения смотрят на контейнер (порт хоста или сеть compose)"
	else
		say "    (никого)"
	fi
}

# check_target_image — пробный сервер из образа без тома: есть ли там
# расширения, кодировки с локалями и значения переносимых настроек источника.
check_target_image() {
	section "Цель: образ $PG_IMAGE"
	PROBE="nexus-pg-probe-$$"
	docker run -d --rm --name "$PROBE" -e POSTGRES_PASSWORD=probe "$PG_IMAGE" >/dev/null
	TGT_CTR="$PROBE"
	wait_ready "$PROBE"
	avail="$(tgt_psql "select name from pg_available_extensions")"
	dbs="$(src_psql "$SQL_DBS")"
	while IFS= read -r db; do
		[ -n "$db" ] || continue
		exts="$(src_psql "select extname from pg_extension where extname <> 'plpgsql'" "$db")"
		for ext in $exts; do
			printf '%s\n' "$avail" | grep -Fqx "$ext" || fail "база $db: расширения $ext нет в образе"
		done
	done <<EOF
$dbs
EOF
	locs="$(src_psql "select distinct pg_encoding_to_char(encoding) || '|' || datcollate || '|' || datctype
		from pg_database where datallowconn and datname <> 'template0'")"
	while IFS='|' read -r enc coll ctype; do
		[ -n "$enc" ] || continue
		q() { printf '%s' "$1" | sed "s/'/''/g"; }
		if tgt_psql "create database nexus_locale_probe template template0 encoding '$(q "$enc")'
			lc_collate '$(q "$coll")' lc_ctype '$(q "$ctype")'" >/dev/null 2>&1; then
			tgt_psql "drop database nexus_locale_probe" >/dev/null
			say "  кодировка $enc, локаль $coll / $ctype — есть (сортировка текста в alpine побайтная, §116.5)"
		else
			fail "кодировки $enc с локалью $coll / $ctype нет в образе — CREATE DATABASE из дампа упадёт"
		fi
	done <<EOF
$locs
EOF
	conf_sql="$(src_psql "$SQL_POST_CONF")"
	if printf '%s\n' "$conf_sql" | docker exec -i "$PROBE" psql -X -q -v ON_ERROR_STOP=1 -U "$TGT_SUPER" >/dev/null 2>&1; then
		say "  переносимые настройки (часовой пояс, форматы, lc_*) образ принимает"
	else
		fail "образ не принимает значения переносимых настроек: $(printf '%s ' "$conf_sql")"
	fi
	stop_probe
	TGT_CTR=
}

check_target() {
	section "Цель: сервис $PG_SERVICE стека"
	if target_volume_empty; then
		say "  том данных пуст — восстанавливать можно"
	else
		fail "том данных сервиса $PG_SERVICE не пуст — restore пишет только в пустой том"
	fi
	if port_public; then
		if [ "${NEXUS_PG_ALLOW_PUBLIC:-}" = 1 ]; then
			warn "5432 публикуется на всех интерфейсах (NEXUS_PG_ALLOW_PUBLIC=1)"
		else
			fail "$(port_public_message)"
		fi
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

# check_snapshot — пробный снимок источника: запрос, который упадёт в окне
# (роль с необычным сроком, недоступная таблица), падает здесь; заодно замер.
check_snapshot() {
	section "Пробный снимок для сверки"
	tmpd="$(mktemp -d)"
	t0="$(date +%s)"
	snapshot src_psql "$tmpd/probe"
	t1="$(date +%s)"
	say "  снят за $((t1 - t0)) с: баз $(wc -l <"$tmpd/probe.dbs" | tr -d ' '), ролей $(wc -l <"$tmpd/probe.roles" | tr -d ' '), таблиц $(wc -l <"$tmpd/probe.counts" | tr -d ' ')"
	say "  в окне он снимается трижды (dump, restore, verify) — заложите это время"
	rm -rf "$tmpd"
}

cmd_check() {
	FAIL_LOG="$(mktemp)"
	trap on_exit EXIT
	trap 'exit 130' INT TERM
	check_source
	check_unsupported
	check_config
	check_target_image
	check_target
	check_snapshot
	echo
	fails="$(wc -l <"$FAIL_LOG" | tr -d ' ')"
	rm -f "$FAIL_LOG"
	[ "$fails" = 0 ] || die "провалов: $fails — переносить нельзя, пока они не устранены"
	say "check: препятствий нет"
}

# --- dump -------------------------------------------------------------------

# refuse_clients — пишущий клиент во время и после дампа = молча потерянные
# записи: dump отказывает, пока к источнику кто-то подключён.
refuse_clients() {
	clients="$(src_psql "$SQL_CLIENTS")" || die "не удалось получить список клиентов источника"
	[ -n "$clients" ] || return 0
	say "к источнику подключены (база | роль | откуда | приложение):"
	printf '%s\n' "$clients" | sed 's/^/  /'
	if [ "${NEXUS_PG_ALLOW_CLIENTS:-}" = 1 ]; then
		say "NEXUS_PG_ALLOW_CLIENTS=1: продолжаю — записи после дампа поймает fence"
		return 0
	fi
	die "остановите клиентов (их записи после дампа потеряются) или, если они только читают, NEXUS_PG_ALLOW_CLIENTS=1"
}

cmd_dump() {
	out="${1:-deploy/arc/pg_cluster_$(date +%F_%H%M).sql}"
	mkdir -p "$(dirname "$out")" || die "не удалось создать каталог для '$out'"
	src_psql "select 1" >/dev/null 2>&1 || die "нативный сервер не отвечает — сначала check"
	refuse_clients
	# .writes появляется только после чистого прохода: без него restore дамп не
	# примет, поэтому дамп, во время которого писали, использовать нельзя.
	trap 'rm -f "$out.part" "$out.writes0" "$out.writes1"' EXIT
	trap 'exit 130' INT TERM
	rm -f "$out.writes"
	writes_snapshot "$out.writes0"
	say "pg_dumpall (клиент $PG_IMAGE) → $out"
	(umask 077 && src_run postgres pg_dumpall >"$out.part") || die "pg_dumpall завершился с ошибкой"
	tail -n 20 "$out.part" | grep -q "$DUMP_COMPLETE" || die "дамп оборван: нет строки «$DUMP_COMPLETE»"
	mv "$out.part" "$out"
	q_to "$out.post.sql" src_psql "$SQL_POST"
	say "снимок для сверки → $out.{$(printf '%s' "$SNAPSHOT_PARTS" | tr ' ' ',')}"
	snapshot src_psql "$out"
	writes_snapshot "$out.writes1"
	cmp -s "$out.writes0" "$out.writes1" ||
		die "во время дампа в источник писали (база|строк до → после: $(diff "$out.writes0" "$out.writes1" | grep '^[<>]' | tr '\n' ' ')) — остановите пишущих клиентов и повторите dump"
	mv "$out.writes0" "$out.writes"
	rm -f "$out.writes1"
	trap - EXIT INT TERM
	say "готово: $(wc -c <"$out" | tr -d ' ') байт, баз $(wc -l <"$out.dbs" | tr -d ' '), ролей $(wc -l <"$out.roles" | tr -d ' '), таблиц $(wc -l <"$out.counts" | tr -d ' ')"
	say "дальше: $0 restore $out"
}

# --- restore ----------------------------------------------------------------

require_dump() {
	[ -n "${1:-}" ] || die "не указан файл дампа"
	[ -s "$1" ] || die "файл дампа '$1' не найден или пуст"
	tail -n 20 "$1" | grep -q "$DUMP_COMPLETE" || die "дамп '$1' оборван: нет строки «$DUMP_COMPLETE»"
	for part in $SNAPSHOT_PARTS writes post.sql; do
		[ -f "$1.$part" ] || die "нет снимка '$1.$part' — дамп снимался не этим скриптом (или его версией до §116.5)"
	done
}

# restrict_hba — вместо «host all all all md5» образа — только частные сети:
# даже опубликованный наружу порт не пускает к ролям всех баз из интернета.
restrict_hba() {
	docker exec "$TGT_CTR" sh -c '
		f="$1/pg_hba.conf"
		grep -qx "host all all all md5" "$f" || exit 3
		grep -vx "host all all all md5" "$f" >"$f.new"
		printf "%s\n" "# nexus 116.5: private networks only (was: host all all all md5)" \
			"host all all 10.0.0.0/8 md5" "host all all 172.16.0.0/12 md5" \
			"host all all 192.168.0.0/16 md5" "host all all fc00::/7 md5" >>"$f.new"
		cat "$f.new" >"$f" && rm -f "$f.new"' sh "$PGDATA_DIR" ||
		die "не удалось ограничить pg_hba тома (нет строки host all all all md5?)"
	tgt_psql "select pg_reload_conf()" >/dev/null
}

# psql_in LOG — SQL со stdin в цель с остановкой на первой ошибке.
psql_in() {
	docker exec -i "$TGT_CTR" psql -X -q -v ON_ERROR_STOP=1 -U "$TGT_SUPER" -d postgres >"$1" 2>&1
}

# restore_failed WHAT LOG — хвост лога и остановка.
restore_failed() {
	tail -n 5 "$2" >&2
	die "восстановление прервано ($1, полный лог — $2)"
}

# apply_dump DUMP — дамп в цель потоком (без копии на диске). CREATE ROLE
# суперпользователя тома (его создал initdb) — единственная заведомая ошибка;
# строка вырезается, остальное — без исключений. Код выхода psql проверяется:
# обрыв соединения (OOM, падение backend) даёт не «ERROR:», а «server closed
# the connection» и код 2. Без pipefail код конвейера — код psql, что и нужно.
apply_dump() {
	(umask 077 && sed "/^CREATE ROLE $TGT_SUPER;\$/d" "$1" | psql_in "$1.restore.log") ||
		restore_failed "$1" "$1.restore.log"
}

# apply_sql FILE LOG — SQL-файл в цель с остановкой на первой ошибке.
apply_sql() {
	(umask 077 && psql_in "$2" <"$1") || restore_failed "$1" "$2"
}

# app_login_check — вход приложения (PG_USER/PG_PASSWORD из .env) по паролю
# через сеть контейнера: если нативный сервер пускал PG_USER по trust/peer,
# стек сломался бы только после остановки нативного сервера.
app_login_check() {
	user="$(unquote "$(env_get PG_USER)")"
	db="$(unquote "$(env_get PG_DATABASE)")"
	pw="$(unquote "$(env_get PG_PASSWORD)")"
	: "${user:=nexus}" "${db:=nexus}"
	if [ -z "$pw" ]; then
		warn "PG_PASSWORD в $ENV_FILE не найден — вход приложения не проверен"
		return 0
	fi
	ok="$(PGPASSWORD="$pw" docker exec -e PGPASSWORD -e PGDATABASE="$db" -e APP_USER="$user" "$TGT_CTR" \
		sh -c 'psql -X -A -t -q -h "$(hostname -i | cut -d" " -f1)" -U "$APP_USER" -c "select 1"' 2>&1)" ||
		die "вход приложения $user@$db по паролю из $ENV_FILE не прошёл: $ok"
	[ "$ok" = 1 ] || die "вход приложения $user@$db: неожиданный ответ «$ok»"
	say "  вход приложения $user@$db по паролю (md5, по сети): работает"
}

# fence_check DUMP — после дампа в источник не писали (счётчики записи по
# базам те же). 0 — не писали, 1 — писали.
fence_check() {
	writes_snapshot "$1.fence"
	if cmp -s "$1.writes" "$1.fence"; then
		rm -f "$1.fence"
		return 0
	fi
	say "после дампа в источник писали (база|строк: при дампе → сейчас):"
	diff "$1.writes" "$1.fence" | grep '^[<>]' | sed 's/^/  /' || true
	rm -f "$1.fence"
	return 1
}

cmd_restore() {
	require_dump "${1:-}"
	dump="$1"
	if port_public && [ "${NEXUS_PG_ALLOW_PUBLIC:-}" != 1 ]; then
		die "$(port_public_message)"
	fi
	target_volume_empty || die "том данных сервиса $PG_SERVICE не пуст — restore пишет только в пустой том"
	trap on_exit EXIT
	trap 'exit 130' INT TERM
	say "временный контейнер $MIG (том сервиса $PG_SERVICE, порт не публикуется)"
	VOLUME_TOUCHED=1
	start_target "$dump.dbs"
	restrict_hba
	say "восстановление $dump"
	apply_dump "$dump"
	# Нет пароля у суперпользователя в источнике — нет и в цели (initdb дал ему
	# случайный): иначе сверка ролей разошлась бы, а вход по сокету и так trust.
	if grep -q "^$TGT_SUPER|.*|-|[^|]*$" "$dump.roles"; then
		tgt_psql "alter role $TGT_SUPER password null" >/dev/null
	fi
	say "свойства баз postgres/template1 и настройки сервера ($dump.post.sql)"
	apply_sql "$dump.post.sql" "$dump.post.log"
	tgt_psql "select pg_reload_conf()" >/dev/null
	sleep 1
	say "статистика планировщика (vacuumdb --analyze-in-stages)"
	docker exec "$TGT_CTR" vacuumdb -q -U "$TGT_SUPER" --all --analyze-in-stages >/dev/null
	app_login_check
	verify_against "$dump"
	stop_target
	VOLUME_TOUCHED=0
	trap - EXIT INT TERM
	restore_fence_hint "$dump"
}

# restore_fence_hint — если источник ещё доступен, сразу проверить ограждение.
restore_fence_hint() {
	if { [ -n "${NEXUS_PG_SRC_UID:-}" ] || id -u "$SRC_USER" >/dev/null 2>&1; }; then
		resolve_src
		if src_psql "select 1" >/dev/null 2>&1; then
			fence_check "$1" ||
				die "данные этих записей в том не попали — остановите писателей, удалите том и повторите dump и restore"
			say "  после дампа в источник не писали"
		fi
	fi
	say "restore: готово. Дальше (DEPLOYMENT §5.4.4): $0 fence $1 → остановить нативный сервер → docker compose up -d $PG_SERVICE"
}

# --- fence / verify ---------------------------------------------------------

cmd_fence() {
	require_dump "${1:-}"
	src_psql "select 1" >/dev/null 2>&1 || die "нативный сервер не отвечает — проверять нечего (он уже остановлен?)"
	fence_check "$1" ||
		die "данные этих записей в том не попали — остановите писателей, удалите том и повторите dump и restore"
	clients="$(src_psql "$SQL_CLIENTS")"
	[ -z "$clients" ] || {
		say "ВНИМАНИЕ: к источнику подключены — их записи до остановки сервера тоже потеряются:"
		printf '%s\n' "$clients" | sed 's/^/  /'
	}
	say "fence: после дампа в источник не писали — можно останавливать нативный сервер"
}

# verify_against DUMP — сверка текущей цели (TGT_CTR) со снимком источника.
verify_against() {
	got="$1.target"
	snapshot tgt_psql "$got"
	bad=0
	for part in $SNAPSHOT_PARTS; do
		if cmp -s "$1.$part" "$got.$part"; then
			say "  $part: совпадает ($(wc -l <"$1.$part" | tr -d ' ') строк)"
		else
			say "  $part: РАСХОЖДЕНИЕ (< источник, > цель):"
			diff "$1.$part" "$got.$part" | grep '^[<>]' | head -n 20 | sed 's/^/    /' || true
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
	# Пустой том временный контейнер инициализировал бы от PG_USER со SCRAM, и
	# restore после этого отказал бы.
	target_volume_empty && die "том сервиса $PG_SERVICE пуст — сначала restore"
	trap on_exit EXIT
	trap 'exit 130' INT TERM
	start_target
	verify_against "$1"
	stop_target
	trap - EXIT INT TERM
}

# --- main -------------------------------------------------------------------

case "${1:-}" in
check | dump | restore | fence | verify) ;;
-h | --help | help) usage 0 ;;
*) echo "неизвестная подкоманда: '${1:-}'" >&2; usage 2 ;;
esac
command -v docker >/dev/null 2>&1 || die "docker не найден в PATH"

case "$1" in
check) resolve_src; cmd_check ;;
dump) shift; resolve_src; cmd_dump "$@" ;;
restore) shift; cmd_restore "$@" ;;
fence) shift; resolve_src; cmd_fence "$@" ;;
verify) shift; cmd_verify "$@" ;;
esac
