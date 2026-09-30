package usecase

import (
	"context"
	"fmt"
	"strconv"

	"nexus/internal/domain"
)

// planSettings — глобальные настройки с теми же проверками, что у формы
// (§110.7). Поле, не прошедшее проверку на цели, пропускается с
// предупреждением; clickhouse и version_override не переносятся.
func (b *importBuilder) planSettings(ctx context.Context) error {
	s := b.src.AppSettings
	if s == nil {
		return nil
	}
	if ch := describeCHSettings(s.ClickHouse); ch != "" {
		b.warn("settings_clickhouse_not_imported", ch, "")
	}
	merged, sections, skipped, err := b.u.settings.PrepareImport(ctx, s)
	if err != nil {
		return fmt.Errorf("backup import: settings: %w", err)
	}
	for _, sk := range skipped {
		b.warn("settings_field_skipped", sk.Field, sk.Err.Error())
	}
	if len(sections) == 0 {
		return nil
	}
	b.set.AppSettings = merged
	b.preview.SettingsSections = sections
	return nil
}

// describeCHSettings — хост, порт, база и пользователь ClickHouse из файла для
// отчёта; без пароля. "" — подключение в файле не задано.
func describeCHSettings(c domain.ClickHouseSettings) string {
	if c.Host == nil || *c.Host == "" {
		return ""
	}
	out := *c.Host
	if c.Port != nil {
		out += ":" + strconv.Itoa(*c.Port)
	}
	if c.Database != nil && *c.Database != "" {
		out += "/" + *c.Database
	}
	if c.User != nil && *c.User != "" {
		out += " (" + *c.User + ")"
	}
	return out
}

// finish — заголовок файла и запись аудита для транзакции записи.
func (b *importBuilder) finish(actor Actor) {
	b.preview.Header = *b.h
	b.preview.ExportedBy = b.src.ExportedBy
	b.preview.SourcePublicURL = b.src.SourcePublicURL
	if b.set.AppSettings != nil {
		b.set.AppSettings.UpdatedBy = actor.UserID
	}
	b.set.Audit = auditEntry(actor, domain.ActionConfigImport, "config", "", map[string]any{
		"exported_at":        b.h.ExportedAt,
		"source_instance_id": b.h.SourceInstanceID,
		"nexus_version":      b.h.NexusVersion,
		"schema_version":     b.h.SchemaVersion,
		"status_mode":        string(b.req.Options.StatusMode),
		"adopt_unclaimed":    b.req.Options.AdoptUnclaimed,
		"entities":           b.preview.Entities,
		"node_statuses":      b.preview.NodeStatuses,
		"warnings":           len(b.preview.Warnings),
	})
}

func (b *importBuilder) plan() *backupImportPlan {
	b.preview.Databases = b.databases
	return &backupImportPlan{
		preview:   b.preview,
		set:       b.set,
		databases: b.databases,
		tables:    b.tables,
	}
}
