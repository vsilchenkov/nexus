package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// movePlan фиксирует, что Move сделал с ClickHouse: переименование и/или
// создание таблицы. Реализует port.TeamProvisioner.
type movePlan struct {
	renameFrom   string
	renameTo     string
	renameErr    error
	createdTable string
	createErr    error
}

func (p *movePlan) CreateDatabase(context.Context, string) error { return nil }
func (p *movePlan) DropDatabase(context.Context, string) error   { return nil }

func (p *movePlan) RenameTable(_ context.Context, from, to string) error {
	p.renameFrom, p.renameTo = from, to
	return p.renameErr
}

func (p *movePlan) CreateTable(_ context.Context, table, _ string) error {
	p.createdTable = table
	return p.createErr
}

func (p *movePlan) VerifyTemplate(context.Context, string, *domain.CHTemplate) error { return nil }

// tableUsageStub — port.NodeTableUsage: сколько ДРУГИХ узлов на той же таблице.
type tableUsageStub struct {
	others  int
	err     error
	gotTbl  string
	gotSkip string
}

func (s *tableUsageStub) CountByCHTable(_ context.Context, table, excludeNodeID string) (int, error) {
	s.gotTbl, s.gotSkip = table, excludeNodeID
	return s.others, s.err
}

// moveTeamRepo — целевая команда переноса (slug → CH-база).
type moveTeamRepo struct{ nopTeamRepo }

func (moveTeamRepo) GetBySlug(_ context.Context, slug string) (*domain.Team, error) {
	return &domain.Team{ID: "team2", Slug: slug, CHDatabase: "nexus_" + slug}, nil
}

func movableNode() *domain.Node {
	return &domain.Node{
		ID: "node-1", Path: "svc/orders", TeamID: "team1",
		RootMethod: domain.RootMethodRequest, TargetURL: "https://backend.example",
		Status:               domain.NodeStatusEnabled,
		ClickHouseTable:      "nexus_default.orders",
		ClickHouseTemplateID: "tmpl-1",
		LoggingEnabled:       true,
		UpdatedAt:            time.Now(),
	}
}

func newMoveUC(repo *memNodeRepo, prov port.TeamProvisioner, usage port.NodeTableUsage) *NodeUsecase {
	tmpl := &domain.CHTemplate{ID: "tmpl-1", Name: "std", Spec: domain.DefaultCHTemplateSpec()}
	uc := NewNodeUsecase(repo, nopNodeCache{}, NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop()),
		nil, moveTeamRepo{}, prov, &memCHTemplateRepo{items: map[string]*domain.CHTemplate{tmpl.ID: tmpl}},
		time.Minute, 0, "team1", nil, logging.NewNoop())
	if usage != nil {
		uc.SetTableUsage(usage)
	}
	return uc
}

// TestNodeUC_Move_CHTable — как перенос узла поступает с таблицей логов.
//
// Ключевой случай — shared: таблицу делят несколько узлов, и безусловный RENAME
// (прежнее поведение) утаскивал её за одним переехавшим узлом, оставляя
// остальные со ссылкой на несуществующую таблицу.
func TestNodeUC_Move_CHTable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		others        int   // сколько ДРУГИХ узлов на той же таблице
		usageErr      error // сбой подсчёта
		renameErr     error
		noUsagePort   bool
		wantRenamed   bool
		wantCreated   string
		wantAuditMove bool
	}{
		{
			name:          "личная таблица переезжает вместе с узлом",
			others:        0,
			wantRenamed:   true,
			wantAuditMove: true,
		},
		{
			name:          "общую таблицу не трогаем, узлу создаём свою",
			others:        3,
			wantRenamed:   false,
			wantCreated:   "nexus_target.orders",
			wantAuditMove: true,
		},
		{
			name:          "исходной таблицы нет — перенос не ломается",
			others:        0,
			renameErr:     port.ErrSourceTableAbsent,
			wantRenamed:   true,
			wantAuditMove: true,
		},
		{
			name:          "имя занято в целевой БД — перенос не ломается",
			others:        0,
			renameErr:     port.ErrTargetTableExists,
			wantRenamed:   true,
			wantAuditMove: true,
		},
		{
			name:          "сбой подсчёта — прежнее поведение (rename)",
			others:        7,
			usageErr:      errors.New("pg down"),
			wantRenamed:   true,
			wantAuditMove: true,
		},
		{
			name:          "порт не подключён — прежнее поведение (rename)",
			noUsagePort:   true,
			wantRenamed:   true,
			wantAuditMove: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := newMemNodeRepo()
			n := movableNode()
			repo.items[n.ID] = n
			prov := &movePlan{renameErr: tc.renameErr}
			var usage *tableUsageStub
			if !tc.noUsagePort {
				usage = &tableUsageStub{others: tc.others, err: tc.usageErr}
			}
			uc := newMoveUC(repo, prov, usageOrNil(usage))

			err := uc.Move(context.Background(), SystemActor(), n.ID, "team1", "target")
			require.NoError(t, err)

			// PG-запись авторитетна в любом случае: узел уехал вместе со ссылкой.
			moved := repo.items[n.ID]
			assert.Equal(t, "team2", moved.TeamID)
			assert.Equal(t, "nexus_target.orders", moved.ClickHouseTable)

			if tc.wantRenamed {
				assert.Equal(t, "nexus_default.orders", prov.renameFrom)
				assert.Equal(t, "nexus_target.orders", prov.renameTo)
				assert.Empty(t, prov.createdTable, "при переносе таблицы новая не создаётся")
			} else {
				assert.Empty(t, prov.renameFrom, "общая таблица остаётся на месте")
				assert.Equal(t, tc.wantCreated, prov.createdTable)
			}

			if usage != nil {
				assert.Equal(t, "nexus_default.orders", usage.gotTbl, "считаем по ИСХОДНОЙ таблице")
				assert.Equal(t, n.ID, usage.gotSkip, "сам переносимый узел из подсчёта исключён")
			}
		})
	}
}

// usageOrNil — типизированный nil ломает проверку `u.tableUsage == nil`
// (интерфейс с nil-значением внутри не равен nil), поэтому отсутствие стаба
// передаём настоящим nil'ом интерфейса.
func usageOrNil(s *tableUsageStub) port.NodeTableUsage {
	if s == nil {
		return nil
	}
	return s
}

// TestNodeUC_Move_SharedTable_ProvisionFails: создание новой таблицы для
// переехавшего узла — best-effort. Узел уже перенесён в PG, откатывать нечего.
func TestNodeUC_Move_SharedTable_ProvisionFails(t *testing.T) {
	t.Parallel()
	repo := newMemNodeRepo()
	n := movableNode()
	repo.items[n.ID] = n
	prov := &movePlan{createErr: errors.New("clickhouse down")}
	uc := newMoveUC(repo, prov, &tableUsageStub{others: 2})

	err := uc.Move(context.Background(), SystemActor(), n.ID, "team1", "target")

	require.NoError(t, err, "сбой CH не отменяет перенос узла")
	assert.Equal(t, "team2", repo.items[n.ID].TeamID)
	assert.Empty(t, prov.renameFrom, "общая таблица не тронута даже при сбое создания новой")
}
