package usecase

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/sender/usecase/port"
)

// §69.3: адрес доставки для static-узла пересобирается по актуальному конфигу.
// Без этого исправление target_url не действовало на уже принятые сообщения:
// они падали до истечения DLQ TTL (боевой инцидент 2026-07-27).
func TestResolveTargetURL(t *testing.T) {
	t.Parallel()

	staticNode := func(target string) *domain.Node {
		return &domain.Node{URLMode: domain.URLModeStatic, TargetURL: target}
	}

	cases := []struct {
		name string
		node *domain.Node
		env  Envelope
		want string
	}{
		{
			name: "исправленная схема применяется к застрявшему сообщению",
			node: staticNode("https://vozovoz.lenobl.com"),
			env: Envelope{
				TargetURL:   "vozovoz.lenobl.com/vozovoz/getstat?date_since=x",
				RequestPath: "vozovoz/getstat",
			},
			want: "https://vozovoz.lenobl.com/vozovoz/getstat?date_since=x",
		},
		{
			name: "смена хоста",
			node: staticNode("https://new.example.com"),
			env:  Envelope{TargetURL: "https://old.example.com/hook?a=1", RequestPath: "hook"},
			want: "https://new.example.com/hook?a=1",
		},
		{
			name: "без path-passthrough хвост не приклеивается",
			node: staticNode("https://example.com/fixed/path"),
			env:  Envelope{TargetURL: "https://example.com/fixed/path?a=1"},
			want: "https://example.com/fixed/path?a=1",
		},
		{
			name: "query конверта сохраняется целиком",
			node: staticNode("https://example.com/hook"),
			env:  Envelope{TargetURL: "https://example.com/hook?a=1&b=2"},
			want: "https://example.com/hook?a=1&b=2",
		},
		{
			name: "query самого target_url не дублируется (она уже в конверте)",
			node: staticNode("https://example.com/hook?fixed=1"),
			env:  Envelope{TargetURL: "https://example.com/hook?fixed=1&a=2"},
			want: "https://example.com/hook?fixed=1&a=2",
		},
		{
			name: "без query — чистый адрес",
			node: staticNode("https://example.com/hook"),
			env:  Envelope{TargetURL: "https://example.com/hook"},
			want: "https://example.com/hook",
		},
		{
			name: "from_request: пересобрать нельзя, адрес из конверта",
			node: &domain.Node{URLMode: domain.URLModeFromRequest, TargetURL: "https://ignored.example.com"},
			env:  Envelope{TargetURL: "https://partner.example.com/hook?a=1"},
			want: "https://partner.example.com/hook?a=1",
		},
		{
			name: "битый target_url узла: fallback на конверт",
			node: staticNode("example.com"),
			env:  Envelope{TargetURL: "https://example.com/hook?a=1"},
			want: "https://example.com/hook?a=1",
		},
		{
			name: "пустой target_url узла: fallback на конверт",
			node: staticNode(""),
			env:  Envelope{TargetURL: "https://example.com/hook"},
			want: "https://example.com/hook",
		},
		{
			name: "хвост из «../» не выводит за пределы базового пути",
			node: staticNode("https://example.com/base"),
			env:  Envelope{TargetURL: "https://example.com/base/x", RequestPath: "../escape"},
			want: "https://example.com/escape",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, c.want, resolveTargetURL(c.node, c.env))
		})
	}
}

func TestUrlWithoutQuery(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "https://h/p", urlWithoutQuery("https://h/p?token=secret"))
	assert.Equal(t, "https://h/p", urlWithoutQuery("https://h/p#frag"))
	assert.Equal(t, "host/p", urlWithoutQuery("host/p?a=1"), "строка без схемы (боевой случай)")
	assert.Equal(t, "https://h/p", urlWithoutQuery("https://h/p"))
}

// TestReprocess_RebuiltURL_DeliversAfterNodeFix — регрессия боевого инцидента
// целиком: сообщение принято с адресом без схемы, узел исправлен, DLQ-проход
// обязан доставить его по новому адресу.
func TestReprocess_RebuiltURL_DeliversAfterNodeFix(t *testing.T) {
	t.Parallel()

	node := enabledNode()
	node.URLMode = domain.URLModeStatic
	node.TargetURL = "https://vozovoz.lenobl.com" // уже исправлен оператором
	node.PathPassthrough = true

	h := newReprocessorForTest(t, node, nil, &port.HTTPResponse{StatusCode: 200}, nil, nil)

	env := Envelope{
		ID:          "id-1",
		NodePath:    node.Path,
		Method:      "GET",
		TargetURL:   "vozovoz.lenobl.com/vozovoz/getstat?date_since=x", // снимок без схемы
		RequestPath: "vozovoz/getstat",
		ReceivedAt:  time.Now(),
	}
	raw, err := json.Marshal(env)
	require.NoError(t, err)

	got := h.proc.ProcessMessage(context.Background(), raw, nil)
	assert.Equal(t, ReprocessCommit, got, "доставлено → commit, сообщение больше не циркулирует")
	assert.Equal(t, "https://vozovoz.lenobl.com/vozovoz/getstat?date_since=x", h.httpc.lastReqURL,
		"запрос ушёл по адресу из свежего конфига, а не по снимку из конверта")
	assert.Empty(t, h.dlq.produced, "republish в хвост DLQ не нужен")
}
