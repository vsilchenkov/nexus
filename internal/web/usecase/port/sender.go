package port

import (
	"context"

	senderv1 "nexus/proto/sender/v1"
)

// SenderClient — вызов внешнего узла через Sender (§55). Нужен dry-run'у в
// реальном режиме: тест обязан идти ТЕМ ЖЕ путём и тем же HTTP-клиентом, что
// боевой трафик, иначе он врёт про сетевую доступность (у Web и Sender разные
// контейнеры) и теряет диагностику Sender'а — редиректы §50, retry с бэкоффом,
// лимит тела §43, TLS-настройки.
//
// Интерфейс объявлен на стороне консьюмера (§17.2 «accept interfaces»): зеркало
// receiver/usecase.SenderClient. Реализация — platform/grpcsender.
type SenderClient interface {
	Send(ctx context.Context, req *senderv1.SendRequest) (*senderv1.SendResponse, error)
}
