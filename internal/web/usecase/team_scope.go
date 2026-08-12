package usecase

import (
	"context"
	"fmt"

	"nexus/internal/domain"
	"nexus/internal/web/usecase/port"
)

// Резолв сквозного скоупа «все команды пользователя» (§86.2).
//
// Общий для трёх потребителей — списка узлов (§86.3), журнала (§86.7) и метрик
// (§86.4) — потому что правило одно: множество задаётся ЧЛЕНСТВАМИ, читается на
// каждый запрос и пустым не бывает молча. Три копии этого правила разъехались бы
// при первой же правке, а расхождение здесь означает утечку чужих команд.

// teamScope — id команд пользователя и карта id → команда.
//
// Карта нужна там, где выдачу надо обогатить именем команды (§62), и ничего не
// стоит: она строится из того же ответа.
//
// Пустой список членств — НЕ ошибка: пользователь без команд просто ничего не
// видит. Но и запрос с пустым набором уходить не должен — в адаптерах пустой
// TeamIDs означает «фильтра нет», то есть выдачу всего инстанса. Проверку
// делает вызывающий, здесь только резолв.
func teamScope(ctx context.Context, teams port.TeamRepo, userID string) ([]string, map[string]domain.Team, error) {
	if teams == nil {
		return nil, nil, fmt.Errorf("team scope: team repo unavailable")
	}
	memberships, err := teams.ListUserTeams(ctx, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("team scope: list memberships: %w", err)
	}
	ids := make([]string, 0, len(memberships))
	byID := make(map[string]domain.Team, len(memberships))
	for _, m := range memberships {
		ids = append(ids, m.Team.ID)
		byID[m.Team.ID] = m.Team
	}
	return ids, byID, nil
}
