package clientip

import "testing"

func TestNormalizeIPv4(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain ipv4", "203.0.113.7", "203.0.113.7"},
		{"ipv6 loopback", "::1", "127.0.0.1"},
		{"ipv4-mapped ipv6", "::ffff:10.0.0.5", "10.0.0.5"},
		{"real ipv6 unchanged", "2001:db8::1", "2001:db8::1"},
		{"not an ip passthrough", "not-an-ip", "not-an-ip"},
		{"ipv4 with leading zeros invalid -> passthrough", "10.0.0.001", "10.0.0.001"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeIPv4(tc.in); got != tc.want {
				t.Errorf("NormalizeIPv4(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
