package usecase

import (
	"strings"

	"nexus/internal/domain/backup"
	"nexus/internal/platform/backupfile"
	"nexus/internal/web/usecase/port"
)

// importBuilder собирает план восстановления: сопоставляет строки файла со
// строками цели по естественным ключам (§110.6). Совпавшая строка остаётся с
// id цели, ссылки на неё перенаправляются; новая получает id из файла.
type importBuilder struct {
	u   *BackupUsecase
	h   *backupfile.Header
	src *backup.Snapshot
	tgt *backup.Snapshot
	req BackupImportRequest

	set     *port.RestoreSet
	preview *BackupImportPreview

	// keepDBNames — у источника и цели один instance.id: имена баз команд
	// берутся из файла как есть. Иначе база команды, созданной до §70
	// (nexus_<slug> на ноде, получившей идентификатор позже), не совпала бы с
	// вычисленной, и уцелевшие в ней логи стали бы невидимы.
	keepDBNames bool

	teamID     map[string]string // id команды в файле → id на цели
	teamDB     map[string]string // база команды в файле → база на цели
	teams      []plannedTeam     // все команды цели после восстановления
	userID     map[string]string
	templateID map[string]string
	groupID    map[string]string
	// hostByID — хост файла → строка, которая будет на цели (своя или цели).
	hostByID map[string]backup.Host
	// members — членства (user, team) на цели после восстановления: избранное
	// и личные настройки без членства нарушили бы внешний ключ.
	members map[[2]string]bool

	databases []BackupCHDatabasePlan
	tables    []chTableAction
}

// plannedTeam — команда цели после восстановления.
type plannedTeam struct {
	id, slug, db string
}

func newImportBuilder(u *BackupUsecase, h *backupfile.Header, src, tgt *backup.Snapshot, req BackupImportRequest) *importBuilder {
	return &importBuilder{
		u: u, h: h, src: src, tgt: tgt, req: req,
		set: &port.RestoreSet{ReplaceLogMasks: true},
		preview: &BackupImportPreview{
			Entities: map[string]BackupEntityPlan{},
			Skipped:  []BackupIssue{}, Warnings: []BackupIssue{}, Blockers: []BackupIssue{},
			Databases: []BackupCHDatabasePlan{}, SettingsSections: []string{},
		},
		keepDBNames: h.SourceInstanceID == u.source.InstanceID.String(),
		teamID:      map[string]string{}, teamDB: map[string]string{},
		userID: map[string]string{}, templateID: map[string]string{}, groupID: map[string]string{},
		hostByID: map[string]backup.Host{}, members: map[[2]string]bool{},
	}
}

func (b *importBuilder) count(entity string, create, existing int) {
	e := b.preview.Entities[entity]
	e.Create += create
	e.Existing += existing
	b.preview.Entities[entity] = e
}

func (b *importBuilder) warn(code, subject, detail string) {
	b.preview.Warnings = append(b.preview.Warnings, BackupIssue{Code: code, Subject: subject, Detail: detail})
}

func (b *importBuilder) block(code, subject, detail string) {
	b.preview.Blockers = append(b.preview.Blockers, BackupIssue{Code: code, Subject: subject, Detail: detail})
}

func (b *importBuilder) skip(code, subject string) {
	b.preview.Skipped = append(b.preview.Skipped, BackupIssue{Code: code, Subject: subject})
}

// mapped — id на цели; ссылка, которой нет в карте, остаётся как в файле
// (Validate снимка гарантирует, что она указывает на строку файла).
func mapped(m map[string]string, id string) string {
	if to, ok := m[id]; ok {
		return to
	}
	return id
}

// remapIdentity — команды, пользователи, членство.
func (b *importBuilder) remapIdentity() {
	b.remapTeams()
	byLogin := map[string]string{}
	for _, u := range b.tgt.Users {
		byLogin[strings.ToLower(u.Login)] = u.ID
	}
	for _, u := range b.src.Users {
		if id, ok := byLogin[strings.ToLower(u.Login)]; ok {
			// Учётная запись цели не меняется: пароль, который администратор
			// задал при установке, остаётся рабочим.
			b.userID[u.ID] = id
			b.skip("user_exists", u.Login)
			b.count("users", 0, 1)
			continue
		}
		b.userID[u.ID] = u.ID
		u.DefaultTeamID = mapped(b.teamID, u.DefaultTeamID)
		b.set.Users = append(b.set.Users, u)
		b.count("users", 1, 0)
	}
	for _, m := range b.tgt.UserTeams {
		b.members[[2]string{m.UserID, m.TeamID}] = true
	}
	for _, m := range b.src.UserTeams {
		m.UserID, m.TeamID = mapped(b.userID, m.UserID), mapped(b.teamID, m.TeamID)
		key := [2]string{m.UserID, m.TeamID}
		if b.members[key] {
			continue
		}
		b.members[key] = true
		b.set.UserTeams = append(b.set.UserTeams, m)
	}
}

// remapTeams — совпавшая по слагу команда (Default) остаётся строкой цели:
// её id Web кеширует на старте. Меняются название, внешняя ссылка и, при том
// же instance.id, имя базы.
func (b *importBuilder) remapTeams() {
	bySlug := map[string]backup.Team{}
	for _, t := range b.tgt.Teams {
		bySlug[t.Slug] = t
	}
	for _, t := range b.src.Teams {
		if cur, ok := bySlug[t.Slug]; ok {
			cur.Name, cur.ExternalURL = t.Name, t.ExternalURL
			if b.keepDBNames {
				cur.CHDatabase = t.CHDatabase
			}
			b.addTeam(t, cur)
			b.set.TeamUpdates = append(b.set.TeamUpdates, cur)
			b.count("teams", 0, 1)
			continue
		}
		if !b.keepDBNames {
			if err := b.u.source.InstanceID.ValidateTeamSlug(t.Slug); err != nil {
				b.block("team_slug_too_long", t.Slug, err.Error())
			}
		}
		nt := t
		if !b.keepDBNames {
			nt.CHDatabase = b.u.source.InstanceID.CHDatabase(t.Slug)
		}
		b.addTeam(t, nt)
		b.set.Teams = append(b.set.Teams, nt)
		b.count("teams", 1, 0)
	}
}

func (b *importBuilder) addTeam(src, dst backup.Team) {
	b.teamID[src.ID] = dst.ID
	b.teamDB[src.CHDatabase] = dst.CHDatabase
	b.teams = append(b.teams, plannedTeam{id: dst.ID, slug: dst.Slug, db: dst.CHDatabase})
}

// remapAccess — избранное, личные настройки, API-токены.
func (b *importBuilder) remapAccess() {
	for _, f := range b.src.Favorites {
		f.UserID, f.TeamID = mapped(b.userID, f.UserID), mapped(b.teamID, f.TeamID)
		if b.members[[2]string{f.UserID, f.TeamID}] {
			b.set.Favorites = append(b.set.Favorites, f)
		}
	}
	for _, p := range b.src.Preferences {
		p.UserID = mapped(b.userID, p.UserID)
		if p.TeamID != nil {
			team := mapped(b.teamID, *p.TeamID)
			if !b.members[[2]string{p.UserID, team}] {
				continue
			}
			p.TeamID = &team
		}
		b.set.Preferences = append(b.set.Preferences, p)
	}
	hashes := map[string]bool{}
	for _, t := range b.tgt.APITokens {
		hashes[t.TokenHash] = true
	}
	for _, t := range b.src.APITokens {
		if hashes[t.TokenHash] {
			b.skip("api_token_exists", t.Name)
			b.count("api_tokens", 0, 1)
			continue
		}
		t.UserID, t.TeamID = mapped(b.userID, t.UserID), mapped(b.teamID, t.TeamID)
		b.set.APITokens = append(b.set.APITokens, t)
		b.count("api_tokens", 1, 0)
	}
}
