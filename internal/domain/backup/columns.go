package backup

import (
	"reflect"
	"strings"
)

// TableSpec — как таблица PostgreSQL представлена в снимке: тип строки
// (json-теги = имена колонок) и колонки, исключённые сознательно.
// Интеграционный тест сверяет Exported ∪ Excluded с information_schema:
// колонка, добавленная миграцией, но не попавшая ни в снимок, ни в список
// исключений, валит тест, а не молча выпадает из резервной копии.
type TableSpec struct {
	Name string
	// Row — нулевое значение типа строки снимка. nil — колонки перечислены
	// в Columns явно (app_settings: значение — одна JSONB-колонка).
	Row     any
	Columns []string
	// Excluded — колонки, которые не переносятся, с причиной в комментарии.
	Excluded []string
}

// Exported — колонки таблицы, которые несёт снимок.
func (t TableSpec) Exported() []string {
	if t.Row == nil {
		return t.Columns
	}
	return jsonTags(reflect.TypeOf(t.Row))
}

// Tables — переносимые таблицы.
func Tables() []TableSpec {
	return []TableSpec{
		{Name: "teams", Row: Team{}},
		// last_login_at — история, а не конфигурация.
		{Name: "users", Row: User{}, Excluded: []string{"last_login_at"}},
		{Name: "user_teams", Row: UserTeam{}},
		{Name: "user_team_favorites", Row: Favorite{}},
		{Name: "user_preferences", Row: Preference{}},
		// last_used_at — история; revoked_at — отозванные токены не переносятся.
		{Name: "api_tokens", Row: APIToken{}, Excluded: []string{"last_used_at", "revoked_at"}},
		{Name: "ch_templates", Row: CHTemplate{}},
		{Name: "node_groups", Row: NodeGroup{}},
		// usage_count ведёт триггер на node_allowed_hosts.
		{Name: "host_allowlist", Row: Host{}, Excluded: []string{"usage_count"}},
		{Name: "headers_catalog", Row: CatalogEntry{}},
		{Name: "request_fields_catalog", Row: CatalogEntry{}},
		{Name: "log_mask_patterns", Row: LogMask{}},
		// last_* — кеш проб соседа (§73), пересчитывается первой же пробой.
		{Name: "peer_instances", Row: PeerInstance{}, Excluded: []string{
			"last_status", "last_version", "last_instance_id", "last_latency_ms", "last_error", "last_checked_at",
		}},
		{Name: "nodes", Row: Node{}},
		{Name: "node_allowed_hosts", Row: NodeHost{}},
		// id — синглтон; updated_* пишет импортирующий.
		{Name: "app_settings", Columns: []string{"value"}, Excluded: []string{"id", "updated_at", "updated_by"}},
	}
}

// ExcludedTables — таблицы, которые не переносятся целиком (§110.2).
func ExcludedTables() []string {
	return []string{
		"schema_migrations", // состояние миграций цели
		"methods",           // статический сид, есть на любой ноде
		"instance_identity", // идентичность задаёт конфиг ноды (§70.1)
		"user_audit",        // история
		"user_search_history",
		"one_time_tokens", // одноразовые ссылки §88
		"rejected_groups", // журнал отказов §94
		"rejected_clients",
		"rejected_samples",
	}
}

func jsonTags(t reflect.Type) []string {
	out := make([]string, 0, t.NumField())
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out = append(out, name)
		}
	}
	return out
}
