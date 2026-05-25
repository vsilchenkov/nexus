// Package grpcsender — клиент к Sender Service по gRPC.
//
// Используется Receiver'ом для синхронной доставки (/v1/request/*).
// Простой пул из N подключений с round-robin (§9.3 ТЗ).
package grpcsender

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	"bus/internal/platform/config"
	"bus/internal/platform/logging"
	senderv1 "bus/proto/sender/v1"
)

// Client — пул gRPC-соединений к Sender.
type Client struct {
	conns   []*grpc.ClientConn
	clients []senderv1.SenderServiceClient
	next    atomic.Uint64
	logger  logging.Logger
}

func New(cfg *config.ReceiverSenderGRPCConfig, logger logging.Logger) (*Client, error) {
	if cfg.PoolSize <= 0 {
		cfg.PoolSize = 1
	}
	conns := make([]*grpc.ClientConn, 0, cfg.PoolSize)
	clients := make([]senderv1.SenderServiceClient, 0, cfg.PoolSize)
	for i := 0; i < cfg.PoolSize; i++ {
		cc, err := grpc.NewClient(
			cfg.Addr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithKeepaliveParams(keepalive.ClientParameters{
				Time:    time.Duration(cfg.KeepaliveTimeSec) * time.Second,
				Timeout: time.Duration(cfg.KeepaliveTimeoutSec) * time.Second,
			}),
		)
		if err != nil {
			for _, prev := range conns {
				_ = prev.Close()
			}
			return nil, fmt.Errorf("grpc.NewClient %s: %w", cfg.Addr, err)
		}
		conns = append(conns, cc)
		clients = append(clients, senderv1.NewSenderServiceClient(cc))
	}
	return &Client{conns: conns, clients: clients, logger: logger}, nil
}

// Send выбирает round-robin соединение и выполняет RPC.
func (c *Client) Send(ctx context.Context, req *senderv1.SendRequest) (*senderv1.SendResponse, error) {
	if len(c.clients) == 0 {
		return nil, fmt.Errorf("grpcsender: no clients")
	}
	idx := c.next.Add(1) % uint64(len(c.clients))
	return c.clients[idx].Send(ctx, req)
}

func (c *Client) Close() error {
	var firstErr error
	for _, cc := range c.conns {
		if err := cc.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
