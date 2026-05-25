package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"bus/internal/platform/crypto"
	"bus/internal/platform/logging"
	"bus/internal/web/usecase/port"
)

// UnitOfWorkPg реализует port.UnitOfWork. Запускает pgx-транзакцию,
// собирает транзакционные клоны NodeRepoPg и AuditRepoPg на её Tx и
// передаёт в fn. Коммитит при success, откатывает при ошибке (§17.4 ТЗ).
type UnitOfWorkPg struct {
	pool   *pgxpool.Pool
	cipher *crypto.Cipher
	logger logging.Logger
}

var _ port.UnitOfWork = (*UnitOfWorkPg)(nil)

func NewUnitOfWorkPg(pool *pgxpool.Pool, cipher *crypto.Cipher, logger logging.Logger) *UnitOfWorkPg {
	return &UnitOfWorkPg{pool: pool, cipher: cipher, logger: logger}
}

func (u *UnitOfWorkPg) Execute(ctx context.Context, fn func(ctx context.Context, repos port.Repos) error) error {
	tx, err := u.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("uow begin: %w", err)
	}

	repos := port.Repos{
		Nodes: NewNodeRepoPg(tx, u.cipher, u.logger),
		Audit: NewAuditRepoPg(tx, u.logger),
	}

	if err := fn(ctx, repos); err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil && rbErr != pgx.ErrTxClosed {
			u.logger.Warn("uow rollback failed",
				u.logger.Err(rbErr))
		}
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("uow commit: %w", err)
	}
	return nil
}
