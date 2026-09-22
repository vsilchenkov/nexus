package netbios

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"nexus/internal/platform/logging"
)

// readBufSize вмещает ответ с максимальной таблицей имён (NUM_NAMES = 255:
// 12 + 34 + 10 + 1 + 255×18 + 46 = 4693 байта) с запасом.
const readBufSize = 8192

// Config — параметры клиента (из sender.rdns.netbios, §106.5).
type Config struct {
	Port    int           // UDP-порт NetBIOS Name Service, обычно 137
	Timeout time.Duration // таймаут одного запроса; клиент владеет этим дедлайном
}

// Client шлёт запросы NBSTAT. Потокобезопасен; своих горутин не создаёт.
type Client struct {
	cfg    Config
	logger logging.Logger
	// txSeq — счётчик номеров транзакций. Номер лишь отличает наш ответ от
	// чужой датаграммы на том же сокете, секретом он не является.
	txSeq atomic.Uint32
}

// Option — функциональная опция конструктора Client.
type Option func(*Client)

// WithTransactionSeed задаёт начальный номер транзакции (детерминизм в
// тестах). Первый запрос получит seed+1.
func WithTransactionSeed(seed uint16) Option {
	return func(c *Client) { c.txSeq.Store(uint32(seed)) }
}

// New создаёт клиента. Начальный номер транзакции случайный: иначе все
// реплики начинали бы с одного номера.
func New(cfg Config, logger logging.Logger, opts ...Option) *Client {
	c := &Client{cfg: cfg, logger: logger}
	c.txSeq.Store(rand.Uint32())
	for _, o := range opts {
		o(c)
	}
	return c
}

// Hostname возвращает имя компьютера по NBSTAT (WorkstationName) или "",
// если подходящего имени в таблице нет. Ошибка — хост не ответил или ответ
// негоден; резолвер трактует её как «имени нет».
func (c *Client) Hostname(ctx context.Context, ip string) (string, error) {
	names, err := c.NodeStatus(ctx, ip)
	if err != nil {
		return "", err
	}
	name, ok := WorkstationName(names)
	if !ok {
		// §51.9: хост ответил, но уникального имени <00> в таблице нет.
		c.logger.Debug("netbios: no unique workstation name",
			c.logger.Str("ip", ip), c.logger.Int("names", len(names)))
	}
	return name, nil
}

// NodeStatus шлёт один запрос NBSTAT на ip:Port и ждёт ответа не дольше
// Timeout (или отмены ctx). Датаграммы с чужим номером транзакции
// пропускаются до дедлайна.
func (c *Client) NodeStatus(ctx context.Context, ip string) ([]Name, error) {
	start := time.Now()
	names, err := c.nodeStatus(ctx, ip)
	// §51.9: исход внешнего вызова. Молчащий хост — норма, поэтому Debug.
	attrs := c.logAttrs(ip, len(names), start)
	if err != nil {
		attrs = append(attrs, c.logger.Err(err))
	}
	c.logger.Debug("netbios: node status", attrs...)
	return names, err
}

func (c *Client) nodeStatus(ctx context.Context, ip string) ([]Name, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	var d net.Dialer
	// Подключённый сокет: датаграммы с других адресов отбрасывает ядро, а
	// ICMP port unreachable приходит быстрой ошибкой чтения, а не таймаутом.
	conn, err := d.DialContext(ctx, "udp4", net.JoinHostPort(ip, strconv.Itoa(c.cfg.Port)))
	if err != nil {
		return nil, fmt.Errorf("netbios: dial %s: %w", ip, err)
	}
	defer conn.Close()

	if dl, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(dl); err != nil {
			return nil, fmt.Errorf("netbios: set deadline: %w", err)
		}
	}
	// Отмена ctx снимает блокировку чтения немедленно. Горутины не нужно:
	// AfterFunc сработает только при отмене, stop() снимает регистрацию.
	stop := context.AfterFunc(ctx, func() {
		// Ошибка неинтересна: сокет закрывается следом по defer.
		_ = conn.SetDeadline(time.Now())
	})
	defer stop()

	txID := uint16(c.txSeq.Add(1))
	if _, err := conn.Write(BuildNodeStatusRequest(txID)); err != nil {
		return nil, fmt.Errorf("netbios: send: %w", withCtxErr(ctx, err))
	}
	return c.readResponse(ctx, conn, ip, txID)
}

// readResponse читает датаграммы до ответа с нашим номером транзакции.
func (c *Client) readResponse(ctx context.Context, conn net.Conn, ip string, txID uint16) ([]Name, error) {
	buf := make([]byte, readBufSize)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, fmt.Errorf("netbios: read: %w", withCtxErr(ctx, err))
		}
		names, err := ParseNodeStatusResponse(txID, buf[:n])
		if errors.Is(err, ErrTransactionID) {
			// §51.9: опоздавший ответ на прежний запрос — ждём свой.
			c.logger.Debug("netbios: foreign transaction id, waiting", c.logger.Str("ip", ip))
			continue
		}
		return names, err
	}
}

func (c *Client) logAttrs(ip string, names int, start time.Time) []slog.Attr {
	return []slog.Attr{
		c.logger.Str("ip", ip),
		c.logger.Int("port", c.cfg.Port),
		c.logger.Int("names", names),
		c.logger.Int("duration_ms", int(time.Since(start).Milliseconds())),
	}
}

// withCtxErr добавляет к сетевой ошибке причину из ctx (истёк таймаут
// клиента или отменил вызывающий), чтобы errors.Is(err, context.Canceled)
// работал и после дедлайна, выставленного из AfterFunc.
//
// Дедлайн сокета равен дедлайну ctx, и чтение может упереться в него на
// мгновение раньше, чем таймер контекста пометит ctx истёкшим. Дедлайны
// сокету ставит только этот клиент, поэтому истёкший дедлайн без причины в
// ctx — это наш таймаут.
func withCtxErr(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return fmt.Errorf("%w: %w", cerr, err)
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return fmt.Errorf("%w: %w", context.DeadlineExceeded, err)
	}
	return err
}
