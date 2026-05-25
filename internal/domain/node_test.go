package domain

import (
	"errors"
	"testing"
)

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
