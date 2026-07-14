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
	// Get возвращает запись по id (с usage_count). Нет записи → ErrHeaderNotFound.
	Get(ctx context.Context, id string) (*domain.HeaderCatalogEntry, error)
	// GetByName ищет запись по имени без учёта регистра (идемпотентный create).
	GetByName(ctx context.Context, name string) (*domain.HeaderCatalogEntry, error)
	Create(ctx context.Context, e *domain.HeaderCatalogEntry) error
	// UpdateName переименовывает заголовок. Коллизия lower(name) →
	// ErrHeaderAlreadyExists, нет записи → ErrHeaderNotFound.
	UpdateName(ctx context.Context, id, name string) error
	// UpdateDescription меняет описание. Нет записи → ErrHeaderNotFound.
	UpdateDescription(ctx context.Context, id, description string) error
	// Delete удаляет запись. Нет записи → ErrHeaderNotFound (FK нет — защита по
	// usage_count в usecase).
	Delete(ctx context.Context, id string) error
}
