package usecase

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"nexus/internal/domain"
	"nexus/internal/domain/backup"
	"nexus/internal/web/usecase/port"
)

// planClickHouse — решения по базам команд и таблицам узлов (§110.6).
// ClickHouse только опрашивается; создаёт базы и таблицы применение.
func (b *importBuilder) planClickHouse(ctx context.Context) error {
	actions := map[string]string{}
	for _, t := range b.teams {
		if _, seen := actions[t.db]; seen {
			continue
		}
		d := b.planDatabase(ctx, t)
		actions[t.db] = d.Action
		b.databases = append(b.databases, d)
	}
	return b.planTables(ctx, actions)
}

// planDatabase — решение по базе команды по маркеру владения §70.
func (b *importBuilder) planDatabase(ctx context.Context, t plannedTeam) BackupCHDatabasePlan {
	d := BackupCHDatabasePlan{Team: t.slug, Name: t.db, Action: chActionBlocked}
	st, err := b.u.chInspector.InspectDatabase(ctx, t.db)
	if err != nil {
		d.Ownership = string(port.CHOwnershipUnknown)
		b.block("ch_database_unknown", t.db, err.Error())
		return d
	}
	d.Ownership, d.Tables = string(st.Ownership), st.Tables
	switch st.Ownership {
	case port.CHOwnershipNone:
		d.Action = chActionCreate
	case port.CHOwnershipOwned:
		d.Action = chActionReuse
		b.warnOwnedDatabase(t.db, st.Tables)
	case port.CHOwnershipUnclaimed:
		switch {
		case st.Tables == 0:
			d.Action = chActionClaim
		case b.req.Options.AdoptUnclaimed:
			d.Action = chActionAdopt
			b.warn("ch_database_adopted", t.db, strconv.Itoa(st.Tables))
		default:
			b.block("ch_database_unclaimed", t.db, strconv.Itoa(st.Tables))
		}
	case port.CHOwnershipForeign:
		b.block("ch_database_foreign", t.db, st.Owner)
	case port.CHOwnershipConflict:
		b.block("ch_database_conflict", t.db, "")
	default:
		b.block("ch_database_unknown", t.db, "")
	}
	b.u.logger.Debug("backup import: clickhouse database planned",
		b.u.logger.Str("database", t.db), b.u.logger.Str("ownership", d.Ownership),
		b.u.logger.Int("tables", st.Tables), b.u.logger.Str("action", d.Action))
	return d
}

// warnOwnedDatabase — своя база с таблицами: старые логи снова видны. Пустая
// своя база — штатная база Default, которую занял стартовый гейт; о ней молчим.
func (b *importBuilder) warnOwnedDatabase(db string, tables int) {
	if tables == 0 {
		return
	}
	b.warn("ch_database_owned", db, strconv.Itoa(tables))
	if b.u.source.InstanceID.IsZero() {
		// Пустой идентификатор совпадает с пустым идентификатором любого
		// соседа (§70.1): «наша» база может оказаться базой живого источника.
		b.warn("ch_database_owned_empty_instance", db, "")
	}
}

// planTables — таблица на каждое уникальное имя; общая для нескольких узлов
// создаётся по самому раннему узлу (его шаблон и срок хранения).
func (b *importBuilder) planTables(ctx context.Context, actions map[string]string) error {
	templates, defaultID := b.domainTemplates()
	nodes := slices.Clone(b.set.Nodes)
	slices.SortStableFunc(nodes, func(x, y *domain.Node) int { return x.CreatedAt.Compare(y.CreatedAt) })
	seen := map[string]bool{}
	for _, n := range nodes {
		if n.ClickHouseTable == "" || seen[n.ClickHouseTable] {
			continue
		}
		seen[n.ClickHouseTable] = true
		if err := b.planTable(ctx, n, actions, templates, defaultID); err != nil {
			return err
		}
	}
	return nil
}

func (b *importBuilder) planTable(ctx context.Context, n *domain.Node, actions map[string]string,
	templates map[string]*domain.CHTemplate, defaultID string) error {
	if n.ExternalTable {
		b.planExternalTable(ctx, n.ClickHouseTable)
		return nil
	}
	plan := planTableProvision(n)
	if plan.mode == tableProvisionSkip {
		return nil
	}
	db, _, _ := strings.Cut(n.ClickHouseTable, ".")
	action, teamDB := actions[db]
	switch {
	case !teamDB:
		b.preview.Tables.Unmanaged++
		return nil
	case action == chActionBlocked:
		return nil
	}
	tmplID := n.ClickHouseTemplateID
	if plan.mode == tableProvisionDefault {
		tmplID = defaultID
	}
	tmpl := templates[tmplID]
	if tmpl == nil {
		b.block("table_template_missing", n.ClickHouseTable, tmplID)
		return nil
	}
	ddl, err := tmpl.RenderCreateTable(n.ClickHouseTable, n.ClickHouseRetentionDays)
	if err != nil {
		b.block("table_ddl_invalid", n.ClickHouseTable, err.Error())
		return nil
	}
	exists := false
	if action != chActionCreate {
		if exists, err = b.u.chInspector.TableExists(ctx, n.ClickHouseTable); err != nil {
			b.block("ch_database_unknown", n.ClickHouseTable, err.Error())
			return nil
		}
	}
	b.tables = append(b.tables, chTableAction{table: n.ClickHouseTable, ddl: ddl, exists: exists})
	if exists {
		b.preview.Tables.Existing++
	} else {
		b.preview.Tables.Create++
	}
	return nil
}

// planExternalTable — внешнюю таблицу §64 Nexus не создаёт; отсутствующая —
// предупреждение: оператор создаёт её сам.
func (b *importBuilder) planExternalTable(ctx context.Context, table string) {
	b.preview.Tables.External++
	ok, err := b.u.chInspector.TableExists(ctx, table)
	switch {
	case err != nil:
		b.warn("external_table_unchecked", table, err.Error())
	case !ok:
		b.preview.Tables.ExternalMissing++
		b.warn("external_table_missing", table, "")
	}
}

// domainTemplates — шаблоны цели после восстановления: строки цели, поверх
// них шаблоны файла (совпавшие несут id цели). defaultID — шаблон «по
// умолчанию» после восстановления.
func (b *importBuilder) domainTemplates() (map[string]*domain.CHTemplate, string) {
	out := map[string]*domain.CHTemplate{}
	defaultID := b.set.DefaultTemplateID
	add := func(t backup.CHTemplate) {
		d, err := toDomainTemplate(t)
		if err != nil {
			b.block("template_invalid", t.Name, err.Error())
			return
		}
		out[t.ID] = d
	}
	for _, t := range b.tgt.CHTemplates {
		add(t)
		if defaultID == "" && t.IsDefault {
			defaultID = t.ID
		}
	}
	for _, t := range b.set.CHTemplates {
		add(t)
	}
	return out, defaultID
}

func toDomainTemplate(t backup.CHTemplate) (*domain.CHTemplate, error) {
	d := &domain.CHTemplate{
		ID: t.ID, Name: t.Name, Description: t.Description, IsDefault: t.IsDefault,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
	if err := json.Unmarshal(t.Spec, &d.Spec); err != nil {
		return nil, err
	}
	return d, nil
}
