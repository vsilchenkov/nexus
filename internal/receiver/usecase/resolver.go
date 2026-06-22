package usecase

import (
	"context"
	"errors"
	"strings"

	"nexus/internal/domain"
	"nexus/internal/receiver/usecase/port"
)

// resolveNode ищет узел по (teamSlug, nodePath), полученным из catch-all
// сегмента URL (см. splitTeamSlugAndPath в adapter/in/http), и возвращает
// найденный узел вместе с remainder — хвостом входящего пути после пути узла.
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
func resolveNode(ctx context.Context, nodes port.NodeReader, teamSlug, nodePath string) (*domain.Node, string, error) {
	// 1. Точное совпадение, интерпретация A (team=teamSlug, path=nodePath).
	node, err := nodes.Get(ctx, teamSlug, nodePath)
	if err == nil {
		return node, "", nil
	}
	if !errors.Is(err, domain.ErrNodeNotFound) {
		return nil, "", err // инфра-ошибка — наружу как 502, не маскируем под 404
	}

	// 2. Точное совпадение, интерпретация B (legacy без слога: default-team,
	// path="teamSlug/nodePath"). Только если слог реально был.
	if teamSlug != "" {
		if node2, err2 := nodes.Get(ctx, "", teamSlug+"/"+nodePath); err2 == nil {
			return node2, "", nil
		}
	}

	// 3. Префиксный (passthrough) матч, интерпретация A.
	if n, remainder, perr := prefixMatch(ctx, nodes, teamSlug, nodePath); perr == nil {
		return n, remainder, nil
	} else if !errors.Is(perr, domain.ErrNodeNotFound) {
		return nil, "", perr
	}

	// 4. Префиксный (passthrough) матч, интерпретация B (legacy).
	if teamSlug != "" {
		if n, remainder, perr := prefixMatch(ctx, nodes, "", teamSlug+"/"+nodePath); perr == nil {
			return n, remainder, nil
		} else if !errors.Is(perr, domain.ErrNodeNotFound) {
			return nil, "", perr
		}
	}

	return nil, "", domain.ErrNodeNotFound
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
