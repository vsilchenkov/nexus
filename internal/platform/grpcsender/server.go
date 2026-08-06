package grpcsender

import (
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"nexus/internal/platform/config"
)

// ServerKeepalivePolicy — enforcement-политика keepalive для gRPC-СЕРВЕРА
// Sender'а (§82.1). Отдаётся как готовая опция `grpc.NewServer`.
//
// Зачем она обязательна. Без явной политики grpc-go считает допустимым интервал
// между ping'ами клиента в 5 минут, а клиенты из [New] пингуют каждые
// `keepalive_time_sec` (боевое значение — 30 секунд). Каждый ping сверх политики
// сервер засчитывает нарушением и на третьем шлёт
// `GOAWAY ENHANCE_YOUR_CALM / "too_many_pings"`, **закрывая соединение целиком**
// — вместе со всеми идущими по нему вызовами.
//
// В unary-RPC сервер до самого ответа не пишет в стрим ничего, поэтому счётчик
// нарушений во время долгого вызова растёт беспрепятственно. Замеренный потолок
// sync-запроса до §82: 121 секунда напрямую в Receiver и 91 секунда через Web
// (плавает, потому что ping-таймер привязан к соединению пула, а не к запросу).
// `timeout_ms` узла на это не влияет никак, а в журнале исход выглядел уходом
// вызывающей стороны — см. godoc domain.ReasonClientCanceled.
//
// Согласованность с клиентом гарантирует config.Validate: политика не может
// быть строже, чем `keepalive_time_sec` любого из клиентов.
func ServerKeepalivePolicy(cfg *config.SenderGRPCKeepaliveConfig) grpc.ServerOption {
	return grpc.KeepaliveEnforcementPolicy(enforcementPolicy(cfg))
}

// enforcementPolicy — чистый перевод конфига в термины grpc-go.
//
// Отделён от [ServerKeepalivePolicy], потому что `grpc.ServerOption`
// непрозрачна: применённую политику из неё не прочитать, и проверить перевод
// (в первую очередь инверсию флага) можно только на этом уровне.
func enforcementPolicy(cfg *config.SenderGRPCKeepaliveConfig) keepalive.EnforcementPolicy {
	return keepalive.EnforcementPolicy{
		MinTime: time.Duration(cfg.EnforcementMinTimeSec) * time.Second,
		// Инверсия флага — из конфига (см. godoc SenderGRPCKeepaliveConfig):
		// разрешать ping без активных стримов нужно почти всегда, иначе
		// простаивающее соединение копит нарушения «в кредит» (порог для таких
		// ping'ов — 2 часа) и первый же долгий запрос получает GOAWAY сразу.
		PermitWithoutStream: !cfg.DenyPingWithoutStream,
	}
}
