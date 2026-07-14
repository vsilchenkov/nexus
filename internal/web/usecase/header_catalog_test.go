package usecase

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

// memHeaderRepo — in-memory port.HeaderCatalogRepo для unit-тестов usecase.
// UsageCount выставляется тестом вручную (в проде считается on-read по узлам).
type memHeaderRepo struct {
	items map[string]*domain.HeaderCatalogEntry
	seq   int
}

func newMemHeaderRepo() *memHeaderRepo {
	return &memHeaderRepo{items: map[string]*domain.HeaderCatalogEntry{}}
}

func (r *memHeaderRepo) Search(_ context.Context, _ string, _ int) ([]*domain.HeaderCatalogEntry, error) {
	out := make([]*domain.HeaderCatalogEntry, 0, len(r.items))
	for _, e := range r.items {
		cp := *e
		out = append(out, &cp)
	}
	return out, nil
}

func (r *memHeaderRepo) Get(_ context.Context, id string) (*domain.HeaderCatalogEntry, error) {
	if e, ok := r.items[id]; ok {
		cp := *e
		return &cp, nil
	}
	return nil, domain.ErrHeaderNotFound
}

func (r *memHeaderRepo) GetByName(_ context.Context, name string) (*domain.HeaderCatalogEntry, error) {
	for _, e := range r.items {
		if strings.EqualFold(e.Name, name) {
			cp := *e
			return &cp, nil
		}
	}
	return nil, domain.ErrHeaderNotFound
}

func (r *memHeaderRepo) Create(_ context.Context, e *domain.HeaderCatalogEntry) error {
	for _, x := range r.items {
		if strings.EqualFold(x.Name, e.Name) {
			return domain.ErrHeaderAlreadyExists
		}
	}
	r.seq++
	e.ID = string(rune('a'+r.seq)) + "-hdr"
	cp := *e
	r.items[e.ID] = &cp
	return nil
}

func (r *memHeaderRepo) UpdateName(_ context.Context, id, name string) error {
	e, ok := r.items[id]
	if !ok {
		return domain.ErrHeaderNotFound
	}
	for otherID, x := range r.items {
		if otherID != id && strings.EqualFold(x.Name, name) {
			return domain.ErrHeaderAlreadyExists
		}
	}
	e.Name = name
	return nil
}

func (r *memHeaderRepo) UpdateDescription(_ context.Context, id, description string) error {
	e, ok := r.items[id]
	if !ok {
		return domain.ErrHeaderNotFound
	}
	e.Description = description
	return nil
}

func (r *memHeaderRepo) Delete(_ context.Context, id string) error {
	if _, ok := r.items[id]; !ok {
		return domain.ErrHeaderNotFound
	}
	delete(r.items, id)
	return nil
}

func newHeaderUC(repo *memHeaderRepo) *HeaderCatalogUsecase {
	audit := NewAuditUsecase(&stubAuditRepo{}, logging.NewNoop())
	return NewHeaderCatalogUsecase(repo, audit, logging.NewNoop())
}

func TestHeaderUC_Create_Idempotent(t *testing.T) {
	t.Parallel()
	repo := newMemHeaderRepo()
	uc := newHeaderUC(repo)
	ctx := context.Background()

	e1 := &domain.HeaderCatalogEntry{Name: "X-Trace-Id"}
	require.NoError(t, uc.Create(ctx, SystemActor(), e1))
	require.NotEmpty(t, e1.ID)

	// Повторное создание того же имени (другой регистр) → возвращает существующую.
	e2 := &domain.HeaderCatalogEntry{Name: "x-trace-id"}
	require.NoError(t, uc.Create(ctx, SystemActor(), e2))
	assert.Equal(t, e1.ID, e2.ID, "идемпотентность: тот же ID")
	assert.Len(t, repo.items, 1)
}

func TestHeaderUC_Update_NameGuardedByUsage(t *testing.T) {
	t.Parallel()
	repo := newMemHeaderRepo()
	uc := newHeaderUC(repo)
	ctx := context.Background()

	e := &domain.HeaderCatalogEntry{Name: "X-Trace-Id"}
	require.NoError(t, uc.Create(ctx, SystemActor(), e))
	repo.items[e.ID].UsageCount = 2

	// Переименование при usage>0 запрещено (узлы ссылаются по имени).
	err := uc.Update(ctx, SystemActor(), e.ID, "X-Request-Id", "")
	assert.ErrorIs(t, err, domain.ErrHeaderInUse)
	assert.Equal(t, "X-Trace-Id", repo.items[e.ID].Name, "имя не изменилось")

	// Смена только описания — разрешена даже при usage>0.
	require.NoError(t, uc.Update(ctx, SystemActor(), e.ID, "X-Trace-Id", "trace correlation"))
	assert.Equal(t, "trace correlation", repo.items[e.ID].Description)
}

func TestHeaderUC_Update_RenameCollision(t *testing.T) {
	t.Parallel()
	repo := newMemHeaderRepo()
	uc := newHeaderUC(repo)
	ctx := context.Background()

	a := &domain.HeaderCatalogEntry{Name: "X-A"}
	require.NoError(t, uc.Create(ctx, SystemActor(), a))
	b := &domain.HeaderCatalogEntry{Name: "X-B"}
	require.NoError(t, uc.Create(ctx, SystemActor(), b))

	// Переименование неиспользуемого B в занятое имя A → конфликт.
	err := uc.Update(ctx, SystemActor(), b.ID, "x-a", "")
	assert.ErrorIs(t, err, domain.ErrHeaderAlreadyExists)
}

func TestHeaderUC_Update_InvalidName(t *testing.T) {
	t.Parallel()
	repo := newMemHeaderRepo()
	uc := newHeaderUC(repo)
	ctx := context.Background()

	e := &domain.HeaderCatalogEntry{Name: "X-Ok"}
	require.NoError(t, uc.Create(ctx, SystemActor(), e))

	// Пробел недопустим по RFC 7230 token.
	err := uc.Update(ctx, SystemActor(), e.ID, "X Bad", "")
	assert.ErrorIs(t, err, domain.ErrHeaderNameFormat)
}

func TestHeaderUC_Update_NotFound(t *testing.T) {
	t.Parallel()
	uc := newHeaderUC(newMemHeaderRepo())
	err := uc.Update(context.Background(), SystemActor(), "missing", "X-New", "")
	assert.ErrorIs(t, err, domain.ErrHeaderNotFound)
}

func TestHeaderUC_Delete_GuardedByUsage(t *testing.T) {
	t.Parallel()
	repo := newMemHeaderRepo()
	uc := newHeaderUC(repo)
	ctx := context.Background()

	e := &domain.HeaderCatalogEntry{Name: "X-Trace-Id"}
	require.NoError(t, uc.Create(ctx, SystemActor(), e))
	repo.items[e.ID].UsageCount = 1
	assert.ErrorIs(t, uc.Delete(ctx, SystemActor(), e.ID), domain.ErrHeaderInUse)

	repo.items[e.ID].UsageCount = 0
	require.NoError(t, uc.Delete(ctx, SystemActor(), e.ID))
	assert.Empty(t, repo.items)
}
