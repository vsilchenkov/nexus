package usecase

import (
	"errors"
	"net/url"
	"time"

	"nexus/internal/domain"
	"nexus/internal/domain/ackspec"
)

// AckResponse — готовый ответ узла на приём запроса в очередь (§83).
// nil в RouteAsyncResult означает «отвечаем как раньше».
type AckResponse struct {
	Status      int
	ContentType string
	Body        []byte
}

// renderAck собирает ответ по спеке узла.
//
// Вызывается ДО публикации в Kafka — это не деталь реализации, а требование:
// при on_error=error отказ обязан означать «запрос НЕ принят». Рендер после
// Produce вернул бы клиенту 400 на уже принятое сообщение, и клиент повторил бы
// пакет — то есть шина сама порождала бы дубли, ради устранения которых §83 и
// написан.
//
// Возвращает (nil, "", nil), если у узла нет спеки.
// При провале рендера с политикой default — (nil, <reason>, nil): отвечаем
// штатно, а reason уходит в метрику на стороне handler'а.
// При политике error — (nil, <reason>, domain.ErrAckRenderFailed).
//
// body и query обязаны быть УЖЕ очищенными от кред (§41): сюда передаются
// effBody и cleanQuery, а не исходные in.Body/in.Query.
func (u *RouteAsyncUsecase) renderAck(
	node *domain.Node, in RouteInput, id, remainder string, body []byte, query url.Values,
) (*AckResponse, string, error) {
	if node.AsyncAck == nil {
		return nil, "", nil
	}
	spec := node.AsyncAck
	queued := node.Status == domain.NodeStatusPaused

	tmpl, err := u.ackCache.Get(spec.Body, spec.ContentType)
	if err == nil {
		var out []byte
		out, err = tmpl.Render(&ackspec.Ctx{
			Body:        body,
			ContentType: in.Header.Get("Content-Type"),
			Query:       ackQuery(node, query),
			PathSuffix:  remainder,
			ID:          id,
			Node:        node.Path,
			Team:        in.TeamSlug,
			ReceivedAt:  time.Now().UTC(),
			Queued:      queued,
		})
		if err == nil {
			return &AckResponse{
				Status:      spec.EffectiveStatus(queued),
				ContentType: spec.ContentType.HTTPValue(),
				Body:        out,
			}, "", nil
		}
	}
	return u.ackFailure(node, spec, err)
}

// ackFailure разбирает отказ рендера по политике узла. Отдельной функцией,
// чтобы renderAck читался одним экраном.
func (u *RouteAsyncUsecase) ackFailure(node *domain.Node, spec *ackspec.Spec, err error) (*AckResponse, string, error) {
	reason := ackReason(err)
	// §51.9: молчаливая деградация здесь недопустима — интегратор получает НЕ
	// тот ответ, которого ждёт, и по коду 200 это неотличимо от нормы.
	u.logger.Warn("async ack template failed",
		u.logger.Str("op", "receiver.async_ack"),
		u.logger.Str("node", node.Path),
		u.logger.Str("node_id", node.ID),
		u.logger.Str("reason", reason),
		u.logger.Str("placeholder", ackPlaceholder(err)),
		u.logger.Str("on_error", string(spec.OnError)))

	if spec.OnError == ackspec.OnErrorError {
		return nil, reason, domain.ErrAckRenderFailed
	}
	return nil, reason, nil
}

// ackQuery убирает из query поле входящей авторизации (§41): CheckIncomingAuth
// его не вырезает, а эхо-шаблон не должен возвращать клиенту его же креду.
// Исходящая динамическая авторизация свою копию query чистит сама.
func ackQuery(node *domain.Node, query url.Values) url.Values {
	field := node.IncomingAuthDynamicField
	if node.IncomingAuthDynamicSource != domain.IncomingAuthSourceQuery || field == "" {
		return query
	}
	if _, present := query[field]; !present {
		return query
	}
	clean := make(url.Values, len(query))
	for k, v := range query {
		if k != field {
			clean[k] = v
		}
	}
	return clean
}

// ackReason — метка причины для метрики и лога. Замкнутое множество значений
// ackspec.Reason плюс «compile» для битой спеки, дошедшей до рантайма
// (валидация формы её отбивает, но узел мог быть правлен SQL'ем напрямую).
func ackReason(err error) string {
	var re *ackspec.RenderError
	if errors.As(err, &re) {
		return string(re.Reason)
	}
	if errors.Is(err, ackspec.ErrTemplateSyntax) {
		return "compile"
	}
	return "unknown"
}

func ackPlaceholder(err error) string {
	var re *ackspec.RenderError
	if errors.As(err, &re) {
		return re.Placeholder
	}
	return ""
}
