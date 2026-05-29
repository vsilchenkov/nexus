package domain

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestNode_UnmarshalJSON_LoggingEnabledDefault(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"missing field defaults to true", `{"Path":"x"}`, true},
		{"explicit true", `{"Path":"x","LoggingEnabled":true}`, true},
		{"explicit false respected", `{"Path":"x","LoggingEnabled":false}`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var n Node
			if err := json.Unmarshal([]byte(tc.in), &n); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if n.LoggingEnabled != tc.want {
				t.Errorf("LoggingEnabled = %v, want %v", n.LoggingEnabled, tc.want)
			}
			if n.Path != "x" {
				t.Errorf("Path = %q, want x (остальные поля должны десериализоваться)", n.Path)
			}
		})
	}
}

func TestNode_JSON_RoundTrip(t *testing.T) {
	orig := Node{Path: "p", LoggingEnabled: false, MaxBodySizeEnabled: true, MaxBodySize: 100}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Node
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.LoggingEnabled != false || got.MaxBodySize != 100 || !got.MaxBodySizeEnabled {
		t.Errorf("round-trip lost fields: %+v", got)
	}
}

func TestNode_SetDefaults(t *testing.T) {
	n := &Node{Path: "x", RootMethod: RootMethodRequest, TargetURL: "https://example.com"}
	n.SetDefaults()
	if n.URLMode != URLModeStatic {
		t.Errorf("url_mode default = %q", n.URLMode)
	}
	if n.URLParamName != "url_base" {
		t.Errorf("url_param_name default = %q", n.URLParamName)
	}
	if n.AuthType != AuthTypeNone {
		t.Errorf("auth_type default = %q", n.AuthType)
	}
	if n.Status != NodeStatusEnabled {
		t.Errorf("status default = %q", n.Status)
	}
	if n.TimeoutMs != 30_000 {
		t.Errorf("timeout_ms default = %d", n.TimeoutMs)
	}
}

func TestNode_Validate_OK(t *testing.T) {
	n := &Node{
		Path: "test/path", RootMethod: RootMethodRequest,
		TargetURL: "https://example.com",
	}
	n.SetDefaults()
	if err := n.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestNode_Validate_Errors(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Node)
		want error
	}{
		{"bad path", func(n *Node) { n.Path = "" }, ErrNodePathLength},
		{"bad path format", func(n *Node) { n.Path = "/leading-slash" }, ErrNodePathFormat},
		{"static without url", func(n *Node) { n.TargetURL = "" }, ErrNodeStaticNeedsTargetURL},
		{"bad timeout", func(n *Node) { n.TimeoutMs = 1 }, ErrNodeTimeoutRange},
		{"bad retry", func(n *Node) { n.RetryCount = 999 }, ErrNodeRetryCountRange},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := &Node{Path: "x", RootMethod: RootMethodRequest, TargetURL: "https://example.com"}
			n.SetDefaults()
			c.mut(n)
			err := n.Validate()
			if !errors.Is(err, c.want) {
				t.Fatalf("want %v, got %v", c.want, err)
			}
		})
	}
}

func TestAuthType_IsDynamic(t *testing.T) {
	cases := map[AuthType]bool{
		AuthTypeNone:             false,
		AuthTypeBasic:            false,
		AuthTypeToken:            false,
		AuthTypeTokenFromRequest: true,
		AuthTypeBasicFromRequest: true,
	}
	for a, want := range cases {
		if got := a.IsDynamic(); got != want {
			t.Errorf("%s.IsDynamic() = %v, want %v", a, got, want)
		}
	}
}
