package usecase

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

// memCHTemplateRepo — in-memory port.CHTemplateRepo для unit-тестов.
type memCHTemplateRepo struct {
	items      map[string]*domain.CHTemplate
	nodesUsing map[string]int
	seq        int
}

func newMemCHTemplateRepo() *memCHTemplateRepo {
	return &memCHTemplateRepo{items: map[string]*domain.CHTemplate{}, nodesUsing: map[string]int{}}
}

func (r *memCHTemplateRepo) Get(_ context.Context, id string) (*domain.CHTemplate, error) {
	if t, ok := r.items[id]; ok {
		cp := *t
		return &cp, nil
	}
	return nil, domain.ErrCHTemplateNotFound
}
func (r *memCHTemplateRepo) GetDefault(_ context.Context) (*domain.CHTemplate, error) {
	for _, t := range r.items {
		if t.IsDefault {
			cp := *t
			return &cp, nil
		}
	}
	return nil, domain.ErrCHTemplateNotFound
}
func (r *memCHTemplateRepo) List(_ context.Context) ([]*domain.CHTemplate, error) {
	out := make([]*domain.CHTemplate, 0, len(r.items))
	for _, t := range r.items {
		cp := *t
		out = append(out, &cp)
	}
	return out, nil
}
func (r *memCHTemplateRepo) Create(_ context.Context, t *domain.CHTemplate) error {
	for _, e := range r.items {
		if e.Name == t.Name {
			return domain.ErrCHTemplateAlreadyExists
		}
	}
	r.seq++
	t.ID = string(rune('a'+r.seq)) + "-id"
	cp := *t
	r.items[t.ID] = &cp
	return nil
}
func (r *memCHTemplateRepo) Update(_ context.Context, t *domain.CHTemplate) error {
	if _, ok := r.items[t.ID]; !ok {
		return domain.ErrCHTemplateNotFound
	}
	cp := *t
	r.items[t.ID] = &cp
	return nil
}
func (r *memCHTemplateRepo) Delete(_ context.Context, id string) error {
	if _, ok := r.items[id]; !ok {
		return domain.ErrCHTemplateNotFound
	}
	delete(r.items, id)
	return nil
}
func (r *memCHTemplateRepo) CountNodesUsing(_ context.Context, id string) (int, error) {
	return r.nodesUsing[id], nil
}

// verifyProvisioner — port.TeamProvisioner, фиксирует вызовы VerifyTemplate.
type verifyProvisioner struct {
	verifiedDB string
	verifyErr  error
}

func (p *verifyProvisioner) CreateDatabase(context.Context, string) error { return nil }
func (p *verifyProvisioner) DropDatabase(context.Context, string) error   { return nil }
func (p *verifyProvisioner) RenameTable(context.Context, string, string) error {
	return nil
}
func (p *verifyProvisioner) CreateTable(context.Context, string, string) error { return nil }
func (p *verifyProvisioner) VerifyTemplate(_ context.Context, db string, _ *domain.CHTemplate) error {
	p.verifiedDB = db
	return p.verifyErr
}

// defaultTeamRepo — nopTeamRepo с GetBySlug, возвращающим ch_database.
type defaultTeamRepo struct {
	nopTeamRepo
	chDatabase string
}

func (r defaultTeamRepo) GetBySlug(context.Context, string) (*domain.Team, error) {
	return &domain.Team{ID: "t1", Slug: "default", CHDatabase: r.chDatabase}, nil
}

func newCHTemplateUC(repo *memCHTemplateRepo, prov *verifyProvisioner) *CHTemplateUsecase {
	audit := NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop())
	teams := defaultTeamRepo{chDatabase: "nexus_default"}
	if prov == nil {
		return NewCHTemplateUsecase(repo, nil, teams, audit, logging.NewNoop())
	}
	return NewCHTemplateUsecase(repo, prov, teams, audit, logging.NewNoop())
}

func validTemplate(name string, isDefault bool) *domain.CHTemplate {
	return &domain.CHTemplate{Name: name, Spec: domain.DefaultCHTemplateSpec(), IsDefault: isDefault}
}

func TestCHTemplateUC_Create_OK(t *testing.T) {
	t.Parallel()
	repo := newMemCHTemplateRepo()
	uc := newCHTemplateUC(repo, nil)

	err := uc.Create(context.Background(), SystemActor(), validTemplate("Logs A", false))
	require.NoError(t, err)
	assert.Len(t, repo.items, 1)
}

func TestCHTemplateUC_Create_InvalidSpec(t *testing.T) {
	t.Parallel()
	repo := newMemCHTemplateRepo()
	uc := newCHTemplateUC(repo, nil)

	bad := validTemplate("Bad", false)
	bad.Spec.Engine = "ReplacingMergeTree"
	err := uc.Create(context.Background(), SystemActor(), bad)
	assert.ErrorIs(t, err, domain.ErrCHTemplateInvalidEngine)
	assert.Empty(t, repo.items, "invalid template must not be persisted")
}

func TestCHTemplateUC_Create_SwitchesDefault(t *testing.T) {
	t.Parallel()
	repo := newMemCHTemplateRepo()
	uc := newCHTemplateUC(repo, nil)
	ctx := context.Background()

	require.NoError(t, uc.Create(ctx, SystemActor(), validTemplate("First", true)))
	require.NoError(t, uc.Create(ctx, SystemActor(), validTemplate("Second", true)))

	defaults := 0
	for _, t2 := range repo.items {
		if t2.IsDefault {
			defaults++
		}
	}
	assert.Equal(t, 1, defaults, "only one default after switch")
}

func TestCHTemplateUC_Delete_Default(t *testing.T) {
	t.Parallel()
	repo := newMemCHTemplateRepo()
	uc := newCHTemplateUC(repo, nil)
	ctx := context.Background()
	tpl := validTemplate("Def", true)
	require.NoError(t, uc.Create(ctx, SystemActor(), tpl))

	err := uc.Delete(ctx, SystemActor(), tpl.ID)
	assert.ErrorIs(t, err, domain.ErrCHTemplateDefaultImmutable)
}

func TestCHTemplateUC_Delete_InUse(t *testing.T) {
	t.Parallel()
	repo := newMemCHTemplateRepo()
	uc := newCHTemplateUC(repo, nil)
	ctx := context.Background()
	tpl := validTemplate("Used", false)
	require.NoError(t, uc.Create(ctx, SystemActor(), tpl))
	repo.nodesUsing[tpl.ID] = 3

	err := uc.Delete(ctx, SystemActor(), tpl.ID)
	assert.ErrorIs(t, err, domain.ErrCHTemplateInUse)
}

func TestCHTemplateUC_Verify_NilProvisioner(t *testing.T) {
	t.Parallel()
	uc := newCHTemplateUC(newMemCHTemplateRepo(), nil)
	err := uc.Verify(context.Background(), validTemplate("V", false))
	assert.ErrorIs(t, err, ErrCHUnavailable)
}

func TestCHTemplateUC_Verify_CallsProvisioner(t *testing.T) {
	t.Parallel()
	prov := &verifyProvisioner{}
	uc := newCHTemplateUC(newMemCHTemplateRepo(), prov)

	require.NoError(t, uc.Verify(context.Background(), validTemplate("V", false)))
	assert.Equal(t, "nexus_default", prov.verifiedDB, "verify must target default team db")
}
