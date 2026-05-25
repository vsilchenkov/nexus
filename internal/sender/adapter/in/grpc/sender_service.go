// Package grpc — gRPC adapter Sender Service.
// Реализует SenderServiceServer из сгенерированного proto/sender/v1.
package grpc

import (
	"context"

	"bus/internal/domain"
	"bus/internal/platform/logging"
	"bus/internal/sender/usecase"
	senderv1 "bus/proto/sender/v1"
)

// Server реализует senderv1.SenderServiceServer.
type Server struct {
	senderv1.UnimplementedSenderServiceServer
	uc     *usecase.SendUsecase
	logger logging.Logger
}

func NewServer(uc *usecase.SendUsecase, logger logging.Logger) *Server {
	return &Server{uc: uc, logger: logger}
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
		LogRequestBody:  req.GetLogRequestBody(),
		LogResponseBody: req.GetLogResponseBody(),
		LogHeaders:      req.GetLogHeaders(),
		ClientIP:        req.GetClientIp(),
	})

	return &senderv1.SendResponse{
		StatusCode: out.StatusCode,
		Body:       out.Body,
		Headers:    out.Headers,
		Error:      out.Error,
		Attempts:   out.Attempts,
		DurationMs: out.DurationMs,
	}, nil
}
