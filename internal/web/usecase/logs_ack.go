package usecase

import (
	"context"
	"errors"
	"strings"

	"nexus/internal/domain"
	"nexus/internal/domain/ackspec"
)

// Почему блок пересчёта неприменим к записи (§84.9.1).
const (
	AckNotApplicableNoSpec       = "no_spec"         // у узла выключено изменение ответа
	AckNotApplicableSyncRecord   = "sync_record"     // запись прошла синхронным путём
	AckNotApplicableBodyNotSaved = "body_not_logged" // тела запроса в журнале нет
)

// Откуда взялось тело, на котором считали (§84.9.2).
const (
	AckBodySourceStored    = "stored"    // сохранено целиком
	AckBodySourceTruncated = "truncated" // усечено max_body_size
)

// AckWarnQueryNotStored — query входящего запроса в журнале не хранится вовсе
// (колонка parameters содержит query ИСХОДЯЩЕГО URL), поэтому `${ query.… }`
// вернёт «путь не найден». Предупреждение обязано быть: иначе оператор решит,
// что сломался шаблон.
const AckWarnQueryNotStored = "query_not_stored"

// AckFromLogReport — что шина ответила БЫ на этот запрос по текущему шаблону.
//
// Это пересчёт, а не факт. Отрендеренный ответ приёма не сохраняется нигде:
// Receiver отдаёт его в сокет и с ClickHouse не соединён вовсе. Все поля,
// помогающие оператору не принять расчёт за факт, — часть контракта, а не
// украшение (§84.9.3).
type AckFromLogReport struct {
	Applicable          bool
	NotApplicableReason string

	// OK=false — НЕ ошибка запроса: подстановка не удалась, и причину надо
	// показать так же, как в предпросмотре формы (§83.6).
	OK          bool
	Status      int
	ContentType string
	Body        string
	Reason      string
	Placeholder string
	Message     string

	// OnError — политика узла. При `error` провал подстановки означал бы, что
	// запрос НЕ принят и записи в журнале не было бы вовсе; при `default`
	// клиент получил бы обычный ответ. Оператор обязан видеть, какая из двух.
	OnError string

	// SpecChangedAfterRequest — узел менялся ПОСЛЕ этого запроса.
	// Признак консервативный: updated_at меняется от любой правки узла, не
	// только спеки. Ложная тревога дешевле молчания.
	SpecChangedAfterRequest bool
	SpecUpdatedAtMs         int64
	RequestAtMs             int64

	BodySource string
	Warnings   []string
}

// AckFromLog — пересчёт ответа шины по записи журнала (§84.9).
//
// Метод на LogsUsecase, а не отдельный usecase: здесь уже есть resolveNode со
// всеми гейтами (team-scope, «логи не настроены», §43.1 черновое имя таблицы) и
// доступ к записи. Отдельный usecase дублировал бы эти гейты — ровно тот класс
// расхождений, который ревизия §70 ловила постфактум.
func (u *LogsUsecase) AckFromLog(ctx context.Context, nodeID, teamID, logID string) (*AckFromLogReport, error) {
	n, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return nil, err
	}

	// Первый гейт — сам признак «изменение ответа включено». У узла со
	// стандартным ответом пересчитывать нечего.
	if n.AsyncAck == nil {
		u.logger.Debug("ack from log: node has no ack spec", u.logger.Str("node", n.Path))
		return &AckFromLogReport{NotApplicableReason: AckNotApplicableNoSpec}, nil
	}

	rec, err := u.logs.GetByID(ctx, n.ClickHouseTable, logID)
	if err != nil {
		return nil, err
	}

	// Второй гейт — ПО ЗАПИСИ, а не по текущему типу узла, и это не
	// перестраховка: по §83.5 спека переживает перевод узла requestAsync →
	// request и продолжает работать, пока узел на паузе (§3.6). Значит у
	// sync-узла могут лежать записи, чей ответ шаблон реально формировал, а у
	// бывшего async-узла — записи, к которым он уже неприменим.
	if rec.Type != domain.RootMethodRequestAsync {
		u.logger.Debug("ack from log: record went the sync path",
			u.logger.Str("node", n.Path), u.logger.Str("type", string(rec.Type)))
		return &AckFromLogReport{NotApplicableReason: AckNotApplicableSyncRecord}, nil
	}

	if strings.TrimSpace(rec.Request) == "" {
		u.logger.Debug("ack from log: request body is not stored",
			u.logger.Str("node", n.Path), u.logger.Any("log_request_body", n.LogRequestBody))
		return &AckFromLogReport{NotApplicableReason: AckNotApplicableBodyNotSaved}, nil
	}

	rep := &AckFromLogReport{
		Applicable:      true,
		OnError:         string(n.AsyncAck.OnError),
		SpecUpdatedAtMs: n.UpdatedAt.UnixMilli(),
		RequestAtMs:     rec.DateRequest.UnixMilli(),
		// Тело считается усечённым, когда сохранённая копия короче
		// зафиксированного размера запроса (max_body_size, §42.10).
		BodySource: AckBodySourceStored,
		Warnings:   []string{AckWarnQueryNotStored},
	}
	if rec.RequestSize > int64(len(rec.Request)) {
		rep.BodySource = AckBodySourceTruncated
	}
	rep.SpecChangedAfterRequest = n.UpdatedAt.After(rec.DateRequest)

	spec := *n.AsyncAck
	spec.SetDefaults()
	tmpl, err := ackspec.Compile(spec.Body, spec.ContentType)
	if err != nil {
		// Битая спека в БД (правка SQL мимо валидации) — не 500: оператору
		// нужна причина, а не «сервер сломался».
		u.logger.Warn("ack from log: spec compile failed",
			u.logger.Str("node", n.Path), u.logger.Err(err))
		rep.Message = err.Error()
		return rep, nil
	}

	out, err := tmpl.Render(&ackspec.Ctx{
		Body: []byte(rec.Request),
		// Content-Type входящего запроса в журнале не хранится. Пустое значение
		// трактуется как JSON — для не-JSON исходника реальный ответ мог
		// отличаться, и об этом сказано в интерфейсе.
		ContentType: "",
		// Query входящего запроса не хранится тоже: колонка parameters несёт
		// query ИСХОДЯЩЕГО URL. Подставлять её сюда было бы прямой ложью.
		Query:      nil,
		PathSuffix: rec.Method,
		ID:         rec.ID,
		Node:       n.Path,
		Team:       n.TeamID,
		ReceivedAt: rec.DateRequest,
		// Признак «узел был на паузе в момент приёма» не хранится.
		Queued: false,
	})
	if err != nil {
		u.logger.Debug("ack from log: render failed",
			u.logger.Str("node", n.Path), u.logger.Err(err))
		rep.Message = err.Error()
		var re *ackspec.RenderError
		if errors.As(err, &re) {
			rep.Reason = string(re.Reason)
			rep.Placeholder = re.Placeholder
		}
		return rep, nil
	}

	rep.OK = true
	rep.Status = spec.EffectiveStatus(false)
	rep.ContentType = spec.ContentType.HTTPValue()
	rep.Body = string(out)
	return rep, nil
}
