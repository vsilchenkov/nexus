package backup

import (
	"errors"
	"fmt"
	"strings"

	"nexus/internal/domain"
)

// ErrInvalidSnapshot — снимок внутренне противоречив: висячая ссылка, дубль
// ключа, недопустимое значение перечисления. Выгрузка идёт одной транзакцией
// под внешними ключами, поэтому такого в честном файле не бывает: это
// повреждённый либо собранный вручную снимок, и восстанавливать его нельзя.
var ErrInvalidSnapshot = errors.New("backup: invalid snapshot")

// maxProblems — сколько нарушений перечислять: сотня одинаковых строк в
// ответе API не помогает, первых хватает, чтобы понять причину.
const maxProblems = 20

// Validate проверяет ссылочную целостность и перечисления снимка.
func (s *Snapshot) Validate() error {
	v := &validator{}
	teams := s.validateTeams(v)
	users := s.validateUsers(v, teams)
	s.validateMemberships(v, users, teams)
	templates, groups, hosts := s.validateCatalogs(v)
	nodes := s.validateNodes(v, teams, templates, groups)
	for _, l := range s.NodeHosts {
		v.ref(nodes, l.NodeID, "node_allowed_hosts.node_id")
		v.ref(hosts, l.HostID, "node_allowed_hosts.host_id")
	}
	return v.err()
}

func (s *Snapshot) validateTeams(v *validator) set {
	teams, slugs := set{}, set{}
	for _, t := range s.Teams {
		v.unique(teams, t.ID, "teams.id")
		v.unique(slugs, t.Slug, "teams.slug")
	}
	return teams
}

func (s *Snapshot) validateUsers(v *validator, teams set) set {
	users, logins := set{}, set{}
	for _, u := range s.Users {
		v.unique(users, u.ID, "users.id")
		v.unique(logins, u.Login, "users.login")
		v.check(domain.UserRole(u.Role).Valid(), "users.role %q of %s", u.Role, u.Login)
		v.ref(teams, u.DefaultTeamID, "users.default_team_id")
	}
	return users
}

func (s *Snapshot) validateMemberships(v *validator, users, teams set) {
	for _, m := range s.UserTeams {
		v.ref(users, m.UserID, "user_teams.user_id")
		v.ref(teams, m.TeamID, "user_teams.team_id")
		v.check(domain.TeamRole(m.Role).Valid(), "user_teams.role %q", m.Role)
	}
	for _, f := range s.Favorites {
		v.ref(users, f.UserID, "user_team_favorites.user_id")
		v.ref(teams, f.TeamID, "user_team_favorites.team_id")
	}
	for _, p := range s.Preferences {
		v.ref(users, p.UserID, "user_preferences.user_id")
		if p.TeamID != nil {
			v.ref(teams, *p.TeamID, "user_preferences.team_id")
		}
	}
	for _, t := range s.APITokens {
		v.ref(users, t.UserID, "api_tokens.user_id")
		v.ref(teams, t.TeamID, "api_tokens.team_id")
	}
}

func (s *Snapshot) validateCatalogs(v *validator) (templates, groups, hosts set) {
	templates, groups, hosts = set{}, set{}, set{}
	defaults := 0
	for _, t := range s.CHTemplates {
		v.unique(templates, t.ID, "ch_templates.id")
		if t.IsDefault {
			defaults++
		}
	}
	v.check(defaults <= 1, "ch_templates: %d default templates", defaults)
	for _, g := range s.NodeGroups {
		v.unique(groups, g.ID, "node_groups.id")
	}
	for _, h := range s.Hosts {
		v.unique(hosts, h.ID, "host_allowlist.id")
		v.check(domain.HostKind(h.Kind).Valid(), "host_allowlist.kind %q", h.Kind)
	}
	return templates, groups, hosts
}

func (s *Snapshot) validateNodes(v *validator, teams, templates, groups set) set {
	nodes, paths := set{}, set{}
	for _, n := range s.Nodes {
		v.unique(nodes, n.ID, "nodes.id")
		v.unique(paths, n.TeamID+"/"+n.Path, "nodes.team_id+path")
		v.ref(teams, n.TeamID, "nodes.team_id")
		if n.ClickHouseTemplateID != "" {
			v.ref(templates, n.ClickHouseTemplateID, "nodes.clickhouse_template_id")
		}
		if n.GroupID != "" {
			v.ref(groups, n.GroupID, "nodes.group_id")
		}
	}
	return nodes
}

type set map[string]struct{}

// validator копит нарушения, а не останавливается на первом: оператору
// полезнее увидеть все причины разом.
type validator struct {
	problems []string
	total    int
}

func (v *validator) check(ok bool, format string, args ...any) {
	if ok {
		return
	}
	v.total++
	if len(v.problems) < maxProblems {
		v.problems = append(v.problems, fmt.Sprintf(format, args...))
	}
}

func (v *validator) unique(seen set, key, what string) {
	_, dup := seen[key]
	v.check(key != "" && !dup, "%s: empty or duplicate %q", what, key)
	seen[key] = struct{}{}
}

func (v *validator) ref(known set, key, what string) {
	_, ok := known[key]
	v.check(ok, "%s: dangling reference %q", what, key)
}

func (v *validator) err() error {
	if v.total == 0 {
		return nil
	}
	msg := strings.Join(v.problems, "; ")
	if v.total > len(v.problems) {
		msg += fmt.Sprintf("; … and %d more", v.total-len(v.problems))
	}
	return fmt.Errorf("%w: %s", ErrInvalidSnapshot, msg)
}
