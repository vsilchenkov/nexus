package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// AppSettingsRepoPg — singleton-репозиторий app_settings.
//
// JSON-документ хранится в `value JSONB` строки `id = 1` (см. миграцию
// 0006). Это даёт атомарность update'а без отдельных колонок и упрощает
// добавление новых полей в будущем.
//
// Секреты (§90.1) шифруются AES-256-GCM здесь же, в адаптере: domain.AppSettings
// снаружи всегда plaintext — то же правило, что у кредов узла в node_repo.go.
type AppSettingsRepoPg struct {
	db     DBTX
	cipher *crypto.Cipher
	logger logging.Logger
}

var _ port.AppSettingsRepo = (*AppSettingsRepoPg)(nil)

// NewAppSettingsRepoPg. cipher обязателен: без него секреты ушли бы в БД
// открытым текстом — ровно то, что чинит §90.1. Отсутствие шифра не остаётся
// незамеченным (Error при каждом создании репозитория), но и не валит сборку
// графа: единственный источник nil сегодня — тесты, которым шифрование не нужно.
func NewAppSettingsRepoPg(db DBTX, cipher *crypto.Cipher, logger logging.Logger) *AppSettingsRepoPg {
	if cipher == nil {
		logger.Error("app_settings repo created without cipher: secrets will be stored as plaintext (§90.1)")
	}
	return &AppSettingsRepoPg{db: db, cipher: cipher, logger: logger}
}

// appSettingsSecrets возвращает адреса секретных полей вместе с их путями в
// JSON — один список на шифрование и расшифровку, чтобы поле нельзя было
// добавить в одну сторону и забыть в другой.
func appSettingsSecrets(s *domain.AppSettings) []struct {
	path string
	ptr  **string
} {
	return []struct {
		path string
		ptr  **string
	}{
		{"sentry.dsn", &s.Sentry.DSN},
		{"clickhouse.password", &s.ClickHouse.Password},
		{"mail.password", &s.Mail.Password},
		{"notifications.telegram.bot_token", &s.Notifications.Telegram.BotToken},
	}
}

// encryptAppSettingsSecrets возвращает КОПИЮ настроек с зашифрованными
// секретами. Копия, а не мутация входа: usecase.Update продолжает работать с
// тем же merged-объектом после сохранения, и подмена там значений на
// шифротекст оставила бы вызывателя с нечитаемыми полями.
//
// nil-поля остаются nil («не задано»), пустые строки Cipher пропускает как есть.
func encryptAppSettingsSecrets(c *crypto.Cipher, s *domain.AppSettings) (*domain.AppSettings, error) {
	if c == nil {
		return s, nil
	}
	out := *s // секции — значения, поэтому копия структуры отвязывает указатели ниже
	for _, f := range appSettingsSecrets(&out) {
		if *f.ptr == nil {
			continue
		}
		enc, err := c.Encrypt(**f.ptr)
		if err != nil {
			return nil, fmt.Errorf("app_settings encrypt %s: %w", f.path, err)
		}
		*f.ptr = &enc
	}
	return &out, nil
}

// decryptAppSettingsSecrets расшифровывает секреты на месте.
//
// В отличие от bootstrap-overlay (там битое поле зануляется и сервис живёт на
// env-значении), здесь ошибка расшифровки возвращается наверх. Причина —
// read-modify-write в usecase.Update: Get отдаёт текущие настройки, поверх
// которых накладывается патч, и молчаливая подмена секрета на nil или на
// шифротекст затёрла бы рабочее значение при первом же сохранении любой
// соседней настройки.
func decryptAppSettingsSecrets(c *crypto.Cipher, s *domain.AppSettings, logger logging.Logger) error {
	if c == nil {
		return nil
	}
	for _, f := range appSettingsSecrets(s) {
		if *f.ptr == nil {
			continue
		}
		plain, wasEncrypted, err := c.DecryptLenient(**f.ptr)
		if err != nil {
			return fmt.Errorf("app_settings decrypt %s: %w", f.path, err)
		}
		if !wasEncrypted {
			// §90.1: значение исторически лежит открытым текстом и дозреет при
			// следующем сохранении настроек либо при ротации ключа.
			logger.Debug("app_settings: plaintext secret read as is",
				logger.Str("field", f.path))
			continue
		}
		*f.ptr = &plain
	}
	return nil
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
		if err := decryptAppSettingsSecrets(r.cipher, settings, r.logger); err != nil {
			return nil, err
		}
	}
	return settings, nil
}

func (r *AppSettingsRepoPg) Update(ctx context.Context, s *domain.AppSettings) error {
	// §90.1: секреты уходят в БД только зашифрованными. Вызыватель продолжает
	// работать со своим plaintext-объектом — шифруется копия.
	s, err := encryptAppSettingsSecrets(r.cipher, s)
	if err != nil {
		return err
	}

	// ВНИМАНИЕ: новая секция domain.AppSettings обязана попасть и сюда —
	// иначе она молча теряется при сохранении (грабли §51: logging нашёл
	// integration-сценарий TestServiceLogs_ReloadLevelAcrossServices).
	payload, err := json.Marshal(struct {
		General       domain.GeneralSettings       `json:"general"`
		Security      domain.SecuritySettings      `json:"security"`
		Sentry        domain.SentrySettings        `json:"sentry"`
		ClickHouse    domain.ClickHouseSettings    `json:"clickhouse"`
		Notifications domain.NotificationsSettings `json:"notifications"`
		Logging       domain.LoggingSettings       `json:"logging"`
		Mail          domain.MailSettings          `json:"mail"`
	}{s.General, s.Security, s.Sentry, s.ClickHouse, s.Notifications, s.Logging, s.Mail})
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
