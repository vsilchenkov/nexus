package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"bus/internal/domain"
	"bus/internal/platform/logging"
	"bus/internal/web/usecase/port"
)

// AppSettingsRepoPg — singleton-репозиторий app_settings.
//
// JSON-документ хранится в `value JSONB` строки `id = 1` (см. миграцию
// 0006). Это даёт атомарность update'а без отдельных колонок и упрощает
// добавление новых полей в будущем.
type AppSettingsRepoPg struct {
	db     DBTX
	logger logging.Logger
}

var _ port.AppSettingsRepo = (*AppSettingsRepoPg)(nil)

func NewAppSettingsRepoPg(db DBTX, logger logging.Logger) *AppSettingsRepoPg {
	return &AppSettingsRepoPg{db: db, logger: logger}
}

func (r *AppSettingsRepoPg) Get(ctx context.Context) (*domain.AppSettings, error) {
	var (
		raw       []byte
		updatedBy *string
	)
	settings := &domain.AppSettings{}

	err := r.db.QueryRow(ctx,
		`SELECT value, updated_at, updated_by FROM app_settings WHERE id = 1`,
	).Scan(&raw, &settings.UpdatedAt, &updatedBy)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Миграция гарантирует строку — но на всякий случай.
			return settings, nil
		}
		return nil, fmt.Errorf("app_settings get: %w", err)
	}
	if updatedBy != nil {
		settings.UpdatedBy = *updatedBy
	}
	if len(raw) > 0 && string(raw) != "{}" {
		if err := json.Unmarshal(raw, settings); err != nil {
			return nil, fmt.Errorf("app_settings unmarshal: %w", err)
		}
	}
	return settings, nil
}

func (r *AppSettingsRepoPg) Update(ctx context.Context, s *domain.AppSettings) error {
	payload, err := json.Marshal(struct {
		Sentry     domain.SentrySettings     `json:"sentry"`
		ClickHouse domain.ClickHouseSettings `json:"clickhouse"`
	}{s.Sentry, s.ClickHouse})
	if err != nil {
		return fmt.Errorf("app_settings marshal: %w", err)
	}

	var updatedBy any
	if s.UpdatedBy != "" {
		updatedBy = s.UpdatedBy
	}

	_, err = r.db.Exec(ctx,
		`UPDATE app_settings
		   SET value = $1::jsonb, updated_at = now(), updated_by = $2
		 WHERE id = 1`, payload, updatedBy)
	if err != nil {
		return fmt.Errorf("app_settings update: %w", err)
	}
	return nil
}
