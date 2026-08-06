package http

import (
	"encoding/json"
	"testing"

	"nexus/internal/domain"
	"nexus/internal/domain/ackspec"
)

func ackTestSpec() *ackspec.Spec {
	return &ackspec.Spec{
		Version:     ackspec.Version,
		ContentType: ackspec.ContentTypeJSON,
		Body:        `{"confirmedLogId": "${ body.logs[*].logId | max }"}`,
		OnError:     ackspec.OnErrorDefault,
	}
}

// TestNodeDTO_AsyncAckRoundTrip — спека должна доехать от формы до домена и
// обратно без потерь: форма перечитывает узел после сохранения, и потерянная
// на этом пути настройка выглядела бы как «не сохранилось».
func TestNodeDTO_AsyncAckRoundTrip(t *testing.T) {
	t.Parallel()

	req := &CreateNodeRequest{
		Path:         "acs_sigur",
		RootMethod:   string(domain.RootMethodRequestAsync),
		AsyncAckSpec: ackTestSpec(),
	}

	n := reqToDomain(*req)
	if n.AsyncAck == nil || n.AsyncAck.Body != req.AsyncAckSpec.Body {
		t.Fatalf("spec lost in reqToDomain: %+v", n.AsyncAck)
	}

	resp := nodeToResponse(n)
	if resp.AsyncAckSpec == nil || resp.AsyncAckSpec.Body != req.AsyncAckSpec.Body {
		t.Fatalf("spec lost in nodeToResponse: %+v", resp.AsyncAckSpec)
	}
	if resp.AsyncAckSpec.ContentType != ackspec.ContentTypeJSON {
		t.Errorf("content_type=%q", resp.AsyncAckSpec.ContentType)
	}
}

// TestNodeDTO_AsyncAckAbsent — узел без спеки отдаётся с null, а не с пустым
// объектом: форма по null понимает, что переключатель выключен.
func TestNodeDTO_AsyncAckAbsent(t *testing.T) {
	t.Parallel()

	n := reqToDomain(CreateNodeRequest{Path: "plain", RootMethod: string(domain.RootMethodRequest)})
	if n.AsyncAck != nil {
		t.Fatalf("want nil spec, got %+v", n.AsyncAck)
	}

	raw, err := json.Marshal(nodeToResponse(n))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	v, present := got["async_ack_spec"]
	if !present {
		t.Fatal("async_ack_spec must be present in the response")
	}
	if v != nil {
		t.Fatalf("want null, got %v", v)
	}
}

// TestNodeDTO_AsyncAckFromJSON — форма присылает спеку как вложенный объект;
// имена полей — часть контракта с фронтом и с колонкой в БД.
func TestNodeDTO_AsyncAckFromJSON(t *testing.T) {
	t.Parallel()

	raw := `{
		"path": "acs_sigur",
		"root_method": "requestAsync",
		"async_ack_spec": {
			"version": 1,
			"content_type": "application/json",
			"body": "{\"confirmedLogId\": \"${ body.logs[*].logId | max }\"}",
			"on_error": "default"
		}
	}`

	var req CreateNodeRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatal(err)
	}
	if req.AsyncAckSpec == nil {
		t.Fatal("spec not parsed")
	}
	if req.AsyncAckSpec.OnError != ackspec.OnErrorDefault {
		t.Errorf("on_error=%q", req.AsyncAckSpec.OnError)
	}
	if err := req.AsyncAckSpec.Validate(); err != nil {
		t.Fatalf("parsed spec must be valid: %v", err)
	}
}

// TestNodeValidationCode_AsyncAck — ошибки спеки обязаны подсвечивать поле
// формы, а не падать общим 500: оператор правит шаблон прямо в карточке.
func TestNodeValidationCode_AsyncAck(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		err       error
		wantCode  string
		wantField string
	}{
		{"template syntax", ackspec.ErrTemplateSyntax, "node.validation.async_ack_template", "async_ack_spec"},
		{"spec invalid", ackspec.ErrSpecInvalid, "node.validation.async_ack_spec", "async_ack_spec"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			code, field, ok := nodeValidationCode(tc.err)
			if !ok {
				t.Fatalf("error %v is not mapped to a form field", tc.err)
			}
			if code != tc.wantCode || field != tc.wantField {
				t.Fatalf("code=%q field=%q", code, field)
			}
		})
	}
}
