package domain_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"nexus/internal/domain"
)

// multipartHead — начало §68-плейсхолдера в том виде, в каком его пишет
// write-path: media type первой строкой, дальше строка-часть.
const multipartHead = "multipart/form-data\n- part 1: name=\"file\"; filename=\"a.pdf\"; type=application/pdf; size=1024\n[1 part, body 1200 bytes]"

func TestIsTruncatedLogBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		hasMarker   bool
		storedBytes int64
		fullSize    int64
		want        bool
	}{
		{"целое тело", false, 100, 100, false},
		{"тела нет вовсе", false, 0, 0, false},
		{"размерный признак", false, 100, 5000, true},
		{"маркер без размерного признака", true, 100, 100, true},
		// Многобайтовый текст, обрезанный на одну руну: копия С МАРКЕРОМ длиннее
		// оригинала, и размерный признак молчит — спасает только маркер.
		{"кириллица, обрезка на руну", true, 2*10 + 14, 2 * 11, true},
		{"кириллица, обрезка на руну, без маркера", false, 2*10 + 14, 2 * 11, false},
		// Строки до бэкфилла §42.10: request_size = длина уже усечённой копии.
		{"legacy-строка, размер равен копии", false, 500, 500, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, domain.IsTruncatedLogBody(tt.hasMarker, tt.storedBytes, tt.fullSize))
		})
	}
}

func TestReplayEffectiveMethod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		incoming   domain.HTTPMethod
		loggedVerb string
		want       string
	}{
		{"входящий метод узла", domain.HTTPMethodPUT, "GET", "PUT"},
		{"ANY берёт глагол записи", domain.HTTPMethodAny, "DELETE", "DELETE"},
		{"пустой входящий → POST", "", "GET", "POST"},
		{"ANY без глагола записи → POST", domain.HTTPMethodAny, "", "POST"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, domain.ReplayEffectiveMethod(tt.incoming, tt.loggedVerb))
		})
	}
}

func TestClassifyReplayCandidate(t *testing.T) {
	t.Parallel()

	// async — пригодная async-запись; каждый подтест портит ровно одно поле.
	async := func() domain.ReplayCandidate {
		return domain.ReplayCandidate{
			ID:          "id-1",
			Type:        domain.RootMethodRequestAsync,
			HTTPMethod:  "POST",
			BodyHead:    `{"logs":[]}`,
			StoredBytes: 11,
			RequestSize: 11,
		}
	}

	tests := []struct {
		name             string
		mutate           func(*domain.ReplayCandidate)
		effectiveMethod  string
		skipReplayCopies bool
		want             domain.ReplaySkipReason
	}{
		{
			name:            "обычная async-запись пригодна",
			mutate:          func(*domain.ReplayCandidate) {},
			effectiveMethod: "POST",
			want:            domain.ReplaySkipNone,
		},
		{
			name:            "синхронная запись",
			mutate:          func(c *domain.ReplayCandidate) { c.Type = domain.RootMethodRequest },
			effectiveMethod: "POST",
			want:            domain.ReplaySkipNotAsync,
		},
		{
			name:            "pull-запись тоже не async-путь",
			mutate:          func(c *domain.ReplayCandidate) { c.Type = domain.RootMethodRabbitMQAsync },
			effectiveMethod: "POST",
			want:            domain.ReplaySkipNotAsync,
		},
		{
			name:            "multipart-плейсхолдер",
			mutate:          func(c *domain.ReplayCandidate) { c.BodyHead = multipartHead; c.RequestSize = 1200 },
			effectiveMethod: "POST",
			want:            domain.ReplaySkipMultipart,
		},
		{
			name: "усечённое тело",
			mutate: func(c *domain.ReplayCandidate) {
				c.BodyHead = strings.Repeat("a", 10)
				c.StoredBytes = 10
				c.RequestSize = 5000
			},
			effectiveMethod: "POST",
			want:            domain.ReplaySkipTruncated,
		},
		{
			name: "усечение по маркеру, размеры совпали",
			mutate: func(c *domain.ReplayCandidate) {
				c.BodyTruncationMarker = true
			},
			effectiveMethod: "POST",
			want:            domain.ReplaySkipTruncated,
		},
		{
			name:            "тело не сохранено",
			mutate:          func(c *domain.ReplayCandidate) { c.BodyHead = ""; c.StoredBytes = 0; c.RequestSize = 0 },
			effectiveMethod: "POST",
			want:            domain.ReplaySkipBodyMissing,
		},
		{
			name:            "пустое тело у GET — не отказ",
			mutate:          func(c *domain.ReplayCandidate) { c.BodyHead = ""; c.StoredBytes = 0; c.RequestSize = 0 },
			effectiveMethod: "GET",
			want:            domain.ReplaySkipNone,
		},
		{
			name:             "запись-повтор исключается",
			mutate:           func(c *domain.ReplayCandidate) { c.IsReplayCopy = true },
			effectiveMethod:  "POST",
			skipReplayCopies: true,
			want:             domain.ReplaySkipReplayCopy,
		},
		{
			name:             "запись-повтор при снятом флажке пригодна",
			mutate:           func(c *domain.ReplayCandidate) { c.IsReplayCopy = true },
			effectiveMethod:  "POST",
			skipReplayCopies: false,
			want:             domain.ReplaySkipNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := async()
			tt.mutate(&c)
			assert.Equal(t, tt.want, domain.ClassifyReplayCandidate(c, tt.effectiveMethod, tt.skipReplayCopies))
		})
	}
}

// TestClassifyReplayCandidate_MultipartBeforeTruncated фиксирует ПОРЯДОК
// проверок (§85.3): плейсхолдер §68 короче исходного тела, поэтому размерный
// признак усечения на нём срабатывает всегда. Если проверки поменять местами,
// оператор увидит «тело обрезано» вместо «multipart» — причина подменяется
// неточной, и разбор уходит в настройку max_body_size, где всё в порядке.
func TestClassifyReplayCandidate_MultipartBeforeTruncated(t *testing.T) {
	t.Parallel()

	c := domain.ReplayCandidate{
		Type:        domain.RootMethodRequestAsync,
		BodyHead:    multipartHead,
		StoredBytes: int64(len(multipartHead)),
		RequestSize: 4 * 1024 * 1024, // вложение много больше плейсхолдера
	}
	assert.True(t, domain.IsTruncatedLogBody(c.BodyTruncationMarker, c.StoredBytes, c.RequestSize),
		"предусловие теста: размерный признак усечения на плейсхолдере срабатывает")
	assert.Equal(t, domain.ReplaySkipMultipart, domain.ClassifyReplayCandidate(c, "POST", true))
}
