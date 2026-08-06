package ackspec_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain/ackspec"
)

func validSpec() *ackspec.Spec {
	return &ackspec.Spec{
		Version:     ackspec.Version,
		ContentType: ackspec.ContentTypeJSON,
		Body:        `{"confirmedLogId": "${ body.logs[*].logId | max }"}`,
		OnError:     ackspec.OnErrorDefault,
	}
}

func TestSpec_Validate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		mutate  func(*ackspec.Spec)
		wantErr error
	}{
		{"valid", func(*ackspec.Spec) {}, nil},
		{"nil spec is valid", nil, nil},
		{"status 200 allowed", func(s *ackspec.Spec) { s.Status = 200 }, nil},
		{"status 202 allowed", func(s *ackspec.Spec) { s.Status = 202 }, nil},
		{"text/plain allowed", func(s *ackspec.Spec) {
			s.ContentType = ackspec.ContentTypeText
			s.Body = `${ body.id }`
		}, nil},
		{"unsupported version", func(s *ackspec.Spec) { s.Version = 2 }, ackspec.ErrSpecInvalid},
		{"unknown content type", func(s *ackspec.Spec) { s.ContentType = "application/xml" }, ackspec.ErrSpecInvalid},
		{"unknown on_error", func(s *ackspec.Spec) { s.OnError = "retry" }, ackspec.ErrSpecInvalid},
		{"status 404 rejected", func(s *ackspec.Spec) { s.Status = 404 }, ackspec.ErrSpecInvalid},
		{"status 500 rejected", func(s *ackspec.Spec) { s.Status = 500 }, ackspec.ErrSpecInvalid},
		{"status 199 rejected", func(s *ackspec.Spec) { s.Status = 199 }, ackspec.ErrSpecInvalid},
		{"empty body", func(s *ackspec.Spec) { s.Body = "" }, ackspec.ErrSpecInvalid},
		{"body over limit", func(s *ackspec.Spec) {
			s.Body = `{"a": "` + strings.Repeat("x", ackspec.MaxTemplateBytes) + `"}`
		}, ackspec.ErrTemplateSyntax},
		{"broken template", func(s *ackspec.Spec) { s.Body = `{"a": "${ body.x"}` }, ackspec.ErrTemplateSyntax},
		{"template yields invalid json", func(s *ackspec.Spec) { s.Body = `{"a": ${ body.x }}` }, ackspec.ErrTemplateSyntax},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var s *ackspec.Spec
			if tc.mutate != nil {
				s = validSpec()
				tc.mutate(s)
			}
			err := s.Validate()
			if tc.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestSpec_SetDefaults(t *testing.T) {
	t.Parallel()

	s := &ackspec.Spec{Body: `{"ok": true}`}
	s.SetDefaults()

	assert.Equal(t, ackspec.Version, s.Version)
	assert.Equal(t, ackspec.ContentTypeJSON, s.ContentType)
	assert.Equal(t, ackspec.OnErrorDefault, s.OnError)
	assert.Zero(t, s.Status, "status stays zero — «как было до §83»")
	require.NoError(t, s.Validate())

	var nilSpec *ackspec.Spec
	assert.NotPanics(t, func() { nilSpec.SetDefaults() })
}

// TestSpec_EffectiveStatus — нулевой status обязан сохранять прежние коды,
// иначе включение шаблона перекрасило бы nexus_requests_total{status}.
func TestSpec_EffectiveStatus(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		spec   *ackspec.Spec
		queued bool
		want   int
	}{
		{"zero status, normal accept", validSpec(), false, http.StatusOK},
		{"zero status, paused node", validSpec(), true, http.StatusAccepted},
		{"explicit status wins over queued", &ackspec.Spec{Status: 200}, true, http.StatusOK},
		{"nil spec, normal accept", nil, false, http.StatusOK},
		{"nil spec, paused node", nil, true, http.StatusAccepted},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.spec.EffectiveStatus(tc.queued))
		})
	}
}

func TestContentType_HTTPValue(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "application/json; charset=utf-8", ackspec.ContentTypeJSON.HTTPValue())
	assert.Equal(t, "text/plain; charset=utf-8", ackspec.ContentTypeText.HTTPValue())
}

// TestSpec_JSONRoundTrip — спека ездит в БД и в Redis-кеш узла как JSON;
// имена полей менять нельзя, не потеряв уже сохранённые настройки.
func TestSpec_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	raw := `{"version":1,"status":0,"content_type":"application/json","body":"{\"ok\": true}","on_error":"default"}`
	var s ackspec.Spec
	require.NoError(t, json.Unmarshal([]byte(raw), &s))
	assert.Equal(t, ackspec.ContentTypeJSON, s.ContentType)
	assert.Equal(t, `{"ok": true}`, s.Body)

	out, err := json.Marshal(&s)
	require.NoError(t, err)
	// status=0 не сериализуется (omitempty) — «как было» не занимает место в БД.
	assert.JSONEq(t, `{"version":1,"content_type":"application/json","body":"{\"ok\": true}","on_error":"default"}`, string(out))
}
