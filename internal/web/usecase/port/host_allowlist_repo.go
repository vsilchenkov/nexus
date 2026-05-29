package port

import (
	"context"

	"nexus/internal/domain"
)

// HostAllowlistRepo — каталог разрешённых хостов (§23 ТЗ) и привязка к узлам.
//
// Каталог общий для инсталляции (не per-team). usage_count денормализован
// (PostgreSQL trigger на node_allowed_hosts); репозиторий его только читает.
// Источник истины для allowlist узла — таблица node_allowed_hosts; снимок
// nodes.url_allowed_hosts пересобирается usecase'ом через NodeRepo.
type HostAllowlistRepo interface {
	Get(ctx context.Context, id string) (*domain.HostAllowlistEntry, error)
	// GetByPattern ищет запись по паттерну без учёта регистра (для
	// идемпотентного создания из combobox).
	GetByPattern(ctx context.Context, pattern string) (*domain.HostAllowlistEntry, error)
	// Search — листинг/поиск по паттерну и описанию. kind="" = любой тип.
	Search(ctx context.Context, q string, kind domain.HostKind, limit int) ([]*domain.HostAllowlistEntry, error)
	Create(ctx context.Context, e *domain.HostAllowlistEntry) error
	UpdateDescription(ctx context.Context, id, description string) error
	// UpdatePattern меняет паттерн и тип. Вызывается usecase'ом только при
	// usage_count = 0 (иначе денормализованные снимки разъедутся).
	UpdatePattern(ctx context.Context, id, pattern string, kind domain.HostKind) error
	Delete(ctx context.Context, id string) error

	// Link/Unlink — привязка паттерна к узлу (M2M). trigger обновляет usage_count.
	Link(ctx context.Context, nodeID, hostID string) error
	Unlink(ctx context.Context, nodeID, hostID string) error
	// ListByNode — паттерны, привязанные к узлу (для chips и пересборки снимка).
	ListByNode(ctx context.Context, nodeID string) ([]*domain.HostAllowlistEntry, error)
}
