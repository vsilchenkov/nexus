package port

import (
	"context"

	"nexus/internal/domain"
)

// LogMaskRepo — справочник шаблонов маскирования логов узлов (§95). Глобальный
// на инсталляцию, без team-скоупа и usage_count (узлы на шаблоны не ссылаются —
// они применяются ко всем логам). Управляется только из Web (admin-only); Sender
// читает эту же таблицу своим read-only reader'ом (adapter/out/maskpg).
type LogMaskRepo interface {
	// List возвращает все шаблоны (включая выключенные) в порядке применения
	// (sort_order, затем created_at). q — необязательный ILIKE-фильтр по
	// pattern/description; пусто = все.
	List(ctx context.Context, q string) ([]*domain.LogMaskPattern, error)
	// Get возвращает шаблон по id. Нет записи → ErrLogMaskNotFound.
	Get(ctx context.Context, id string) (*domain.LogMaskPattern, error)
	Create(ctx context.Context, e *domain.LogMaskPattern) error
	// Update меняет pattern/replacement/description/enabled/sort_order (+ updated_by,
	// updated_at). Нет записи → ErrLogMaskNotFound.
	Update(ctx context.Context, e *domain.LogMaskPattern) error
	// Delete удаляет шаблон. Нет записи → ErrLogMaskNotFound.
	Delete(ctx context.Context, id string) error
}
