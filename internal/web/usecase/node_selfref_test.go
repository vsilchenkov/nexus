package usecase

import (
	"errors"
	"testing"

	"nexus/internal/domain"
)

func TestIsSelfReferenceTarget(t *testing.T) {
	t.Parallel()
	self := []string{"receiver:8080", "nexus.example.com"}
	tests := []struct {
		name   string
		target string
		want   bool
	}{
		{"exact authority + ingress path", "http://receiver:8080/v1/request/demo", true},
		{"api prefix ingress path", "http://receiver:8080/api/v1/request/demo", true},
		{"requestAsync ingress path", "http://receiver:8080/v1/requestAsync/demo", true},
		{"host without port matches port-agnostic self", "https://nexus.example.com/v1/request/x", true},
		{"host without port, self has no port, target has port", "https://nexus.example.com:443/v1/request/x", true},
		{"self host:port but target different port", "http://receiver:9090/v1/request/demo", false},
		{"matching host but non-ingress path", "http://receiver:8080/health", false},
		// §78.4: короткий адрес узла — тоже вход шины. Без этого узел с
		// target_url на собственный /api/v1/<команда>/<путь> проходил бы
		// проверку, и защита §32.2 молча ослабла бы.
		{"short url ingress path", "http://receiver:8080/api/v1/webhook/sbp-qr", true},
		{"short url without team slug", "https://nexus.example.com/api/v1/sbp-qr", true},
		{"legacy v1 short url", "http://receiver:8080/v1/webhook/sbp-qr", true},
		{"api path outside v1 is not ingress", "http://receiver:8080/api/nodes", false},
		{"different host", "https://external.example.org/v1/request/x", false},
		{"empty target", "", false},
		{"not a url", "://bad", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isSelfReferenceTarget(tt.target, self); got != tt.want {
				t.Fatalf("isSelfReferenceTarget(%q) = %v, want %v", tt.target, got, tt.want)
			}
		})
	}
}

func TestCheckSelfReference(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		selfHosts []string
		node      *domain.Node
		wantErr   bool
	}{
		{
			name:      "static self-ref rejected",
			selfHosts: []string{"receiver:8080"},
			node:      &domain.Node{URLMode: domain.URLModeStatic, TargetURL: "http://receiver:8080/v1/request/demo"},
			wantErr:   true,
		},
		{
			name:      "static external allowed",
			selfHosts: []string{"receiver:8080"},
			node:      &domain.Node{URLMode: domain.URLModeStatic, TargetURL: "https://api.partner.com/hook"},
			wantErr:   false,
		},
		{
			name:      "from_request skipped",
			selfHosts: []string{"receiver:8080"},
			node:      &domain.Node{URLMode: domain.URLModeFromRequest, TargetURL: "http://receiver:8080/v1/request/demo"},
			wantErr:   false,
		},
		{
			name:      "empty self hosts skips check",
			selfHosts: nil,
			node:      &domain.Node{URLMode: domain.URLModeStatic, TargetURL: "http://receiver:8080/v1/request/demo"},
			wantErr:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			u := &NodeUsecase{selfIngressHosts: tt.selfHosts}
			err := u.checkSelfReference(tt.node)
			if tt.wantErr && !errors.Is(err, domain.ErrNodeTargetURLSelfReference) {
				t.Fatalf("want ErrNodeTargetURLSelfReference, got %v", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("want nil, got %v", err)
			}
		})
	}
}
