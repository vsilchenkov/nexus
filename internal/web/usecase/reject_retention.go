package usecase

import (
	"sync/atomic"

	"nexus/internal/domain"
)

// RejectRetentionProvider — атомарный держатель срока хранения журнала
// отказов в днях (§94.5).
//
// Тот же приём, что у SessionTTLProvider (§34.2): на старте сидится значением
// из app_settings, обновляется hot-reload'ом секции general без рестарта.
// Ноль — не «не задано», а «сбор выключен и накопленное удаляется», поэтому
// хранится ИМЕННО заданное значение, а подстановка дефолта делается один раз
// при записи.
//
// Безопасен для конкурентного доступа.
type RejectRetentionProvider struct {
	days atomic.Int64
}

// NewRejectRetentionProvider создаёт провайдер с дефолтом домена: до первого
// чтения app_settings журнал ведётся со стандартным сроком.
func NewRejectRetentionProvider() *RejectRetentionProvider {
	p := &RejectRetentionProvider{}
	p.days.Store(int64(domain.RejectedRetentionDefaultDays))
	return p
}

// Days возвращает действующий срок хранения. 0 = журнал выключен.
func (p *RejectRetentionProvider) Days() int { return int(p.days.Load()) }

// Enabled сообщает, ведётся ли журнал.
func (p *RejectRetentionProvider) Enabled() bool { return p.Days() > 0 }

// Set задаёт срок из настройки: nil разворачивается в дефолт домена,
// отрицательное значение (в БД его быть не должно — валидатор не пропустит)
// трактуется как выключение.
func (p *RejectRetentionProvider) Set(days *int) {
	// max(…, 0): отрицательное значение в БД появиться не должно (валидатор его
	// не пропустит), но провайдер обязан оставаться безопасным и с руками
	// поправленной строкой — отрицательный срок трактуется как «выключено».
	p.days.Store(int64(max(domain.RejectedRetentionOrDefault(days), 0)))
}
