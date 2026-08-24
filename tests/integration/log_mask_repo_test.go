//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	maskpg "nexus/internal/sender/adapter/out/maskpg"
	senderuc "nexus/internal/sender/usecase"
	pgrepo "nexus/internal/web/adapter/out/postgres"
)

// TestLogMask_Repo_E2E: справочник маскирования (§95) на реальном PG. Проверяет
// сиды миграции 0041, CRUD, чтение только активных через reader Sender'а и
// сквозную маскировку через LogMaskProvider.
func TestLogMask_Repo_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	logger := logging.NewNoop()
	repo := pgrepo.NewLogMaskRepoPg(pool, logger)

	// Миграция 0041 засеяла два шаблона (Telegram, green-api).
	seeded, err := repo.List(ctx, "")
	if err != nil {
		t.Fatalf("list seeds: %v", err)
	}
	if len(seeded) < 2 {
		t.Fatalf("want >=2 seeded patterns, got %d", len(seeded))
	}

	// Create.
	e := &domain.LogMaskPattern{
		Pattern: `(secret=)[A-Za-z0-9]+`, Replacement: "$1***",
		Description: "test", Enabled: true, SortOrder: 5, CreatedBy: "admin", UpdatedBy: "admin",
	}
	if err := repo.Create(ctx, e); err != nil {
		t.Fatalf("create: %v", err)
	}
	if e.ID == "" {
		t.Fatal("id empty after create")
	}

	// Get.
	got, err := repo.Get(ctx, e.ID)
	if err != nil || got.Pattern != e.Pattern {
		t.Fatalf("get: got=%+v err=%v", got, err)
	}

	// reader Sender'а видит активный шаблон и применяется через провайдер.
	reader := maskpg.New(pool, logger)
	prov := senderuc.NewLogMaskProvider()
	patterns, err := reader.Load(ctx)
	if err != nil {
		t.Fatalf("reader load: %v", err)
	}
	prov.Set(patterns)
	if masked := prov.Mask("url?secret=ABCdef123"); masked != "url?secret=***" {
		t.Fatalf("mask active: got %q", masked)
	}

	// Update → выключить: reader перестаёт его отдавать.
	e.Enabled = false
	e.UpdatedBy = "admin2"
	if err := repo.Update(ctx, e); err != nil {
		t.Fatalf("update: %v", err)
	}
	patterns, err = reader.Load(ctx)
	if err != nil {
		t.Fatalf("reader load after disable: %v", err)
	}
	prov.Set(patterns)
	if masked := prov.Mask("url?secret=ABCdef123"); masked != "url?secret=ABCdef123" {
		t.Fatalf("disabled pattern must not mask: got %q", masked)
	}

	// Delete.
	if err := repo.Delete(ctx, e.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.Get(ctx, e.ID); !errors.Is(err, domain.ErrLogMaskNotFound) {
		t.Fatalf("get after delete err = %v, want ErrLogMaskNotFound", err)
	}
	if err := repo.Delete(ctx, e.ID); !errors.Is(err, domain.ErrLogMaskNotFound) {
		t.Fatalf("delete missing err = %v, want ErrLogMaskNotFound", err)
	}
}
