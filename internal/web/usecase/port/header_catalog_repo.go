package port

import (
	"context"

	"nexus/internal/domain"
)

// HeaderCatalogRepo — справочник HTTP-заголовков (§24). usage_count считается
// on-read (COUNT узлов с этим именем в forward_headers), отдельной таблицы
// привязки нет.
type HeaderCatalogRepo interface {
	// Search — prefix-поиск по имени (case-insensitive), сортировка по
	// usage_count desc, затем имени. Пустой q = топ-используемые. С usage_count.
	Search(ctx context.Context, q string, limit int) ([]*domain.HeaderCatalogEntry, error)
	// GetByName ищет запись по имени без учёта регистра (идемпотентный create).
	GetByName(ctx context.Context, name string) (*domain.HeaderCatalogEntry, error)
	Create(ctx context.Context, e *domain.HeaderCatalogEntry) error
}
