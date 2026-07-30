package domain_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
)

func TestNormalizePeerBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "https origin", in: "https://nexus-kz.vozovoz.ru", want: "https://nexus-kz.vozovoz.ru"},
		{name: "http origin", in: "http://nexus-kz.vozovoz.ru", want: "http://nexus-kz.vozovoz.ru"},
		{name: "host with port", in: "http://10.0.5.7:8000", want: "http://10.0.5.7:8000"},
		{name: "trailing slash trimmed", in: "https://node.example.ru/", want: "https://node.example.ru"},
		{name: "surrounding spaces trimmed", in: "  https://node.example.ru  ", want: "https://node.example.ru"},
		{name: "host lowercased", in: "https://Node.Example.RU", want: "https://node.example.ru"},
		{name: "scheme lowercased", in: "HTTPS://node.example.ru", want: "https://node.example.ru"},
		{name: "ipv6 host", in: "http://[2001:db8::1]:8000", want: "http://[2001:db8::1]:8000"},

		{name: "empty", in: "", wantErr: true},
		{name: "only spaces", in: "   ", wantErr: true},
		{name: "no scheme", in: "nexus-kz.vozovoz.ru", wantErr: true},
		{name: "no scheme with port", in: "10.0.5.7:8000", wantErr: true},
		{name: "unsupported scheme", in: "ftp://node.example.ru", wantErr: true},
		{name: "grpc scheme", in: "grpc://node.example.ru", wantErr: true},
		{name: "empty host", in: "https://", wantErr: true},
		// Путь запрещён: проба достраивает /api/version сама, иначе вышло бы
		// //api/version с дублирующимся слешем.
		{name: "with path", in: "https://node.example.ru/nexus", wantErr: true},
		{name: "with deep path", in: "https://node.example.ru/a/b", wantErr: true},
		{name: "with query", in: "https://node.example.ru?a=1", wantErr: true},
		{name: "with fragment", in: "https://node.example.ru#x", wantErr: true},
		// Креды в URL утекли бы в интерфейс и в журнал аудита.
		{name: "with userinfo", in: "https://user:pass@node.example.ru", wantErr: true},
		{name: "with user only", in: "https://user@node.example.ru", wantErr: true},
		{name: "too long", in: "https://" + strings.Repeat("a", 300) + ".ru", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := domain.NormalizePeerBaseURL(tt.in)
			if tt.wantErr {
				require.ErrorIs(t, err, domain.ErrPeerInstanceURLInvalid)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPeerInstanceValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      domain.PeerInstance
		wantErr error
	}{
		{
			name: "valid",
			in:   domain.PeerInstance{Title: "Казахстан", BaseURL: "https://nexus-kz.vozovoz.ru"},
		},
		{
			name:    "empty title",
			in:      domain.PeerInstance{Title: "", BaseURL: "https://node.example.ru"},
			wantErr: domain.ErrPeerInstanceTitleLength,
		},
		{
			name:    "blank title",
			in:      domain.PeerInstance{Title: "   ", BaseURL: "https://node.example.ru"},
			wantErr: domain.ErrPeerInstanceTitleLength,
		},
		{
			name:    "title too long",
			in:      domain.PeerInstance{Title: strings.Repeat("я", 65), BaseURL: "https://node.example.ru"},
			wantErr: domain.ErrPeerInstanceTitleLength,
		},
		{
			// Длина в рунах, а не в байтах: 64 кириллических символа занимают
			// 128 байт и по байтовой мерке ошибочно превысили бы лимит.
			name: "title 64 runes is allowed",
			in:   domain.PeerInstance{Title: strings.Repeat("я", 64), BaseURL: "https://node.example.ru"},
		},
		{
			name:    "comment too long",
			in:      domain.PeerInstance{Title: "ok", BaseURL: "https://node.example.ru", Comment: strings.Repeat("a", 256)},
			wantErr: domain.ErrPeerInstanceCommentLength,
		},
		{
			name:    "bad url",
			in:      domain.PeerInstance{Title: "ok", BaseURL: "node.example.ru"},
			wantErr: domain.ErrPeerInstanceURLInvalid,
		},
		{
			name:    "unknown status",
			in:      domain.PeerInstance{Title: "ok", BaseURL: "https://node.example.ru", LastStatus: "bogus"},
			wantErr: domain.ErrPeerInstanceStatusInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := tt.in
			err := p.Validate()
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			// Пустой статус доводится до "unknown", иначе запись нарушила бы
			// CHECK-констрейнт миграции 0032.
			assert.Equal(t, domain.PeerInstanceUnknown, p.LastStatus)
		})
	}
}

func TestPeerInstanceValidateNormalizesInPlace(t *testing.T) {
	t.Parallel()

	p := domain.PeerInstance{Title: "  Казахстан  ", BaseURL: "  HTTPS://Nexus-KZ.example.RU/  ", Comment: "  тест  "}
	require.NoError(t, p.Validate())

	assert.Equal(t, "Казахстан", p.Title)
	assert.Equal(t, "тест", p.Comment)
	// Уникальность адреса держится на индексе по lower(base_url): без
	// нормализации две записи на один инстанс разошлись бы регистром.
	assert.Equal(t, "https://nexus-kz.example.ru", p.BaseURL)
}

func TestPeerInstanceStatusValid(t *testing.T) {
	t.Parallel()

	for _, s := range []domain.PeerInstanceStatus{
		domain.PeerInstanceUnknown, domain.PeerInstanceActive, domain.PeerInstanceDegraded,
		domain.PeerInstanceUnreachable, domain.PeerInstanceError,
	} {
		assert.True(t, s.Valid(), "status %q must be valid", s)
	}
	for _, s := range []domain.PeerInstanceStatus{"", "ok", "down", "ACTIVE"} {
		assert.False(t, s.Valid(), "status %q must be invalid", s)
	}
}
