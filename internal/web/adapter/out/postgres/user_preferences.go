package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	"nexus/internal/domain"
	"nexus/internal/web/usecase/port"
)

// UserPreferenceRepo (§71) лёг на UserRepoPg — как SearchHistoryRepo (§62):
// преф привязан к пользователю, команда лишь уточняет область действия.
var _ port.UserPreferenceRepo = (*UserRepoPg)(nil)

// ListPreferences — все префы пользователя: глобальные (team_id IS NULL) и по
// всем его командам (§71). Порядок стабильный — по ключу, глобальный перед
// командными: клиент резолвит «команда → глобальный», и предсказуемый порядок
// упрощает чтение дампа в отладке.
func (r *UserRepoPg) ListPreferences(ctx context.Context, userID string) ([]*domain.UserPreference, error) {
	rows, err := r.pool.Query(ctx, `
SELECT COALESCE(team_id::text, ''), key, value, updated_at
FROM user_preferences
WHERE user_id = $1::uuid
ORDER BY key, team_id NULLS FIRST`, userID)
	if err != nil {
		return nil, fmt.Errorf("list user preferences: %w", err)
	}
	defer rows.Close()
	var out []*domain.UserPreference
	for rows.Next() {
		p := &domain.UserPreference{UserID: userID}
		var value []byte
		if err := rows.Scan(&p.TeamID, &p.Key, &value, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan user preference: %w", err)
		}
		p.Value = value
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetPreference — upsert префа с атомарной проверкой потолка записей (§71).
//
// Лимит проверяется в самом INSERT'е (INSERT … SELECT … WHERE), а не отдельным
// SELECT'ом: иначе между проверкой и вставкой оставалось бы окно. Условие
// пропускает запрос, если записей меньше потолка ЛИБО такой ключ у пользователя
// уже есть — обновление существующего префа не должно упираться в лимит.
//
// IS NOT DISTINCT FROM обязателен: обычное `=` с NULL даёт NULL, и глобальный
// преф (team_id IS NULL) всегда считался бы новой записью.
//
// Выражение в ON CONFLICT обязано совпадать с индексом user_preferences_uniq
// (миграция 0031) — по нему PostgreSQL и находит нужный индекс. Индекс сделан
// по COALESCE, а не UNIQUE NULLS NOT DISTINCT: последнее требует PostgreSQL 15,
// а поддерживаемый минимум — 12 (версия боевого сервера).
//
// Значение передаётся строкой, а не []byte: pgx кодирует []byte как bytea, из
// которого приведения к jsonb нет.
//
// Каст `$3::text` обязателен в ОБОИХ вхождениях ключа: без него PostgreSQL
// выводит для параметра разные типы (колонка VARCHAR(64) в INSERT против
// сравнения в EXISTS) и падает с «inconsistent types deduced for parameter»
// (SQLSTATE 42P08).
func (r *UserRepoPg) SetPreference(ctx context.Context, p *domain.UserPreference, maxPerUser int) error {
	tag, err := r.pool.Exec(ctx, `
INSERT INTO user_preferences (user_id, team_id, key, value, updated_at)
SELECT $1::uuid, NULLIF($2::text, '')::uuid, $3::text, $4::jsonb, now()
WHERE (SELECT count(*) FROM user_preferences WHERE user_id = $1::uuid) < $5
   OR EXISTS (
        SELECT 1 FROM user_preferences
        WHERE user_id = $1::uuid
          AND team_id IS NOT DISTINCT FROM NULLIF($2::text, '')::uuid
          AND key = $3::text)
ON CONFLICT (user_id, COALESCE(team_id, '00000000-0000-0000-0000-000000000000'::uuid), key)
DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		p.UserID, p.TeamID, p.Key, string(p.Value), maxPerUser)
	if err != nil {
		// FK-нарушение — team_id вне членств пользователя (в том числе гонка с
		// исключением из команды), как в ReplaceFavoriteTeams (§49).
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return domain.ErrUserNotTeamMember
		}
		return fmt.Errorf("set user preference: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Единственная причина нуля: WHERE не пропустил запрос — новых ключей
		// больше потолка (конфликт по констрейнту дал бы UPDATE, то есть 1).
		return domain.ErrPreferencesLimit
	}
	return nil
}
