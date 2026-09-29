// Package backup — снимок конфигурации Nexus для резервной копии (§110.2).
//
// Снимок — явный версионированный DTO, а не сериализация доменных типов: у
// домена свои json-теги (API), и их правка не должна молча менять формат
// файла, который обязан читаться следующими версиями. json-теги полей
// совпадают с именами колонок PostgreSQL — интеграционный тест покрытия
// сверяет их с information_schema, чтобы новая колонка не выпала из бэкапа
// незамеченной (см. Tables).
//
// Значения в снимке — как в источнике: id, имена баз ClickHouse, ссылки. Их
// перемап на целевую ноду — забота импорта, а не формата.
package backup

import (
	"encoding/json"
	"time"

	"nexus/internal/domain"
)

// Snapshot — вся переносимая конфигурация. Порядок полей — порядок записи
// при восстановлении (родители раньше ссылок на них).
type Snapshot struct {
	// ExportedBy — логин выгрузившего; SourcePublicURL — публичный адрес
	// источника (§28). Лежат в зашифрованной части: заголовок файла открыт.
	ExportedBy      string `json:"exported_by"`
	SourcePublicURL string `json:"source_public_url"`

	Teams         []Team         `json:"teams"`
	Users         []User         `json:"users"`
	UserTeams     []UserTeam     `json:"user_teams"`
	Favorites     []Favorite     `json:"user_team_favorites"`
	Preferences   []Preference   `json:"user_preferences"`
	APITokens     []APIToken     `json:"api_tokens"`
	CHTemplates   []CHTemplate   `json:"ch_templates"`
	NodeGroups    []NodeGroup    `json:"node_groups"`
	Hosts         []Host         `json:"host_allowlist"`
	Headers       []CatalogEntry `json:"headers_catalog"`
	RequestFields []CatalogEntry `json:"request_fields_catalog"`
	LogMasks      []LogMask      `json:"log_mask_patterns"`
	PeerInstances []PeerInstance `json:"peer_instances"`
	Nodes         []Node         `json:"nodes"`
	NodeHosts     []NodeHost     `json:"node_allowed_hosts"`

	// AppSettings — глобальные настройки (app_settings.value) с расшифрованными
	// секретами. nil — строки настроек в источнике не было.
	AppSettings *domain.AppSettings `json:"app_settings,omitempty"`
}

// Team — строка teams. CHDatabase — имя базы в ИСТОЧНИКЕ: на цели оно
// пересчитывается, а здесь нужно как ключ карты переноса таблиц узлов.
type Team struct {
	ID          string    `json:"id"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	CHDatabase  string    `json:"ch_database"`
	ExternalURL string    `json:"external_url"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// User — строка users. PasswordHash — bcrypt, от ENCRYPTION_KEY не зависит;
// nil — пароль не задан (сидированный admin до первого входа).
type User struct {
	ID                 string    `json:"id"`
	Login              string    `json:"login"`
	Name               string    `json:"name"`
	Email              *string   `json:"email"`
	PasswordHash       *string   `json:"password_hash"`
	Role               string    `json:"role"`
	Active             bool      `json:"active"`
	MustChangePassword bool      `json:"must_change_password"`
	Lang               string    `json:"lang"`
	DefaultTeamID      string    `json:"default_team_id"`
	CreatedAt          time.Time `json:"created_at"`
}

// UserTeam — членство пользователя в команде.
type UserTeam struct {
	UserID    string    `json:"user_id"`
	TeamID    string    `json:"team_id"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// Favorite — избранная команда пользователя (§49).
type Favorite struct {
	UserID    string    `json:"user_id"`
	TeamID    string    `json:"team_id"`
	Position  int       `json:"position"`
	CreatedAt time.Time `json:"created_at"`
}

// Preference — личная настройка (§71). TeamID nil — глобальная для пользователя.
type Preference struct {
	UserID    string          `json:"user_id"`
	TeamID    *string         `json:"team_id"`
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// APIToken — API-токен (§7.14). Хранится только SHA-256 значения, поэтому
// перенос хеша сохраняет работоспособность выданных токенов.
type APIToken struct {
	ID        string     `json:"id"`
	UserID    string     `json:"user_id"`
	TeamID    string     `json:"team_id"`
	Name      string     `json:"name"`
	TokenHash string     `json:"token_hash"`
	Prefix    string     `json:"prefix"`
	Scopes    []string   `json:"scopes"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// CHTemplate — шаблон таблицы логов (§19). Spec — как в колонке JSONB.
type CHTemplate struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Spec        json.RawMessage `json:"spec"`
	IsDefault   bool            `json:"is_default"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// NodeGroup — группа узлов (§99).
type NodeGroup struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	SortOrder   int       `json:"sort_order"`
	CreatedBy   string    `json:"created_by"`
	UpdatedBy   string    `json:"updated_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Host — элемент каталога разрешённых хостов (§23). usage_count не
// переносится: его ведёт триггер на node_allowed_hosts.
type Host struct {
	ID          string    `json:"id"`
	Pattern     string    `json:"pattern"`
	Kind        string    `json:"kind"`
	Description string    `json:"description"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// CatalogEntry — строка справочника заголовков (§24) или полей запроса (§41):
// у обоих одинаковая форма. Узлы ссылаются на них по имени, не по id.
type CatalogEntry struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// LogMask — шаблон маскирования секретов в логах (§95).
type LogMask struct {
	ID          string    `json:"id"`
	Pattern     string    `json:"pattern"`
	Replacement string    `json:"replacement"`
	Description string    `json:"description"`
	Enabled     bool      `json:"enabled"`
	SortOrder   int       `json:"sort_order"`
	CreatedBy   string    `json:"created_by"`
	UpdatedBy   string    `json:"updated_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// PeerInstance — соседний инстанс Nexus (§73), без кеша проб last_*.
type PeerInstance struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	BaseURL   string    `json:"base_url"`
	Comment   string    `json:"comment"`
	CreatedBy string    `json:"created_by"`
	UpdatedBy string    `json:"updated_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NodeHost — связь узла с элементом каталога хостов (§23).
type NodeHost struct {
	NodeID    string    `json:"node_id"`
	HostID    string    `json:"host_id"`
	CreatedAt time.Time `json:"created_at"`
}

// Counts — число строк по разделам снимка (для открытого заголовка файла и
// карточки выгрузки).
func (s *Snapshot) Counts() map[string]int {
	return map[string]int{
		"teams":          len(s.Teams),
		"nodes":          len(s.Nodes),
		"users":          len(s.Users),
		"api_tokens":     len(s.APITokens),
		"node_groups":    len(s.NodeGroups),
		"hosts":          len(s.Hosts),
		"headers":        len(s.Headers),
		"request_fields": len(s.RequestFields),
		"ch_templates":   len(s.CHTemplates),
		"log_masks":      len(s.LogMasks),
		"peer_instances": len(s.PeerInstances),
	}
}
