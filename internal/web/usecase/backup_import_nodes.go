package usecase

import (
	"fmt"
	"net/url"
	"strings"

	"nexus/internal/domain"
	"nexus/internal/domain/backup"
)

// planNodes — узлы: команда, группа, шаблон и таблица по картам, снимок
// allowlist по сопоставленным хостам, проверка, статусы, лимит на команду.
// UUID узла сохраняется всегда: логи в ClickHouse привязаны к node_id (§61).
func (b *importBuilder) planNodes() {
	slugs := map[string]string{}
	for _, t := range b.src.Teams {
		slugs[t.ID] = t.Slug
	}
	links := b.remapNodeHosts()
	perTeam := map[string]int{}
	for _, bn := range b.src.Nodes {
		n := b.prepareNode(bn, slugs[bn.TeamID]+"/"+bn.Path, links[bn.ID])
		perTeam[n.TeamID]++
		b.set.Nodes = append(b.set.Nodes, n)
		b.countStatus(n.Status)
	}
	b.count("nodes", len(b.set.Nodes), 0)
	b.checkNodeLimit(perTeam)
}

// remapNodeHosts — связи узлов с хостами на id цели; возвращает хосты узла.
func (b *importBuilder) remapNodeHosts() map[string][]backup.Host {
	out := map[string][]backup.Host{}
	for _, l := range b.src.NodeHosts {
		h := b.hostByID[l.HostID]
		l.HostID = h.ID
		b.set.NodeHosts = append(b.set.NodeHosts, l)
		out[l.NodeID] = append(out[l.NodeID], h)
	}
	return out
}

func (b *importBuilder) prepareNode(bn backup.Node, subject string, hosts []backup.Host) *domain.Node {
	n := bn.ToDomain()
	spec, ackErr := bn.AckSpec()
	n.AsyncAck = spec
	n.TeamID = mapped(b.teamID, n.TeamID)
	if n.GroupID != "" {
		n.GroupID = mapped(b.groupID, n.GroupID)
	}
	if n.ClickHouseTemplateID != "" {
		n.ClickHouseTemplateID = mapped(b.templateID, n.ClickHouseTemplateID)
	}
	b.rebaseTable(n, subject)
	if len(hosts) > 0 {
		// Снимок allowlist — производное каталога (§23): у совпавшего хоста
		// цели шаблон может отличаться регистром.
		n.URLAllowedHosts = encodeBackupHosts(hosts)
	}
	n.NormalizeForRootMethod()
	b.applyStatusMode(n)
	b.disableIfRejected(n, subject, ackErr)
	return n
}

// rebaseTable переносит таблицу узла из базы команды источника в базу
// команды цели. Внешняя таблица §64 не меняется никогда.
func (b *importBuilder) rebaseTable(n *domain.Node, subject string) {
	if n.ClickHouseTable == "" || n.ExternalTable {
		return
	}
	db, table, ok := strings.Cut(n.ClickHouseTable, ".")
	if !ok {
		return
	}
	if to, ok := b.teamDB[db]; ok {
		n.ClickHouseTable = to + "." + table
		return
	}
	b.warn("table_outside_team_db", subject, n.ClickHouseTable)
}

func encodeBackupHosts(hosts []backup.Host) []string {
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, (&domain.HostAllowlistEntry{Pattern: h.Pattern, Kind: domain.HostKind(h.Kind)}).EncodedPattern())
	}
	return out
}

// applyStatusMode — выбор администратора; выключенные узлы не трогаются.
func (b *importBuilder) applyStatusMode(n *domain.Node) {
	if n.Status != domain.NodeStatusEnabled {
		return
	}
	switch b.req.Options.StatusMode {
	case BackupStatusPauseAll:
		n.Status = domain.NodeStatusPaused
	case BackupStatusPausePull:
		// Pull-узел начинает забирать очередь через 15 с после появления: при
		// живом источнике два сервера разбирали бы её наперегонки.
		if n.RootMethod.IsPull() {
			n.Status = domain.NodeStatusPaused
		}
	}
}

// disableIfRejected — узел, не проходящий нынешнюю проверку, или ведущий на
// вход самой ноды (§32.2), восстанавливается выключенным: правила со временем
// ужесточаются, и старый узел не должен блокировать всё восстановление.
func (b *importBuilder) disableIfRejected(n *domain.Node, subject string, ackErr error) {
	reason := ""
	switch err := n.Validate(); {
	case ackErr != nil:
		reason = ackErr.Error()
	case err != nil:
		reason = err.Error()
	}
	if reason != "" {
		b.warn("node_invalid", subject, reason)
		n.Status = domain.NodeStatusDisabled
		return
	}
	if n.URLMode == domain.URLModeStatic && isSelfReferenceTarget(n.TargetURL, b.u.selfIngressHosts) {
		b.warn("node_self_reference", subject, urlWithoutSecrets(n.TargetURL))
		n.Status = domain.NodeStatusDisabled
	}
}

func (b *importBuilder) countStatus(s domain.NodeStatus) {
	switch s {
	case domain.NodeStatusEnabled:
		b.preview.NodeStatuses.Enabled++
	case domain.NodeStatusPaused:
		b.preview.NodeStatuses.Paused++
	default:
		b.preview.NodeStatuses.Disabled++
	}
}

// checkNodeLimit — потолок узлов на команду (§3.3) блокирует восстановление:
// выбрать, какие узлы не переносить, может только администратор.
func (b *importBuilder) checkNodeLimit(perTeam map[string]int) {
	limit := b.u.nodesHardLimit
	if limit <= 0 {
		return
	}
	for _, t := range b.teams {
		if perTeam[t.id] > limit {
			b.block("team_node_limit", t.slug, fmt.Sprintf("%d > %d", perTeam[t.id], limit))
		}
	}
}

// urlWithoutSecrets — адрес для отчёта: без учётных данных, query и
// фрагмента (§51.9 — токены часто передаются параметром).
func urlWithoutSecrets(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return u.String()
}
