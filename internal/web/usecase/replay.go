package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
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
	// NewLogID — идентификатор записи, созданной повтором, как его сообщила
	// шина (см. replayedLogID). ПУСТ у sync-повтора: там ответ приходит от
	// приёмника, и идентификатора записи в нём нет ни в каком виде. Пустое
	// значение наружу не отдаётся — интерфейсу нечего показывать.
	NewLogID    string            `json:"new_log_id,omitempty"`
	StatusCode  int               `json:"status_code"`
	DurationMs  int64             `json:"duration_ms"`
	BodyPreview string            `json:"body_preview"`
	Headers     map[string]string `json:"headers,omitempty"`
	// BodySource — §96: откуда взято отправленное тело (queue | log | override).
	// Интерфейсу это нужно, чтобы не гадать, ушла ли полная копия запроса.
	BodySource string `json:"body_source,omitempty"`
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
	// originals — §96: источник ПОЛНОГО тела для повтора (оригинальный конверт в
	// очереди Kafka). nil без Kafka — тогда источником остаётся журнальная
	// копия, и усечённая запись честно отклоняется.
	originals port.AsyncOriginalReader
	// queueSources — топики очереди в порядке поиска (§96.3): DLQ, delay-топик
	// паузы, основной. Пусто = поиск выключен.
	queueSources []port.QueueSource
	// metrics — §96.9: счётчик источников тела повтора. nil допустим.
	metrics   ReplayMetrics
	retention time.Duration // TTL tombstone'а отмены (= retention топика)
	rateLimit int           // запросов/мин на пользователя (§7.4.1: 10)
	// periodRateLimit — §85.9: свой лимит батчей массового повтора за период.
	// Общий rateLimit (10/мин) остановил бы цикл после десятого батча. 0 →
	// дефолт replayPeriodRateLimit.
	periodRateLimit int
	logger          logging.Logger
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

// WithQueueOriginals подключает §96: тело для повтора берётся из оригинального
// конверта в очереди Kafka, а журнальная копия остаётся запасным источником.
// Без опции (нет Kafka) повтор работает как до раздела — с той разницей, что
// усечённую копию он теперь не отправляет, а отклоняет.
//
// sources перечисляются в порядке поиска (§96.3); пустой список или nil-reader
// опцию не включают.
func WithQueueOriginals(r port.AsyncOriginalReader, sources []port.QueueSource) ReplayOption {
	return func(u *ReplayUsecase) {
		if r == nil || len(sources) == 0 {
			return
		}
		u.originals = r
		u.queueSources = sources
	}
}

// ReplayMetrics — учёт источника тела повтора (§96.9). Реализует
// metrics.Metrics; nil допустим (unit-тесты, сборка без метрик).
type ReplayMetrics interface {
	IncReplayBodySource(node, source string)
}

// Источники тела повтора для метрики (§96.9).
const (
	replaySourceQueue    = "queue"    // полное тело из конверта очереди
	replaySourceLog      = "log"      // целая журнальная копия
	replaySourceRejected = "rejected" // конверта нет, копия усечена — отказ
	replaySourceOverride = "override" // тело задано оператором в диалоге §7.4.1
)

// WithReplayMetrics подключает счётчик источников тела (§96.9). Без него повтор
// работает так же, просто молча: понять, чем кормится приёмник, будет нельзя.
func WithReplayMetrics(m ReplayMetrics) ReplayOption {
	return func(u *ReplayUsecase) { u.metrics = m }
}

// WithPeriodRateLimit задаёт лимит батчей массового повтора за период (§85.9).
// Ноль/отрицательное значение оставляет дефолт replayPeriodRateLimit.
func WithPeriodRateLimit(n int) ReplayOption {
	return func(u *ReplayUsecase) {
		if n > 0 {
			u.periodRateLimit = n
		}
	}
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

// ErrReplayOriginalUnavailable — §96: оригинального конверта в очереди уже нет
// (старше retention топика, Kafka не сконфигурирована, скан упёрся в cap), а
// журнальная копия усечена по max_body_size. Отправлять её нельзя: приёмник
// получит оборванный JSON с приклеенным маркером и ответит ошибкой — ровно
// боевой инцидент 31.08.2026, из-за которого раздел и появился.
//
// Handler отдаёт 422: это не сбой шины, а состояние записи.
var ErrReplayOriginalUnavailable = errors.New("original message is gone from the queue and the log copy is truncated")

// ErrReplayBodyTruncated — журнальная копия усечена, а конверта в очереди у
// записи не бывает в принципе: она прошла СИНХРОННЫМ путём (§96.10 п.1).
// Отдельная ошибка, потому что оператору нужен разный совет: здесь ждать
// нечего, помочь может только ручное тело в диалоге повтора.
var ErrReplayBodyTruncated = errors.New("log copy of the request body is truncated; provide body manually")

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
	// Одиночный повтор конверт ещё не искал — replayOne сделает это сам (§96.3).
	res, err := u.replayOne(ctx, node, logID, opts, replaySource{})
	if err != nil {
		return nil, err
	}
	details := map[string]any{
		"node_id":       node.ID,
		"status_code":   res.StatusCode,
		"sync_override": opts.SyncOverride,
		// §96.9: чем кормили приёмник. Без этого по журналу аудита нельзя
		// отличить повтор с полным телом от повтора журнальной копии.
		"body_source": res.BodySource,
	}
	// Пустой идентификатор в аудит НЕ пишем: строка «new_log_id: » читается как
	// «запись есть, но безымянная», хотя означает «шина его не вернула».
	if res.NewLogID != "" {
		details["new_log_id"] = res.NewLogID
	}
	u.audit.Log(ctx, actor, domain.ActionNodeReplay, "log", logID, details)
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
func (u *ReplayUsecase) replayOne(
	ctx context.Context, node *domain.Node, logID string, opts ReplayOptions, src replaySource,
) (*ReplayResult, error) {
	orig, err := u.logs.GetByID(ctx, node.ClickHouseTable, logID)
	if err != nil {
		return nil, fmt.Errorf("replay get original log: %w", err)
	}
	if !orig.Done && time.Since(orig.DateRequest) > 7*24*time.Hour {
		return nil, ErrReplayTooOldFailure
	}
	// §96: если конверт не искали заранее (одиночный повтор), ищем сейчас —
	// массовые операции передают уже найденное, чтобы не сканировать очередь на
	// каждую запись.
	if !src.searched {
		src = u.lookupOriginal(ctx, node, orig)
	}

	// Сборка нового запроса.
	// HTTP-глагол берём из ВХОДЯЩЕГО метода узла, а не из лога (§39/§40/§34.5) —
	// правило целиком в domain.ReplayEffectiveMethod. Оно общее с массовым
	// повтором §85, который по тому же глаголу решает, обязательно ли телу быть
	// в журнале: две копии правила разошлись бы, и предпросмотр обещал бы не то,
	// что уходит.
	method := domain.ReplayEffectiveMethod(node.IncomingMethod, orig.HTTPMethod)
	// Тело: явный BodyOverride важнее всего (оператор задал его руками), затем
	// оригинальный конверт из очереди (§96), и только потом журнальная копия.
	body, bodySource := opts.BodyOverride, replaySourceOverride
	if body == nil {
		body, bodySource, err = u.replayBody(node, orig, method, src)
		if err != nil {
			return nil, err
		}
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
	// Имя параметра — доменная константа: по нему массовый повтор §85.6 отсеивает
	// записи, порождённые прошлыми повторами.
	q.Set(domain.ReplayOfParam, logID)

	async := orig.Type == domain.RootMethodRequestAsync && !opts.SyncOverride

	// §96.5: заголовки исходного запроса живут только в конверте — журнал их не
	// хранит вовсе. Берём Content-Type и то, что узел пробрасывает дальше;
	// авторизацию Receiver подставит сам (ниже), поэтому её из конверта не тянем.
	headers := replayHeaders(node, src.original)
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
		NewLogID:    replayedLogID(resp, async),
		StatusCode:  resp.StatusCode,
		BodyPreview: previewBody(resp.Body, 512),
		Headers:     resp.Headers,
		BodySource:  bodySource,
	}, nil
}

// replaySource — оригинальный конверт записи, найденный в очереди (§96).
// searched отличает «искали и не нашли» от «ещё не искали»: массовые операции
// ищут пачкой заранее, одиночный повтор — сам, и повторный скан на каждую
// запись был бы лишним обходом всех топиков очереди.
type replaySource struct {
	original *port.AsyncOriginal
	searched bool
}

// queueLookupSkew — запас назад от времени приёма записи при поиске конверта.
// Конверт попадает в DLQ ПОЗЖЕ приёма, так что запас нужен только на расхождение
// часов между сервисом и брокером.
const queueLookupSkew = time.Minute

// lookupOriginal ищет конверт одной записи (§96.3). Промах — не ошибка: решение
// принимает replayBody по правилам §96.4.
func (u *ReplayUsecase) lookupOriginal(ctx context.Context, node *domain.Node, orig *domain.LogRecord) replaySource {
	if u.originals == nil || orig.Type != domain.RootMethodRequestAsync {
		return replaySource{searched: true}
	}
	found := u.findOriginals(ctx, node, []string{orig.ID}, orig.DateRequest.Add(-queueLookupSkew))
	if env, ok := found[orig.ID]; ok {
		return replaySource{original: &env, searched: true}
	}
	return replaySource{searched: true}
}

// findOriginals — общий вызов поиска конвертов для набора записей. Пустая карта
// при выключенном §96 или ошибке: источником тела тогда остаётся журнал.
func (u *ReplayUsecase) findOriginals(
	ctx context.Context, node *domain.Node, ids []string, since time.Time,
) map[string]port.AsyncOriginal {
	if u.originals == nil || len(ids) == 0 {
		return nil
	}
	found, err := u.originals.FindOriginals(ctx, port.OriginalLookup{
		Sources:  u.queueSources,
		NodePath: node.Path,
		IDs:      ids,
		Since:    since,
	})
	if err != nil {
		// Поиск best-effort: недоступная Kafka не должна валить повтор записи,
		// чья журнальная копия цела. Ошибку логируем — молчаливый переход на
		// журнал скрыл бы деградацию источника.
		u.logger.Warn("replay: queue lookup failed, falling back to log copy",
			u.logger.Str("node", node.Path), u.logger.Int("ids", len(ids)), u.logger.Err(err))
		return nil
	}
	u.logger.Debug("replay: queue lookup done",
		u.logger.Str("node", node.Path),
		u.logger.Int("requested", len(ids)),
		u.logger.Int("found", len(found)))
	return found
}

// lookupSince — нижняя граница поиска конвертов для массовых операций (§96.3).
//
// Берём начало окна операции; когда окно открытое («все неудачные»), опираемся
// на retention топика: раньше него конвертов не существует физически, а скан
// «с начала времён» по топику, общему для всех узлов, стоил бы дорого и всё
// равно ничего бы не нашёл.
func (u *ReplayUsecase) lookupSince(from time.Time) time.Time {
	if !from.IsZero() {
		return from.Add(-queueLookupSkew)
	}
	return time.Now().Add(-u.retention)
}

// replayBody выбирает источник тела по правилам §96.4 и объясняет отказ.
//
// Порядок: конверт из очереди (полное тело) → журнальная копия, если она целая →
// отказ. Журнальные признаки «нет тела» (плейсхолдер §68, пустая колонка,
// усечение) проверяются только когда конверта нет: они говорят о том, чего нет
// в ЖУРНАЛЕ, а не о том, что нечего отправлять.
func (u *ReplayUsecase) replayBody(
	node *domain.Node, orig *domain.LogRecord, method string, src replaySource,
) ([]byte, string, error) {
	if src.original != nil {
		u.logger.Debug("replay: body taken from queue envelope",
			u.logger.Str("log_id", orig.ID), u.logger.Str("node", node.Path),
			u.logger.Str("topic", src.original.Topic),
			u.logger.Int("body_bytes", len(src.original.Body)))
		u.countBodySource(node.Path, replaySourceQueue)
		return src.original.Body, replaySourceQueue, nil
	}
	if domain.IsMultipartLogPlaceholder(orig.Request) {
		u.logger.Debug("replay: multipart original, body not stored — reject",
			u.logger.Str("log_id", orig.ID))
		return nil, "", ErrReplayBodyMultipart
	}
	// §96.1: усечённую копию отправлять нельзя — приёмник получит оборванный
	// JSON с приклеенным маркером и будет отвечать ошибкой на каждый повтор.
	if domain.IsTruncatedLogBody(
		strings.HasSuffix(orig.Request, domain.LogBodyTruncationMarker),
		int64(len(orig.Request)), orig.RequestSize,
	) {
		u.logger.Debug("replay: log copy truncated and no envelope in queue — reject",
			u.logger.Str("log_id", orig.ID), u.logger.Str("node", node.Path),
			u.logger.Int("stored_bytes", len(orig.Request)),
			u.logger.Any("request_size", orig.RequestSize))
		u.countBodySource(node.Path, replaySourceRejected)
		if orig.Type == domain.RootMethodRequest {
			return nil, "", ErrReplayBodyTruncated
		}
		return nil, "", ErrReplayOriginalUnavailable
	}
	// Пустое тело у GET — норма by design, а не «не сохранилось» (боевой кейс
	// legat_by: GET-логи блокировались 422).
	if orig.Request == "" && method != "GET" {
		return nil, "", ErrReplayBodyUnavailable
	}
	// §51.9: тихий переход на журнальную копию — событие, которое надо видеть:
	// именно оно означает, что конверта в очереди уже нет.
	u.logger.Debug("replay: body taken from log copy, envelope not found",
		u.logger.Str("log_id", orig.ID), u.logger.Str("node", node.Path),
		u.logger.Int("body_bytes", len(orig.Request)))
	u.countBodySource(node.Path, replaySourceLog)
	return []byte(orig.Request), replaySourceLog, nil
}

// countBodySource — учёт источника тела (§96.9). Метрики опциональны.
func (u *ReplayUsecase) countBodySource(node, source string) {
	if u.metrics != nil {
		u.metrics.IncReplayBodySource(node, source)
	}
}

// replayHeaders восстанавливает заголовки исходного запроса из конверта (§96.5):
// Content-Type и то, что узел пробрасывает дальше. Остальное отбрасывается
// намеренно — в конверте лежат и служебные заголовки шины, а исходящую
// авторизацию Receiver соберёт заново по актуальному конфигу узла.
func replayHeaders(node *domain.Node, env *port.AsyncOriginal) map[string]string {
	out := map[string]string{}
	if env == nil {
		return out
	}
	allowed := make(map[string]struct{}, len(node.ForwardHeaders)+1)
	allowed["content-type"] = struct{}{}
	for _, h := range node.ForwardHeaders {
		allowed[strings.ToLower(h)] = struct{}{}
	}
	for k, v := range env.Headers {
		if _, ok := allowed[strings.ToLower(k)]; ok {
			out[k] = v
		}
	}
	return out
}

// replayedLogID — идентификатор ЗАПИСИ, созданной повтором, как его сообщила
// сама шина. Пустая строка означает «шина идентификатор не вернула», и это
// честный ответ, а не деградация.
//
// До §85.10 здесь стоял свежий uuid.NewString(): значение выглядело как
// идентификатор записи, но не было связано ни с чем — журнал аудита ссылался на
// запись, которой не существует.
//
// Два источника, и порядок между ними важен:
//
//   - заголовок X-Nexus-Id — его Receiver ставит, когда отвечает по шаблону
//     §83: тело там пишет оператор, и идентификатора в нём может не быть вовсе;
//   - тело штатного async-ответа `{"result":true,"id":"<uuid>"}` (и 202 узла на
//     паузе, §3.6) — собственная форма шины.
//
// Тело SYNC-ответа не разбирается никогда: это ответ ПРИЁМНИКА, и его
// собственное поле `id` (заказ, документ, что угодно) уехало бы в аудит как
// идентификатор записи журнала. По той же причине значение из тела обязано быть
// валидным UUID — форма шины гарантирует именно его.
func replayedLogID(resp *port.DispatchResponse, async bool) string {
	if id := resp.Headers["X-Nexus-Id"]; id != "" {
		return id
	}
	if !async {
		return ""
	}
	var ack struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(resp.Body, &ack); err != nil || ack.ID == "" {
		return ""
	}
	if _, err := uuid.Parse(ack.ID); err != nil {
		return ""
	}
	return ack.ID
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
	// §96.4: пропущено записей, у которых отправлять нечего — оригинального
	// конверта в очереди нет, а журнальная копия усечена (сюда же попадает
	// sync-запись, у которой конверта не бывает вовсе). Их оригиналы НЕ отменены
	// и строки НЕ вычищены — запись остаётся в «Неудачных доставках» вместе со
	// своим следом.
	SkippedOriginalUnavailable int `json:"skipped_original_unavailable"`
}

// originalsChunk — по скольку записей за раз искать конверты в очереди (§96.7).
// Компромисс: скан на КАЖДУЮ запись — это N обходов топиков, поиск сразу на весь
// набор (до replayAllCap = 500) держал бы в памяти Web все их тела.
const originalsChunk = 20

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
	// §96: конверты ищем ПОРЦИЯМИ, а не по одному (скан очереди на каждую
	// запись — это N обходов топиков) и не все разом (тела всего набора в
	// памяти Web). Порция подобрана так, чтобы обычный набор укладывался в
	// один-два прохода.
	for chunk := range slices.Chunk(ids, originalsChunk) {
		found := u.findOriginals(ctx, node, chunk, u.lookupSince(from))
		for _, id := range chunk {
			if err := ctx.Err(); err != nil {
				return res, err // контекст отменён (клиент отвалился) — прерываем
			}
			src := replaySource{searched: true}
			if env, ok := found[id]; ok {
				src.original = &env
			}
			if _, rerr := u.replayOne(ctx, node, id, ReplayOptions{UseNodeAuth: true}, src); rerr != nil {
				// §96.4: конверта нет, копия обрезана — запись пропускается
				// БЕЗ отмены оригинала и без очистки строк. Иначе операция
				// уничтожала бы последний след полного тела (боевой инцидент).
				// Обе ошибки означают одно: отправлять нечего. ErrReplayBodyTruncated
				// прилетает от sync-записи, попавшей в набор async-узла (§3.6), и
				// считать её «ошибкой отправки» неверно — отправки не было.
				if errors.Is(rerr, ErrReplayOriginalUnavailable) || errors.Is(rerr, ErrReplayBodyTruncated) {
					res.SkippedOriginalUnavailable++
					u.logger.Debug("replay-all: body unavailable, record skipped",
						u.logger.Str("log_id", id), u.logger.Str("node", node.Path), u.logger.Err(rerr))
					continue
				}
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
	}
	res.Cleaned = u.cleanupReplayed(ctx, q, replayed)
	u.audit.Log(ctx, actor, domain.ActionNodeReplay, "node", node.ID, map[string]any{
		"op": "replay_all", "total": res.Total, "replayed": res.Replayed,
		"failed": res.Failed, "cleaned": res.Cleaned, "capped": res.Capped,
		"skipped_client_canceled":      res.SkippedClientCanceled,
		"skipped_original_unavailable": res.SkippedOriginalUnavailable,
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
