package port

import (
	"context"

	"nexus/internal/domain"
)

// InstanceProber — проверка доступности соседнего инстанса Nexus (§73.2).
//
// Реализация ходит по сети, поэтому usecase зависит только от этого интерфейса:
// в тестах он подменяется без поднятия HTTP-сервера.
type InstanceProber interface {
	// Probe опрашивает инстанс по базовому адресу (origin вида
	// scheme://host[:port], уже нормализованный domain.NormalizePeerBaseURL).
	//
	// Ошибку НЕ возвращает: недоступность соседа — это штатный результат
	// (статус unreachable/error), а не сбой операции. Иначе каждый вызывающий
	// был бы обязан отличать «инстанс лежит» от «проба сломалась», а массовый
	// опрос падал бы целиком из-за одного мёртвого адреса.
	Probe(ctx context.Context, baseURL string) InstanceProbeResult
}

// InstanceProbeResult — исход одной пробы.
type InstanceProbeResult struct {
	Status domain.PeerInstanceStatus
	// Version — версия Web Service соседа из GET /api/version.
	Version string
	// InstanceID — код инстанса соседа (instance.id, §70.1); пустая строка
	// валидна и означает ноду без суффикса.
	InstanceID string
	// LatencyMS — время ответа /api/version; nil, если ответа не было.
	LatencyMS *int
	// Error — короткая нормализованная причина отказа ("timeout", "http 502",
	// "invalid response"). Сырое тело ответа и текст ошибки транспорта сюда не
	// попадают: они могут нести адреса и куски конфигурации соседа.
	Error string
}
