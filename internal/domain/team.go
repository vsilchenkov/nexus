package domain

import (
	"regexp"
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
type TeamMember struct {
	UserID    string
	TeamID    string
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

// teamSlugPattern и teamCHDatabasePattern совпадают с CHECK-constraint'ами
// в миграции 0008 (teams_slug_format / teams_ch_database_format).
var (
	teamSlugPattern       = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	teamCHDatabasePattern = regexp.MustCompile(`^nexus_[a-z][a-z0-9_]{0,31}$`)
)

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
