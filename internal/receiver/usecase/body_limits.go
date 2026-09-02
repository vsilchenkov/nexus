package usecase

import (
	"sync/atomic"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

// bodyLimits — согласованная пара действующих лимитов тела, байты.
//
// Хранится и подменяется ЦЕЛИКОМ, а не двумя отдельными atomic'ами: handleSync
// читает оба значения (тело — по sync-лимиту, а запрос к узлу на паузе §3.6
// уходит в очередь и режется async-лимитом), и раздельные атомики позволили бы
// увидеть половину старой пары и половину новой — с нарушенным инвариантом
// async ≤ sync.
type bodyLimits struct {
	sync  int
	async int
}

// BodyLimitsProvider — атомарный держатель действующих лимитов размера тела (§97).
//
// Разделяет два разных понятия:
//   - ПОТОЛОК (capSync/capAsync из конфига `receiver.max_body_bytes` и
//     `max_async_body_bytes») — сколько шина способна переварить физически: под
//     него настроены nginx, gRPC, Kafka и память сервисов. Меняется только выкатом;
//   - РАБОЧИЙ лимит — сколько администратор разрешает сегодня. Приходит из
//     app_settings, меняется в интерфейсе и применяется без рестарта.
//
// Безопасен для конкурентного доступа: чтение/запись через atomic.Pointer.
type BodyLimitsProvider struct {
	cur      atomic.Pointer[bodyLimits]
	capSync  int
	capAsync int
	logger   logging.Logger
}

// NewBodyLimitsProvider создаёт провайдер с потолками из конфига. До первого
// Set действует дефолт домена (domain.BodyLimitDefaultBytes), зажатый потолком:
// так сервис, поднятый до того, как приехали настройки, работает по
// консервативному значению, а не по потолку.
//
// Потолок ≤ 0 означает «не задан» и не зажимает — конфиг без ключа ведёт себя
// как раньше.
func NewBodyLimitsProvider(capSync, capAsync int, logger logging.Logger) *BodyLimitsProvider {
	p := &BodyLimitsProvider{capSync: capSync, capAsync: capAsync, logger: logger}
	p.Set(domain.BodyLimitDefaultBytes, domain.BodyLimitDefaultBytes)
	return p
}

// Limits возвращает согласованную пару действующих лимитов (sync, async).
// Nil-safe: у Handler'а, собранного литералом в тестах, провайдера может не
// быть — тогда действует дефолт домена.
func (p *BodyLimitsProvider) Limits() (syncBytes, asyncBytes int) {
	if p == nil {
		return domain.BodyLimitDefaultBytes, domain.BodyLimitDefaultBytes
	}
	l := p.cur.Load()
	if l == nil {
		return domain.BodyLimitDefaultBytes, domain.BodyLimitDefaultBytes
	}
	return l.sync, l.async
}

// Sync возвращает действующий лимит тела синхронного запроса.
func (p *BodyLimitsProvider) Sync() int {
	s, _ := p.Limits()
	return s
}

// Async возвращает действующий лимит тела, уезжающего в очередь.
func (p *BodyLimitsProvider) Async() int {
	_, a := p.Limits()
	return a
}

// Set задаёт новую пару лимитов (байты) и нормализует её:
//
//   - значение ≤ 0 трактуется как «не задано» → дефолт домена;
//   - каждое зажимается своим потолком из конфига (значение могло быть
//     сохранено, когда потолок был выше);
//   - async не может превышать sync — иначе запрос к узлу на паузе (§3.6)
//     принимался бы в очередь крупнее, чем разрешено принять напрямую.
//
// Зажатие потолком логируется как warn: это расхождение между тем, что
// администратор видит в интерфейсе, и тем, что реально применяется.
func (p *BodyLimitsProvider) Set(syncBytes, asyncBytes int) {
	if syncBytes <= 0 {
		syncBytes = domain.BodyLimitDefaultBytes
	}
	if asyncBytes <= 0 {
		asyncBytes = domain.BodyLimitDefaultBytes
	}

	syncBytes = p.clamp(syncBytes, p.capSync, "max_body_bytes")
	asyncBytes = p.clamp(asyncBytes, p.capAsync, "max_async_body_bytes")

	if asyncBytes > syncBytes {
		if p.logger != nil {
			p.logger.Warn("async body limit exceeds sync limit; using sync limit for the queue path",
				p.logger.Int("max_async_body_bytes", asyncBytes),
				p.logger.Int("max_body_bytes", syncBytes))
		}
		asyncBytes = syncBytes
	}

	p.cur.Store(&bodyLimits{sync: syncBytes, async: asyncBytes})
}

// clamp зажимает значение потолком и предупреждает, если пришлось зажать.
func (p *BodyLimitsProvider) clamp(v, capacity int, field string) int {
	out, clamped := domain.ClampBodyLimit(v, capacity)
	if clamped && p.logger != nil {
		p.logger.Warn("configured body limit exceeds the ceiling from config; using the ceiling",
			p.logger.Str("field", field),
			p.logger.Int("requested_bytes", v),
			p.logger.Int("ceiling_bytes", capacity))
	}
	return out
}
