package usecase

import (
	"context"
	"errors"
	"testing"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

type nodeGetterStub struct {
	node *domain.Node
	err  error
}

func (s *nodeGetterStub) Get(_ context.Context, _, _ string) (*domain.Node, error) {
	return s.node, s.err
}

type templateGetterStub struct{ tmpl *domain.CHTemplate }

func (s *templateGetterStub) Get(_ context.Context, _ string) (*domain.CHTemplate, error) {
	return s.tmpl, nil
}
func (s *templateGetterStub) GetDefault(_ context.Context) (*domain.CHTemplate, error) {
	return s.tmpl, nil
}

type inspectorStub struct {
	schema   domain.CurrentTableSchema
	found    bool
	readErr  error
	applied  []string
	applyErr error
}

func (s *inspectorStub) ReadTableSchema(_ context.Context, _ string) (domain.CurrentTableSchema, bool, error) {
	return s.schema, s.found, s.readErr
}
func (s *inspectorStub) ApplyAlter(_ context.Context, _ string, statements []string) error {
	if s.applyErr != nil {
		return s.applyErr
	}
	s.applied = append(s.applied, statements...)
	return nil
}

func syncNode() *domain.Node {
	return &domain.Node{
		ID:                      "n-1",
		ClickHouseTable:         "nexus_default.logs",
		ClickHouseTemplateID:    "tpl-1",
		ClickHouseRetentionDays: 0,
		LoggingEnabled:          true,
	}
}

func codecTemplate() *domain.CHTemplate {
	return &domain.CHTemplate{
		Spec: domain.CHTemplateSpec{
			Engine:          domain.CHEngineMergeTree,
			PartitionBy:     "toYYYYMM(date_create)",
			OrderBy:         []string{"date_create", "date_request", "method"},
			ColumnOverrides: []domain.CHColumnOverride{{Name: "request", Codec: "ZSTD(3)"}},
			TTLMode:         domain.CHTTLModeNone,
		},
	}
}

func matchingSnapshot() domain.CurrentTableSchema {
	return domain.CurrentTableSchema{
		Codecs:      map[string]string{},
		Engine:      "MergeTree",
		OrderBy:     []string{"date_create", "date_request", "method"},
		PartitionBy: "toYYYYMM(date_create)",
	}
}

func newSyncUC(node *domain.Node, tmpl *domain.CHTemplate, insp *inspectorStub) (*CHSchemaSyncUsecase, *stubAuditRepo) {
	auditRepo := &stubAuditRepo{}
	audit := NewAuditUsecase(auditRepo, logging.NewNoop())
	uc := NewCHSchemaSyncUsecase(&nodeGetterStub{node: node}, &templateGetterStub{tmpl: tmpl}, insp, audit, logging.NewNoop())
	return uc, auditRepo
}

// §64: узел с внешней таблицей отвергается и на Plan, и на Apply — Nexus не
// диффит и не альтерит таблицу, которой не управляет. До инспектора не доходим.
func TestCHSchemaSync_ExternalTable_Rejected(t *testing.T) {
	t.Parallel()
	n := syncNode()
	n.ClickHouseTemplateID = "" // внешняя таблица несовместима с шаблоном
	n.ExternalTable = true
	insp := &inspectorStub{schema: matchingSnapshot(), found: true}
	uc, auditRepo := newSyncUC(n, codecTemplate(), insp)

	if _, err := uc.Plan(context.Background(), "n-1", ""); !errors.Is(err, domain.ErrNodeExternalTable) {
		t.Fatalf("Plan: want ErrNodeExternalTable, got %v", err)
	}
	if _, err := uc.Apply(context.Background(), SystemActor(), "n-1", ""); !errors.Is(err, domain.ErrNodeExternalTable) {
		t.Fatalf("Apply: want ErrNodeExternalTable, got %v", err)
	}
	if len(insp.applied) != 0 {
		t.Fatalf("no ALTER must be executed, got %v", insp.applied)
	}
	if len(auditRepo.entries) != 0 {
		t.Fatalf("no audit entry expected, got %d", len(auditRepo.entries))
	}
}

func TestCHSchemaSync_Plan_ProducesAlter(t *testing.T) {
	t.Parallel()
	insp := &inspectorStub{schema: matchingSnapshot(), found: true}
	uc, _ := newSyncUC(syncNode(), codecTemplate(), insp)

	res, err := uc.Plan(context.Background(), "n-1", "team-1")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.TableMissing {
		t.Fatal("table exists — TableMissing must be false")
	}
	if len(res.Plan.Statements) != 1 ||
		res.Plan.Statements[0] != "ALTER TABLE nexus_default.logs MODIFY COLUMN request String CODEC(ZSTD(3))" {
		t.Fatalf("unexpected statements: %+v", res.Plan.Statements)
	}
}

func TestCHSchemaSync_Plan_TableMissing(t *testing.T) {
	t.Parallel()

	t.Run("no table in ClickHouse", func(t *testing.T) {
		t.Parallel()
		insp := &inspectorStub{found: false}
		uc, _ := newSyncUC(syncNode(), codecTemplate(), insp)
		res, err := uc.Plan(context.Background(), "n-1", "team-1")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if !res.TableMissing || len(res.Plan.Statements) != 0 {
			t.Fatalf("want TableMissing + empty plan, got %+v", res)
		}
	})

	t.Run("node has no table configured", func(t *testing.T) {
		t.Parallel()
		n := syncNode()
		n.ClickHouseTable = ""
		insp := &inspectorStub{found: true}
		uc, _ := newSyncUC(n, codecTemplate(), insp)
		res, err := uc.Plan(context.Background(), "n-1", "team-1")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if !res.TableMissing {
			t.Fatalf("want TableMissing, got %+v", res)
		}
	})
}

func TestCHSchemaSync_Apply_ExecutesAndAudits(t *testing.T) {
	t.Parallel()
	insp := &inspectorStub{schema: matchingSnapshot(), found: true}
	uc, auditRepo := newSyncUC(syncNode(), codecTemplate(), insp)

	res, err := uc.Apply(context.Background(), Actor{UserLogin: "alice"}, "n-1", "team-1")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Applied != 1 || len(insp.applied) != 1 {
		t.Fatalf("want 1 applied ALTER, got res.Applied=%d insp.applied=%+v", res.Applied, insp.applied)
	}
	if len(auditRepo.entries) != 1 || auditRepo.entries[0].Action != domain.ActionNodeCHSchemaSync {
		t.Fatalf("want one audit entry node.ch_schema_sync, got %+v", auditRepo.entries)
	}
}

func TestCHSchemaSync_Apply_NothingToDo(t *testing.T) {
	t.Parallel()
	// Снимок совпадает с шаблоном → пустой план → ни ALTER, ни аудита.
	insp := &inspectorStub{schema: matchingSnapshot(), found: true}
	uc, auditRepo := newSyncUC(syncNode(), codecTemplate2NoOverride(), insp)

	res, err := uc.Apply(context.Background(), Actor{}, "n-1", "team-1")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Applied != 0 || len(insp.applied) != 0 {
		t.Fatalf("want no ALTER, got %+v", insp.applied)
	}
	if len(auditRepo.entries) != 0 {
		t.Fatalf("no-op не должен писать аудит, got %+v", auditRepo.entries)
	}
}

func codecTemplate2NoOverride() *domain.CHTemplate {
	return &domain.CHTemplate{
		Spec: domain.CHTemplateSpec{
			Engine:      domain.CHEngineMergeTree,
			PartitionBy: "toYYYYMM(date_create)",
			OrderBy:     []string{"date_create", "date_request", "method"},
			TTLMode:     domain.CHTTLModeNone,
		},
	}
}

func TestCHSchemaSync_CHUnavailable(t *testing.T) {
	t.Parallel()
	uc := NewCHSchemaSyncUsecase(&nodeGetterStub{node: syncNode()},
		&templateGetterStub{tmpl: codecTemplate()}, nil, // inspector nil
		NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), logging.NewNoop())

	_, err := uc.Plan(context.Background(), "n-1", "team-1")
	if !errors.Is(err, ErrCHUnavailable) {
		t.Fatalf("want ErrCHUnavailable, got %v", err)
	}
}

func TestCHSchemaSync_NodeNotFound(t *testing.T) {
	t.Parallel()
	insp := &inspectorStub{found: true}
	uc := NewCHSchemaSyncUsecase(&nodeGetterStub{err: domain.ErrNodeNotFound},
		&templateGetterStub{tmpl: codecTemplate()}, insp,
		NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()), logging.NewNoop())

	_, err := uc.Plan(context.Background(), "n-1", "team-1")
	if !errors.Is(err, domain.ErrNodeNotFound) {
		t.Fatalf("want ErrNodeNotFound, got %v", err)
	}
}
