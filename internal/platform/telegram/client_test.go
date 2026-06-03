package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nexus/internal/platform/logging"
)

func TestClient_Send_OK(t *testing.T) {
	t.Parallel()
	var gotPath, gotChat, gotText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		var p sendMessagePayload
		_ = json.Unmarshal(body, &p)
		gotChat, gotText = p.ChatID, p.Text
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := New(logging.NewNoop()).WithBaseURL(srv.URL)
	if err := c.Send(context.Background(), "123:abc", "-100777", "hello"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if !strings.HasPrefix(gotPath, "/bot123:abc/sendMessage") {
		t.Errorf("path = %q", gotPath)
	}
	if gotChat != "-100777" || gotText != "hello" {
		t.Errorf("chat=%q text=%q", gotChat, gotText)
	}
}

func TestClient_Send_APIError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":false,"description":"chat not found"}`))
	}))
	defer srv.Close()

	c := New(logging.NewNoop()).WithBaseURL(srv.URL)
	err := c.Send(context.Background(), "t", "x", "y")
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("expected api error, got %v", err)
	}
}

func TestClient_Send_HTTPError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	c := New(logging.NewNoop()).WithBaseURL(srv.URL)
	if err := c.Send(context.Background(), "t", "x", "y"); err == nil {
		t.Fatal("expected error on HTTP 502")
	}
}
