package usecase

import (
	"context"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain/ackspec"
	"nexus/internal/platform/logging"
)

func previewSpec(body string) *ackspec.Spec {
	return &ackspec.Spec{
		Version:     ackspec.Version,
		ContentType: ackspec.ContentTypeJSON,
		Body:        body,
		OnError:     ackspec.OnErrorDefault,
	}
}

// TestAckPreview_Sigur — оператор видит ровно тот ответ, который получит СКУД.
func TestAckPreview_Sigur(t *testing.T) {
	t.Parallel()

	uc := NewAckPreviewUsecase(logging.NewNoop())

	rep, err := uc.Run(context.Background(), AckPreviewRequest{
		Spec:       previewSpec(`{"confirmedLogId": "${ body.logs[*].logId | max }"}`),
		SampleBody: []byte(`{"logs":[{"logId":79154},{"logId":79000}]}`),
	})

	require.NoError(t, err)
	assert.True(t, rep.OK)
	assert.Equal(t, `{"confirmedLogId": 79154}`, rep.Body)
	assert.Equal(t, 200, rep.Status)
	assert.Equal(t, "application/json; charset=utf-8", rep.ContentType)
}

// TestAckPreview_FailureIsNotAnError — провал подстановки приходит отчётом с
// причиной, а не ошибкой вызова: оператору нужно увидеть, ЧТО не так.
func TestAckPreview_FailureIsNotAnError(t *testing.T) {
	t.Parallel()

	uc := NewAckPreviewUsecase(logging.NewNoop())

	cases := []struct {
		name       string
		spec       *ackspec.Spec
		body       string
		ct         string
		wantReason ackspec.Reason
	}{
		{
			name:       "path not found",
			spec:       previewSpec(`{"a": "${ body.missing }"}`),
			body:       `{"present":1}`,
			wantReason: ackspec.ReasonPathNotFound,
		},
		{
			name:       "ambiguous without function",
			spec:       previewSpec(`{"a": "${ body.logs[*].logId }"}`),
			body:       `{"logs":[{"logId":1},{"logId":2}]}`,
			wantReason: ackspec.ReasonAmbiguous,
		},
		{
			name:       "sample body is not json",
			spec:       previewSpec(`{"a": "${ body.x }"}`),
			body:       `not json`,
			wantReason: ackspec.ReasonBodyNotJSON,
		},
		{
			name:       "multipart sample",
			spec:       previewSpec(`{"a": "${ body.x }"}`),
			body:       "--b\r\n",
			ct:         "multipart/form-data; boundary=b",
			wantReason: ackspec.ReasonBodyNotJSON,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rep, err := uc.Run(context.Background(), AckPreviewRequest{
				Spec:        tc.spec,
				SampleBody:  []byte(tc.body),
				ContentType: tc.ct,
			})

			require.NoError(t, err, "провал рендера не должен быть ошибкой вызова")
			assert.False(t, rep.OK)
			assert.Equal(t, string(tc.wantReason), rep.Reason)
			assert.NotEmpty(t, rep.Message)
		})
	}
}

// TestAckPreview_ReportsProblemPlaceholder — без указания на конкретную
// подстановку отчёт бесполезен в шаблоне с десятком полей.
func TestAckPreview_ReportsProblemPlaceholder(t *testing.T) {
	t.Parallel()

	uc := NewAckPreviewUsecase(logging.NewNoop())

	rep, err := uc.Run(context.Background(), AckPreviewRequest{
		Spec:       previewSpec(`{"ok": true, "cursor": "${ body.cursor }"}`),
		SampleBody: []byte(`{}`),
	})

	require.NoError(t, err)
	assert.False(t, rep.OK)
	assert.Contains(t, rep.Placeholder, "body.cursor")
}

// TestAckPreview_InvalidSpecIsAnError — невалидная спека правится в форме,
// поэтому приходит ошибкой (её handler мапит в 400 с подсветкой поля).
func TestAckPreview_InvalidSpecIsAnError(t *testing.T) {
	t.Parallel()

	uc := NewAckPreviewUsecase(logging.NewNoop())

	t.Run("broken template", func(t *testing.T) {
		t.Parallel()
		_, err := uc.Run(context.Background(), AckPreviewRequest{
			Spec: previewSpec(`{"a": "${ body.x"}`), SampleBody: []byte(`{}`),
		})
		require.ErrorIs(t, err, ackspec.ErrTemplateSyntax)
	})

	t.Run("non-2xx status", func(t *testing.T) {
		t.Parallel()
		spec := previewSpec(`{"ok": true}`)
		spec.Status = 404
		_, err := uc.Run(context.Background(), AckPreviewRequest{Spec: spec})
		require.ErrorIs(t, err, ackspec.ErrSpecInvalid)
	})

	t.Run("no spec", func(t *testing.T) {
		t.Parallel()
		_, err := uc.Run(context.Background(), AckPreviewRequest{})
		require.Error(t, err)
	})
}

// TestAckPreview_NonBodySources — предпросмотр умеет и остальные источники,
// иначе шаблон с query/path проверить было бы нечем.
func TestAckPreview_NonBodySources(t *testing.T) {
	t.Parallel()

	uc := NewAckPreviewUsecase(logging.NewNoop())

	rep, err := uc.Run(context.Background(), AckPreviewRequest{
		Spec:        previewSpec(`{"c": "${ query.code }", "p": "${ path.segment[0] }", "n": "${ nexus.node }"}`),
		SampleQuery: url.Values{"code": {"abc"}},
		SamplePath:  "/2026/08",
	})

	require.NoError(t, err)
	require.True(t, rep.OK, "reason=%s message=%s", rep.Reason, rep.Message)
	assert.Equal(t, `{"c": "abc", "p": "2026", "n": "preview"}`, rep.Body)
}

// TestAckPreview_TextPlain — второй формат v1 тоже проверяем, включая правило
// «кавычки в text/plain остаются буквальными».
func TestAckPreview_TextPlain(t *testing.T) {
	t.Parallel()

	uc := NewAckPreviewUsecase(logging.NewNoop())
	spec := previewSpec(`${ body.code }`)
	spec.ContentType = ackspec.ContentTypeText

	rep, err := uc.Run(context.Background(), AckPreviewRequest{
		Spec: spec, SampleBody: []byte(`{"code":"vk-confirm-42"}`),
	})

	require.NoError(t, err)
	require.True(t, rep.OK)
	assert.Equal(t, "vk-confirm-42", rep.Body)
	assert.Equal(t, "text/plain; charset=utf-8", rep.ContentType)
}
