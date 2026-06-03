package usecase

import (
	"context"
	"testing"

	"nexus/internal/platform/logging"
)

type fakeRMQDialer struct {
	got RMQProbeParams
	res RMQTestResult
}

func (f *fakeRMQDialer) Probe(_ context.Context, p RMQProbeParams) RMQTestResult {
	f.got = p
	return f.res
}

func TestRMQTester_DefaultsPortAndVhost(t *testing.T) {
	cases := []struct {
		name     string
		in       RMQProbeParams
		wantPort int
		wantVH   string
	}{
		{"amqp default", RMQProbeParams{Host: "h", Queue: "q"}, 5672, "/"},
		{"amqps default", RMQProbeParams{Host: "h", Queue: "q", UseTLS: true}, 5671, "/"},
		{"explicit kept", RMQProbeParams{Host: "h", Queue: "q", Port: 5673, VHost: "/prod"}, 5673, "/prod"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &fakeRMQDialer{res: RMQTestResult{OK: true}}
			tr := NewRMQTester(d, logging.NewNoop())
			out := tr.Test(context.Background(), c.in)
			if !out.OK {
				t.Fatalf("want OK passthrough")
			}
			if d.got.Port != c.wantPort {
				t.Errorf("port = %d, want %d", d.got.Port, c.wantPort)
			}
			if d.got.VHost != c.wantVH {
				t.Errorf("vhost = %q, want %q", d.got.VHost, c.wantVH)
			}
		})
	}
}
