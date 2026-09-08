package port

import (
	"context"

	"nexus/internal/domain"
)

// NodeGroupRepo — справочник групп узлов (§99). Справочник глобальный (без
// team-скоупа), usage_count считается on-read (COUNT узлов с этим group_id).
type NodeGroupRepo interface {
	// List возвращает группы в порядке вывода (sort_order, затем name) с
	// usage_count. Пустой q = весь справочник; непустой — поиск ПОДСТРОКИ без
	// учёта регистра (имя группы — свободный текст, и «обмен» обязано находить
	// «1С Обмен»; prefix-поиск §24 здесь не годится).
	List(ctx context.Context, q string, limit int) ([]*domain.NodeGroup, error)
	// Get возвращает группу по id (с usage_count). Нет записи →
	// ErrNodeGroupNotFound.
	Get(ctx context.Context, id string) (*domain.NodeGroup, error)
	// GetByName ищет группу по имени без учёта регистра — для идемпотентного
	// создания из комбобокса формы узла (§99.4).
	GetByName(ctx context.Context, name string) (*domain.NodeGroup, error)
	// Create добавляет группу. Коллизия lower(name) → ErrNodeGroupAlreadyExists.
	Create(ctx context.Context, g *domain.NodeGroup) error
	// Update меняет имя, описание, порядок и updated_by одним запросом.
	// Переименование НЕ ограничено usage_count (узлы ссылаются по id, ссылка не
	// осиротеет — в отличие от заголовков §24). Коллизия lower(name) →
	// ErrNodeGroupAlreadyExists, нет записи → ErrNodeGroupNotFound.
	Update(ctx context.Context, g *domain.NodeGroup) error
	// Delete удаляет группу. Нет записи → ErrNodeGroupNotFound; на группу
	// ссылаются узлы (FK 23503) → ErrNodeGroupInUse. FK — второй уровень к
	// guard-проверке usecase: даже в обход неё удаление не пройдёт.
	Delete(ctx context.Context, id string) error
	// Reorder присваивает группам sort_order по позиции в orderedIDs
	// ((позиция+1)*10). Именно переприсваивание, а не обмен двух значений: у
	// исторических записей sort_order совпадает (дефолт 0), и обмен равных
	// значений был бы no-op (§99.4).
	Reorder(ctx context.Context, orderedIDs []string, updatedBy string) error
}
