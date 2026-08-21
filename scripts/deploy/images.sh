#!/bin/sh
# §74.5 — версионирование образов Nexus и мгновенный откат кода.
#
# Образы приложения собираются на сервере (`docker compose up -d --build`) и в
# registry не публикуются, а compose при каждой сборке перетирает тег
# `nexus-<service>:latest`. Прежний образ остаётся в системе, но БЕЗ имени
# (`<none>`), поэтому вернуться на него штатными средствами нельзя — откат
# превращается в пересборку трёх образов. Скрипт закрывает ровно этот разрыв:
# перед обновлением вешает на текущие образы версионный тег, а в аварии
# возвращает `:latest` на сохранённый образ и перезапускает сервисы БЕЗ сборки.
#
# Использование:
#   ./scripts/deploy/images.sh tag [версия]      # перед обновлением; версия по умолчанию — git describe
#   ./scripts/deploy/images.sh list              # какие версии сохранены на сервере
#   ./scripts/deploy/images.sh rollback <версия> # вернуть :latest и напечатать команду перезапуска
#   ./scripts/deploy/images.sh rollback <версия> --apply   # ... и сразу её выполнить
#
# Переменные окружения (варианты установки A/B/C — см. DEPLOYMENT.md):
#   NEXUS_IMAGE_PREFIX   префикс имён образов        (по умолчанию nexus)
#   NEXUS_SERVICES       список ОБРАЗОВ               (по умолчанию "web receiver sender")
#   NEXUS_RESTART_SERVICES  какие сервисы compose перезапускать при откате
#                        (по умолчанию = NEXUS_SERVICES)
#   NEXUS_COMPOSE_FILE   compose-файл для перезапуска (по умолчанию корневой, без -f)
#
# Зачем NEXUS_RESTART_SERVICES отдельно от NEXUS_SERVICES: в профиле двух реплик
# (§93, deploy/docker-compose.ha.yml) сервисы называются web-1/web-2/…, а образ
# у пары общий — nexus-web:latest. Тегировать надо образы, перезапускать —
# сервисы, и списки перестают совпадать. Скрипт rolling.sh выставляет эту
# переменную сам.
#
# Скрипт НЕ заменяет `git checkout` прежнего тега: рабочее дерево нужно вернуть,
# чтобы следующая сборка шла от правильного кода. Но в аварии порядок обратный —
# сперва поднять сервис из сохранённого образа, потом приводить дерево в порядок.

set -eu

PREFIX="${NEXUS_IMAGE_PREFIX:-nexus}"
SERVICES="${NEXUS_SERVICES:-web receiver sender}"
RESTART_SERVICES="${NEXUS_RESTART_SERVICES:-$SERVICES}"
COMPOSE_FILE="${NEXUS_COMPOSE_FILE:-}"

die() {
	echo "ОШИБКА: $*" >&2
	exit 1
}

usage() {
	sed -n '2,33p' "$0" | sed 's/^# \{0,1\}//'
	exit "${1:-0}"
}

require_docker() {
	command -v docker >/dev/null 2>&1 || die "docker не найден в PATH"
}

# normalize_version срезает ведущий `v` (v1.21.1 → 1.21.1), как это делает
# сборка (см. DEPLOYMENT.md §9.4): иначе тег образа разошёлся бы с версией,
# которую сервис отдаёт в /api/version.
normalize_version() {
	printf '%s' "${1#v}"
}

current_version() {
	git describe --tags --always --dirty 2>/dev/null || die "не удалось определить версию через git — укажите её аргументом"
}

image_exists() {
	docker image inspect "$1" >/dev/null 2>&1
}

compose_cmd() {
	if [ -n "$COMPOSE_FILE" ]; then
		echo "docker compose -f $COMPOSE_FILE"
	else
		echo "docker compose"
	fi
}

cmd_tag() {
	version="$(normalize_version "${1:-$(current_version)}")"
	case "$version" in
	*-dirty)
		echo "ПРЕДУПРЕЖДЕНИЕ: рабочее дерево грязное, версия '$version' не соответствует тегу" >&2
		;;
	esac

	for svc in $SERVICES; do
		src="$PREFIX-$svc:latest"
		dst="$PREFIX-$svc:$version"
		image_exists "$src" || die "образ $src не найден — сервисы ещё не собирались на этом хосте?"
		if image_exists "$dst"; then
			echo "ПРЕДУПРЕЖДЕНИЕ: тег $dst уже существует и будет переставлен" >&2
		fi
		docker image tag "$src" "$dst"
		echo "сохранено: $dst"
	done
	echo
	echo "Откат на эту версию: ./scripts/deploy/images.sh rollback $version"
}

cmd_list() {
	for svc in $SERVICES; do
		echo "$PREFIX-$svc:"
		docker image ls "$PREFIX-$svc" --format '  {{.Tag}}\t{{.ID}}\t{{.CreatedSince}}' || true
	done
}

cmd_rollback() {
	[ $# -ge 1 ] || die "нужна версия: images.sh rollback <версия> [--apply]"
	version="$(normalize_version "$1")"
	shift
	apply=0
	for arg in "$@"; do
		case "$arg" in
		--apply) apply=1 ;;
		*) die "неизвестный аргумент: $arg" ;;
		esac
	done

	# Сперва проверяем ВСЕ образы и только потом переставляем теги: половина
	# сервисов на старой версии, половина на новой — худший из исходов.
	for svc in $SERVICES; do
		image_exists "$PREFIX-$svc:$version" ||
			die "образ $PREFIX-$svc:$version не найден. Сохранённые версии: ./scripts/deploy/images.sh list"
	done

	for svc in $SERVICES; do
		docker image tag "$PREFIX-$svc:$version" "$PREFIX-$svc:latest"
		echo "$PREFIX-$svc:latest → $version"
	done

	restart="$(compose_cmd) up -d --no-build --force-recreate $RESTART_SERVICES"
	echo
	if [ "$apply" -eq 1 ]; then
		echo "+ $restart"
		# shellcheck disable=SC2086 # список сервисов и -f должны разбиться на слова
		$restart
		echo
		echo "Готово. Проверьте версию: curl -s http://localhost:8000/api/version"
	else
		echo "Теги переставлены. Перезапустите сервисы БЕЗ сборки:"
		echo "  $restart"
	fi
	echo "ВАЖНО: если новая версия добавляла миграции, схему откатывайте ОБРАЗОМ НОВОЙ версии"
	echo "       и до пересборки — см. DEPLOYMENT.md §10."
}

require_docker

case "${1:-}" in
tag)
	shift
	cmd_tag "$@"
	;;
list)
	cmd_list
	;;
rollback)
	shift
	cmd_rollback "$@"
	;;
-h | --help | help | "")
	usage 0
	;;
*)
	echo "неизвестная команда: $1" >&2
	usage 1
	;;
esac
