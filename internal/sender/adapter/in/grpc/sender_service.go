// Package grpc — gRPC adapter Sender Service.
// Реализует SenderServiceServer из сгенерированного proto/sender/v1.
package grpc

import (
	"context"
	"maps"
	"strconv"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	"nexus/internal/sender/usecase"
	senderv1 "nexus/proto/sender/v1"
)

// nodeStatusWriteTimeout — предел на запись персистентного исхода узла (§52),
// выполняемую на отвязанном контексте после ответа (§81.2). Best-effort:
// задерживать возврат RPC ради неё нельзя.
const nodeStatusWriteTimeout = 2 * time.Second

// Server реализует senderv1.SenderServiceServer.
type Server struct {
	senderv1.UnimplementedSenderServiceServer
	uc         *usecase.SendUsecase
	logger     logging.Logger
	metrics    *metrics.Metrics
	nodeStatus usecase.NodeStatusWriter // §46: персист «Down» в Redis (Noop без Redis)
}

func NewServer(uc *usecase.SendUsecase, m *metrics.Metrics, nodeStatus usecase.NodeStatusWriter, logger logging.Logger) *Server {
	return &Server{uc: uc, metrics: m, nodeStatus: nodeStatus, logger: logger}
}

func (s *Server) Send(ctx context.Context, req *senderv1.SendRequest) (*senderv1.SendResponse, error) {
	headers := make(map[string]string, len(req.GetHeaders())+1)
	maps.Copy(headers, req.GetHeaders())
	if a := req.GetAuth(); a != nil && a.GetAuthorizationHeader() != "" {
		headers["Authorization"] = a.GetAuthorizationHeader()
	}

	out := s.uc.Send(ctx, usecase.SendInput{
		ID:                 req.GetId(),
		NodePath:           req.GetNodePath(),
		NodeID:             req.GetNodeId(),
		RootMethod:         domain.RootMethodRequest, // sync-путь
		TargetURL:          req.GetTargetUrl(),
		Method:             req.GetMethod(),
		RequestPath:        req.GetRequestPath(),
		Headers:            headers,
		Body:               req.GetBody(),
		TimeoutMs:          req.GetTimeoutMs(),
		RetryCount:         req.GetRetryCount(),
		RetryBackoffMs:     req.GetRetryBackoffMs(),
		ClickHouseTable:    req.GetClickhouseTable(),
		LogRequestBody:     req.GetLogRequestBody(),
		LogResponseBody:    req.GetLogResponseBody(),
		LogHeaders:         req.GetLogHeaders(),
		ClientIP:           req.GetClientIp(),
		LoggingEnabled:     req.GetLoggingEnabled(),
		MaxBodySizeEnabled: req.GetMaxBodySizeEnabled(),
		MaxBodySize:        req.GetMaxBodySize(),
		DryRun:             req.GetDryRun(), // §55
		// §81.3: политику несёт запрос — sync-путь узел из БД не читает.
		// Старый Receiver поля не пришлёт: нули = глобальная политика.
		BreakerThreshold:   req.GetCircuitBreakerThreshold(),
		BreakerCooldownSec: req.GetCircuitBreakerCooldownSec(),
	})

	// §55: тестовый вызов (dry-run из UI) не оставляет следов на узле — ни в
	// метриках, ни в гаудже исхода, ни в персистентном статусе. Иначе неудачный
	// тест покрасил бы живой узел в Down на дашборде и накрутил счётчики
	// ошибок. Сам HTTP-вызов при этом настоящий (см. §55.3).
	if req.GetDryRun() {
		s.logger.Debug("send: dry-run, metrics and node status skipped",
			s.logger.Str("id", req.GetId()),
			s.logger.Int("status", int(out.StatusCode)))
		return &senderv1.SendResponse{
			StatusCode: out.StatusCode,
			Body:       out.Body,
			Headers:    out.Headers,
			Error:      out.Error,
			Timeout:    out.Timeout,
			Attempts:   out.Attempts,
			DurationMs: out.DurationMs,
		}, nil
	}

	// §52: outcome (ok/degraded/down) — для бейджа узла; isErr («любой
	// не-2xx») — прежняя семантика incomplete_total.
	outcome := domain.OutcomeFromStatusCode(out.StatusCode)
	// §46: персистентный исход в Redis (переживает рестарт; Noop без Redis).
	// Идёт ПЕРЕД метриками намеренно (§52-доп): счётчик подряд идущих отказов
	// живёт там же и возвращает эффективный исход — сырой down остаётся
	// degraded, пока порог не набран. Гаудж ниже выставляется по нему, иначе
	// бейдж узла и алерты Prometheus говорили бы про один узел разное.
	// §81.2: контекст отвязан — на sync-пути он умирает вместе с ушедшим
	// клиентом, а go-redis отбрасывает команду с отменённым контекстом ещё в
	// пуле: бейдж узла молча не обновлялся именно тогда, когда это важнее всего.
	effOutcome := outcome
	if s.nodeStatus != nil && !out.ParentGone {
		statusCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nodeStatusWriteTimeout)
		effOutcome = s.nodeStatus.SetLastOutcome(statusCtx, req.GetNodePath(), outcome)
		cancel()
	} else if out.ParentGone {
		// §51.9: тихий пропуск — иначе «почему узел не покраснел» не разобрать.
		s.logger.Debug("send: caller gone, node outcome not updated",
			s.logger.Str("id", req.GetId()),
			s.logger.Str("node", req.GetNodePath()))
	}

	if s.metrics != nil {
		s.metrics.RequestsTotal.
			WithLabelValues("request", req.GetNodePath(), strconv.FormatInt(int64(out.StatusCode), 10)).Inc()
		s.metrics.RequestDuration.
			WithLabelValues("request", req.GetNodePath()).Observe(float64(out.DurationMs) / 1000.0)
		if outcome.IsError() {
			s.metrics.RequestsIncompleteTotal.WithLabelValues("request", req.GetNodePath()).Inc()
		}
		// §41/§52: исход последнего вызова узла (in-memory гаудж) — по
		// ЭФФЕКТИВНОМУ значению, см. выше.
		// §81.2: обрыв вызывающей стороной исходом узла НЕ считается — он о
		// здоровье приёмника не говорит ничего.
		if !out.ParentGone {
			s.metrics.SetNodeLastRequestOutcome(req.GetNodePath(), effOutcome)
		}
	}

	return &senderv1.SendResponse{
		StatusCode: out.StatusCode,
		Body:       out.Body,
		Headers:    out.Headers,
		Error:      out.Error,
		Timeout:    out.Timeout,
		Attempts:   out.Attempts,
		DurationMs: out.DurationMs,
	}, nil
}
