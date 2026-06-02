package usecase

import (
	"context"
	"errors"

	"nexus/internal/domain"
	"nexus/internal/receiver/usecase/port"
)

// resolveNode ищет узел по (teamSlug, nodePath), полученным из catch-all
// сегмента URL (см. splitTeamSlugAndPath в adapter/in/http).
//
// Разбор пути неоднозначен: /api/v1/request/a/b может означать
// (team=a, path=b) ИЛИ legacy-URL без слога команды с путём-со-слешем
// (team=default, path=a/b). Парсер выбирает первую интерпретацию.
//
// Фикс #8: если первая интерпретация дала ErrNodeNotFound (команды `a` нет
// или в ней нет узла `b`), повторяем как (default-team, "a/b"). Узлы с путём
// вида `webhook/sendasynq` в default-команде теперь находятся и по адресу
// без слога (именно такой показывает UI). Явная существующая команда имеет
// приоритет: fallback срабатывает только на miss, существование узла в чужой
// команде не утекает.
func resolveNode(ctx context.Context, nodes port.NodeReader, teamSlug, nodePath string) (*domain.Node, error) {
	node, err := nodes.Get(ctx, teamSlug, nodePath)
	if err == nil || teamSlug == "" || !errors.Is(err, domain.ErrNodeNotFound) {
		return node, err
	}
	if node2, err2 := nodes.Get(ctx, "", teamSlug+"/"+nodePath); err2 == nil {
		return node2, nil
	}
	return nil, err
}
