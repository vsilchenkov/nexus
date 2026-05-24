package user_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"bus/app/internal/models"
	"bus/app/internal/storage/repository/user"
	"bus/app/internal/testutil"

	"github.com/stretchr/testify/assert"
	"golang.org/x/crypto/bcrypt"
)

// MockResult - мок-реализация sql.Result
type MockResult struct {
	rowsAffected int64
	lastInsertId int64
}

func (m *MockResult) RowsAffected() (int64, error) {
	return m.rowsAffected, nil
}

func (m *MockResult) LastInsertId() (int64, error) {
	return m.lastInsertId, nil
}

// MockDB - мок-реализация storage.DB
type MockDB struct {
	QueryRowContextFunc func(ctx context.Context, query string, args ...any) *sql.Row
	QueryContextFunc    func(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContextFunc     func(ctx context.Context, query string, args ...any) (sql.Result, error)
	PingContextFunc     func(ctx context.Context) error
	CloseFunc           func() error
	IsPostgresFunc      func() bool
	IsMSSqlFunc         func() bool
	GetDBFunc           func() *sql.DB
	DBTypeFunc          func() string
}

func (m *MockDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if m.QueryRowContextFunc != nil {
		return m.QueryRowContextFunc(ctx, query, args...)
	}
	return nil
}

func (m *MockDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if m.QueryContextFunc != nil {
		return m.QueryContextFunc(ctx, query, args...)
	}
	return nil, nil
}

func (m *MockDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if m.ExecContextFunc != nil {
		return m.ExecContextFunc(ctx, query, args...)
	}
	return &MockResult{rowsAffected: 1}, nil
}

func (m *MockDB) PingContext(ctx context.Context) error {
	if m.PingContextFunc != nil {
		return m.PingContextFunc(ctx)
	}
	return nil
}

func (m *MockDB) Close() error {
	if m.CloseFunc != nil {
		return m.CloseFunc()
	}
	return nil
}

func (m *MockDB) IsPostgres() bool {
	if m.IsPostgresFunc != nil {
		return m.IsPostgresFunc()
	}
	return true // по умолчанию postgres
}

func (m *MockDB) IsMSSql() bool {
	if m.IsMSSqlFunc != nil {
		return m.IsMSSqlFunc()
	}
	return false
}

func (m *MockDB) GetDB() *sql.DB {
	if m.GetDBFunc != nil {
		return m.GetDBFunc()
	}
	return nil
}

func (m *MockDB) DBType() string {
	if m.DBTypeFunc != nil {
		return m.DBTypeFunc()
	}
	return "postgres"
}

// MockCacher - мок-реализация caching.Cacher
type MockCacher struct {
	GetFunc           func(ctx context.Context, key string, dest any) (bool, error)
	SetFunc           func(ctx context.Context, key string, value any, expire time.Duration) error
	IncrFunc          func(ctx context.Context, key string, expire time.Duration) (int64, error)
	ClearFunc         func(ctx context.Context) error
	ClearByPrefixFunc func(ctx context.Context, prefix string) error
}

func (m *MockCacher) Get(ctx context.Context, key string, dest any) (bool, error) {
	if m.GetFunc != nil {
		return m.GetFunc(ctx, key, dest)
	}
	return false, nil
}

func (m *MockCacher) Set(ctx context.Context, key string, value any, expire time.Duration) error {
	if m.SetFunc != nil {
		return m.SetFunc(ctx, key, value, expire)
	}
	return nil
}

func (m *MockCacher) Incr(ctx context.Context, key string, expire time.Duration) (int64, error) {
	if m.IncrFunc != nil {
		return m.IncrFunc(ctx, key, expire)
	}
	return 0, nil
}

func (m *MockCacher) Clear(ctx context.Context) error {
	if m.ClearFunc != nil {
		return m.ClearFunc(ctx)
	}
	return nil
}

func (m *MockCacher) ClearByPrefix(ctx context.Context, prefix string) error {
	if m.ClearByPrefixFunc != nil {
		return m.ClearByPrefixFunc(ctx, prefix)
	}
	return nil
}

func TestUserRepository_CreateUser(t *testing.T) {
	logger := &testutil.TestLogger{}
	mockDB := &MockDB{}
	mockCacher := &MockCacher{}

	repo := user.NewUserRepository(mockDB, mockCacher, logger)

	t.Run("should return error for invalid role", func(t *testing.T) {
		ctx := context.Background()

		userID, err := repo.CreateUser(ctx, "testuser", "password", "invalid_role")

		assert.Error(t, err)
		assert.Equal(t, 0, userID)
		assert.Contains(t, err.Error(), models.ErrUnknownRole)
	})
}

func TestHashPassword(t *testing.T) {
	password := "testpassword"

	hash, err := user.HashPassword(password)

	assert.NoError(t, err)
	assert.NotEmpty(t, hash)

	// Проверяем, что хеш действительно действителен для исходного пароля
	err = bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	assert.NoError(t, err)
}

func TestCheckPasswordHash(t *testing.T) {
	password := "testpassword"
	wrongPassword := "wrongpassword"

	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)

	t.Run("should return true for correct password", func(t *testing.T) {
		result := user.CheckPasswordHash(password, string(hash))
		assert.True(t, result)
	})

	t.Run("should return false for incorrect password", func(t *testing.T) {
		result := user.CheckPasswordHash(wrongPassword, string(hash))
		assert.False(t, result)
	})
}
