//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
)

// TestCHTemplateRepo_E2E: CRUD каталога шаблонов на реальном PG (§19, Phase F1.2).
// Проверяет сид «Standard logs» (GetDefault), создание/чтение/обновление/удаление
// и CountNodesUsing.
func TestCHTemplateRepo_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	repo := pgrepo.NewCHTemplateRepoPg(pool, logging.NewNoop())

	// Сид: ровно один default «Standard logs».
	def, err := repo.GetDefault(ctx)
	if err != nil {
		t.Fatalf("get default: %v", err)
	}
	if def.Name != "Standard logs" || !def.IsDefault {
		t.Fatalf("unexpected default template: %+v", def)
	}
	if def.Spec.Engine != domain.CHEngineMergeTree {
		t.Fatalf("seeded engine = %q", def.Spec.Engine)
	}
	if _, err := def.RenderCreateTable("nexus_default.x", 0); err != nil {
		t.Fatalf("render seeded: %v", err)
	}

	// Create.
	tpl := &domain.CHTemplate{
		Name:        "Compressed logs",
		Description: "zstd on bodies",
		Spec: domain.CHTemplateSpec{
			Engine:      domain.CHEngineMergeTree,
			PartitionBy: "toYYYYMM(date_create)",
			OrderBy:     []string{"date_create", "date_request", "method"},
			ColumnOverrides: []domain.CHColumnOverride{
				{Name: "request", Codec: "ZSTD(3)"},
				{Name: "response", Codec: "ZSTD(3)"},
			},
			TTLMode: domain.CHTTLModeNone,
		},
	}
	if err := repo.Create(ctx, tpl); err != nil {
		t.Fatalf("create: %v", err)
	}
	if tpl.ID == "" {
		t.Fatal("template id empty after create")
	}

	// Duplicate name → ErrCHTemplateAlreadyExists.
	dup := &domain.CHTemplate{Name: "Compressed logs", Spec: domain.DefaultCHTemplateSpec()}
	if err := repo.Create(ctx, dup); err != domain.ErrCHTemplateAlreadyExists {
		t.Fatalf("duplicate create err = %v, want ErrCHTemplateAlreadyExists", err)
	}

	// Get + JSONB round-trip.
	got, err := repo.Get(ctx, tpl.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Spec.ColumnOverrides) != 2 || got.Spec.ColumnOverrides[0].Codec != "ZSTD(3)" {
		t.Fatalf("spec round-trip mismatch: %+v", got.Spec)
	}

	// List: default + созданный = 2.
	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("list len = %d, want 2", len(list))
	}

	// Update.
	got.Description = "updated"
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	reread, _ := repo.Get(ctx, tpl.ID)
	if reread.Description != "updated" {
		t.Fatalf("description not updated: %q", reread.Description)
	}

	// CountNodesUsing: пока 0.
	cnt, err := repo.CountNodesUsing(ctx, tpl.ID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != 0 {
		t.Fatalf("count = %d, want 0", cnt)
	}

	// Delete.
	if err := repo.Delete(ctx, tpl.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.Get(ctx, tpl.ID); err != domain.ErrCHTemplateNotFound {
		t.Fatalf("get after delete err = %v, want ErrCHTemplateNotFound", err)
	}
}
