package usecase

import (
	"context"
	"encoding/json"
	"fmt"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// TeamMembershipLister — членства пользователя. Малый consumer-side интерфейс
// вместо port.TeamRepo (12 методов): PreferenceUsecase проверяет только то, что
// команда префа входит в членства (ISP). Реализуется тем же TeamRepoPg.
type TeamMembershipLister interface {
	ListUserTeams(ctx context.Context, userID string) ([]*domain.UserTeam, error)
}

// maxPreferencesPerUser — потолок числа записей предпочтений на пользователя
// (§71): защита от раздувания GET /api/me/prefs (он отдаёт ВСЕ префы одним
// ответом) и от накрутки ключей скриптом. Реальная потребность — примерно
// «ключ × (членства + 1 глобальный)».
const maxPreferencesPerUser = 200

// PreferenceUsecase — персональные предпочтения пользователя (§71): настройки
// UI, привязанные к пользователю и, опционально, к команде.
//
// Отдельный usecase, а не builder на AuthUsecase (как §49/§62): тот уже несёт
// login/logout, пароли, team-switcher, избранное и историю поиска, а builder
// там был компромиссом обратной совместимости («существующие вызовы
// NewAuthUsecase не меняются»), не архитектурным выбором. Для нового типа это
// ограничение отсутствует, поэтому — обычная конструкторная инъекция.
type PreferenceUsecase struct {
	prefs   port.UserPreferenceRepo
	members TeamMembershipLister
	logger  logging.Logger
}

func NewPreferenceUsecase(
	prefs port.UserPreferenceRepo,
	members TeamMembershipLister,
	logger logging.Logger,
) *PreferenceUsecase {
	return &PreferenceUsecase{prefs: prefs, members: members, logger: logger}
}

// Preferences — все префы пользователя: глобальные и по всем его командам (§71).
//
// Никогда не возвращает ошибку: преф — настройка UI, и при сбое хранилища она
// обязана деградировать в «дефолт», а не валить страницу, которая её читает
// (рабочий стол ждёт префы, прежде чем запросить метрики). Зеркало
// AuthUsecase.SearchHistory.
func (u *PreferenceUsecase) Preferences(ctx context.Context, userID string) []*domain.UserPreference {
	items, err := u.prefs.ListPreferences(ctx, userID)
	if err != nil {
		u.logger.Warn("list preferences failed; returning empty",
			u.logger.Str("user_id", userID), u.logger.Err(err))
		return []*domain.UserPreference{}
	}
	if items == nil {
		items = []*domain.UserPreference{}
	}
	return items
}

// SetPreference — upsert префа пользователя (§71).
//
// teamID пустой — глобальный преф (членства не проверяются, он действует
// везде). Непустой обязан входить в членства пользователя, иначе
// domain.ErrUserNotTeamMember — одинаково для чужой и несуществующей команды,
// чтобы по коду ответа нельзя было проверять существование команды.
//
// Семантику value не интерпретируем: это generic-хранилище, контракт значения
// держит клиент (§71.3).
func (u *PreferenceUsecase) SetPreference(ctx context.Context, userID, teamID, key string, value json.RawMessage) error {
	p := &domain.UserPreference{UserID: userID, TeamID: teamID, Key: key, Value: value}
	if err := p.Validate(); err != nil {
		// Ключ усечён: сюда он приходит ещё не проверенным, и мусорный запрос с
		// километровым ключом иначе целиком осел бы в журнале.
		u.logger.Debug("set preference: rejected by validation",
			u.logger.Str("user_id", userID), u.logger.Str("key", keyForLog(key)),
			u.logger.Int("key_len", len(key)),
			u.logger.Int("value_bytes", len(value)), u.logger.Err(err))
		return err
	}
	if teamID != "" {
		memberships, err := u.members.ListUserTeams(ctx, userID)
		if err != nil {
			return fmt.Errorf("list memberships: %w", err)
		}
		if !teamInMemberships(teamID, memberships) {
			u.logger.Debug("set preference: team is not in user memberships",
				u.logger.Str("user_id", userID), u.logger.Str("team_id", teamID),
				u.logger.Str("key", key))
			return domain.ErrUserNotTeamMember
		}
	}
	if err := u.prefs.SetPreference(ctx, p, maxPreferencesPerUser); err != nil {
		u.logger.Debug("set preference: storage rejected",
			u.logger.Str("user_id", userID), u.logger.Str("team_id", teamID),
			u.logger.Str("key", key), u.logger.Err(err))
		return fmt.Errorf("set preference: %w", err)
	}
	// Аудит не пишем намеренно (§71.4): клик по кнопке «По умолчанию» —
	// высокочастотное личное действие без ценности для расследования, оно бы
	// только зашумило журнал. То же правило, что для истории поиска (§62).
	return nil
}

// keyForLog — ключ, пригодный для журнала: обрезан по доменной границе. Нужен
// только на ветке отказа валидации — там ключ ещё произвольный (клиент мог
// прислать что угодно), а логировать его целиком нельзя.
func keyForLog(key string) string {
	const maxLogged = 64
	if len(key) <= maxLogged {
		return key
	}
	return key[:maxLogged] + "…"
}
