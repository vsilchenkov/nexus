package usecase

import (
	"context"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// signalPausedSweep — сигнал Sender'у пройти очередь паузы после отметок отмены
// (§113.3). Зовётся ОДИН раз на операцию и только для узла на паузе.
//
// Сообщения в очереди паузы бывают только у узла на паузе: у снятого с паузы
// раунд уже запущен сменой отпечатка узлов и встретит отметки сам, у
// выключенного очередь снимается тем же раундом. Каждый сигнал сбрасывает цель
// раунда, поэтому сигнал на запись («Повторить все» отменяет оригиналы по
// одному) или на очистку постороннего узла возвращал бы перезапись очереди
// паузы на каждом тике (ревизия §113).
//
// Best-effort: без сигнала отменённое снимется страховочным проходом
// (backstop), поэтому ошибку не возвращаем — операция отмены уже состоялась.
func signalPausedSweep(ctx context.Context, s port.PausedSweepSignaler, node *domain.Node, logger logging.Logger) {
	if s == nil || node.Status != domain.NodeStatusPaused {
		return
	}
	if _, err := s.Bump(ctx); err != nil {
		logger.Warn("paused sweep signal failed; cancelled messages wait for the backstop pass",
			logger.Str("node_path", node.Path), logger.Err(err))
		return
	}
	logger.Debug("paused sweep signalled after cancel", logger.Str("node_path", node.Path))
}
