package rabbitmq

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	"nexus/internal/receiver/usecase"
)

// NodeLister реализует usecase.PullNodeLister: отдаёт узлы RabbitMQAsync с
// полным конфигом (включая расшифрованные rmq_password/auth_credentials,
// которые нужны воркеру для AMQP-подключения и исходящей авторизации). Обычный
// nodecache.Reader не годится — он не тянет rmq_*-колонки.
type NodeLister struct {
	pg     *pgxpool.Pool
	cipher *crypto.Cipher
	logger logging.Logger
}

var _ usecase.PullNodeLister = (*NodeLister)(nil)

func NewNodeLister(pg *pgxpool.Pool, cipher *crypto.Cipher, logger logging.Logger) *NodeLister {
	return &NodeLister{pg: pg, cipher: cipher, logger: logger}
}

const selectPullNodes = `
SELECT
	n.id, n.path, n.root_method,
	n.url_mode, n.target_url, n.url_param_name,
	n.auth_type, n.auth_credentials,
	n.forward_headers, n.status,
	n.rmq_host, n.rmq_port, n.rmq_vhost, n.rmq_user, n.rmq_password, n.rmq_queue, n.rmq_use_tls,
	n.pull_interval_sec, n.pull_batch_size, n.pull_prefetch,
	n.updated_at
FROM nodes n
WHERE n.root_method = 'RabbitMQAsync' AND n.status <> 'disabled'`

// ListRabbitMQAsync возвращает активные (не disabled) узлы RabbitMQAsync.
// paused-узлы тоже возвращаются: §3.6 — для них воркер не должен публиковать?
// В v1 paused-узел не забирается (его пропускает Sender как async paused). Но
// чтобы не плодить дубли логики, paused-узлы исключаем из поллинга на уровне
// менеджера — см. ниже фильтр по статусу.
func (l *NodeLister) ListRabbitMQAsync(ctx context.Context) ([]*domain.Node, error) {
	rows, err := l.pg.Query(ctx, selectPullNodes)
	if err != nil {
		return nil, fmt.Errorf("list rabbitmq nodes: %w", err)
	}
	defer rows.Close()

	var nodes []*domain.Node
	for rows.Next() {
		var n domain.Node
		var rootMethod, urlMode, authType, status string
		var encAuth, encRMQ string
		var updated time.Time
		if err := rows.Scan(
			&n.ID, &n.Path, &rootMethod,
			&urlMode, &n.TargetURL, &n.URLParamName,
			&authType, &encAuth,
			&n.ForwardHeaders, &status,
			&n.RMQHost, &n.RMQPort, &n.RMQVHost, &n.RMQUser, &encRMQ, &n.RMQQueue, &n.RMQUseTLS,
			&n.PullIntervalSec, &n.PullBatchSize, &n.PullPrefetch,
			&updated,
		); err != nil {
			return nil, fmt.Errorf("scan rabbitmq node: %w", err)
		}
		// paused-узлы не поллим в v1 (источник сообщений останавливается).
		if status == string(domain.NodeStatusPaused) {
			continue
		}
		n.RootMethod = domain.RootMethod(rootMethod)
		n.URLMode = domain.URLMode(urlMode)
		n.AuthType = domain.AuthType(authType)
		n.Status = domain.NodeStatus(status)
		n.UpdatedAt = updated
		if n.ForwardHeaders == nil {
			n.ForwardHeaders = []string{}
		}
		n.AuthCredentials, err = l.cipher.Decrypt(encAuth)
		if err != nil {
			return nil, fmt.Errorf("decrypt auth for %s: %w", n.Path, err)
		}
		n.RMQPassword, err = l.cipher.Decrypt(encRMQ)
		if err != nil {
			return nil, fmt.Errorf("decrypt rmq for %s: %w", n.Path, err)
		}
		nodes = append(nodes, &n)
	}
	return nodes, rows.Err()
}
