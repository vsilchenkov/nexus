package usecase

import (
	"context"
	"fmt"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// §56: приведение схемы существующей таблицы логов узла к его шаблону через
// ALTER. Модель — предпросмотр (Plan, read-only) + явное применение (Apply):
// операции ClickHouse тяжёлые и частично необратимы, поэтому не побочка Update.

// nodeGetter — узел в скоупе команды (тот же Get, что у NodeUsecase). Узкий
// интерфейс на стороне консьюмера (ISP).
type nodeGetter interface {
	Get(ctx context.Context, id, teamID string) (*domain.Node, error)
}

// chTemplateGetter — резолв шаблона узла (как в provisionTable): по id либо
// дефолтный. Подмножество port.CHTemplateRepo.
type chTemplateGetter interface {
	Get(ctx context.Context, id string) (*domain.CHTemplate, error)
	GetDefault(ctx context.Context) (*domain.CHTemplate, error)
}

// SchemaSyncResult — результат Plan/Apply: имя таблицы, план (ALTER'ы +
// неприменимое) и признак отсутствия таблицы (синхронизировать нечего).
type SchemaSyncResult struct {
	Table        string                `json:"table"`
	Plan         domain.SchemaSyncPlan `json:"plan"`
	TableMissing bool                  `json:"table_missing"`
	// Applied — сколько ALTER'ов реально исполнено (только для Apply).
	Applied int `json:"applied"`
}

// CHSchemaSyncUsecase — §56. inspector==nil (CH не настроен) → ErrCHUnavailable.
type CHSchemaSyncUsecase struct {
	nodes     nodeGetter
	templates chTemplateGetter
	inspector port.CHSchemaInspector
	audit     *AuditUsecase
	logger    logging.Logger
}

func NewCHSchemaSyncUsecase(
	nodes nodeGetter,
	templates chTemplateGetter,
	inspector port.CHSchemaInspector,
	audit *AuditUsecase,
	logger logging.Logger,
) *CHSchemaSyncUsecase {
	return &CHSchemaSyncUsecase{nodes: nodes, templates: templates, inspector: inspector, audit: audit, logger: logger}
}

// plan разрешает узел, его шаблон и снимок таблицы, возвращает результат диффа.
// Общая часть Plan и Apply. Не пишет аудит и не исполняет ALTER'ы.
func (u *CHSchemaSyncUsecase) plan(ctx context.Context, id, teamID string) (*SchemaSyncResult, *domain.Node, error) {
	if u.inspector == nil || u.templates == nil {
		return nil, nil, ErrCHUnavailable
	}
	n, err := u.nodes.Get(ctx, id, teamID)
	if err != nil {
		return nil, nil, err
	}
	// Нет таблицы/логирования — синхронизировать нечего (не ошибка).
	if n.ClickHouseTable == "" {
		return &SchemaSyncResult{Table: n.ClickHouseTable, TableMissing: true}, n, nil
	}
	tmpl, err := u.resolveTemplate(ctx, n)
	if err != nil {
		return nil, nil, err
	}
	cur, found, err := u.inspector.ReadTableSchema(ctx, n.ClickHouseTable)
	if err != nil {
		return nil, nil, fmt.Errorf("read table schema: %w", err)
	}
	if !found {
		return &SchemaSyncResult{Table: n.ClickHouseTable, TableMissing: true}, n, nil
	}
	p := domain.PlanSchemaSync(n.ClickHouseTable, tmpl.Spec, n.ClickHouseRetentionDays, cur)
	return &SchemaSyncResult{Table: n.ClickHouseTable, Plan: p}, n, nil
}

// resolveTemplate — тот же выбор, что provisionTable: явный шаблон узла, иначе
// дефолтный.
func (u *CHSchemaSyncUsecase) resolveTemplate(ctx context.Context, n *domain.Node) (*domain.CHTemplate, error) {
	if n.ClickHouseTemplateID != "" {
		return u.templates.Get(ctx, n.ClickHouseTemplateID)
	}
	return u.templates.GetDefault(ctx)
}

// Plan — предпросмотр (read-only): что за ALTER'ы применятся и что неприменимо.
func (u *CHSchemaSyncUsecase) Plan(ctx context.Context, id, teamID string) (*SchemaSyncResult, error) {
	res, _, err := u.plan(ctx, id, teamID)
	return res, err
}

// Apply исполняет применимые ALTER'ы и пишет аудит. Если применять нечего
// (пустой план / нет таблицы) — no-op с Applied=0. Неприменимое (rejections)
// возвращается в результате, но не блокирует применение остального.
func (u *CHSchemaSyncUsecase) Apply(ctx context.Context, actor Actor, id, teamID string) (*SchemaSyncResult, error) {
	res, n, err := u.plan(ctx, id, teamID)
	if err != nil {
		return nil, err
	}
	if len(res.Plan.Statements) == 0 {
		return res, nil
	}
	if err := u.inspector.ApplyAlter(ctx, res.Table, res.Plan.Statements); err != nil {
		return nil, fmt.Errorf("apply schema sync: %w", err)
	}
	res.Applied = len(res.Plan.Statements)
	u.audit.Log(ctx, actor, domain.ActionNodeCHSchemaSync, "node", n.ID, map[string]any{
		"table":      res.Table,
		"statements": res.Plan.Statements,
		"applied":    res.Applied,
		"rejections": len(res.Plan.Rejections),
	})
	u.logger.Info("ch schema sync applied",
		u.logger.Str("node", n.ID), u.logger.Str("table", res.Table), u.logger.Int("applied", res.Applied))
	return res, nil
}
