package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"nexus/internal/domain"
	"nexus/internal/receiver/usecase/port"
)

// resolveNode ищет узел по (teamSlug, nodePath), полученным из catch-all
// сегмента URL (см. splitTeamSlugAndPath в adapter/in/http), и возвращает
// найденный узел, remainder — хвост входящего пути после пути узла — и слог
// команды, в которой узел НАЙДЕН.
//
// remainder пуст при точном совпадении пути (обычный случай). Непустой
// remainder возвращается только при path-passthrough (§39): когда точного узла
// нет, но существует узел-префикс с включённым PathPassthrough — тогда хвост
// пути приклеивается к целевому URL вызывающим кодом (route/route_async).
//
// Разбор пути неоднозначен: /api/v1/request/a/b может означать
// (team=a, path=b) ИЛИ legacy-URL без слога команды с путём-со-слешем
// (team=default, path=a/b). Парсер выбирает первую интерпретацию; при miss
// пробует вторую (фикс #8). Тот же порядок приоритетов сохранён и для
// префиксного (passthrough) матча: сначала интерпретация с явным слогом.
//
// Поэтому слог найденного узла не всегда равен слогу из адреса: при
// интерпретации B узел лежит в default-команде, а первый сегмент адреса
// оказался частью пути. Метки метрик (§101.3) обязаны нести именно его.
//
// Адрес с невалидным UTF-8 (§109.5) не доходит до хранилища — см.
// validUTF8Reader; итог такого резолва — errInvalidUTF8Path (404).
func resolveNode(ctx context.Context, nodes port.NodeReader, teamSlug, nodePath string) (*domain.Node, string, string, error) {
	nodes = validUTF8Reader{NodeReader: nodes}
	explicit := teamSlug
	if explicit == "" {
		explicit = domain.DefaultTeamSlug // NodeReader трактует пустой слог так же
	}

	// 1. Точное совпадение, интерпретация A (team=teamSlug, path=nodePath).
	node, err := nodes.Get(ctx, teamSlug, nodePath)
	if err == nil {
		return node, "", explicit, nil
	}
	if !errors.Is(err, domain.ErrNodeNotFound) {
		return nil, "", "", err // инфра-ошибка — наружу как 502, не маскируем под 404
	}

	// 2. Точное совпадение, интерпретация B (legacy без слога: default-team,
	// path="teamSlug/nodePath"). Только если слог реально был.
	if teamSlug != "" {
		if node2, err2 := nodes.Get(ctx, "", teamSlug+"/"+nodePath); err2 == nil {
			return node2, "", domain.DefaultTeamSlug, nil
		}
	}

	// 3. Префиксный (passthrough) матч, интерпретация A.
	if n, remainder, perr := prefixMatch(ctx, nodes, teamSlug, nodePath); perr == nil {
		return n, remainder, explicit, nil
	} else if !errors.Is(perr, domain.ErrNodeNotFound) {
		return nil, "", "", perr
	}

	// 4. Префиксный (passthrough) матч, интерпретация B (legacy).
	if teamSlug != "" {
		if n, remainder, perr := prefixMatch(ctx, nodes, "", teamSlug+"/"+nodePath); perr == nil {
			return n, remainder, domain.DefaultTeamSlug, nil
		} else if !errors.Is(perr, domain.ErrNodeNotFound) {
			return nil, "", "", perr
		}
	}

	if !utf8.ValidString(teamSlug) || !utf8.ValidString(nodePath) {
		return nil, "", "", errInvalidUTF8Path
	}
	return nil, "", "", domain.ErrNodeNotFound
}

// errInvalidUTF8Path — «узла нет» для адреса в чужой кодировке (§109.5). Для
// клиента неотличим от ErrNodeNotFound (404), а текст причины виден в
// debug-строке отказа: без него 404 на адрес, который «точно есть» (клиент
// прислал кириллицу в cp1251), не объяснить.
var errInvalidUTF8Path = fmt.Errorf("%w: address is not valid UTF-8", domain.ErrNodeNotFound)

// validUTF8Reader не пускает в хранилище слог и путь с невалидным UTF-8
// (§109.5): в UTF8-базе такого узла быть не может, а PostgreSQL отвечает на
// такой параметр ошибкой 22021 — раньше она становилась 502 и событием Sentry
// (NEXUS-12). Проверка стоит на КАЖДОМ обращении, а не один раз на входе:
// префиксный матч passthrough-узла (§39) ищет валидные префиксы как прежде, а
// хвост в чужой кодировке уходит получателю как есть.
type validUTF8Reader struct {
	port.NodeReader
}

// Get возвращает ErrNodeNotFound без обращения к хранилищу, если слог или путь
// — невалидный UTF-8; иначе делегирует обёрнутому NodeReader.
func (r validUTF8Reader) Get(ctx context.Context, teamSlug, path string) (*domain.Node, error) {
	if !utf8.ValidString(teamSlug) || !utf8.ValidString(path) {
		return nil, domain.ErrNodeNotFound
	}
	return r.NodeReader.Get(ctx, teamSlug, path)
}

// prefixMatch реализует path-passthrough (§39): идёт по префиксам path от
// длинного к короткому (по сегментам "/", исключая полный путь — он уже
// проверен на точное совпадение) и берёт ПЕРВЫЙ существующий узел-префикс.
//
// Если у этого узла PathPassthrough выключен — возвращает ErrNodeNotFound и НЕ
// проваливается к более коротким префиксам: более длинный существующий узел
// «выигрывает». Это убирает footgun, когда узел `a/b` (без passthrough) и узел
// `a` (с passthrough) сосуществуют — запрос `a/b/c` не должен молча уезжать на
// `a`. Найден узел с passthrough → возвращает его и remainder (отрезанный хвост).
//
// Стоимость: до N вызовов Get для пути из N сегментов (каждый кешируется в
// L2/Redis/PG). Обычный exact-трафик сюда не доходит. Известный trade-off:
// глубокий несуществующий путь даёт N промахов перед 404.
func prefixMatch(ctx context.Context, nodes port.NodeReader, teamSlug, path string) (*domain.Node, string, error) {
	segments := strings.Split(path, "/")
	for i := len(segments) - 1; i >= 1; i-- {
		prefix := strings.Join(segments[:i], "/")
		node, err := nodes.Get(ctx, teamSlug, prefix)
		if err != nil {
			if errors.Is(err, domain.ErrNodeNotFound) {
				continue
			}
			return nil, "", err
		}
		if !node.PathPassthrough {
			return nil, "", domain.ErrNodeNotFound
		}
		remainder := strings.Join(segments[i:], "/")
		return node, remainder, nil
	}
	return nil, "", domain.ErrNodeNotFound
}
