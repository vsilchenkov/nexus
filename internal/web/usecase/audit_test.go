package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"bus/internal/domain"
	"bus/internal/platform/logging"
	"bus/internal/web/usecase/port"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errAuditRepo возвращает заданную ошибку из Write — для проверки, что
// AuditUsecase.Log не пробрасывает её наверх (§7.13: «сбой аудита не
// должен ломать бизнес-операцию»). Read-операции не нужны.
type errAuditRepo struct{ writeErr error }

func (r *errAuditRepo) Write(_ context.Context, _ *domain.AuditEntry) error { return r.writeErr }
func (r *errAuditRepo) List(_ context.Context, _ port.AuditFilter) ([]*domain.AuditEntry, error) {
	return nil, nil
}
func (r *errAuditRepo) DeleteOlderThan(_ context.Context, _ time.Time) (int, error) {
	return 0, nil
}

func TestSystemActor(t *testing.T) {
	t.Parallel()

	a := SystemActor()
	assert.Equal(t, "system", a.UserLogin)
	assert.Empty(t, a.UserID)
	assert.Empty(t, a.IPAddress)
}

func TestAuditUsecase_Log_StoresEntry(t *testing.T) {
	t.Parallel()

	repo := &stubAuditRepo{}
	uc := NewAuditUsecase(repo, logging.NewNoop())

	actor := Actor{UserID: "u-1", UserLogin: "vasya", IPAddress: "10.0.0.1"}
	before := time.Now().Add(-time.Second)

	uc.Log(context.Background(), actor, "node.create", "node", "node-42",
		map[string]any{"name": "echo"})

	require.Len(t, repo.entries, 1)
	got := repo.entries[0]

	assert.Equal(t, "u-1", got.UserID)
	assert.Equal(t, "vasya", got.UserLogin)
	assert.Equal(t, "10.0.0.1", got.IPAddress)
	assert.Equal(t, "node.create", got.Action)
	assert.Equal(t, "node", got.TargetType)
	assert.Equal(t, "node-42", got.TargetID)
	assert.Equal(t, "echo", got.Details["name"])
	assert.True(t, got.CreatedAt.After(before),
		"CreatedAt должно быть свежее (got %v, before %v)", got.CreatedAt, before)
	// UTC — обязательно (§14.4).
	assert.Equal(t, "UTC", got.CreatedAt.Location().String())
}

func TestAuditUsecase_Log_NilDetails_NormalizedToEmptyMap(t *testing.T) {
	t.Parallel()

	repo := &stubAuditRepo{}
	uc := NewAuditUsecase(repo, logging.NewNoop())

	uc.Log(context.Background(), SystemActor(), "login.success", "user", "vasya", nil)

	require.Len(t, repo.entries, 1)
	got := repo.entries[0]
	require.NotNil(t, got.Details, "Details после Log не должно быть nil — иначе сломаем JSON-serializer")
	assert.Empty(t, got.Details)
}

func TestAuditUsecase_Log_RepoError_Swallowed(t *testing.T) {
	t.Parallel()

	repo := &errAuditRepo{writeErr: errors.New("connection refused")}
	uc := NewAuditUsecase(repo, logging.NewNoop())

	// Не должно ни паниковать, ни возвращать ошибку (Log имеет void-сигнатуру).
	uc.Log(context.Background(), SystemActor(), "node.update", "node", "n-1", nil)

	// Спецсимвол теста: дойти сюда без панике — уже success.
	// Дополнительно: List также должен спокойно работать.
	out, err := uc.List(context.Background(), port.AuditFilter{})
	assert.NoError(t, err)
	assert.Nil(t, out)
}

func TestAuditUsecase_List_DelegatesToRepo(t *testing.T) {
	t.Parallel()

	repo := &stubAuditRepo{
		entries: []*domain.AuditEntry{
			{ID: "1", Action: "login.success"},
			{ID: "2", Action: "node.create"},
		},
	}
	uc := NewAuditUsecase(repo, logging.NewNoop())

	got, err := uc.List(context.Background(), port.AuditFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "1", got[0].ID)
	assert.Equal(t, "2", got[1].ID)
}

func TestAuditEntry_FactoryProducesUTCAndNonNilDetails(t *testing.T) {
	t.Parallel()

	before := time.Now().UTC().Add(-time.Second)
	e := auditEntry(SystemActor(), "x", "y", "z", nil)

	require.NotNil(t, e)
	require.NotNil(t, e.Details, "details=nil ломает downstream JSON-маршаллинг")
	assert.True(t, e.CreatedAt.After(before))
	assert.Equal(t, "UTC", e.CreatedAt.Location().String())
}
