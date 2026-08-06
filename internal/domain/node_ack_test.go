package domain

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"nexus/internal/domain/ackspec"
)

// ackNode — минимальный валидный узел с заданной спекой. Обязательные поля
// добиваются SetDefaults, чтобы тест падал на спеке, а не на соседних полях.
func ackNode(root RootMethod, spec *ackspec.Spec) *Node {
	n := &Node{
		Path:             "acs_sigur",
		RootMethod:       root,
		URLMode:          URLModeStatic,
		URLParamName:     "url_base",
		TargetURL:        "https://receiver.example/api",
		IncomingMethod:   HTTPMethodPOST,
		OutgoingMethod:   HTTPMethodPOST,
		AuthType:         AuthTypeNone,
		IncomingAuthType: IncomingAuthTypeNone,
		Status:           NodeStatusEnabled,
		TimeoutMs:        30000,
	}
	n.SetDefaults()
	n.AsyncAck = spec // после SetDefaults — спека передаётся тестом как есть
	return n
}

func sigurSpec() *ackspec.Spec {
	return &ackspec.Spec{
		Version:     ackspec.Version,
		ContentType: ackspec.ContentTypeJSON,
		Body:        `{"confirmedLogId": "${ body.logs[*].logId | max }"}`,
		OnError:     ackspec.OnErrorDefault,
	}
}

// TestNode_Validate_AsyncAck — битая спека обязана отбиваться на сохранении
// узла, а не на боевом трафике.
func TestNode_Validate_AsyncAck(t *testing.T) {
	tests := []struct {
		name    string
		spec    *ackspec.Spec
		wantErr error
	}{
		{"no spec is valid", nil, nil},
		{"valid spec", sigurSpec(), nil},
		{"broken template", &ackspec.Spec{
			Version: ackspec.Version, ContentType: ackspec.ContentTypeJSON,
			Body: `{"a": "${ body.x"}`, OnError: ackspec.OnErrorDefault,
		}, ackspec.ErrTemplateSyntax},
		{"non-2xx status", &ackspec.Spec{
			Version: ackspec.Version, ContentType: ackspec.ContentTypeJSON,
			Body: `{"ok": true}`, OnError: ackspec.OnErrorDefault, Status: 404,
		}, ackspec.ErrSpecInvalid},
		{"unknown content type", &ackspec.Spec{
			Version: ackspec.Version, ContentType: "application/xml",
			Body: `<ok/>`, OnError: ackspec.OnErrorDefault,
		}, ackspec.ErrSpecInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := ackNode(RootMethodRequestAsync, tt.spec)
			err := n.Validate()
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("want no error, got %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("want %v, got %v", tt.wantErr, err)
			}
		})
	}
}

// TestNode_Validate_AsyncAckOnSyncNode — спека валидируется и у sync-узла:
// она там хранится (см. TestNode_AsyncAckSurvivesRootMethodSwitch) и обязана
// быть исправной к моменту возврата узла в async.
func TestNode_Validate_AsyncAckOnSyncNode(t *testing.T) {
	n := ackNode(RootMethodRequest, &ackspec.Spec{
		Version: ackspec.Version, ContentType: ackspec.ContentTypeJSON,
		Body: `{"a": "${ body.x"}`, OnError: ackspec.OnErrorDefault,
	})
	if err := n.Validate(); !errors.Is(err, ackspec.ErrTemplateSyntax) {
		t.Fatalf("want ErrTemplateSyntax on sync node too, got %v", err)
	}
}

// TestNode_AsyncAckSurvivesRootMethodSwitch — ключевой контракт §83.0: оператор
// временно переводит узел в sync и обратно, НЕ теряя настроенный шаблон.
// Держится на одной строке `if !n.RootMethod.IsPull()` в NormalizeForRootMethod,
// поэтому закреплён отдельно.
func TestNode_AsyncAckSurvivesRootMethodSwitch(t *testing.T) {
	n := ackNode(RootMethodRequestAsync, sigurSpec())
	want := n.AsyncAck.Body

	n.RootMethod = RootMethodRequest
	if cleared := n.NormalizeForRootMethod(); cleared != nil {
		t.Fatalf("switching to sync must clear nothing, got %v", cleared)
	}
	if n.AsyncAck == nil || n.AsyncAck.Body != want {
		t.Fatalf("spec lost on switch to sync: %+v", n.AsyncAck)
	}

	n.RootMethod = RootMethodRequestAsync
	if cleared := n.NormalizeForRootMethod(); cleared != nil {
		t.Fatalf("switching back must clear nothing, got %v", cleared)
	}
	if n.AsyncAck == nil || n.AsyncAck.Body != want {
		t.Fatalf("spec lost on switch back to async: %+v", n.AsyncAck)
	}
}

// TestNode_NormalizeForRootMethod_PullClearsAck — у pull-узла входящего HTTP
// нет вовсе, отвечать некому; сброс обязан попасть в список для аудита.
func TestNode_NormalizeForRootMethod_PullClearsAck(t *testing.T) {
	n := ackNode(RootMethodRabbitMQAsync, sigurSpec())

	cleared := n.NormalizeForRootMethod()

	if n.AsyncAck != nil {
		t.Fatalf("spec must be cleared for pull node, got %+v", n.AsyncAck)
	}
	if !slices.Contains(cleared, "async_ack_spec") {
		t.Fatalf("cleared list must mention async_ack_spec, got %v", cleared)
	}
}

// TestNode_SetDefaults_AsyncAck — форма присылает только шаблон; версия,
// формат и политика проставляются до Validate.
func TestNode_SetDefaults_AsyncAck(t *testing.T) {
	n := ackNode(RootMethodRequestAsync, &ackspec.Spec{Body: `{"ok": true}`})

	n.SetDefaults()

	if n.AsyncAck.Version != ackspec.Version {
		t.Errorf("version=%d", n.AsyncAck.Version)
	}
	if n.AsyncAck.ContentType != ackspec.ContentTypeJSON {
		t.Errorf("content_type=%q", n.AsyncAck.ContentType)
	}
	if n.AsyncAck.OnError != ackspec.OnErrorDefault {
		t.Errorf("on_error=%q", n.AsyncAck.OnError)
	}
	if err := n.Validate(); err != nil {
		t.Fatalf("node with defaulted spec must validate: %v", err)
	}

	// Узел без спеки — SetDefaults не должен её создавать: nil означает
	// «отвечать как раньше», и это поведение по умолчанию.
	plain := ackNode(RootMethodRequestAsync, nil)
	plain.SetDefaults()
	if plain.AsyncAck != nil {
		t.Fatalf("SetDefaults must not invent a spec, got %+v", plain.AsyncAck)
	}
}

// TestNode_UnmarshalJSON_AsyncAckRoundTrip — узел ездит в Redis-кеш Receiver'а
// как JSON. Запись, сделанная бинарём до §83, обязана давать nil-спеку (прежний
// ответ), а не ломать разбор узла целиком.
func TestNode_UnmarshalJSON_AsyncAckRoundTrip(t *testing.T) {
	t.Run("spec survives round trip", func(t *testing.T) {
		src := ackNode(RootMethodRequestAsync, sigurSpec())
		raw, err := json.Marshal(src)
		if err != nil {
			t.Fatal(err)
		}
		var got Node
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.AsyncAck == nil || got.AsyncAck.Body != src.AsyncAck.Body {
			t.Fatalf("spec lost in cache round trip: %+v", got.AsyncAck)
		}
		if got.AsyncAck.ContentType != ackspec.ContentTypeJSON {
			t.Errorf("content_type=%q", got.AsyncAck.ContentType)
		}
	})

	t.Run("pre-83 cache entry yields nil spec", func(t *testing.T) {
		var got Node
		if err := json.Unmarshal([]byte(`{"Path":"a","RootMethod":"requestAsync"}`), &got); err != nil {
			t.Fatal(err)
		}
		if got.AsyncAck != nil {
			t.Fatalf("want nil spec for pre-83 entry, got %+v", got.AsyncAck)
		}
	})
}
