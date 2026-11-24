package usecase

import (
	"maps"
	"strconv"
	"strings"

	"golang.org/x/net/http/httpguts"

	"nexus/internal/domain"
)

// outgoingHeaders — заголовки исходящего запроса к получателю: пробрасываемые
// заголовки входа плюс X-Nexus-Id = ID записи лога доставки (§118.7).
//
// Карта клонируется: SendInput.Headers принадлежит вызывающему (конверту),
// а повторы того же сообщения должны получать один и тот же набор.
//
// Одноимённый заголовок клиента (узел мог пробросить его через forward_headers)
// удаляется в ЛЮБОМ регистре, а не перезаписывается по точному ключу: клиент
// HTTP ставит заголовки обходом карты, порядок обхода случаен, и ключ
// «x-nexus-id» мог бы победить значение шины через раз.
//
// ID, негодный для значения заголовка, не ставится вовсе (заголовок клиента
// при этом всё равно удаляется): у RabbitMQAsync ID — это AMQP message_id
// издателя (§27), и управляющий символ в нём заставил бы net/http отвергать
// КАЖДУЮ попытку ещё до отправки — сообщение уходило бы в DLQ вместо доставки.
// Доставить без ключа дедупликации лучше, чем не доставить.
func (u *SendUsecase) outgoingHeaders(in SendInput) map[string]string {
	out := make(map[string]string, len(in.Headers)+1)
	maps.Copy(out, in.Headers)
	if in.ID == "" {
		return out
	}
	for k := range out {
		if strings.EqualFold(k, domain.HeaderNexusID) {
			// §51.9: тихая подмена значения клиента — на debug видно, что
			// получатель получит не то, что прислал клиент.
			u.logger.Debug("send: client X-Nexus-Id replaced by delivery id",
				u.logger.Str("id", in.ID),
				u.logger.Str("node", in.NodePath))
			delete(out, k)
		}
	}
	if !httpguts.ValidHeaderFieldValue(in.ID) {
		u.logger.Debug("send: delivery id is not a valid header value, X-Nexus-Id omitted",
			u.logger.Str("id", strconv.Quote(in.ID)),
			u.logger.Str("node", in.NodePath))
		return out
	}
	out[domain.HeaderNexusID] = in.ID
	return out
}
