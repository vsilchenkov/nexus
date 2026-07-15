package port

import (
	"context"

	"nexus/internal/domain"
)

// ServiceLogReader — чтение хвоста служебных логов одного сервиса из общего
// хранилища (§51.5; реализация — Redis LRANGE nexus:logs:<service>).
// Записи возвращаются от новейшей к старейшей.
type ServiceLogReader interface {
	Tail(ctx context.Context, service string, limit int) ([]domain.ServiceLogEntry, error)
}
