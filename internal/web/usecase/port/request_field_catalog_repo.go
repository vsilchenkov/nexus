package port

import (
	"context"

	"nexus/internal/domain"
)

// RequestFieldCatalogRepo — справочник полей запроса (§41). usage_count
// считается on-read (COUNT узлов с этим именем в auth_dynamic_field ИЛИ
// incoming_auth_dynamic_field), отдельной таблицы привязки нет.
type RequestFieldCatalogRepo interface {
	// Search — prefix-поиск по имени (case-insensitive), сортировка по
	// usage_count desc, затем имени. Пустой q = топ-используемые. С usage_count.
	Search(ctx context.Context, q string, limit int) ([]*domain.RequestFieldCatalogEntry, error)
	// GetByName ищет запись по имени без учёта регистра (идемпотентный create).
	GetByName(ctx context.Context, name string) (*domain.RequestFieldCatalogEntry, error)
	Create(ctx context.Context, e *domain.RequestFieldCatalogEntry) error
}
