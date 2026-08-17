//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	"nexus/internal/web/usecase/port"
)

// TestAuditScope_TeamIDs_E2E (§86.7): сквозной скоуп журнала на реальном PG.
//
// Тест существует именно как integration: `team_id = ANY($n::uuid[])` — новая
// форма параметра в этом репозитории (остальные UUID-условия здесь одиночные,
// `$n::uuid`), и её совместимость с кодированием []string в pgx проверяется
// только настоящим PostgreSQL. Unit-тесты усечения фильтра этого не видят.
func TestAuditScope_TeamIDs_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	logger := logging.NewNoop()
	repo := pgrepo.NewAuditRepoPg(pool, logger)
	teamRepo := pgrepo.NewTeamRepoPg(pool, logger)

	defaultTeam := resolveDefaultTeamID(t, ctx, pool)

	alpha := &domain.Team{Slug: "alpha86", Name: "Alpha", CHDatabase: "nexus_alpha86"}
	beta := &domain.Team{Slug: "beta86", Name: "Beta", CHDatabase: "nexus_beta86"}
	for _, tm := range []*domain.Team{alpha, beta} {
		if err := teamRepo.Create(ctx, tm); err != nil {
			t.Fatalf("create team %s: %v", tm.Slug, err)
		}
	}

	// По записи на команду плюс глобальная (team_id IS NULL, §18.1).
	write := func(teamID, action string) {
		t.Helper()
		e := &domain.AuditEntry{UserLogin: "alice", TeamID: teamID, Action: action}
		if err := repo.Write(ctx, e); err != nil {
			t.Fatalf("write audit %s: %v", action, err)
		}
	}
	write(alpha.ID, "node.create")
	write(beta.ID, "node.update")
	write(defaultTeam, "node.delete")
	write("", "settings.update") // глобальное действие admin'а

	actions := func(entries []*domain.AuditEntry) map[string]bool {
		got := make(map[string]bool, len(entries))
		for _, e := range entries {
			got[e.Action] = true
		}
		return got
	}

	// Сквозной скоуп: только перечисленные команды.
	got, err := repo.List(ctx, port.AuditFilter{TeamIDs: []string{alpha.ID, beta.ID}})
	if err != nil {
		t.Fatalf("list by TeamIDs: %v", err)
	}
	a := actions(got)
	if !a["node.create"] || !a["node.update"] {
		t.Fatalf("сквозной скоуп потерял записи своих команд: %v", a)
	}
	if a["node.delete"] {
		t.Fatal("в выдачу попала запись команды вне скоупа")
	}
	if a["settings.update"] {
		t.Fatal("глобальная запись (team_id IS NULL) не должна попадать в скоуп по командам")
	}

	// TeamIDs имеет приоритет над TeamID: иначе в SQL уехали бы два
	// взаимоисключающих условия и выдача всегда была бы пустой.
	got, err = repo.List(ctx, port.AuditFilter{TeamID: defaultTeam, TeamIDs: []string{alpha.ID}})
	if err != nil {
		t.Fatalf("list by TeamIDs with TeamID set: %v", err)
	}
	a = actions(got)
	if !a["node.create"] || len(got) != 1 {
		t.Fatalf("TeamIDs должен подавлять TeamID, получено: %v", a)
	}

	// Однокомандный путь не задет.
	got, err = repo.List(ctx, port.AuditFilter{TeamID: defaultTeam})
	if err != nil {
		t.Fatalf("list by TeamID: %v", err)
	}
	a = actions(got)
	if !a["node.delete"] || a["node.create"] {
		t.Fatalf("однокомандный скоуп изменился: %v", a)
	}

	// §91.2: IncludeGlobal добавляет к скоупу записи без команды — входы,
	// неудачные логины, восстановление пароля. Без флага они не видны нигде,
	// кроме ручного team_id=*, из-за чего журнал и выглядел полупустым.
	got, err = repo.List(ctx, port.AuditFilter{TeamID: defaultTeam, IncludeGlobal: true})
	if err != nil {
		t.Fatalf("list with IncludeGlobal: %v", err)
	}
	a = actions(got)
	if !a["node.delete"] {
		t.Fatal("IncludeGlobal потерял записи собственной команды")
	}
	if !a["settings.update"] {
		t.Fatal("IncludeGlobal обязан показывать записи без команды")
	}
	if a["node.create"] {
		t.Fatal("IncludeGlobal не должен раскрывать записи ЧУЖИХ команд")
	}

	// Тот же флаг в сквозном режиме.
	got, err = repo.List(ctx, port.AuditFilter{TeamIDs: []string{alpha.ID}, IncludeGlobal: true})
	if err != nil {
		t.Fatalf("list across teams with IncludeGlobal: %v", err)
	}
	a = actions(got)
	if !a["node.create"] || !a["settings.update"] || a["node.update"] {
		t.Fatalf("сквозной скоуп с IncludeGlobal вернул не то: %v", a)
	}
}

// §91.1: keyset-пагинация и счётчик. Проверяется на реальном PG, потому что
// сравнение кортежей `(created_at, id) < ($1, $2::uuid)` — новая для этого
// репозитория форма условия, а её поведение на равных метках времени и есть
// то, ради чего курсор вводился.
func TestAuditKeysetPagination_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	logger := logging.NewNoop()
	repo := pgrepo.NewAuditRepoPg(pool, logger)
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)

	// Половина записей — с ОДИНАКОВОЙ меткой времени: на таких данных
	// пагинация только по created_at теряла бы или дублировала строки.
	const total = 25
	shared := time.Now().UTC().Truncate(time.Second)
	for i := range total {
		e := &domain.AuditEntry{UserLogin: "alice", TeamID: defaultTeam, Action: "node.update"}
		if err := repo.Write(ctx, e); err != nil {
			t.Fatalf("write audit %d: %v", i, err)
		}
		if i%2 == 0 {
			if _, err := pool.Exec(ctx,
				`UPDATE user_audit SET created_at = $1 WHERE id = $2::uuid`, shared, e.ID); err != nil {
				t.Fatalf("stamp shared created_at: %v", err)
			}
		}
	}

	f := port.AuditFilter{TeamID: defaultTeam, Limit: 10}
	seen := map[string]bool{}
	pages := 0
	for {
		page, err := repo.List(ctx, f)
		if err != nil {
			t.Fatalf("list page %d: %v", pages, err)
		}
		if len(page) == 0 {
			break
		}
		for _, e := range page {
			if seen[e.ID] {
				t.Fatalf("запись %s пришла дважды — курсор не двигается", e.ID)
			}
			seen[e.ID] = true
		}
		last := page[len(page)-1]
		f.BeforeTS, f.BeforeID = &last.CreatedAt, last.ID
		pages++
		if pages > 10 {
			t.Fatal("пагинация не сходится")
		}
	}
	if len(seen) != total {
		t.Fatalf("прочитано %d записей из %d — часть потеряна на границах страниц", len(seen), total)
	}

	// Count игнорирует limit и курсор: он отвечает «из скольких», а не
	// «сколько на странице».
	n, err := repo.Count(ctx, port.AuditFilter{TeamID: defaultTeam, Limit: 10})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != total {
		t.Fatalf("count = %d, want %d", n, total)
	}

	// Фильтры счётчика те же, что у списка.
	n, err = repo.Count(ctx, port.AuditFilter{TeamID: defaultTeam, Actions: []string{"node.create"}})
	if err != nil {
		t.Fatalf("count filtered: %v", err)
	}
	if n != 0 {
		t.Fatalf("count по чужому action = %d, want 0", n)
	}
}
