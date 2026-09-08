package usecase

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

// memNodeGroupRepo — in-memory port.NodeGroupRepo для unit-тестов usecase.
// UsageCount выставляется тестом вручную (в проде считается on-read по узлам).
// Отдаёт КОПИИ записей: иначе тест правил бы хранилище мимо usecase и не
// заметил бы, что тот забыл сохранить изменение.
type memNodeGroupRepo struct {
	items map[string]*domain.NodeGroup
	seq   int
	// reorderCalls — с какими последовательностями звали Reorder. Нужен, чтобы
	// отличить «порядок переприсвоен» от «ничего не делали».
	reorderCalls [][]string
}

func newMemNodeGroupRepo() *memNodeGroupRepo {
	return &memNodeGroupRepo{items: map[string]*domain.NodeGroup{}}
}

func (r *memNodeGroupRepo) List(_ context.Context, q string, _ int) ([]*domain.NodeGroup, error) {
	out := make([]*domain.NodeGroup, 0, len(r.items))
	for _, g := range r.items {
		if q != "" && !strings.Contains(strings.ToLower(g.Name), strings.ToLower(q)) {
			continue
		}
		cp := *g
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (r *memNodeGroupRepo) Get(_ context.Context, id string) (*domain.NodeGroup, error) {
	if g, ok := r.items[id]; ok {
		cp := *g
		return &cp, nil
	}
	return nil, domain.ErrNodeGroupNotFound
}

func (r *memNodeGroupRepo) GetByName(_ context.Context, name string) (*domain.NodeGroup, error) {
	for _, g := range r.items {
		if strings.EqualFold(g.Name, name) {
			cp := *g
			return &cp, nil
		}
	}
	return nil, domain.ErrNodeGroupNotFound
}

func (r *memNodeGroupRepo) Create(_ context.Context, g *domain.NodeGroup) error {
	for _, x := range r.items {
		if strings.EqualFold(x.Name, g.Name) {
			return domain.ErrNodeGroupAlreadyExists
		}
	}
	r.seq++
	g.ID = string(rune('a'+r.seq)) + "-grp"
	cp := *g
	r.items[g.ID] = &cp
	return nil
}

func (r *memNodeGroupRepo) Update(_ context.Context, g *domain.NodeGroup) error {
	cur, ok := r.items[g.ID]
	if !ok {
		return domain.ErrNodeGroupNotFound
	}
	for id, x := range r.items {
		if id != g.ID && strings.EqualFold(x.Name, g.Name) {
			return domain.ErrNodeGroupAlreadyExists
		}
	}
	cur.Name = g.Name
	cur.Description = g.Description
	cur.SortOrder = g.SortOrder
	cur.UpdatedBy = g.UpdatedBy
	return nil
}

func (r *memNodeGroupRepo) Delete(_ context.Context, id string) error {
	if _, ok := r.items[id]; !ok {
		return domain.ErrNodeGroupNotFound
	}
	delete(r.items, id)
	return nil
}

func (r *memNodeGroupRepo) Reorder(_ context.Context, orderedIDs []string, updatedBy string) error {
	r.reorderCalls = append(r.reorderCalls, append([]string(nil), orderedIDs...))
	for i, id := range orderedIDs {
		g, ok := r.items[id]
		if !ok {
			return domain.ErrNodeGroupNotFound
		}
		g.SortOrder = int32((i + 1) * 10)
		g.UpdatedBy = updatedBy
	}
	return nil
}

func newNodeGroupUC(repo *memNodeGroupRepo) *NodeGroupUsecase {
	audit := NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop())
	return NewNodeGroupUsecase(repo, audit, logging.NewNoop())
}

// seedGroups создаёт группы с одинаковым sort_order — состояние справочника,
// созданного до появления стрелок (дефолт 0).
func seedGroups(t *testing.T, uc *NodeGroupUsecase, names ...string) []*domain.NodeGroup {
	t.Helper()
	ctx := context.Background()
	out := make([]*domain.NodeGroup, 0, len(names))
	for _, n := range names {
		g := &domain.NodeGroup{Name: n}
		require.NoError(t, uc.Create(ctx, SystemActor(), g))
		out = append(out, g)
	}
	return out
}

func TestNodeGroupUC_Create_Idempotent(t *testing.T) {
	t.Parallel()
	repo := newMemNodeGroupRepo()
	uc := newNodeGroupUC(repo)
	ctx := context.Background()

	g1 := &domain.NodeGroup{Name: "1С Обмен"}
	require.NoError(t, uc.Create(ctx, SystemActor(), g1))
	require.NotEmpty(t, g1.ID)

	// Повтор с другим регистром — не ошибка, а та же запись: комбобокс формы
	// узла создаёт группы без диалогов и без гонок между вкладками.
	g2 := &domain.NodeGroup{Name: "1с обмен"}
	require.NoError(t, uc.Create(ctx, SystemActor(), g2))
	assert.Equal(t, g1.ID, g2.ID, "повтор обязан вернуть существующую запись")
	assert.Equal(t, "1С Обмен", g2.Name, "имя остаётся каноническим, как завели впервые")
	assert.Len(t, repo.items, 1)
}

func TestNodeGroupUC_Create_TrimsName(t *testing.T) {
	t.Parallel()
	repo := newMemNodeGroupRepo()
	uc := newNodeGroupUC(repo)
	ctx := context.Background()

	g := &domain.NodeGroup{Name: "  Логистика  "}
	require.NoError(t, uc.Create(ctx, SystemActor(), g))
	// Хранить неурезанное значило бы хранить не то, что проверили: «Логистика »
	// и «Логистика» перестали бы совпадать в UNIQUE lower(name).
	assert.Equal(t, "Логистика", repo.items[g.ID].Name)

	dup := &domain.NodeGroup{Name: "Логистика"}
	require.NoError(t, uc.Create(ctx, SystemActor(), dup))
	assert.Equal(t, g.ID, dup.ID, "обрезка должна делать имена одинаковыми")
}

func TestNodeGroupUC_Create_Invalid(t *testing.T) {
	t.Parallel()
	uc := newNodeGroupUC(newMemNodeGroupRepo())
	ctx := context.Background()

	err := uc.Create(ctx, SystemActor(), &domain.NodeGroup{Name: "   "})
	assert.ErrorIs(t, err, domain.ErrNodeGroupNameLength)

	err = uc.Create(ctx, SystemActor(), &domain.NodeGroup{Name: "Гр", SortOrder: -5})
	assert.ErrorIs(t, err, domain.ErrNodeGroupSortOrderRange)
}

// TestNodeGroupUC_Update_RenameAllowedWhenUsed: ключевое отличие от заголовков
// (§24) — переименование ИСПОЛЬЗУЕМОЙ группы разрешено, потому что узел
// ссылается на неё по id и ссылка не осиротеет.
func TestNodeGroupUC_Update_RenameAllowedWhenUsed(t *testing.T) {
	t.Parallel()
	repo := newMemNodeGroupRepo()
	uc := newNodeGroupUC(repo)
	ctx := context.Background()

	g := &domain.NodeGroup{Name: "Старое имя"}
	require.NoError(t, uc.Create(ctx, SystemActor(), g))
	repo.items[g.ID].UsageCount = 7

	require.NoError(t, uc.Update(ctx, Actor{UserLogin: "manager"}, g.ID, "Новое имя", "описание", 30))
	got := repo.items[g.ID]
	assert.Equal(t, "Новое имя", got.Name)
	assert.Equal(t, "описание", got.Description)
	assert.Equal(t, int32(30), got.SortOrder)
	assert.Equal(t, "manager", got.UpdatedBy)
}

func TestNodeGroupUC_Update_NameCollision(t *testing.T) {
	t.Parallel()
	repo := newMemNodeGroupRepo()
	uc := newNodeGroupUC(repo)
	ctx := context.Background()
	gs := seedGroups(t, uc, "Первая", "Вторая")

	err := uc.Update(ctx, SystemActor(), gs[1].ID, "первая", "", 0)
	assert.ErrorIs(t, err, domain.ErrNodeGroupAlreadyExists)
	assert.Equal(t, "Вторая", repo.items[gs[1].ID].Name, "неудачная правка не должна менять запись")
}

func TestNodeGroupUC_Update_NotFound(t *testing.T) {
	t.Parallel()
	uc := newNodeGroupUC(newMemNodeGroupRepo())
	err := uc.Update(context.Background(), SystemActor(), "нет-такой", "Имя", "", 0)
	assert.ErrorIs(t, err, domain.ErrNodeGroupNotFound)
}

func TestNodeGroupUC_Delete_GuardedByUsage(t *testing.T) {
	t.Parallel()
	repo := newMemNodeGroupRepo()
	uc := newNodeGroupUC(repo)
	ctx := context.Background()

	g := &domain.NodeGroup{Name: "Занятая"}
	require.NoError(t, uc.Create(ctx, SystemActor(), g))
	repo.items[g.ID].UsageCount = 1

	assert.ErrorIs(t, uc.Delete(ctx, SystemActor(), g.ID), domain.ErrNodeGroupInUse)
	assert.Len(t, repo.items, 1, "используемая группа не должна удаляться")

	repo.items[g.ID].UsageCount = 0
	require.NoError(t, uc.Delete(ctx, SystemActor(), g.ID))
	assert.Empty(t, repo.items)
}

// TestNodeGroupUC_Move_EqualSortOrders: сдвиг обязан работать на справочнике,
// где sort_order у всех одинаков (дефолт 0) — обмен двух значений в этом
// случае был бы no-op, и кнопка молча не работала бы.
func TestNodeGroupUC_Move_EqualSortOrders(t *testing.T) {
	t.Parallel()
	repo := newMemNodeGroupRepo()
	uc := newNodeGroupUC(repo)
	ctx := context.Background()
	gs := seedGroups(t, uc, "Альфа", "Бета", "Гамма")
	for _, g := range gs {
		require.Equal(t, int32(0), repo.items[g.ID].SortOrder, "предусловие: порядок одинаков")
	}

	// Поднимаем «Гамма»: Альфа, Гамма, Бета.
	require.NoError(t, uc.Move(ctx, Actor{UserLogin: "manager"}, gs[2].ID, MoveUp))
	assert.Equal(t, []string{"Альфа", "Гамма", "Бета"}, listNames(t, uc))
	assert.Equal(t, int32(20), repo.items[gs[2].ID].SortOrder)
	assert.Equal(t, "manager", repo.items[gs[2].ID].UpdatedBy)

	// Опускаем её обратно.
	require.NoError(t, uc.Move(ctx, SystemActor(), gs[2].ID, MoveDown))
	assert.Equal(t, []string{"Альфа", "Бета", "Гамма"}, listNames(t, uc))
}

// TestNodeGroupUC_Move_AtEdge: край списка — не ошибка. Кнопка в UI неактивна,
// но параллельное удаление соседа могло превратить её в клик в пустоту; порядок
// при этом трогать нельзя.
func TestNodeGroupUC_Move_AtEdge(t *testing.T) {
	t.Parallel()
	repo := newMemNodeGroupRepo()
	uc := newNodeGroupUC(repo)
	ctx := context.Background()
	gs := seedGroups(t, uc, "Альфа", "Бета")

	require.NoError(t, uc.Move(ctx, SystemActor(), gs[0].ID, MoveUp))
	require.NoError(t, uc.Move(ctx, SystemActor(), gs[1].ID, MoveDown))
	assert.Empty(t, repo.reorderCalls, "сдвиг за край не должен переписывать порядок")
	assert.Equal(t, []string{"Альфа", "Бета"}, listNames(t, uc))
}

func TestNodeGroupUC_Move_Invalid(t *testing.T) {
	t.Parallel()
	repo := newMemNodeGroupRepo()
	uc := newNodeGroupUC(repo)
	ctx := context.Background()
	gs := seedGroups(t, uc, "Альфа")

	assert.ErrorIs(t, uc.Move(ctx, SystemActor(), gs[0].ID, MoveDirection("sideways")),
		domain.ErrNodeGroupInvalidDirection)
	assert.ErrorIs(t, uc.Move(ctx, SystemActor(), "нет-такой", MoveUp),
		domain.ErrNodeGroupNotFound)
	assert.Empty(t, repo.reorderCalls)
}

// TestNodeGroupUC_Move_CatalogTooLarge (ревизия §99): Move переприсваивает
// порядок ВСЕМ группам, поэтому читает справочник целиком. Если он перерос
// лимит чтения, сдвиг тронул бы только начало, а хвост остался бы со старой
// шкалой и перемешался. Отказ честнее молчаливой перетасовки.
func TestNodeGroupUC_Move_CatalogTooLarge(t *testing.T) {
	t.Parallel()
	repo := newMemNodeGroupRepo()
	uc := newNodeGroupUC(repo)
	ctx := context.Background()

	names := make([]string, 0, maxGroupsForReorder)
	for i := range maxGroupsForReorder {
		names = append(names, fmt.Sprintf("Группа %04d", i))
	}
	gs := seedGroups(t, uc, names...)

	err := uc.Move(ctx, SystemActor(), gs[len(gs)-1].ID, MoveUp)
	assert.ErrorIs(t, err, domain.ErrNodeGroupTooManyToReorder)
	assert.Empty(t, repo.reorderCalls, "порядок не должен переписываться частично")
}

func listNames(t *testing.T, uc *NodeGroupUsecase) []string {
	t.Helper()
	gs, err := uc.List(context.Background(), "", 0)
	require.NoError(t, err)
	out := make([]string, 0, len(gs))
	for _, g := range gs {
		out = append(out, g.Name)
	}
	return out
}
