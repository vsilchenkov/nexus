package usecase

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

func rotateUC(repo *inMemAPITokenRepo, audit *stubAuditRepo) (*APITokenUsecase, *userRepoStub) {
	users := &userRepoStub{byID: map[string]*domain.User{"u-1": {ID: "u-1", Active: true}}}
	return NewAPITokenUsecase(repo, users, memberOf("u-1", "team-1"),
		NewAuditUsecase(audit, logging.NewNoop()), logging.NewNoop()), users
}

// TestAPITokenUsecase_Rotate_HappyPath: перевыпуск активного токена — новое
// значение авторизует, старое перестаёт; поля (id/name/scopes/team) сохранены;
// пишется audit api_token.rotate.
func TestAPITokenUsecase_Rotate_HappyPath(t *testing.T) {
	t.Parallel()
	repo := newInMemAPITokenRepo()
	auditRepo := &stubAuditRepo{}
	uc, _ := rotateUC(repo, auditRepo)
	ctx := context.Background()

	created, err := uc.Create(ctx, Actor{}, "u-1", "team-1", "prod",
		[]string{domain.ScopeLogsRead}, nil)
	require.NoError(t, err)
	oldPlain := created.Plain
	oldID := created.Token.ID

	newPlain, err := uc.Rotate(ctx, Actor{UserLogin: "alice"}, oldID, "u-1")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(newPlain, APITokenPrefix))
	assert.NotEqual(t, oldPlain, newPlain, "значение должно смениться")

	// Старое значение больше не авторизует, новое — авторизует тот же токен.
	_, _, err = uc.Verify(ctx, oldPlain)
	assert.ErrorIs(t, err, domain.ErrUnauthorized, "старый секрет должен потерять силу")
	gotTok, _, err := uc.Verify(ctx, newPlain)
	require.NoError(t, err)
	assert.Equal(t, oldID, gotTok.ID, "id/имя/scopes сохраняются — это тот же токен")
	assert.Equal(t, "prod", gotTok.Name)
	assert.Equal(t, []string{domain.ScopeLogsRead}, gotTok.Scopes)
	assert.Equal(t, newPlain[:8], gotTok.Prefix, "prefix обновлён под новое значение")

	require.NotEmpty(t, auditRepo.entries)
	last := auditRepo.entries[len(auditRepo.entries)-1]
	assert.Equal(t, domain.ActionAPITokenRotate, last.Action)
	assert.Equal(t, oldID, last.TargetID)
}

// TestAPITokenUsecase_Rotate_Revoked_NotFound: отозванный токен не ротируется —
// для него нужно создать новый.
func TestAPITokenUsecase_Rotate_Revoked_NotFound(t *testing.T) {
	t.Parallel()
	repo := newInMemAPITokenRepo()
	uc, _ := rotateUC(repo, &stubAuditRepo{})
	ctx := context.Background()

	created, err := uc.Create(ctx, Actor{}, "u-1", "team-1", "t", []string{domain.ScopeLogsRead}, nil)
	require.NoError(t, err)
	now := time.Now()
	created.Token.RevokedAt = &now

	_, err = uc.Rotate(ctx, Actor{}, created.Token.ID, "u-1")
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

// TestAPITokenUsecase_Rotate_Expired_NotFound: просроченный токен не ротируется.
func TestAPITokenUsecase_Rotate_Expired_NotFound(t *testing.T) {
	t.Parallel()
	repo := newInMemAPITokenRepo()
	uc, _ := rotateUC(repo, &stubAuditRepo{})
	ctx := context.Background()

	created, err := uc.Create(ctx, Actor{}, "u-1", "team-1", "t", []string{domain.ScopeLogsRead}, nil)
	require.NoError(t, err)
	past := time.Now().Add(-time.Hour)
	created.Token.ExpiresAt = &past

	_, err = uc.Rotate(ctx, Actor{}, created.Token.ID, "u-1")
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

// TestAPITokenUsecase_Rotate_ForeignOwner_NotFound: чужой токен ротировать нельзя
// (скоуп по user_id) — не канал доступа к чужим токенам.
func TestAPITokenUsecase_Rotate_ForeignOwner_NotFound(t *testing.T) {
	t.Parallel()
	repo := newInMemAPITokenRepo()
	uc, _ := rotateUC(repo, &stubAuditRepo{})
	ctx := context.Background()

	created, err := uc.Create(ctx, Actor{}, "u-1", "team-1", "t", []string{domain.ScopeLogsRead}, nil)
	require.NoError(t, err)

	_, err = uc.Rotate(ctx, Actor{}, created.Token.ID, "u-2")
	assert.ErrorIs(t, err, domain.ErrNotFound)
}
