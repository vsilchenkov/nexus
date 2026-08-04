package domain

import (
	"regexp"
	"strings"
	"time"
)

// Team — команда (tenant) в multi-tenancy v2.
//
// Каждой команде соответствует своя БД ClickHouse (CHDatabase, формат
// "nexus_<slug>"). Узлы (Node) и токены (api_tokens) принадлежат ровно
// одной команде; пользователи (User) — состоят в N командах через
// user_teams membership (см. TeamMember).
type Team struct {
	ID         string
	Slug       string
	Name       string
	CHDatabase string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// TeamRole — роль пользователя внутри команды.
type TeamRole string

const (
	TeamRoleOwner  TeamRole = "owner"
	TeamRoleAdmin  TeamRole = "admin"
	TeamRoleMember TeamRole = "member"
)

func (r TeamRole) Valid() bool {
	switch r {
	case TeamRoleOwner, TeamRoleAdmin, TeamRoleMember:
		return true
	}
	return false
}

// TeamMember — запись о членстве пользователя в команде (user_teams).
//
// Login и Email — обогащение из users при чтении (ListMembers) для UI
// «Участники команды»: иначе фронт резолвил бы user_id→login из team-scoped
// списка пользователей и для не-членов текущей команды показывал сырой UUID.
// Membership-операции (AddMember/UpdateMemberRole) эти поля не используют.
type TeamMember struct {
	UserID string
	TeamID string
	Login  string
	// Name — отображаемое имя пользователя (§66), обогащение из users как и
	// Login: UI «Участники команды» показывает имя вместо логина.
	Name      string
	Email     string
	Role      TeamRole
	CreatedAt time.Time
}

// UserTeam — обогащённая запись «команда + роль» для list-эндпоинтов,
// возвращающих набор команд конкретного пользователя.
type UserTeam struct {
	Team Team
	Role TeamRole
}

// DefaultTeamSlug — slug команды, сидируемой миграцией 0008. Используется
// в bootstrap для резолва default-team UUID и в legacy-коде до перехода
// на team-switcher в сессии.
const DefaultTeamSlug = "default"

// Имя БД команды собирает InstanceID.CHDatabase (§70.2, instance.go): на ноде
// без идентификатора — "nexus_<slug>", с идентификатором — "nexus_<id>_<slug>".
// Обёртка для первого случая — CHDatabaseForSlug там же.

// teamSlugPattern и teamCHDatabasePattern совпадают с CHECK-constraint'ами
// в миграциях 0008 (teams_slug_format) и 0029 (teams_ch_database_format).
//
// §70.2: хвост ch_database — 40 символов, а не 31, как было до §70. Иначе
// суффикс ноды ("nexus_kz_" вместо "nexus_") не оставлял бы места длинному
// слагу: слаг сам допускает 32 символа. Бюджет слага под конкретную ноду
// считает InstanceID.MaxTeamSlugLen.
var (
	teamSlugPattern       = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	teamCHDatabasePattern = regexp.MustCompile(`^nexus_[a-z][a-z0-9_]{0,40}$`)
)

// reservedTeamSlugs — слаги, занятые сегментами-методами боевого адреса шины
// (§78.3). Команда с таким слагом сделала бы короткую форму адреса
// /api/v1/<slug>/<path> неоднозначной: первый сегмент прочитался бы как метод.
//
// «requestasync» в списке ради читаемости адресов: каноническое `requestAsync`
// формату слага и так не удовлетворяет (верхний регистр), а нижний регистр
// технически безопасен — сравнение сегмента-метода регистрозависимо.
//
// Миграции и CHECK-constraint'а под этот список НЕТ намеренно: уже созданные
// команды ломать нельзя. Их узлы остаются доступны по legacy-форме адреса, а
// наличие таких команд проверяется при выкате (см. DEPLOYMENT §9.5).
var reservedTeamSlugs = map[string]struct{}{
	"request":      {},
	"requestasync": {},
	"callback":     {},
}

// TeamSlugReserved сообщает, что слаг занят сегментом-методом боевого адреса
// (§78.3).
//
// Намеренно НЕ часть Team.Validate: правило применяется только к НОВЫМ слагам
// (проверка в TeamUsecase.Create). Validate вызывается и при переименовании
// команды, поэтому запрет внутри него сломал бы правку уже существующей
// команды с таким слагом — а ТЗ обещает их не ломать.
func TeamSlugReserved(slug string) bool {
	_, ok := reservedTeamSlugs[strings.ToLower(strings.TrimSpace(slug))]
	return ok
}

// Validate проверяет инварианты команды.
func (t *Team) Validate() error {
	if !teamSlugPattern.MatchString(t.Slug) {
		return ErrTeamSlugFormat
	}
	if l := len(t.Name); l < 1 || l > 255 {
		return ErrTeamNameLength
	}
	if !teamCHDatabasePattern.MatchString(t.CHDatabase) {
		return ErrTeamCHDatabaseFormat
	}
	return nil
}
