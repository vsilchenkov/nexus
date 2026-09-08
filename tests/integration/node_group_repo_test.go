//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
)

// makeNodeInGroup создаёт узел с заданной группой на реальном PG.
func makeNodeInGroup(t *testing.T, ctx context.Context, nodeRepo *pgrepo.NodeRepoPg, teamID, path, groupID string) *domain.Node {
	t.Helper()
	n := &domain.Node{Path: path, RootMethod: domain.RootMethodRequest, TargetURL: "https://example.com"}
	n.SetDefaults()
	n.TeamID = teamID
	n.GroupID = groupID
	if err := nodeRepo.Create(ctx, n); err != nil {
		t.Fatalf("create node %q: %v", path, err)
	}
	return n
}

// TestNodeGroup_Repo_E2E: справочник групп узлов на реальном PG (§99).
//
// Проверяет то, что моками не проверяется вовсе: case-insensitive UNIQUE,
// usage_count on-read по nodes.group_id, круговой рейс group_id через
// INSERT/SELECT/UPDATE узла и защиту ON DELETE RESTRICT.
func TestNodeGroup_Repo_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	logger := logging.NewNoop()
	cipher, _ := crypto.NewCipher(testEncryptionKey)
	repo := pgrepo.NewNodeGroupRepoPg(pool, logger)
	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	teamID := resolveDefaultTeamID(t, ctx, pool)

	// Create + case-insensitive UNIQUE. Имя со ВСЕМИ признаками свободного
	// текста (пробел, кириллица, цифра) — CHECK формата у node_groups нет.
	g := &domain.NodeGroup{Name: "1С Обмен", Description: "интеграции 1С", SortOrder: 10, CreatedBy: "admin"}
	if err := repo.Create(ctx, g); err != nil {
		t.Fatalf("create: %v", err)
	}
	if g.ID == "" || g.CreatedAt.IsZero() {
		t.Fatalf("create did not fill id/created_at: %+v", g)
	}
	dup := &domain.NodeGroup{Name: "1с обмен"}
	if err := repo.Create(ctx, dup); !errors.Is(err, domain.ErrNodeGroupAlreadyExists) {
		t.Fatalf("dup create err = %v, want ErrNodeGroupAlreadyExists", err)
	}

	// GetByName — без учёта регистра (идемпотентный create комбобокса).
	got, err := repo.GetByName(ctx, "1С ОБМЕН")
	if err != nil || got.ID != g.ID {
		t.Fatalf("GetByName case-insensitive: got=%+v err=%v", got, err)
	}

	// Ещё две группы: порядок вывода — sort_order, затем name.
	g2 := &domain.NodeGroup{Name: "Курьерские службы", SortOrder: 20, CreatedBy: "admin"}
	g3 := &domain.NodeGroup{Name: "Маркетплейсы", SortOrder: 20, CreatedBy: "admin"}
	for _, x := range []*domain.NodeGroup{g2, g3} {
		if err := repo.Create(ctx, x); err != nil {
			t.Fatalf("create %s: %v", x.Name, err)
		}
	}
	list, err := repo.List(ctx, "", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if names := groupNames(list); len(names) != 3 ||
		names[0] != "1С Обмен" || names[1] != "Курьерские службы" || names[2] != "Маркетплейсы" {
		t.Fatalf("list order = %v, want [1С Обмен, Курьерские службы, Маркетплейсы]", names)
	}

	// Поиск — ПОДСТРОКА без учёта регистра: имя из нескольких слов, и
	// prefix-поиск §24 нашёл бы «1С Обмен» только по «1С».
	found, err := repo.List(ctx, "обмен", 0)
	if err != nil || len(found) != 1 || found[0].ID != g.ID {
		t.Fatalf("substring search = %v err=%v, want [1С Обмен]", groupNames(found), err)
	}

	// usage_count on-read: два узла в группе g, один в g2, один без группы.
	n1 := makeNodeInGroup(t, ctx, nodeRepo, teamID, "erp/orders", g.ID)
	makeNodeInGroup(t, ctx, nodeRepo, teamID, "erp/stock", g.ID)
	makeNodeInGroup(t, ctx, nodeRepo, teamID, "cdek/label", g2.ID)
	makeNodeInGroup(t, ctx, nodeRepo, teamID, "geo/search", "")

	if got, err = repo.Get(ctx, g.ID); err != nil || got.UsageCount != 2 {
		t.Fatalf("usage_count = %d err=%v, want 2", got.UsageCount, err)
	}
	if got, err = repo.Get(ctx, g3.ID); err != nil || got.UsageCount != 0 {
		t.Fatalf("unused group usage_count = %d err=%v, want 0", got.UsageCount, err)
	}

	// Круговой рейс group_id через репозиторий узла: записан, прочитан,
	// переписан и снят.
	read, err := nodeRepo.Get(ctx, n1.ID)
	if err != nil || read.GroupID != g.ID {
		t.Fatalf("node group_id after create = %q err=%v, want %q", read.GroupID, err, g.ID)
	}
	read.GroupID = g2.ID
	if err = nodeRepo.Update(ctx, read); err != nil {
		t.Fatalf("update node group: %v", err)
	}
	if read, err = nodeRepo.Get(ctx, n1.ID); err != nil || read.GroupID != g2.ID {
		t.Fatalf("node group_id after update = %q err=%v, want %q", read.GroupID, err, g2.ID)
	}
	read.GroupID = ""
	if err = nodeRepo.Update(ctx, read); err != nil {
		t.Fatalf("clear node group: %v", err)
	}
	if read, err = nodeRepo.Get(ctx, n1.ID); err != nil || read.GroupID != "" {
		t.Fatalf("node group_id after clear = %q err=%v, want empty", read.GroupID, err)
	}
}

// TestNodeGroup_Repo_DeleteRestrict: ON DELETE RESTRICT — второй уровень защиты
// к guard-проверке usecase. Проверяется именно на БД: guard в usecase читает
// usage_count заранее, и между чтением и удалением узел может быть привязан из
// другой вкладки — тогда единственным барьером остаётся FK.
func TestNodeGroup_Repo_DeleteRestrict(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	logger := logging.NewNoop()
	cipher, _ := crypto.NewCipher(testEncryptionKey)
	repo := pgrepo.NewNodeGroupRepoPg(pool, logger)
	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	teamID := resolveDefaultTeamID(t, ctx, pool)

	g := &domain.NodeGroup{Name: "Занятая", CreatedBy: "admin"}
	if err := repo.Create(ctx, g); err != nil {
		t.Fatalf("create: %v", err)
	}
	n := makeNodeInGroup(t, ctx, nodeRepo, teamID, "busy/node", g.ID)

	if err := repo.Delete(ctx, g.ID); !errors.Is(err, domain.ErrNodeGroupInUse) {
		t.Fatalf("delete used group err = %v, want ErrNodeGroupInUse", err)
	}

	// Переименование используемой группы, наоборот, РАЗРЕШЕНО: узел ссылается
	// по id, ссылка не осиротеет (в отличие от заголовков §24).
	g.Name = "Переименованная"
	g.UpdatedBy = "manager"
	if err := repo.Update(ctx, g); err != nil {
		t.Fatalf("rename used group: %v", err)
	}
	got, err := repo.Get(ctx, g.ID)
	if err != nil || got.Name != "Переименованная" || got.UpdatedBy != "manager" {
		t.Fatalf("after rename: %+v err=%v", got, err)
	}
	// Узел не осиротел — он всё ещё в той же группе.
	read, err := nodeRepo.Get(ctx, n.ID)
	if err != nil || read.GroupID != g.ID {
		t.Fatalf("node lost group after rename: %q err=%v", read.GroupID, err)
	}

	// Освободили группу — удаление проходит.
	read.GroupID = ""
	if err = nodeRepo.Update(ctx, read); err != nil {
		t.Fatalf("clear node group: %v", err)
	}
	if err = repo.Delete(ctx, g.ID); err != nil {
		t.Fatalf("delete free group: %v", err)
	}
	if _, err = repo.Get(ctx, g.ID); !errors.Is(err, domain.ErrNodeGroupNotFound) {
		t.Fatalf("get after delete err = %v, want ErrNodeGroupNotFound", err)
	}
	if err = repo.Delete(ctx, g.ID); !errors.Is(err, domain.ErrNodeGroupNotFound) {
		t.Fatalf("delete missing err = %v, want ErrNodeGroupNotFound", err)
	}
}

// TestNodeGroup_Repo_Reorder: сдвиг порядка обязан работать и тогда, когда у
// всех групп sort_order одинаков (дефолт 0 у записей, созданных до появления
// стрелок) — именно этот случай обмен двух значений не покрывает.
func TestNodeGroup_Repo_Reorder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	repo := pgrepo.NewNodeGroupRepoPg(pool, logging.NewNoop())

	// Все три с sort_order = 0: порядок определяется только именем.
	a := &domain.NodeGroup{Name: "Альфа", CreatedBy: "admin"}
	b := &domain.NodeGroup{Name: "Бета", CreatedBy: "admin"}
	c := &domain.NodeGroup{Name: "Гамма", CreatedBy: "admin"}
	for _, g := range []*domain.NodeGroup{a, b, c} {
		if err := repo.Create(ctx, g); err != nil {
			t.Fatalf("create %s: %v", g.Name, err)
		}
	}
	list, err := repo.List(ctx, "", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if names := groupNames(list); names[0] != "Альфа" || names[1] != "Бета" || names[2] != "Гамма" {
		t.Fatalf("initial order = %v", names)
	}

	// Поднимаем «Гамма» на одну позицию: Альфа, Гамма, Бета.
	if err = repo.Reorder(ctx, []string{a.ID, c.ID, b.ID}, "manager"); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	list, err = repo.List(ctx, "", 0)
	if err != nil {
		t.Fatalf("list after reorder: %v", err)
	}
	names := groupNames(list)
	if names[0] != "Альфа" || names[1] != "Гамма" || names[2] != "Бета" {
		t.Fatalf("order after reorder = %v, want [Альфа Гамма Бета]", names)
	}
	// Шкала разрежённая: 10/20/30 — место для ручной вставки между группами.
	for i, g := range list {
		if want := int32((i + 1) * 10); g.SortOrder != want {
			t.Fatalf("%s sort_order = %d, want %d", g.Name, g.SortOrder, want)
		}
		if g.UpdatedBy != "manager" {
			t.Fatalf("%s updated_by = %q, want manager", g.Name, g.UpdatedBy)
		}
	}

	// Пустой порядок — no-op, а не ошибка и не затирание шкалы.
	if err = repo.Reorder(ctx, nil, "manager"); err != nil {
		t.Fatalf("empty reorder: %v", err)
	}
	if list, err = repo.List(ctx, "", 0); err != nil || groupNames(list)[1] != "Гамма" {
		t.Fatalf("order changed after empty reorder: %v err=%v", groupNames(list), err)
	}
}

func groupNames(gs []*domain.NodeGroup) []string {
	out := make([]string, 0, len(gs))
	for _, g := range gs {
		out = append(out, g.Name)
	}
	return out
}

// TestNodeGroup_Repo_NodeWithMissingGroup: узел ссылается на группу, которой
// нет. Сценарий не выдуманный: оператор открыл форму, коллега удалил пустую
// группу в соседней вкладке, оператор сохраняет.
//
// Ревизия §99: до правки FK-нарушение уезжало в fmt.Errorf → 500 «внутренняя
// ошибка», и починить форму по такому ответу было нечем. Теперь это доменная
// ошибка, которую handler показывает у поля «Группа».
func TestNodeGroup_Repo_NodeWithMissingGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	logger := logging.NewNoop()
	cipher, _ := crypto.NewCipher(testEncryptionKey)
	repo := pgrepo.NewNodeGroupRepoPg(pool, logger)
	nodeRepo := pgrepo.NewNodeRepoPg(pool, cipher, logger)
	teamID := resolveDefaultTeamID(t, ctx, pool)

	const ghost = "8f2c1a94-7d31-4b0e-9a11-2c3d4e5f6071"

	// Create с несуществующей группой.
	n := &domain.Node{Path: "ghost/create", RootMethod: domain.RootMethodRequest, TargetURL: "https://example.com"}
	n.SetDefaults()
	n.TeamID = teamID
	n.GroupID = ghost
	if err := nodeRepo.Create(ctx, n); !errors.Is(err, domain.ErrNodeGroupNotFound) {
		t.Fatalf("create with missing group err = %v, want ErrNodeGroupNotFound", err)
	}

	// Update существующего узла на несуществующую группу.
	live := makeNodeInGroup(t, ctx, nodeRepo, teamID, "ghost/update", "")
	live.GroupID = ghost
	if err := nodeRepo.Update(ctx, live); !errors.Is(err, domain.ErrNodeGroupNotFound) {
		t.Fatalf("update with missing group err = %v, want ErrNodeGroupNotFound", err)
	}

	// Контроль: та же операция с РЕАЛЬНОЙ группой проходит — значит ошибка выше
	// приходит именно от FK группы, а не от любого другого ограничения узла.
	g := &domain.NodeGroup{Name: "Живая", CreatedBy: "admin"}
	if err := repo.Create(ctx, g); err != nil {
		t.Fatalf("create group: %v", err)
	}
	live.GroupID = g.ID
	if err := nodeRepo.Update(ctx, live); err != nil {
		t.Fatalf("update with existing group: %v", err)
	}
}
