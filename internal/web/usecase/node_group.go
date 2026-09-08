package usecase

import (
	"context"
	"errors"
	"strings"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// MoveDirection — направление сдвига группы в порядке вывода (§99.4).
type MoveDirection string

const (
	MoveUp   MoveDirection = "up"
	MoveDown MoveDirection = "down"
)

// maxGroupsForReorder — потолок справочника, при котором сдвиг стрелками ещё
// корректен. Move переприсваивает порядок ВСЕМ группам, поэтому обязан прочитать
// их все; читать страницами нельзя (хвост сохранил бы старую шкалу и
// перемешался бы). Справочник групп по природе небольшой — десятки записей, — и
// тысяча выбрана как заведомо недостижимый в эксплуатации предел.
const maxGroupsForReorder = 1000

// NodeGroupUsecase — справочник групп узлов (§99): управление из Настроек и
// идемпотентное создание из комбобокса формы узла.
type NodeGroupUsecase struct {
	repo   port.NodeGroupRepo
	audit  *AuditUsecase
	logger logging.Logger
}

func NewNodeGroupUsecase(repo port.NodeGroupRepo, audit *AuditUsecase, logger logging.Logger) *NodeGroupUsecase {
	return &NodeGroupUsecase{repo: repo, audit: audit, logger: logger}
}

// List возвращает справочник в порядке вывода (sort_order, затем name).
// Пустой q — весь справочник; непустой — поиск подстроки без учёта регистра.
func (u *NodeGroupUsecase) List(ctx context.Context, q string, limit int) ([]*domain.NodeGroup, error) {
	return u.repo.List(ctx, q, limit)
}

// Get возвращает группу по id (с usage_count).
func (u *NodeGroupUsecase) Get(ctx context.Context, id string) (*domain.NodeGroup, error) {
	return u.repo.Get(ctx, id)
}

// Create добавляет группу. Идемпотентно по case-insensitive имени: повторный
// вызов возвращает существующую запись (§99.4) — комбобокс формы узла создаёт
// группы без диалогов и без гонок между двумя открытыми вкладками.
//
// Имя обрезается по краям ДО валидации и записи: длину домен считает по
// обрезанной строке, и сохранять неурезанное значило бы хранить не то, что
// проверили (а «Группа » и «Группа» перестали бы совпадать в UNIQUE lower(name)).
func (u *NodeGroupUsecase) Create(ctx context.Context, actor Actor, g *domain.NodeGroup) error {
	g.Name = strings.TrimSpace(g.Name)
	if err := g.Validate(); err != nil {
		return err
	}
	err := u.repo.Create(ctx, g)
	if errors.Is(err, domain.ErrNodeGroupAlreadyExists) {
		existing, getErr := u.repo.GetByName(ctx, g.Name)
		if getErr != nil {
			return getErr
		}
		u.logger.Debug("node group create is idempotent: existing returned",
			u.logger.Str("group_id", existing.ID))
		*g = *existing
		return nil
	}
	if err != nil {
		return err
	}
	u.audit.Log(ctx, actor, domain.ActionNodeGroupCreate, "node_group", g.ID, map[string]any{"name": g.Name})
	return nil
}

// Update меняет имя, описание и порядок.
//
// Переименование НЕ ограничено usage_count — в отличие от заголовков (§24):
// узел ссылается на группу по id, и переименование ссылку не осиротит.
// Единственное ограничение — занятое имя (ErrNodeGroupAlreadyExists).
func (u *NodeGroupUsecase) Update(ctx context.Context, actor Actor, id, name, description string, sortOrder int32) error {
	existing, err := u.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	cand := &domain.NodeGroup{
		ID:          id,
		Name:        strings.TrimSpace(name),
		Description: description,
		SortOrder:   sortOrder,
		UpdatedBy:   actor.UserLogin,
	}
	if err := cand.Validate(); err != nil {
		return err
	}
	if err := u.repo.Update(ctx, cand); err != nil {
		return err
	}
	u.audit.Log(ctx, actor, domain.ActionNodeGroupUpdate, "node_group", id, map[string]any{
		"name":         cand.Name,
		"name_changed": existing.Name != cand.Name,
		"sort_order":   cand.SortOrder,
	})
	return nil
}

// Delete удаляет группу. Запрещено, если к ней привязан хотя бы один узел
// (usage_count > 0): оператор сначала убирает узлы из группы.
//
// Проверка здесь — не единственная: между чтением usage_count и удалением узел
// может быть привязан из другой вкладки, и тогда сработает ON DELETE RESTRICT
// (репозиторий превратит его в ту же ErrNodeGroupInUse). Guard оставлен, чтобы
// типовой случай давал понятный ответ без похода в обработчик ошибок СУБД.
func (u *NodeGroupUsecase) Delete(ctx context.Context, actor Actor, id string) error {
	g, err := u.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if g.UsageCount > 0 {
		u.logger.Debug("node group delete rejected: group is in use",
			u.logger.Str("group_id", id), u.logger.Int("usage_count", g.UsageCount))
		return domain.ErrNodeGroupInUse
	}
	if err := u.repo.Delete(ctx, id); err != nil {
		return err
	}
	u.audit.Log(ctx, actor, domain.ActionNodeGroupDelete, "node_group", id, map[string]any{"name": g.Name})
	return nil
}

// Move сдвигает группу на одну позицию вверх или вниз.
//
// Реализовано переприсваиванием ВСЕГО порядка, а не обменом двух значений
// sort_order: у записей, созданных до появления стрелок, порядок одинаков
// (дефолт 0) и определяется именем — обмен равных значений ничего бы не
// изменил, и кнопка молча не работала бы (§99.4).
func (u *NodeGroupUsecase) Move(ctx context.Context, actor Actor, id string, dir MoveDirection) error {
	if dir != MoveUp && dir != MoveDown {
		return domain.ErrNodeGroupInvalidDirection
	}
	// Порядок переприсваивается ЦЕЛИКОМ, поэтому читать справочник страницами
	// нельзя: группы за пределами страницы сохранили бы старые sort_order и
	// перемешались бы с только что переприсвоенными. Лимит задан явно и с
	// запасом — дефолт репозитория (200) для этой операции не годится, он
	// рассчитан на выдачу списка, а не на перестроение.
	groups, err := u.repo.List(ctx, "", maxGroupsForReorder)
	if err != nil {
		return err
	}
	// Справочник перерос лимит: сдвиг тронул бы только его начало, а хвост
	// остался бы со старой шкалой. Отказ честнее молчаливой перетасовки.
	if len(groups) >= maxGroupsForReorder {
		u.logger.Error("node group move rejected: catalog exceeds reorder limit",
			u.logger.Int("limit", maxGroupsForReorder))
		return domain.ErrNodeGroupTooManyToReorder
	}
	idx := -1
	for i, g := range groups {
		if g.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return domain.ErrNodeGroupNotFound
	}
	swap := idx - 1
	if dir == MoveDown {
		swap = idx + 1
	}
	// Край списка — не ошибка: кнопка в UI неактивна, но параллельное удаление
	// соседа могло сделать её кликом в пустоту. Порядок при этом не трогаем.
	if swap < 0 || swap >= len(groups) {
		u.logger.Debug("node group move ignored: already at the edge",
			u.logger.Str("group_id", id), u.logger.Str("direction", string(dir)))
		return nil
	}
	groups[idx], groups[swap] = groups[swap], groups[idx]

	ids := make([]string, 0, len(groups))
	for _, g := range groups {
		ids = append(ids, g.ID)
	}
	if err := u.repo.Reorder(ctx, ids, actor.UserLogin); err != nil {
		return err
	}
	u.audit.Log(ctx, actor, domain.ActionNodeGroupReorder, "node_group", id, map[string]any{
		"name":      groups[swap].Name,
		"direction": string(dir),
	})
	return nil
}
