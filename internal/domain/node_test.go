package domain

import (
	"encoding/json"
	"errors"
	"strings"
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
	if n.DLQTTLSeconds != 86_400 { // §36: 24ч
		t.Errorf("dlq_ttl_seconds default = %d", n.DLQTTLSeconds)
	}
	if n.DLQRetryDelaySeconds != 300 { // §36: 5 мин
		t.Errorf("dlq_retry_delay_seconds default = %d", n.DLQRetryDelaySeconds)
	}
	// §41: входящая динамическая авторизация — дефолты сохраняют прежнее
	// поведение (читать заголовок Authorization).
	if n.IncomingAuthDynamicSource != IncomingAuthSourceHeader {
		t.Errorf("incoming_auth_dynamic_source default = %q", n.IncomingAuthDynamicSource)
	}
	if n.IncomingAuthDynamicField != "Authorization" {
		t.Errorf("incoming_auth_dynamic_field default = %q", n.IncomingAuthDynamicField)
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
	// §29: лимит комментария — по рунам (символам). 2000 кириллических символов
	// (4000 байт) обязаны проходить, иначе лимит ошибочно считался бы по байтам.
	n.Comment = strings.Repeat("я", 2000)
	if err := n.Validate(); err != nil {
		t.Fatalf("comment of 2000 runes must pass, got %v", err)
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
		{"timeout at max 600000 ok", func(n *Node) { n.TimeoutMs = 600_000 }, nil},
		{"timeout above max", func(n *Node) { n.TimeoutMs = 600_001 }, ErrNodeTimeoutRange},
		{"bad retry", func(n *Node) { n.RetryCount = 999 }, ErrNodeRetryCountRange},
		{"bad dlq_ttl low", func(n *Node) { n.DLQTTLSeconds = 10 }, ErrNodeDLQTTLRange},
		{"bad dlq_ttl high", func(n *Node) { n.DLQTTLSeconds = 9_999_999 }, ErrNodeDLQTTLRange},
		{"bad dlq_retry_delay low", func(n *Node) { n.DLQRetryDelaySeconds = 0 }, ErrNodeDLQRetryDelayRange},
		{"bad dlq_retry_delay high", func(n *Node) { n.DLQRetryDelaySeconds = 99_999 }, ErrNodeDLQRetryDelayRange},
		{"comment too long", func(n *Node) { n.Comment = strings.Repeat("я", 2001) }, ErrNodeCommentLength},
		// §41: исходящий basic_from_request тоже требует валидного source.
		{"basic_from_request bad source", func(n *Node) {
			n.AuthType = AuthTypeBasicFromRequest
			n.AuthDynamicSource = "nonsense"
		}, ErrNodeInvalidAuthDynSource},
		// §41: входящая динамическая авторизация (непустые невалидные значения;
		// пустые трактуются как дефолт header/Authorization и валидны).
		{"incoming dyn source invalid", func(n *Node) { n.IncomingAuthDynamicSource = "body" }, ErrNodeInvalidIncomingAuthDynSource},
		{"incoming dyn field format", func(n *Node) { n.IncomingAuthDynamicField = "1bad" }, ErrNodeIncomingAuthDynFieldFormat},
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

// §41: входящая авторизация из query-параметра и заголовка проходит валидацию;
// существующие basic/token-узлы (дефолтный source=header) остаются валидны.
func TestNode_Validate_IncomingDynamicAuth(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		mut  func(*Node)
	}{
		{"token via header (back-compat)", func(n *Node) {
			n.IncomingAuthType = IncomingAuthTypeToken
			n.IncomingAuthCredentials = "secret"
		}},
		{"token via query field", func(n *Node) {
			n.IncomingAuthType = IncomingAuthTypeToken
			n.IncomingAuthCredentials = "secret"
			n.IncomingAuthDynamicSource = IncomingAuthSourceQuery
			n.IncomingAuthDynamicField = "apikey"
		}},
		{"basic via query field", func(n *Node) {
			n.IncomingAuthType = IncomingAuthTypeBasic
			n.IncomingAuthCredentials = "user:pass"
			n.IncomingAuthDynamicSource = IncomingAuthSourceQuery
			n.IncomingAuthDynamicField = "creds"
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			n := &Node{Path: "x", RootMethod: RootMethodRequest, TargetURL: "https://example.com"}
			c.mut(n)
			n.SetDefaults()
			if err := n.Validate(); err != nil {
				t.Fatalf("want valid, got %v", err)
			}
		})
	}
}

func TestNode_Validate_LoggingRequiresTable(t *testing.T) {
	// §28 Пункт 5: логирование включено, но имя таблицы не задано → ошибка.
	n := &Node{Path: "x", RootMethod: RootMethodRequest, TargetURL: "https://example.com", LoggingEnabled: true}
	n.SetDefaults()
	if err := n.Validate(); !errors.Is(err, ErrNodeLogsNotConfigured) {
		t.Fatalf("want ErrNodeLogsNotConfigured, got %v", err)
	}
	// С таблицей — ок.
	n.ClickHouseTable = "nexus_default.x"
	if err := n.Validate(); err != nil {
		t.Fatalf("with table want nil, got %v", err)
	}
	// Логирование выключено — таблица не требуется.
	n2 := &Node{Path: "y", RootMethod: RootMethodRequest, TargetURL: "https://example.com", LoggingEnabled: false}
	n2.SetDefaults()
	if err := n2.Validate(); err != nil {
		t.Fatalf("logging off want nil, got %v", err)
	}
}

func TestNode_Validate_ClickHouseTableFormat(t *testing.T) {
	// §42: имя таблицы должно быть строго db.table из [A-Za-z0-9_]. Проверка
	// действует только при включённом логировании: при выключенном поля
	// карточки задизейблены, и недозаполненное имя (дефолтный префикс
	// «nexus_x.» формы создания) — черновик, а не блокирующая ошибка.
	base := func(table string, loggingOn bool) *Node {
		n := &Node{
			Path: "x", RootMethod: RootMethodRequest, TargetURL: "https://example.com",
			ClickHouseTable: table, LoggingEnabled: loggingOn,
		}
		n.SetDefaults()
		return n
	}
	bad := []string{
		"my_table",                // без БД
		"db.my table",             // пробел
		"db.my-table",             // дефис
		"db.schema.table",         // две точки
		"db.",                     // пустая таблица
		".table",                  // пустая БД
		"nexus.x; DROP TABLE y--", // SQL-инъекция
	}
	for _, table := range bad {
		if err := base(table, true).Validate(); !errors.Is(err, ErrNodeClickHouseTableInvalid) {
			t.Errorf("table %q: want ErrNodeClickHouseTableInvalid, got %v", table, err)
		}
		// Логирование выключено → черновик допустим (симптом «ругается на имя
		// таблицы при отключённых логах»).
		if err := base(table, false).Validate(); err != nil {
			t.Errorf("logging off, table %q: want nil, got %v", table, err)
		}
	}
	for _, table := range []string{"nexus_default.my_table", "db1.t2", "A_b.C_d"} {
		if err := base(table, true).Validate(); err != nil {
			t.Errorf("valid table %q: want nil, got %v", table, err)
		}
	}
}

func TestNode_Validate_MaxBodySizeRequired_GatedByLogging(t *testing.T) {
	// Лимит тела применяется только при записи лога — включённый чекбокс с
	// нулём при выключенном логировании не должен блокировать сохранение
	// (поле задизейблено, исправить нельзя).
	base := func(loggingOn bool) *Node {
		n := &Node{
			Path: "x", RootMethod: RootMethodRequest, TargetURL: "https://example.com",
			LoggingEnabled: loggingOn, ClickHouseTable: "nexus_default.x",
			MaxBodySizeEnabled: true, MaxBodySize: 0,
		}
		n.SetDefaults()
		return n
	}
	if err := base(true).Validate(); !errors.Is(err, ErrNodeMaxBodySizeRequired) {
		t.Fatalf("logging on: want ErrNodeMaxBodySizeRequired, got %v", err)
	}
	if err := base(false).Validate(); err != nil {
		t.Fatalf("logging off: want nil, got %v", err)
	}
}

func TestNode_Validate_ExternalTableTemplateConflict(t *testing.T) {
	// §64: шаблон означает «таблицей управляет Nexus», external_table — «таблицу
	// не трогаем». Вместе они противоречивы.
	base := func(external bool, tmplID string) *Node {
		n := &Node{
			Path: "x", RootMethod: RootMethodRequest, TargetURL: "https://example.com",
			ClickHouseTable: "nexus_default.x", ExternalTable: external, ClickHouseTemplateID: tmplID,
		}
		n.SetDefaults()
		return n
	}
	const tmplID = "11111111-2222-3333-4444-555555555555"

	if err := base(true, tmplID).Validate(); !errors.Is(err, ErrNodeExternalTableTemplateConflict) {
		t.Fatalf("external+template: want ErrNodeExternalTableTemplateConflict, got %v", err)
	}
	if err := base(true, "").Validate(); err != nil {
		t.Fatalf("external without template: want nil, got %v", err)
	}
	if err := base(false, tmplID).Validate(); err != nil {
		t.Fatalf("template without external: want nil, got %v", err)
	}
}

func TestNode_Validate_RabbitMQAsync(t *testing.T) {
	base := func() *Node {
		n := &Node{
			Path: "billing-events", RootMethod: RootMethodRabbitMQAsync,
			TargetURL: "https://api.partner.com/webhook",
			RMQHost:   "rmq.internal", RMQQueue: "billing.events",
		}
		n.SetDefaults()
		return n
	}

	t.Run("ok with defaults", func(t *testing.T) {
		n := base()
		if err := n.Validate(); err != nil {
			t.Fatal(err)
		}
		// SetDefaults заполняет pull_* и vhost/port.
		if n.RMQVHost != "/" || n.RMQPort != 5672 ||
			n.PullIntervalSec != 5 || n.PullBatchSize != 100 || n.PullPrefetch != 100 {
			t.Fatalf("pull defaults not applied: %+v", n)
		}
	})

	cases := []struct {
		name string
		mut  func(*Node)
		want error
	}{
		{"no host", func(n *Node) { n.RMQHost = "" }, ErrNodeRMQHostRequired},
		{"bad queue", func(n *Node) { n.RMQQueue = "bad queue!" }, ErrNodeRMQQueueInvalid},
		{"interval too big", func(n *Node) { n.PullIntervalSec = 99999 }, ErrNodePullIntervalRange},
		{"batch too big", func(n *Node) { n.PullBatchSize = 99999 }, ErrNodePullBatchRange},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := base()
			c.mut(n)
			if err := n.Validate(); !errors.Is(err, c.want) {
				t.Fatalf("want %v, got %v", c.want, err)
			}
		})
	}
}

func TestNode_NormalizeForRootMethod(t *testing.T) {
	t.Run("pull node clears incompatible fields", func(t *testing.T) {
		n := &Node{
			RootMethod:       RootMethodRabbitMQAsync,
			URLMode:          URLModeFromRequest,
			IncomingAuthType: IncomingAuthTypeToken,
			AuthType:         AuthTypeTokenFromRequest,
		}
		cleared := n.NormalizeForRootMethod()
		if n.URLMode != URLModeStatic || n.IncomingAuthType != IncomingAuthTypeNone ||
			n.AuthType != AuthTypeNone {
			t.Fatalf("fields not normalized: %+v", n)
		}
		if len(cleared) != 3 {
			t.Fatalf("want 3 cleared fields, got %v", cleared)
		}
	})

	t.Run("non-pull node untouched", func(t *testing.T) {
		n := &Node{RootMethod: RootMethodRequestAsync, URLMode: URLModeFromRequest}
		if cleared := n.NormalizeForRootMethod(); cleared != nil {
			t.Fatalf("want nil, got %v", cleared)
		}
		if n.URLMode != URLModeFromRequest {
			t.Fatal("url_mode should remain from_request for non-pull node")
		}
	})
}

func TestRootMethod_IsPull(t *testing.T) {
	if !RootMethodRabbitMQAsync.IsPull() {
		t.Error("RabbitMQAsync must be pull")
	}
	if RootMethodRequest.IsPull() || RootMethodRequestAsync.IsPull() {
		t.Error("request/requestAsync must not be pull")
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
