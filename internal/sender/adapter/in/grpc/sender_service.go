// Package grpc — gRPC adapter Sender Service.
// Реализует SenderServiceServer из сгенерированного proto/sender/v1.
package grpc

import (
	"context"
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
	uc      *usecase.SendUsecase
	logger  logging.Logger
	metrics *metrics.Metrics
}

func NewServer(uc *usecase.SendUsecase, m *metrics.Metrics, logger logging.Logger) *Server {
	return &Server{uc: uc, metrics: m, logger: logger}
}

func (s *Server) Send(ctx context.Context, req *senderv1.SendRequest) (*senderv1.SendResponse, error) {
	headers := make(map[string]string, len(req.GetHeaders())+1)
	for k, v := range req.GetHeaders() {
		headers[k] = v
	}
	if a := req.GetAuth(); a != nil && a.GetAuthorizationHeader() != "" {
		headers["Authorization"] = a.GetAuthorizationHeader()
	}

	out := s.uc.Send(ctx, usecase.SendInput{
		ID:              req.GetId(),
		NodePath:        req.GetNodePath(),
		RootMethod:      domain.RootMethodRequest, // sync-путь
		TargetURL:       req.GetTargetUrl(),
		Method:          req.GetMethod(),
		Headers:         headers,
		Body:            req.GetBody(),
		TimeoutMs:       req.GetTimeoutMs(),
		RetryCount:      req.GetRetryCount(),
		RetryBackoffMs:  req.GetRetryBackoffMs(),
		ClickHouseTable: req.GetClickhouseTable(),
		LogRequestBody:     req.GetLogRequestBody(),
		LogResponseBody:    req.GetLogResponseBody(),
		LogHeaders:         req.GetLogHeaders(),
		ClientIP:           req.GetClientIp(),
		LoggingEnabled:     req.GetLoggingEnabled(),
		MaxBodySizeEnabled: req.GetMaxBodySizeEnabled(),
		MaxBodySize:        req.GetMaxBodySize(),
	})

	if s.metrics != nil {
		s.metrics.RequestsTotal.
			WithLabelValues("request", req.GetNodePath(), strconv.FormatInt(int64(out.StatusCode), 10)).Inc()
		s.metrics.RequestDuration.
			WithLabelValues("request", req.GetNodePath()).Observe(float64(out.DurationMs) / 1000.0)
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
