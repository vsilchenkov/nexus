package user

import (
	"bus/app/internal/lib/caching"
	"bus/app/internal/lib/logging"
	"bus/app/internal/models"
	"bus/app/internal/storage"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	ErrFailedToCreateUser     = "failed to create user"
	ErrUserNotFound           = "user not found"
	ErrFailedToHashPassword   = "failed to hash password"
	ErrFailedPassword         = "failed password"
	ErrFailedToChangePassword = "failed to change password"
)

const prefixCacheKey = "bus:repository:user"

type UserRepository struct {
	db     storage.DB
	cacher caching.Cacher
	logger logging.Logger
}

type User struct {
	ID           int              `db:"id"`
	Username     string           `db:"username"`
	PasswordHash sql.Null[string] `db:"password_hash"`
	Role         models.Role      `db:"role"`
	CreatedAt    time.Time        `db:"created_at"`
}

func NewUserRepository(db storage.DB, cacher caching.Cacher, logger logging.Logger) *UserRepository {

	return &UserRepository{
		db:     db,
		cacher: cacher,
		logger: logger,
	}
}

func (u UserRepository) CreateUser(ctx context.Context, username, password string, role models.Role) (int, error) {

	const op = "repository.user.CreateUser"

	if !role.IsValid() {
		return 0, errors.New(models.ErrUnknownRole)
	}

	ctxDB, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	hash, err := HashPassword(password)
	if err != nil {
		u.logger.ErrorWithOp(ErrFailedToHashPassword, err, op,
			u.logger.Str("username", username))
		return 0, errors.New(ErrFailedToHashPassword)
	}

	var query string
	if u.db.IsPostgres() {
		query = `INSERT INTO users (username, password_hash, role) 
				VALUES (?, ?, ?)
				RETURNING id`
	} else {
		query = `INSERT INTO users (username, password_hash, role) 
                 OUTPUT INSERTED.id
                 VALUES (?, ?, ?)`
	}

	var id int
	err = u.db.QueryRowContext(ctxDB, query, username, hash, role).Scan(&id)
	if err != nil {
		u.logger.ErrorWithOp(ErrFailedToCreateUser, err, op,
			u.logger.Str("username", username))
		return 0, errors.New(ErrFailedToCreateUser)
	}

	return id, nil

}

func (u UserRepository) GetUserByName(ctx context.Context, username string) (*User, error) {

	const op = "repository.user.GetUserByName"

	user := &User{}

	// cacher
	key := fmt.Sprintf("%s:%s", prefixCacheKey, username)
	found, _ := u.cacher.Get(ctx, key, user)
	if found {
		return user, nil
	}

	ctxDB, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	query := `SELECT id, username, password_hash, role, created_at 
				FROM users 
				WHERE username = ?`

	err := u.db.QueryRowContext(ctxDB, query, username).Scan(&user.ID, &user.Username, &user.PasswordHash, &user.Role, &user.CreatedAt)
	if err != nil {
		u.logger.ErrorWithOp(ErrUserNotFound, err, op,
			u.logger.Str("username", username))
		return nil, errors.New(ErrUserNotFound)
	}

	u.cacher.Set(ctx, key, user, time.Duration(24)*time.Hour)
	return user, nil
}

func (u UserRepository) VerifyUser(ctx context.Context, username, password string) (int, *models.Role, error) {

	const op = "repository.user.CreateUser"

	user, err := u.GetUserByName(ctx, username)
	if err != nil {
		return 0, nil, err
	}

	if !user.PasswordHash.Valid {
		return 0, nil, errors.New("password not set, please change password")
	}

	if !CheckPasswordHash(password, user.PasswordHash.V) {
		return 0, nil, errors.New(ErrFailedPassword)
	}

	return user.ID, &user.Role, nil
}

func (u UserRepository) ChangeUserPassword(ctx context.Context, username, password string) error {

	const op = "repository.user.ChangeUserPassword"

	hash, err := HashPassword(password)
	if err != nil {
		u.logger.ErrorWithOp(ErrFailedToHashPassword, err, op,
			u.logger.Str("username", username))
		return errors.New(ErrFailedToHashPassword)
	}

	ctxDB, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	query := `UPDATE users
				SET password_hash = ? 
				WHERE username = ?`

	result, err := u.db.ExecContext(ctxDB, query, hash, username)
	if err != nil {
		u.logger.ErrorWithOp(ErrFailedToChangePassword, err, op,
			u.logger.Str("username", username))
		return errors.New(ErrFailedToChangePassword)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		u.logger.ErrorWithOp(ErrFailedToChangePassword, err, op,
			u.logger.Str("username", username))
		return errors.New(ErrFailedToChangePassword)
	}

	if rows == 0 {
		u.logger.ErrorWithOp(ErrUserNotFound, err, op,
			u.logger.Str("username", username))
		return errors.New(ErrUserNotFound)
	}

	u.clearCache(ctx)
	return nil
}

func (u UserRepository) clearCache(ctx context.Context) {
	u.cacher.ClearByPrefix(ctx, prefixCacheKey)
}

func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

func CheckPasswordHash(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}
