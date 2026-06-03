package port

import (
	"context"

	"nexus/internal/domain"
)

// CHTemplateRepo — CRUD глобального каталога шаблонов CH-таблиц (§19).
// Шаблоны не привязаны к команде; имя БД подставляется при применении к узлу.
type CHTemplateRepo interface {
	Get(ctx context.Context, id string) (*domain.CHTemplate, error)
	GetDefault(ctx context.Context) (*domain.CHTemplate, error)
	List(ctx context.Context) ([]*domain.CHTemplate, error)
	Create(ctx context.Context, t *domain.CHTemplate) error
	Update(ctx context.Context, t *domain.CHTemplate) error
	Delete(ctx context.Context, id string) error
	// CountNodesUsing — сколько узлов ссылается на шаблон (guard перед удалением).
	CountNodesUsing(ctx context.Context, id string) (int, error)
}
