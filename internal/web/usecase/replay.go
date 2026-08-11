package usecase

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	rcv "nexus/internal/receiver/usecase"
	"nexus/internal/web/usecase/port"
)

// RateLimiter — минимальный интерфейс, нужный replay (per-user).
//
// Чтобы не тянуть в usecase сторонний пакет ratelimit, объявляем интерфейс
// на месте — реализация (Redis) подсовывается из main (§17.2 ТЗ).
type RateLimiter interface {
	Allow(ctx context.Context, key string, limit int) (bool, error)
}

// ReplayTeamResolver — минимальный интерфейс резолва команды узла (§18):
// узел не-default команды доступен на Receiver только по пути со слагом
// (/api/v1/request/<team_slug>/<path>), поэтому replay обязан его подставить.
// Реализуется port.TeamRepo; nil допустим (single-team инсталляции/тесты) —
// тогда путь уходит без слага, как до multi-tenancy.
type ReplayTeamResolver interface {
	GetByID(ctx context.Context, id string) (*domain.Team, error)
}

// ReplayOptions — переопределения, которые UI может прислать в диалоге §7.4.1.
type ReplayOptions struct {
	BodyOverride []byte // если nil — используем оригинальное тело лога
	SyncOverride bool   // true → переключить async на sync
	UseNodeAuth  bool   // true (по умолчанию) — берём текущий конфиг узла; false — без авторизации в replay
	CustomAuth   string // если непусто — override на конкретный Authorization-заголовок
	// ParamsOverride — query-параметры вместо orig.Parameters (строка вида
	// "a=1&b=2"). nil — взять из лога; пустая непустышка ("" не nil) — replay
	// без параметров. __replay_of добавляется поверх в любом случае.
	ParamsOverride *string
}

// ReplayResult — что вернётся клиенту в ответ на POST /api/logs/{id}/replay.
type ReplayResult struct {
	NewLogID    string            `json:"new_log_id"`
	StatusCode  int               `json:"status_code"`
	DurationMs  int64             `json:"duration_ms"`
	BodyPreview string            `json:"body_preview"`
	Headers     map[string]string `json:"headers,omitempty"`
}

// ReplayUsecase — §7.4.1 ТЗ.
type ReplayUsecase struct {
	logs       port.LogReader
	nodes      port.NodeRepo
	dispatcher port.ReceiverDispatcher
	rl         RateLimiter
	audit      *AuditUsecase
	cancel     port.QueueCancelWriter // §36.11: отмена оригиналов при «Повторить все» (nil без Redis)
	cleaner    port.FailedLogsCleaner // §79.2: уборка строк done=0 после успешного повтора (nil без CH)
	teams      ReplayTeamResolver     // §18: слаг команды узла для пути реинъекции (nil = без слага)
	retention  time.Duration          // TTL tombstone'а отмены (= retention топика)
	rateLimit  int                    // запросов/мин на пользователя (§7.4.1: 10)
	logger     logging.Logger
}

// ReplayOption — необязательная зависимость ReplayUsecase. Вариадическая форма
// вместо очередного позиционного аргумента: у конструктора их уже десять.
type ReplayOption func(*ReplayUsecase)

// WithFailedCleaner подключает уборку истории неудачных прогонов после
// массового повтора (§79.2). Без неё «Повторить все» работает как раньше:
// сообщения уходят, но записи остаются в «Неудачных доставках» до очистки.
func WithFailedCleaner(c port.FailedLogsCleaner) ReplayOption {
	return func(u *ReplayUsecase) { u.cleaner = c }
}

func NewReplayUsecase(
	logs port.LogReader,
	nodes port.NodeRepo,
	dispatcher port.ReceiverDispatcher,
	rl RateLimiter,
	audit *AuditUsecase,
	rateLimit int,
	logger logging.Logger,
) *ReplayUsecase {
	return NewReplayUsecaseWithCancel(logs, nodes, dispatcher, rl, audit, rateLimit, nil, nil, 0, logger)
}

// NewReplayUsecaseWithCancel — конструктор с cancel-writer'ом для «Повторить все»
// (§36.11): успешно пере-инжектированные сообщения отменяют свой оригинал в DLQ,
// чтобы авто-репроцессор не доставил их повторно. cancel может быть nil (без Redis)
// — тогда массовый replay не отменяет оригиналы (как построчный «Повторить»).
func NewReplayUsecaseWithCancel(
	logs port.LogReader,
	nodes port.NodeRepo,
	dispatcher port.ReceiverDispatcher,
	rl RateLimiter,
	audit *AuditUsecase,
	rateLimit int,
	cancel port.QueueCancelWriter,
	teams ReplayTeamResolver,
	retention time.Duration,
	logger logging.Logger,
	opts ...ReplayOption,
) *ReplayUsecase {
	if rateLimit <= 0 {
		rateLimit = 10
	}
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	u := &ReplayUsecase{
		logs:       logs,
		nodes:      nodes,
		dispatcher: dispatcher,
		rl:         rl,
		audit:      audit,
		cancel:     cancel,
		teams:      teams,
		retention:  retention,
		rateLimit:  rateLimit,
		logger:     logger,
	}
	for _, opt := range opts {
		opt(u)
	}
	return u
}

// ErrReplayRateLimit — пользователь превысил квоту replay-запросов.
var ErrReplayRateLimit = errors.New("replay rate limit exceeded")

// ErrReplayTooOldFailure — replay недоступен для done=false старше 7 дней.
var ErrReplayTooOldFailure = errors.New("cannot replay failed request older than 7 days")

// ErrReplayBodyUnavailable — оригинальное тело запроса не сохранено (узел не
// логирует тело, LogRequestBody=false), а пользователь не задал тело вручную.
// Replay с пустым телом ушёл бы во внешний target и вернул бы невнятную 400
// «empty body» (QA-2026-02 / П1). Возвращаем явную ошибку → handler отдаёт 422.
// Для GET-реинъекции не применяется: у GET тела нет by design.
var ErrReplayBodyUnavailable = errors.New("original request body was not logged; provide body manually")

// ErrReplayBadParams — params_override не парсится как query-строка. В отличие
// от orig.Parameters (ошибку парсинга которого глотаем — лог мог быть записан
// с мусором), явный override пользователя обязан быть валидным → handler 400.
var ErrReplayBadParams = errors.New("params_override is not a valid query string")

// ErrReplayBodyMultipart — §68: у оригинала было multipart/form-data тело, в
// ClickHouse вместо него сохранён плейсхолдер (вложения не хранятся). Отправить
// плейсхолдер во внешний target нельзя — это не исходное тело. Пользователь
// может задать тело вручную (BodyOverride) → handler отдаёт 422.
var ErrReplayBodyMultipart = errors.New("original request body was multipart/form-data and is not stored; provide body manually")

// Replay выполняет повторную отправку запроса через шину.
//
// teamID — multi-tenancy scope (Phase 10.D). Узел чужой команды → 404.
func (u *ReplayUsecase) Replay(
	ctx context.Context,
	actor Actor,
	logID, nodeID, teamID string,
	opts ReplayOptions,
) (*ReplayResult, error) {
	if err := u.checkReplayRate(ctx, actor); err != nil {
		return nil, err
	}
	node, err := u.resolveReplayNode(ctx, nodeID, teamID)
	if err != nil {
		return nil, err
	}
	res, err := u.replayOne(ctx, node, logID, opts)
	if err != nil {
		return nil, err
	}
	u.audit.Log(ctx, actor, domain.ActionNodeReplay, "log", logID, map[string]any{
		"node_id":       node.ID,
		"new_log_id":    res.NewLogID,
		"status_code":   res.StatusCode,
		"sync_override": opts.SyncOverride,
	})
	return res, nil
}

// checkReplayRate — per-user rate-limit (§7.4.1). Ошибка проверки — fail-open.
func (u *ReplayUsecase) checkReplayRate(ctx context.Context, actor Actor) error {
	if actor.UserID == "" || u.rl == nil {
		return nil
	}
	ok, err := u.rl.Allow(ctx, "replay:"+actor.UserID, u.rateLimit)
	if err != nil {
		u.logger.Warn("replay rate limit check failed; allowing",
			u.logger.Str("user_id", actor.UserID), u.logger.Err(err))
		return nil
	}
	if !ok {
		return ErrReplayRateLimit
	}
	return nil
}

// resolveReplayNode — узел по id с team-scope и проверкой «не disabled».
func (u *ReplayUsecase) resolveReplayNode(ctx context.Context, nodeID, teamID string) (*domain.Node, error) {
	node, err := u.nodes.Get(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("replay get node: %w", err)
	}
	if teamID != "" && node.TeamID != teamID {
		return nil, domain.ErrNodeNotFound
	}
	if node.Status == domain.NodeStatusDisabled {
		return nil, fmt.Errorf("replay: %w", domain.ErrNodeDisabled)
	}
	return node, nil
}

// replayOne — пере-инжектирует один залогированный запрос logID через Receiver
// (узел уже резолвлен). Без rate-limit/резолва/аудита — их делают вызывающие
// (Replay — построчно, ReplayFailed — массово).
func (u *ReplayUsecase) replayOne(ctx context.Context, node *domain.Node, logID string, opts ReplayOptions) (*ReplayResult, error) {
	orig, err := u.logs.GetByID(ctx, node.ClickHouseTable, logID)
	if err != nil {
		return nil, fmt.Errorf("replay get original log: %w", err)
	}
	if !orig.Done && time.Since(orig.DateRequest) > 7*24*time.Hour {
		return nil, ErrReplayTooOldFailure
	}

	// Сборка нового запроса.
	// HTTP-глагол берём из ВХОДЯЩЕГО метода узла, а не из лога (§39/§40/§34.5) —
	// правило целиком в domain.ReplayEffectiveMethod. Оно общее с массовым
	// повтором §85, который по тому же глаголу решает, обязательно ли телу быть
	// в журнале: две копии правила разошлись бы, и предпросмотр обещал бы не то,
	// что уходит.
	method := domain.ReplayEffectiveMethod(node.IncomingMethod, orig.HTTPMethod)
	// Тело: при nil-override берём оригинал из лога. Если узел не логировал
	// тело (orig.Request пуст) и пользователь его не задал — отказываем явно,
	// иначе во внешний target ушёл бы пустой body → 400 «empty body» (П1).
	// Явно переданное пустое тело (BodyOverride = []byte{}) считаем намеренным.
	// Исключение — GET: у него тела нет by design, пустой orig.Request — норма,
	// а не «не сохранилось» (боевой кейс legat_by: GET-логи блокировались 422).
	body := opts.BodyOverride
	if body == nil {
		// §68: multipart-запись хранит в orig.Request плейсхолдер, а не тело —
		// отправить его как body нельзя. Разрешаем только явный BodyOverride.
		if domain.IsMultipartLogPlaceholder(orig.Request) {
			u.logger.Debug("replay: multipart original, body not stored — reject",
				u.logger.Str("log_id", logID))
			return nil, ErrReplayBodyMultipart
		}
		if orig.Request == "" && method != "GET" {
			return nil, ErrReplayBodyUnavailable
		}
		body = []byte(orig.Request)
	}

	// Query: явный override из диалога приоритетнее сохранённых параметров
	// лога. Ошибку парсинга оригинала глотаем (мусор в старом логе не должен
	// блокировать replay), ошибку override'а — нет (это ввод пользователя).
	var q url.Values
	if opts.ParamsOverride != nil {
		q, err = url.ParseQuery(strings.TrimPrefix(*opts.ParamsOverride, "?"))
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrReplayBadParams, err)
		}
		u.logger.Debug("replay: params override applied",
			u.logger.Str("log_id", logID), u.logger.Int("params", len(q)))
	} else {
		q, err = url.ParseQuery(orig.Parameters)
		if err != nil {
			u.logger.Debug("replay: original parameters unparsable, dropped",
				u.logger.Str("log_id", logID), u.logger.Err(err))
			q = url.Values{}
		}
	}
	// Маркер § «В поле parameters добавляется __replay_of=<original_id>».
	q.Set("__replay_of", logID)

	async := orig.Type == domain.RootMethodRequestAsync && !opts.SyncOverride

	headers := map[string]string{}
	switch {
	case opts.CustomAuth != "":
		headers["Authorization"] = opts.CustomAuth
	case !opts.UseNodeAuth:
		// «Без авторизации» — отключаем входящую auth узла.
		// Если узел требует Basic/Token — replay упадёт с 401, как и
		// должен (это сценарий отладки 401-ошибок из §7.4.1).
	default:
		// «Использовать авторизацию узла»: replay идёт через реальный входной
		// endpoint Receiver'а, и узел с ВХОДЯЩЕЙ авторизацией требует креду в
		// самом запросе — без неё Receiver отвечает 401 «authorization header
		// missing» (боевой баг: узел с входящей basic). Оператор собрать
		// `Basic base64(login:password)`/HMAC руками не может (креды наружу
		// не отдаются), поэтому сервер строит креду сам из сохранённых кредов
		// узла — тот же механизм, что автоподстановка в dry-run (§55.6).
		// ИСХОДЯЩУЮ авторизацию Receiver подставит из конфига узла как обычно.
		pres, ok, aerr := rcv.BuildIncomingAuthValue(node, body)
		switch {
		case aerr != nil:
			u.logger.Debug("replay: build incoming auth failed, sending without cred",
				u.logger.Str("node_id", node.ID), u.logger.Err(aerr))
		case ok && pres.Source == domain.IncomingAuthSourceQuery:
			q.Set(pres.Field, pres.Value)
		case ok:
			headers[pres.Field] = pres.Value
		}
	}

	// §39: для passthrough-узла исходный запрос бил в подпуть (сохранён в
	// orig.Method — колонка method). Реинъектим по полному пути, иначе replay
	// уйдёт на корень узла вместо исходного эндпоинта; Receiver переразрешит
	// его через prefixMatch обратно на этот же узел + хвост.
	nodePath := node.Path
	if node.PathPassthrough && orig.Method != "" {
		nodePath = node.Path + "/" + orig.Method
	}
	// §18: узел не-default команды доступен на Receiver только по пути со
	// слагом (/api/v1/request/<team_slug>/<path>) — без него Receiver ищет
	// путь в default-команде и отвечает 404 "node not found" (боевой баг:
	// replay узла команды vika). Ошибка резолва — идём без слага (для
	// default-узлов это корректно, для остальных 404 честно всплывёт).
	if u.teams != nil && node.TeamID != "" {
		t, terr := u.teams.GetByID(ctx, node.TeamID)
		switch {
		case terr != nil:
			u.logger.Debug("replay: team slug resolve failed, path without slug",
				u.logger.Str("team_id", node.TeamID), u.logger.Err(terr))
		case t.Slug != domain.DefaultTeamSlug && t.Slug != "":
			nodePath = t.Slug + "/" + nodePath
		}
	}

	resp, err := u.dispatcher.Dispatch(ctx, port.DispatchRequest{
		NodePath: nodePath,
		Async:    async,
		Method:   method,
		Query:    q,
		Headers:  headers,
		Body:     body,
	})
	if err != nil {
		return nil, fmt.Errorf("dispatch replay: %w", err)
	}

	return &ReplayResult{
		NewLogID:    uuid.NewString(),
		StatusCode:  resp.StatusCode,
		BodyPreview: previewBody(resp.Body, 512),
		Headers:     resp.Headers,
	}, nil
}

// ReplayBulkResult — итог «Повторить все сейчас» (§36.11).
type ReplayBulkResult struct {
	Total    int  `json:"total"`    // уникальных неудачных найдено (в пределах cap)
	Replayed int  `json:"replayed"` // пере-инжектировано (оригинал отменён в DLQ)
	Failed   int  `json:"failed"`   // ошибок replay (оригинал НЕ отменён — остаётся авто-репроцессору)
	Cleaned  int  `json:"cleaned"`  // §79.2: записей убрано из «Неудачных доставок»
	Capped   bool `json:"capped"`
	// §81.5: пропущено записей, где ушла ВЫЗЫВАЮЩАЯ сторона. Их приёмник, скорее
	// всего, обработал (тело принял целиком), и повтор дал бы дубли. Показывается
	// оператору отдельной строкой — молча терять их из счёта нельзя.
	SkippedClientCanceled int `json:"skipped_client_canceled"`
}

// replayAllCap — верхняя граница числа сообщений за один «Повторить все»
// (защита от долгого синхронного прохода/таймаута). Превышение → Capped=true.
const replayAllCap = 500

// ReplayFailed — «Повторить все сейчас» (§36.11): пере-инжектирует через Receiver
// НЕДОСТАВЛЕННЫЕ запросы узла за окно [from,to] (нулевые = всё) и при успехе
// отменяет (qcancel) оригинал в DLQ, чтобы авто-репроцессор не доставил их
// повторно (без двойной доставки). Узел резолвится с team-scope; disabled →
// 409; нет CH-таблицы → no-op. Один rate-limit на всю операцию.
//
// §79.1: набор берётся по записям без единого успешного прогона, поэтому уже
// доставленные сообщения не переотправляются. До этого набор строился по
// строкам done=0, и массовый повтор слал дубли получателю — боевой случай
// 2026-08-05: из 16 повторённых сообщений 15 уже были доставлены.
//
// §79.2: после успешной реинъекции строки done=0 оригиналов удаляются одним
// batch-вызовом — записи исчезают из «Неудачных доставок» сразу, без ручной
// очистки. Шаг best-effort (см. cleanupReplayed).
func (u *ReplayUsecase) ReplayFailed(ctx context.Context, actor Actor, nodeID, teamID string, from, to time.Time) (ReplayBulkResult, error) {
	if err := u.checkReplayRate(ctx, actor); err != nil {
		return ReplayBulkResult{}, err
	}
	node, err := u.resolveReplayNode(ctx, nodeID, teamID)
	if err != nil {
		return ReplayBulkResult{}, err
	}
	if node.ClickHouseTable == "" {
		return ReplayBulkResult{}, nil
	}
	q := failedQuery(node, toMs(from), toMs(to))
	// §81.5: сколько всего недоставленных в окне — чтобы показать, сколько из них
	// пропущено. Второй лёгкий запрос по тем же условиям: считать разницу иначе
	// (например, вычитать после выборки) нельзя — cap обрезал бы её произвольно.
	allIDs, allCapped, err := u.logs.FailedIDs(ctx, q, replayAllCap)
	if err != nil {
		return ReplayBulkResult{}, fmt.Errorf("replay-all failed ids: %w", err)
	}
	// Записи, где ушла вызывающая сторона, из массового повтора исключаются:
	// приёмник тело принял и, вероятно, обработал — повтор дал бы дубли (урок
	// §79: 15 дублей в 1С на узле kz). Точечный повтор из строки журнала
	// остаётся — там решение принимает оператор по конкретной записи.
	q.ExcludeReasonPrefixes = []string{domain.ReasonClientCanceled}
	ids, capped, err := u.logs.FailedIDs(ctx, q, replayAllCap)
	if err != nil {
		return ReplayBulkResult{}, fmt.Errorf("replay-all failed ids: %w", err)
	}
	res := ReplayBulkResult{Total: len(ids), Capped: capped}
	// Разница достоверна, только когда ОБА множества уместились в cap: иначе оба
	// обрезаны по одной границе и их разность произвольна (обычно 0). Молчаливое
	// «пропущено 0» на большом окне хуже отсутствия числа, поэтому под cap его не
	// показываем — оператор видит флаг «показано не всё».
	if !capped && !allCapped {
		res.SkippedClientCanceled = max(len(allIDs)-len(ids), 0)
	}
	if res.SkippedClientCanceled > 0 {
		u.logger.Debug("replay-all: client-canceled records skipped",
			u.logger.Str("node", node.Path),
			u.logger.Int("skipped", res.SkippedClientCanceled))
	}
	replayed := make([]string, 0, len(ids))
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return res, err // контекст отменён (клиент отвалился) — прерываем
		}
		if _, rerr := u.replayOne(ctx, node, id, ReplayOptions{UseNodeAuth: true}); rerr != nil {
			res.Failed++
			u.logger.Warn("replay-all: one message failed",
				u.logger.Str("log_id", id), u.logger.Err(rerr))
			continue
		}
		// Успех → отменяем оригинал в DLQ: авто-репроцессор дропнет его по
		// tombstone, иначе при восстановлении адреса сообщение доставилось бы
		// дважды (replay-копия + авто-повтор оригинала).
		if u.cancel != nil {
			if _, cerr := u.cancel.Cancel(ctx, []string{id}, u.retention); cerr != nil {
				u.logger.Warn("replay-all: cancel original failed",
					u.logger.Str("log_id", id), u.logger.Err(cerr))
			}
		}
		replayed = append(replayed, id)
		res.Replayed++
	}
	res.Cleaned = u.cleanupReplayed(ctx, q, replayed)
	u.audit.Log(ctx, actor, domain.ActionNodeReplay, "node", node.ID, map[string]any{
		"op": "replay_all", "total": res.Total, "replayed": res.Replayed,
		"failed": res.Failed, "cleaned": res.Cleaned, "capped": res.Capped,
		"skipped_client_canceled": res.SkippedClientCanceled,
	})
	return res, nil
}

// cleanupReplayed — убрать строки done=0 успешно пере-инжектированных записей
// (§79.2), чтобы они исчезли из «Неудачных доставок» сразу. Возвращает число
// убранных записей.
//
// Шаг BEST-EFFORT и никогда не роняет операцию: сообщения к этому моменту уже
// отправлены во внешнюю систему, и превращать успешную доставку в 500 из-за
// невозможности прибрать логи нельзя.
//
// Границы защиты (что НЕ закрыто): на внешней таблице §64 и на таблице чужой
// ноды §70.4 гейт владения запрещает DML, поэтому история оригиналов остаётся —
// записи продолжат числиться неудачными до истечения периода или ручной
// очистки. Это осознанная цена, а не пропуск.
func (u *ReplayUsecase) cleanupReplayed(ctx context.Context, q port.LogQuery, ids []string) int {
	if u.cleaner == nil || len(ids) == 0 {
		return 0
	}
	n, err := u.cleaner.DeleteFailedRows(ctx, q, ids)
	if err != nil {
		u.logger.Warn("replay-all: cleanup of original failed rows skipped",
			u.logger.Str("table", q.Table), u.logger.Int("records", len(ids)), u.logger.Err(err))
		return 0
	}
	u.logger.Debug("replay-all: originals cleaned",
		u.logger.Str("table", q.Table), u.logger.Int("records", int(n)))
	return int(n)
}

func previewBody(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}
	return strings.ToValidUTF8(string(b[:max]), "") + "…"
}
