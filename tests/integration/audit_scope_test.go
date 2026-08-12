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
}
