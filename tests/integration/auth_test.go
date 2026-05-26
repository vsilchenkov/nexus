//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	webredis "nexus/internal/web/adapter/out/redis"
	webuc "nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// TestAuth_Login_E2E прогоняет полный цикл аутентификации:
//
//	UserRepo (PG) + SessionRepo (Redis) + AuditRepo (PG) → AuthUsecase.
//
// Сценарии в одном тесте, чтобы не платить за два testcontainer-bootstrap'а:
//  1. login c неправильным паролем → ErrUnauthorized + audit 'user.login.failed';
//  2. login с правильным паролем → token + audit 'user.login.success';
//  3. Check(token) валидирует и продлевает TTL;
//  4. ChangePassword инвалидирует ВСЕ сессии пользователя (forced re-login);
//  5. login c inactive=false → ErrUserInactive.
//
// Покрывает §7.1 / §7.9 / §7.13 ТЗ.
func TestAuth_Login_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	pool, pgCleanup := startPostgres(t, ctx)
	defer pgCleanup()

	redisClient, redisCleanup := startRedis(t, ctx)
	defer redisCleanup()

	logger := logging.NewNoop()
	userRepo := pgrepo.NewUserRepoPg(pool, logger)
	sessionRepo := webredis.NewSessionRepoRedis(redisClient)
	auditRepo := pgrepo.NewAuditRepoPg(pool, logger)
	auditUC := webuc.NewAuditUsecase(auditRepo, logger)
	authUC := webuc.NewAuthUsecase(userRepo, sessionRepo, auditUC, time.Hour, logger)

	// Создаём пользователя через UserRepo напрямую — Web-usecase для creation
	// тестируется в unit'ах; здесь интересна вся цепочка login.
	const password = "S3cretPwd!"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)

	u := &domain.User{
		Login:        "alice",
		Email:        "alice@example.com",
		PasswordHash: string(hash),
		Role:         domain.UserRoleAdmin,
		Active:       true,
		Lang:         domain.UserLangEN,
		TeamID:       "default",
	}
	require.NoError(t, userRepo.Create(ctx, u))
	require.NotEmpty(t, u.ID)

	// (1) Неправильный пароль.
	_, _, err = authUC.Login(ctx, "alice", "wrong-password", "127.0.0.1")
	require.ErrorIs(t, err, domain.ErrUnauthorized)

	// (2) Правильный.
	token, gotUser, err := authUC.Login(ctx, "alice", password, "127.0.0.1")
	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.Equal(t, u.ID, gotUser.ID)
	require.Equal(t, domain.UserRoleAdmin, gotUser.Role)

	// (3) Check продлевает TTL — сессия должна вернуться с тем же UserID/Role.
	s, err := authUC.Check(ctx, token)
	require.NoError(t, err)
	require.Equal(t, u.ID, s.UserID)
	require.Equal(t, domain.UserRoleAdmin, s.Role)

	// (4) ChangePassword выкидывает все сессии этого пользователя.
	// Создаём вторую сессию, чтобы убедиться что инвалидируются обе.
	token2, _, err := authUC.Login(ctx, "alice", password, "127.0.0.1")
	require.NoError(t, err)
	require.NotEqual(t, token, token2)

	require.NoError(t, authUC.ChangePassword(ctx,
		webuc.Actor{UserID: u.ID, UserLogin: u.Login, IPAddress: "127.0.0.1"},
		u.ID, "NewSecret!42", false))

	_, err = authUC.Check(ctx, token)
	require.ErrorIs(t, err, domain.ErrSessionNotFound, "old session must be revoked")
	_, err = authUC.Check(ctx, token2)
	require.ErrorIs(t, err, domain.ErrSessionNotFound, "second session must be revoked too")

	// Со старым паролем больше не зайти.
	_, _, err = authUC.Login(ctx, "alice", password, "127.0.0.1")
	require.ErrorIs(t, err, domain.ErrUnauthorized)

	// С новым — заходим.
	tokenNew, _, err := authUC.Login(ctx, "alice", "NewSecret!42", "127.0.0.1")
	require.NoError(t, err)
	require.NotEmpty(t, tokenNew)
	require.NoError(t, authUC.Logout(ctx, tokenNew))
	_, err = authUC.Check(ctx, tokenNew)
	require.ErrorIs(t, err, domain.ErrSessionNotFound, "logout must remove session")

	// (5) Inactive пользователь.
	inactive := &domain.User{
		Login:        "bob",
		PasswordHash: string(hash),
		Role:         domain.UserRoleViewer,
		Active:       false,
		Lang:         domain.UserLangEN,
		TeamID:       "default",
	}
	require.NoError(t, userRepo.Create(ctx, inactive))
	_, _, err = authUC.Login(ctx, "bob", password, "127.0.0.1")
	require.ErrorIs(t, err, domain.ErrUserInactive)

	// Audit: должно быть несколько login.success + login.failed.
	entries, err := auditRepo.List(ctx, port.AuditFilter{UserID: u.ID, Limit: 50})
	require.NoError(t, err)
	// Counters по action:
	var (
		success, failed, pwdChange int
	)
	for _, e := range entries {
		switch e.Action {
		case domain.ActionUserLogin:
			success++
		case domain.ActionUserLoginFailed:
			failed++
		case domain.ActionUserPassword:
			pwdChange++
		}
	}
	require.GreaterOrEqual(t, success, 2, "should record successful logins for alice")
	require.GreaterOrEqual(t, failed, 1, "should record at least one failed login")
	require.Equal(t, 1, pwdChange, "exactly one password change recorded")

	// Inactive-попытка пишется в audit с TargetID=inactive.ID, не u.ID.
	bobAudit, err := auditRepo.List(ctx, port.AuditFilter{UserID: inactive.ID, Limit: 10})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(bobAudit), 1)
	require.Equal(t, domain.ActionUserLoginFailed, bobAudit[0].Action)
	// Reason "inactive" должен попасть в Details.
	require.Equal(t, "inactive", bobAudit[0].Details["reason"])
}
