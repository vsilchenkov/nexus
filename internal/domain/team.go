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
	// ExternalURL — адрес, по которому узлы команды доступны СНАРУЖИ контура
	// (§89.4): внешние системы часто ходят не в Nexus напрямую, а через шлюз со
	// своим адресом и своим префиксом пути. Пустая строка = не задан.
	//
	// Значение — БАЗА: интерфейс дописывает к ней путь узла. Маршрутизации не
	// касается вообще (Receiver его не читает) — это справочные данные для
	// оператора, которому надо выдать наружу правильный адрес.
	ExternalURL string
	CreatedAt   time.Time
	UpdatedAt   time.Time
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

// MaxTeamExternalURLLen — потолок длины внешней ссылки (§89.4). Совпадает с
// лимитом target_url узла и с VARCHAR(2048) в миграции 0037.
const MaxTeamExternalURLLen = 2048

// NormalizeTeamExternalURL приводит внешнюю ссылку к каноническому виду:
// обрезает пробелы по краям и хвостовые слеши.
//
// Хвостовой слеш срезается именно здесь, а не в UI: путь узла приклеивается к
// этой базе, и «https://gw.example.com/nexus/» дал бы адрес с «//» посередине —
// внешний шлюз такой путь обычно не узнаёт, а глазами двойной слеш не виден.
func NormalizeTeamExternalURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

// ValidateTeamExternalURL проверяет внешнюю ссылку команды (§89.4): пустая
// строка допустима (= не задана), иначе — абсолютный http(s)-URL.
//
// В отличие от ValidatePublicBaseURL (§28) ПУТЬ здесь разрешён: внешний шлюз
// обычно публикует шину под своим префиксом («https://gw.example.com/nexus»), и
// запрет пути сделал бы поле бесполезным ровно в основном сценарии. Query и
// fragment запрещены: к базе приклеивается путь узла, после «?» это дало бы
// заведомо неработающий адрес.
//
// Вызывается на УЖЕ нормализованном значении (см. Team.Validate).
func ValidateTeamExternalURL(raw string) error {
	if raw == "" {
		return nil
	}
	if len(raw) > MaxTeamExternalURLLen {
		return ErrTeamExternalURLInvalid
	}
	u, ok := AbsoluteHTTPURL(raw)
	if !ok {
		return ErrTeamExternalURLInvalid
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return ErrTeamExternalURLInvalid
	}
	return nil
}

// Validate проверяет инварианты команды.
//
// Внешняя ссылка нормализуется НА МЕСТЕ (t.ExternalURL перезаписывается) — у
// Team нет SetDefaults, куда это можно было бы вынести; приём тот же, что у
// PeerInstance.Validate с BaseURL. Так в базу и в ответ API попадает ровно то
// значение, которое прошло проверку.
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
	t.ExternalURL = NormalizeTeamExternalURL(t.ExternalURL)
	return ValidateTeamExternalURL(t.ExternalURL)
}
