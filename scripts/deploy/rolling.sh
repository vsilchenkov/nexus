#!/bin/sh
# §93 — обновление стека Nexus без простоя: по одной реплике за раз.
#
# Заменяет `docker compose up -d --build` из DEPLOYMENT §9.1 там, где поднят
# профиль двух реплик (deploy/docker-compose.ha.yml). Обычный `up -d --build`
# в этом профиле пересоздал бы обе реплики сервиса одновременно — то есть дал
# бы ровно тот простой, ради устранения которого профиль и существует.
#
# Использование:
#   ./scripts/deploy/rolling.sh                 # полный цикл обновления
#   ./scripts/deploy/rolling.sh --dry-run       # показать план и выйти
#   ./scripts/deploy/rolling.sh --services web  # обновить только Web
#
# Опции:
#   --services "<список>"  какие сервисы обновлять (по умолчанию: sender receiver web)
#   --skip-tag             не сохранять текущие образы под версионным тегом
#   --skip-dump            не снимать дамп PostgreSQL
#   --skip-migrate         не применять миграции отдельным шагом
#   --no-prune             не чистить кэш сборки и dangling-образы в конце
#   --force                продолжить, даже если избыточность уже нарушена
#   --timeout <сек>        сколько ждать healthy одной реплики (по умолчанию 300)
#   --dry-run              напечатать шаги, ничего не выполняя
#
# Порядок шагов и почему он такой:
#
#   1. Сохранить образы (images.sh tag). ДО сборки: она перетрёт `:latest`, и
#      без версионного тега откат превращается в пересборку трёх образов под
#      нагрузкой (§74.5).
#   2. Дамп PostgreSQL (pg_dump.sh). ДО миграций: down-миграции удаляют данные,
#      и в аварии дамп — единственный путь назад (§10.3).
#   3. Сборка образов ПО ОДНОМУ, а не параллельно. `docker compose build` без
#      аргументов собирает три образа разом, и на слабом сервере это вешает
#      машину: три компилятора Go и линкер web с вшитым SPA не помещаются в
#      память (DEPLOYMENT §5.4, «пиковый потребитель — не работа, а сборка»).
#   4. Миграции отдельным шагом, пока СТАРЫЕ реплики ещё работают. Схема
#      аддитивна (§74.2), поэтому старый код на новой схеме жив — а вот новый
#      код на старой схеме упал бы.
#   5. Раскатка по одной реплике: вывести из ротации nginx → пересоздать →
#      дождаться healthy → вернуть в ротацию. Соседняя реплика держит трафик.
#   6. Уборка кэша сборки — только build cache и dangling-слои, НИКОГДА
#      `docker image prune -a`: он снёс бы сохранённые на шаге 1 версионные
#      образы вместе с возможностью быстрого отката.
#
# Переменные окружения:
#   COMPOSE_FILE       список compose-файлов (по умолчанию корневой + ha-оверлей)
#   NEXUS_PRUNE_UNTIL  возраст кэша сборки, который можно удалять (72h)

set -eu

# Стек описан двумя файлами. Экспортируем штатную переменную docker compose, а
# не собираем строку с -f: её же подхватят images.sh и pg_dump.sh, которые
# вызываются ниже и знают только про `docker compose` без флагов.
: "${COMPOSE_FILE:=docker-compose.yml:deploy/docker-compose.ha.yml}"
export COMPOSE_FILE

SERVICES="sender receiver web"
SKIP_TAG=0
SKIP_DUMP=0
SKIP_MIGRATE=0
NO_PRUNE=0
FORCE=0
DRY_RUN=0
HEALTH_TIMEOUT=300
PRUNE_UNTIL="${NEXUS_PRUNE_UNTIL:-72h}"

# Пауза после возврата реплики в ротацию: nginx должен разложить на неё трафик
# и убедиться, что она отвечает, прежде чем мы уведём соседнюю.
SETTLE_SEC=5

die() {
	echo "ОШИБКА: $*" >&2
	exit 1
}

log() {
	echo "==> $*"
}

usage() {
	sed -n '2,46p' "$0" | sed 's/^# \{0,1\}//'
	exit "${1:-0}"
}

run() {
	echo "  + $*"
	[ "$DRY_RUN" -eq 1 ] && return 0
	"$@"
}

while [ $# -gt 0 ]; do
	case "$1" in
	--services)
		shift
		[ $# -gt 0 ] || die "--services требует список"
		SERVICES="$1"
		;;
	--skip-tag) SKIP_TAG=1 ;;
	--skip-dump) SKIP_DUMP=1 ;;
	--skip-migrate) SKIP_MIGRATE=1 ;;
	--no-prune) NO_PRUNE=1 ;;
	--force) FORCE=1 ;;
	--timeout)
		shift
		[ $# -gt 0 ] || die "--timeout требует значение"
		HEALTH_TIMEOUT="$1"
		;;
	--dry-run) DRY_RUN=1 ;;
	-h | --help) usage 0 ;;
	*) die "неизвестный аргумент: $1 (см. --help)" ;;
	esac
	shift
done

command -v docker >/dev/null 2>&1 || die "docker не найден в PATH"
[ -f .env ] || die "нет .env в текущем каталоге — запускайте из корня проекта"

# ── Состав реплик ────────────────────────────────────────────────────────────
# Пары фиксированы профилем deploy/docker-compose.ha.yml. Отдельные имена, а не
# `--scale`, именно ради обновления по одной.

replicas_of() {
	case "$1" in
	web) echo "web-1 web-2" ;;
	receiver) echo "receiver-1 receiver-2" ;;
	sender) echo "sender-1 sender-2" ;;
	*) die "неизвестный сервис '$1' (ожидается web, receiver или sender)" ;;
	esac
}

# upstream_of — в каком upstream'е nginx состоит сервис. Sender за
# балансировщиком не стоит: к нему ходит Receiver по gRPC, и балансировка там
# клиентская (dns:/// + round_robin, §93.4). Пустая строка = «не в ротации».
upstream_of() {
	case "$1" in
	web) echo "web" ;;
	receiver) echo "receiver" ;;
	*) echo "" ;;
	esac
}

# has_balanced_service — есть ли среди обновляемых сервисов такой, что стоит за
# nginx. От этого зависит, нужен ли финальный reload.
has_balanced_service() {
	for svc in $SERVICES; do
		[ -n "$(upstream_of "$svc")" ] && return 0
	done
	return 1
}

# ── Балансировщик ────────────────────────────────────────────────────────────

nginx_running() {
	[ -n "$(docker compose ps -q nginx 2>/dev/null)" ]
}

# lb_targets читает состав upstream'а из ОКРУЖЕНИЯ контейнера nginx, а не из
# своей копии списка: единственный источник истины — compose-файл, и разойтись
# они не должны. Разошлись бы — скрипт вывел бы из ротации не ту реплику.
lb_targets() {
	case "$1" in
	web) docker compose exec -T nginx printenv NEXUS_WEB_UPSTREAMS 2>/dev/null | tr -d '\r' ;;
	receiver) docker compose exec -T nginx printenv NEXUS_RECEIVER_UPSTREAMS 2>/dev/null | tr -d '\r' ;;
	*) echo "" ;;
	esac
}

# lb_write перезаписывает include-файл upstream'а, помечая одну реплику `down`.
# Пустой второй аргумент = все реплики в строю.
lb_write() {
	upstream="$1"
	down_replica="$2"

	if [ "$DRY_RUN" -eq 1 ]; then
		echo "  + nginx: upstream $upstream ← down=${down_replica:-нет}, затем nginx -t && nginx -s reload"
		return 0
	fi

	targets="$(lb_targets "$upstream")"
	[ -n "$targets" ] || die "не удалось прочитать состав upstream '$upstream' из контейнера nginx"

	body=""
	active=0
	for target in $targets; do
		case "$target" in
		"$down_replica":*)
			body="$body
server $target down;"
			;;
		*)
			body="$body
server $target max_fails=2 fail_timeout=10s;"
			active=$((active + 1))
			;;
		esac
	done

	# Гейт против самого дорогого сценария: пометить `down` последнюю живую
	# реплику. nginx с пустым upstream не перезагрузится, трафик встанет
	# целиком — скрипт для нулевого простоя устроил бы полный.
	[ "$active" -ge 1 ] || die "в upstream '$upstream' не осталось активных реплик — выкат остановлен"

	printf '%s\n' "$body" | docker compose exec -T nginx sh -c "cat > /etc/nginx/dyn/$upstream.conf" ||
		die "не удалось записать /etc/nginx/dyn/$upstream.conf"

	docker compose exec -T nginx nginx -t >/dev/null 2>&1 ||
		die "nginx -t не принял новый список upstream '$upstream' (конфиг не применён)"
	docker compose exec -T nginx nginx -s reload ||
		die "nginx -s reload не выполнен"
}

# lb_refresh — reload без изменения состава. Нужен после пересоздания реплики:
# имена в upstream резолвятся в момент reload, а у нового контейнера другой IP.
# Без этого nginx продолжал бы слать на адрес, которого больше нет.
lb_refresh() {
	if [ "$DRY_RUN" -eq 1 ]; then
		echo "  + nginx -s reload"
		return 0
	fi
	docker compose exec -T nginx nginx -s reload || die "nginx -s reload не выполнен"
}

# ── Здоровье реплик ──────────────────────────────────────────────────────────

replica_health() {
	cid="$(docker compose ps -q "$1" 2>/dev/null)"
	if [ -z "$cid" ]; then
		echo "absent"
		return 0
	fi
	docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$cid" 2>/dev/null ||
		echo "absent"
}

wait_healthy() {
	replica="$1"
	if [ "$DRY_RUN" -eq 1 ]; then
		echo "  + ожидание healthy $replica"
		return 0
	fi

	waited=0
	while [ "$waited" -lt "$HEALTH_TIMEOUT" ]; do
		state="$(replica_health "$replica")"
		case "$state" in
		healthy)
			echo "    $replica: healthy (через ${waited}с)"
			return 0
			;;
		exited | dead)
			docker compose logs --tail 40 "$replica" >&2 || true
			die "$replica завершился со статусом '$state' — выкат остановлен, соседняя реплика продолжает работать"
			;;
		esac
		sleep 3
		waited=$((waited + 3))
	done

	docker compose logs --tail 40 "$replica" >&2 || true
	die "$replica не стал healthy за ${HEALTH_TIMEOUT}с (статус '$(replica_health "$replica")')"
}

# ── Предполётная проверка ────────────────────────────────────────────────────

preflight() {
	log "Предполётная проверка"
	# В --dry-run проверка состояния не блокирует: план выката должен быть виден
	# и на машине, где стек не поднят вовсе (например, при ревью процедуры).
	if ! nginx_running; then
		if [ "$DRY_RUN" -eq 1 ]; then
			echo "  (dry-run: стек не запущен, состояние реплик не проверяется)"
			return 0
		fi
		die "контейнер nginx не запущен: профиль двух реплик не поднят. Первый запуск — docker compose up -d --build (DEPLOYMENT §5.5)"
	fi

	degraded=""
	for svc in $SERVICES; do
		for replica in $(replicas_of "$svc"); do
			state="$(replica_health "$replica")"
			[ "$state" = "healthy" ] || degraded="$degraded $replica($state)"
		done
	done

	if [ -n "$degraded" ]; then
		echo "ВНИМАНИЕ: избыточность уже нарушена:$degraded" >&2
		echo "Обновление в таком состоянии = простой: выводить из ротации нечего." >&2
		[ "$FORCE" -eq 1 ] || die "почините реплику или запустите с --force, если простой приемлем"
	else
		echo "  реплики в строю, можно обновляться"
	fi
}

# ── Шаги ─────────────────────────────────────────────────────────────────────

step_tag() {
	if [ "$SKIP_TAG" -eq 1 ]; then
		log "Шаг 1/6 — сохранение образов пропущено (--skip-tag)"
		return 0
	fi
	log "Шаг 1/6 — сохраняю текущие образы под версионным тегом"
	# NEXUS_RESTART_SERVICES: в профиле двух реплик сервисы называются web-1,
	# web-2 и т.д., а ОБРАЗЫ остались nexus-web/nexus-receiver/nexus-sender.
	# Без этой переменной images.sh печатал бы (и выполнял по --apply) команду
	# перезапуска несуществующих сервисов.
	export NEXUS_RESTART_SERVICES="web-1 web-2 receiver-1 receiver-2 sender-1 sender-2"
	run ./scripts/deploy/images.sh tag
}

step_dump() {
	if [ "$SKIP_DUMP" -eq 1 ]; then
		log "Шаг 2/6 — дамп PostgreSQL пропущен (--skip-dump)"
		return 0
	fi
	log "Шаг 2/6 — дамп PostgreSQL"
	run ./scripts/deploy/pg_dump.sh
}

step_build() {
	log "Шаг 3/6 — сборка образов ПО ОДНОМУ"
	# По одному и последовательно: параллельная сборка трёх Go-образов упирается
	# в память линкера на слабом сервере и подвисает без внятной ошибки.
	# Собираем первую реплику пары — образ у пары общий (image: nexus-<svc>:latest),
	# второй сборки не требуется.
	for svc in $SERVICES; do
		first="$(replicas_of "$svc" | awk '{print $1}')"
		log " сборка $svc (образ nexus-$svc:latest)"
		run docker compose build "$first"
	done
}

step_migrate() {
	if [ "$SKIP_MIGRATE" -eq 1 ]; then
		log "Шаг 4/6 — миграции пропущены (--skip-migrate)"
		return 0
	fi
	log "Шаг 4/6 — миграции схемы (старые реплики продолжают работать)"
	# Образ уже новый после сборки. --no-deps: зависимости подняты, поднимать их
	# заново не нужно. Схема аддитивна (§74.2), поэтому работающие сейчас СТАРЫЕ
	# реплики новую схему переживают — обратное неверно, потому шаг и стоит
	# перед раскаткой.
	run docker compose run --rm --no-deps web-1 --migrate-up
}

roll_replica() {
	svc="$1"
	replica="$2"
	upstream="$(upstream_of "$svc")"

	log " реплика $replica"
	if [ -n "$upstream" ]; then
		echo "    вывожу из ротации"
		lb_write "$upstream" "$replica"
		# Короткая пауза — доиграть запросы, которые nginx успел отдать до
		# reload. Дальше по SIGTERM сервис сам переводит /ready в 503 и
		# выдерживает shutdown.drain_sec (§93.5).
		[ "$DRY_RUN" -eq 1 ] || sleep 2
	fi

	run docker compose up -d --no-deps --force-recreate "$replica"
	wait_healthy "$replica"

	# Sender за nginx не стоит: Receiver держит к нему пул gRPC-соединений и
	# переключается сам по gRPC health-check. Списки upstream'ов для него не
	# трогаем — только выдерживаем паузу стабилизации.
	if [ -n "$upstream" ]; then
		echo "    возвращаю в ротацию"
		lb_write "$upstream" ""
	fi
	[ "$DRY_RUN" -eq 1 ] || sleep "$SETTLE_SEC"
}

step_roll() {
	log "Шаг 5/6 — раскатка по одной реплике"
	for svc in $SERVICES; do
		log "сервис $svc"
		for replica in $(replicas_of "$svc"); do
			roll_replica "$svc" "$replica"
		done
	done
	# Финальный reload: за время выката реплики получили новые адреса. Нужен,
	# только если обновлялось что-то из-за балансировщика.
	if has_balanced_service; then
		lb_refresh
	fi
}

step_prune() {
	if [ "$NO_PRUNE" -eq 1 ]; then
		log "Шаг 6/6 — уборка пропущена (--no-prune)"
		return 0
	fi
	log "Шаг 6/6 — уборка мусора сборки"
	# ВНИМАНИЕ: только кэш сборки и dangling-слои. `docker image prune -a` здесь
	# запрещён — он удаляет образы, на которые не ссылается ни один контейнер,
	# то есть сохранённые шагом 1 версионные nexus-*:<версия>. Быстрый откат
	# (§10.1, секунды) превратился бы в пересборку трёх образов под нагрузкой.
	# Фильтр until оставляет свежий кэш: следующая сборка не начнётся с нуля.
	run docker builder prune -f --filter "until=$PRUNE_UNTIL"
	run docker image prune -f
}

step_verify() {
	log "Проверка"
	if [ "$DRY_RUN" -eq 1 ]; then
		echo "  + docker compose ps"
		echo "  + curl -s http://localhost:8000/api/version"
		return 0
	fi
	docker compose ps --format 'table {{.Service}}\t{{.Status}}' || true
	if command -v curl >/dev/null 2>&1; then
		echo
		echo "версия через балансировщик:"
		curl -fsS http://localhost:8000/api/version ||
			echo "  (не ответила — смотрите docker compose logs nginx)"
		echo
	fi
	echo "Откат: ./scripts/deploy/images.sh list, затем rollback <версия> --apply"
}

# ── Выполнение ───────────────────────────────────────────────────────────────

log "Обновление без простоя: сервисы [$SERVICES]"
[ "$DRY_RUN" -eq 1 ] && log "РЕЖИМ --dry-run: команды только печатаются"

preflight
step_tag
step_dump
step_build
step_migrate
step_roll
step_prune
step_verify

log "Готово"
