// Package grpc — gRPC adapter Sender Service.
// Реализует SenderServiceServer из сгенерированного proto/sender/v1.
package grpc

import (
	"context"
	"maps"
	"strconv"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	"nexus/internal/sender/usecase"
	senderv1 "nexus/proto/sender/v1"
)

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
			Attempts:   out.Attempts,
			DurationMs: out.DurationMs,
		}, nil
	}

	// §52: outcome (ok/degraded/down) — для бейджа узла; isErr («любой
	// не-2xx») — прежняя семантика incomplete_total.
	outcome := domain.OutcomeFromStatusCode(out.StatusCode)
	if s.metrics != nil {
		s.metrics.RequestsTotal.
			WithLabelValues("request", req.GetNodePath(), strconv.FormatInt(int64(out.StatusCode), 10)).Inc()
		s.metrics.RequestDuration.
			WithLabelValues("request", req.GetNodePath()).Observe(float64(out.DurationMs) / 1000.0)
		if outcome.IsError() {
			s.metrics.RequestsIncompleteTotal.WithLabelValues("request", req.GetNodePath()).Inc()
		}
		// §41/§52: исход последнего вызова узла (in-memory гаудж).
		s.metrics.SetNodeLastRequestOutcome(req.GetNodePath(), outcome)
	}
	// §46: персистентный исход в Redis (переживает рестарт; Noop без Redis).
	if s.nodeStatus != nil {
		s.nodeStatus.SetLastOutcome(ctx, req.GetNodePath(), outcome)
	}

	return &senderv1.SendResponse{
		StatusCode: out.StatusCode,
		Body:       out.Body,
		Headers:    out.Headers,
		Error:      out.Error,
		Attempts:   out.Attempts,
		DurationMs: out.DurationMs,
	}, nil
}
