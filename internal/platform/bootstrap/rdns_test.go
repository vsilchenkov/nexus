package bootstrap

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
)

func TestNewHostResolver(t *testing.T) {
	t.Parallel()
	base := config.SenderRDNSConfig{
		TimeoutMs: 2000, CacheTTLSec: 3600, NegativeTTLSec: 600,
		NetBIOS: config.SenderRDNSNetBIOSConfig{Port: 137, TimeoutMs: 1000, Suffix: "vz78.vozovoz.ru"},
	}
	tests := []struct {
		name        string
		mutate      func(*config.SenderRDNSConfig)
		wantNil     bool
		wantNetBIOS bool
	}{
		{name: "disabled", mutate: func(c *config.SenderRDNSConfig) { c.Disabled = true }, wantNil: true},
		// Выключенный rdns выключает и NetBIOS: этап — часть той же цепочки.
		{name: "disabled_with_netbios", mutate: func(c *config.SenderRDNSConfig) {
			c.Disabled = true
			c.NetBIOS.Enabled = true
		}, wantNil: true},
		{name: "ptr_only", mutate: func(*config.SenderRDNSConfig) {}},
		{name: "with_netbios", mutate: func(c *config.SenderRDNSConfig) { c.NetBIOS.Enabled = true }, wantNetBIOS: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := base
			tt.mutate(&cfg)
			var buf bytes.Buffer
			logger := logging.NewLogger(slog.New(slog.NewTextHandler(&buf, nil)))

			r := NewHostResolver(context.Background(), nil, &cfg, logger)
			if tt.wantNil {
				assert.Nil(t, r)
				assert.Empty(t, buf.String())
				return
			}
			require.NotNil(t, r)
			if tt.wantNetBIOS {
				assert.Contains(t, buf.String(), "rdns: netbios stage enabled")
				assert.Contains(t, buf.String(), "suffix=vz78.vozovoz.ru")
				assert.Contains(t, buf.String(), "port=137")
			} else {
				assert.NotContains(t, buf.String(), "netbios")
			}
			// Loopback не резолвится вовсе — и фоновых горутин не остаётся
			// (goleak в TestMain).
			assert.Empty(t, r.ResolveNow(context.Background(), "127.0.0.1"))
		})
	}
}
