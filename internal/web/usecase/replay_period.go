package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"nexus/internal/domain"
	"nexus/internal/web/usecase/port"
)

// Повторная отправка запросов из логов за период (§85).
//
// Отдельный файл, а не продолжение replay.go: тот уже держит одиночный повтор и
// «Повторить все неудачные», и третий сценарий сделал бы его нечитаемым. Общее у
// них ровно одно — replayOne, и оно переиспользуется как есть.

const (
	// replayPeriodBatchCap — сколько записей обрабатывает ОДИН вызов
	// ReplayPeriod. Верхняя граница длительности запроса: цикл крутит клиент,
	// поэтому батч обязан укладываться в обычный HTTP-таймаут.
	replayPeriodBatchCap = 200
	// replayPeriodPreviewCap — потолок разбора предпросмотра (§85.7). Больше
	// него окно не разбирается: ответ помечается exact=false, а остальные записи
	// проверяются по ходу отправки.
	replayPeriodPreviewCap = 2000
	// replayPeriodSamples — сколько примеров каждой группы отдаётся в план.
	replayPeriodSamples = 20
	// replayPeriodRateLimit — батчей в минуту на пользователя по умолчанию.
	// Одиночный replay ограничен 10/мин (§7.4.1), и на цикле батчей этот лимит
	// означал бы остановку после десятого — у операции обязан быть свой счёт.
	replayPeriodRateLimit = 60
)

// ErrReplayPeriodSyncNode — узел работает синхронно: асинхронный вход закрыт
// (§82.3), поставить запросы в очередь нечем. Записи типа requestAsync у такого
// узла БЫВАЮТ (§3.6 принимает их на паузе, плюс узел мог быть переведён из
// async) — невозможна именно реинжекция.
var ErrReplayPeriodSyncNode = errors.New("replay period: node is synchronous, async ingress is closed")

// ErrReplayPeriodBodyNotLogged — узел не логирует тело запроса. Проверяется
// один раз на входе, а не по записи за раз: иначе оператор запускал бы прогон,
// который гарантированно отказывает на каждой записи.
var ErrReplayPeriodBodyNotLogged = errors.New("replay period: node does not log request body")

// ErrReplayPeriodNoWindow — не заданы обе границы окна. Массовая операция без
// верхней границы затягивала бы в набор собственные повторы (§85.6), без
// нижней — всю историю узла.
var ErrReplayPeriodNoWindow = errors.New("replay period: both window bounds are required")

// ReplayPeriodInput — общий вход предпросмотра и батча (§85).
type ReplayPeriodInput struct {
	NodeID string
	TeamID string
	// Filter — окно и фильтры журнала, собранные тем же парсером, что список и
	// счётчик логов. Table/NodeID/DateCreateAligned проставляет usecase.
	Filter port.LogQuery
	// Cursor — позиция продолжения; пустой означает «с начала окна».
	Cursor port.ReplayCursor
	// Limit — размер батча; 0 → replayPeriodBatchCap.
	Limit int
	// SkipReplayCopies — не брать записи, порождённые прошлыми повторами (§85.6).
	SkipReplayCopies bool
}

// ReplayCandidateView — запись в списках предпросмотра.
type ReplayCandidateView struct {
	ID          string    `json:"id"`
	DateRequest time.Time `json:"date_request"`
	HTTPMethod  string    `json:"http_method"`
	Method      string    `json:"method,omitempty"`
	URL         string    `json:"url"`
	Status      int32     `json:"status"`
	Done        bool      `json:"done"`
	RequestSize int64     `json:"request_size"`
	// SkipReason — пусто у пригодной записи.
	SkipReason string `json:"skip_reason,omitempty"`
}

// ReplayPeriodPlan — предпросмотр (§85.7). Ничего не отправляет.
type ReplayPeriodPlan struct {
	// Total — записей окна под фильтром (uniqExact по ID).
	Total uint64 `json:"total"`
	// Scanned — сколько записей реально разобрано.
	Scanned int `json:"scanned"`
	// Exact — окно разобрано целиком, значит счётчики точные. false означает
	// «упёрлись в потолок разбора», и интерфейс обязан сказать это словами.
	Exact bool `json:"exact"`
	// Eligible — сколько из разобранных будет отправлено.
	Eligible int `json:"eligible"`
	// SkippedBy — причина (§85.3) → сколько записей.
	SkippedBy map[string]int `json:"skipped_by"`
	// Eligibles / Ineligibles — примеры обеих групп.
	Eligibles   []ReplayCandidateView `json:"eligibles"`
	Ineligibles []ReplayCandidateView `json:"ineligibles"`
	// From / To — ЗАФИКСИРОВАННЫЕ границы окна: клиент обязан слать их обратно
	// в каждый батч, иначе «по сейчас» затянет в набор собственные повторы.
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// ReplayPeriodBatch — итог одного батча отправки.
type ReplayPeriodBatch struct {
	Scanned   int            `json:"scanned"`
	Replayed  int            `json:"replayed"`
	Failed    int            `json:"failed"`
	SkippedBy map[string]int `json:"skipped_by"`
	// NextCursor — продолжение; nil означает, что окно пройдено до конца.
	NextCursor *port.ReplayCursor `json:"next_cursor"`
}

// PlanPeriod — что уйдёт и что не уйдёт при повторе за период (§85.7).
//
// Операция read-only: диспетчер не вызывается ни разу, в аудит не пишется — тот
// же довод, что у §84.9.4 (исполняется чтение того, что запрашивающий и так
// видит в журнале).
func (u *ReplayUsecase) PlanPeriod(ctx context.Context, in ReplayPeriodInput) (ReplayPeriodPlan, error) {
	node, q, err := u.preparePeriod(ctx, in)
	if err != nil {
		return ReplayPeriodPlan{}, err
	}

	// Списки инициализируются пустыми, а не nil: nil-срез уходит в JSON как
	// null, и клиенту пришлось бы страховаться на каждом обращении к списку.
	plan := ReplayPeriodPlan{
		SkippedBy:   map[string]int{},
		Eligibles:   []ReplayCandidateView{},
		Ineligibles: []ReplayCandidateView{},
		From:        time.UnixMilli(q.SinceMs).UTC(),
		To:          time.UnixMilli(q.UntilMs).UTC(),
	}
	total, err := u.logs.Count(ctx, q)
	if err != nil {
		return ReplayPeriodPlan{}, fmt.Errorf("replay period count: %w", err)
	}
	plan.Total = total

	cursor := in.Cursor
	for plan.Scanned < replayPeriodPreviewCap {
		limit := min(replayPeriodBatchCap, replayPeriodPreviewCap-plan.Scanned)
		items, next, err := u.logs.ReplayCandidates(ctx, q, cursor, limit)
		if err != nil {
			return ReplayPeriodPlan{}, fmt.Errorf("replay period candidates: %w", err)
		}
		for _, c := range items {
			plan.Scanned++
			reason := u.classify(node, c, in.SkipReplayCopies)
			view := candidateView(c, reason)
			if reason == domain.ReplaySkipNone {
				plan.Eligible++
				if len(plan.Eligibles) < replayPeriodSamples {
					plan.Eligibles = append(plan.Eligibles, view)
				}
				continue
			}
			plan.SkippedBy[string(reason)]++
			if len(plan.Ineligibles) < replayPeriodSamples {
				plan.Ineligibles = append(plan.Ineligibles, view)
			}
		}
		if next.IsZero() {
			plan.Exact = true
			break
		}
		cursor = next
	}
	u.logger.Debug("replay period planned",
		u.logger.Str("node", node.Path),
		u.logger.Int("total", int(plan.Total)),
		u.logger.Int("scanned", plan.Scanned),
		u.logger.Int("eligible", plan.Eligible),
		u.logger.Any("exact", plan.Exact))
	return plan, nil
}

// ReplayPeriod — один батч повтора (§85.5). Возвращает курсор продолжения;
// nil-курсор означает, что окно пройдено.
//
// Ошибка одной записи не роняет батч (как в ReplayFailed): сообщения уходят во
// внешнюю систему, и прерывать успевший начаться прогон из-за единичного отказа
// вреднее, чем досчитать его до конца страницы.
func (u *ReplayUsecase) ReplayPeriod(ctx context.Context, actor Actor, in ReplayPeriodInput) (ReplayPeriodBatch, error) {
	if err := u.checkPeriodRate(ctx, actor); err != nil {
		return ReplayPeriodBatch{}, err
	}
	node, q, err := u.preparePeriod(ctx, in)
	if err != nil {
		return ReplayPeriodBatch{}, err
	}

	limit := in.Limit
	if limit <= 0 || limit > replayPeriodBatchCap {
		limit = replayPeriodBatchCap
	}
	items, next, err := u.logs.ReplayCandidates(ctx, q, in.Cursor, limit)
	if err != nil {
		return ReplayPeriodBatch{}, fmt.Errorf("replay period candidates: %w", err)
	}

	res := ReplayPeriodBatch{SkippedBy: map[string]int{}}
	for _, c := range items {
		if err := ctx.Err(); err != nil {
			// Клиент ушёл. Курсор не отдаём: часть страницы уже отправлена, и
			// продолжение с этого места пропустило бы остаток — оператор
			// перезапустит прогон от последней подтверждённой отметки.
			return res, err
		}
		res.Scanned++
		if reason := u.classify(node, c, in.SkipReplayCopies); reason != domain.ReplaySkipNone {
			res.SkippedBy[string(reason)]++
			continue
		}
		if _, rerr := u.replayOne(ctx, node, c.ID, ReplayOptions{UseNodeAuth: true}); rerr != nil {
			res.Failed++
			u.logger.Warn("replay period: one record failed",
				u.logger.Str("log_id", c.ID), u.logger.Str("node", node.Path), u.logger.Err(rerr))
			continue
		}
		res.Replayed++
	}
	if !next.IsZero() {
		cursor := next
		res.NextCursor = &cursor
	}

	// Аудит на КАЖДЫЙ батч: прогон обрывается закрытием вкладки, и след обязан
	// остаться от того, что успело уйти, а не только от завершённой операции.
	u.audit.Log(ctx, actor, domain.ActionNodeReplayPeriod, "node", node.ID, map[string]any{
		"from":     q.SinceMs,
		"to":       q.UntilMs,
		"scanned":  res.Scanned,
		"replayed": res.Replayed,
		"failed":   res.Failed,
		"skipped":  res.SkippedBy,
		"done":     res.NextCursor == nil,
	})
	return res, nil
}

// preparePeriod — общие гейты и сборка запроса к журналу.
//
// Гейты проверяются ДО чтения журнала: узел без async-входа, без логирования или
// без тела запроса не даст ни одной пригодной записи, и разбирать окно ради
// пустого результата незачем.
func (u *ReplayUsecase) preparePeriod(ctx context.Context, in ReplayPeriodInput) (*domain.Node, port.LogQuery, error) {
	node, err := u.resolveReplayNode(ctx, in.NodeID, in.TeamID)
	if err != nil {
		return nil, port.LogQuery{}, err
	}
	if node.RootMethod == domain.RootMethodRequest {
		return nil, port.LogQuery{}, ErrReplayPeriodSyncNode
	}
	if !node.LoggingEnabled || node.ClickHouseTable == "" {
		return nil, port.LogQuery{}, domain.ErrNodeLogsNotConfigured
	}
	if !node.LogRequestBody {
		return nil, port.LogQuery{}, ErrReplayPeriodBodyNotLogged
	}

	q := in.Filter
	if q.SinceMs <= 0 || q.UntilMs <= 0 {
		return nil, port.LogQuery{}, ErrReplayPeriodNoWindow
	}
	// Полнотекстовый фильтр обязан быть разобран здесь: адаптер читает только
	// QExpr, и без разбора непустой q молча не действовал бы — предпросмотр
	// показывал бы одно множество, а отправка брала другое.
	if err := parseSearch(&q); err != nil {
		return nil, port.LogQuery{}, err
	}
	q.Table = node.ClickHouseTable
	q.NodeID = node.ID
	// §72.4: сужение по колонке партиционирования — только там, где инвариант
	// write-path гарантирован; на внешней таблице §64 оно молча теряло бы записи.
	q.DateCreateAligned = !node.ExternalTable
	q.BeforeID, q.Limit, q.Unresolved = "", 0, false
	return node, q, nil
}

// classify — решение о пригодности записи с эффективным глаголом узла.
func (u *ReplayUsecase) classify(node *domain.Node, c domain.ReplayCandidate, skipCopies bool) domain.ReplaySkipReason {
	method := domain.ReplayEffectiveMethod(node.IncomingMethod, c.HTTPMethod)
	return domain.ClassifyReplayCandidate(c, method, skipCopies)
}

// checkPeriodRate — свой ключ и свой лимит (§85.9). Ошибка проверки — fail-open,
// как у одиночного повтора.
func (u *ReplayUsecase) checkPeriodRate(ctx context.Context, actor Actor) error {
	if actor.UserID == "" || u.rl == nil {
		return nil
	}
	limit := u.periodRateLimit
	if limit <= 0 {
		limit = replayPeriodRateLimit
	}
	ok, err := u.rl.Allow(ctx, "replay_period:"+actor.UserID, limit)
	if err != nil {
		u.logger.Warn("replay period rate limit check failed; allowing",
			u.logger.Str("user_id", actor.UserID), u.logger.Err(err))
		return nil
	}
	if !ok {
		return ErrReplayRateLimit
	}
	return nil
}

func candidateView(c domain.ReplayCandidate, reason domain.ReplaySkipReason) ReplayCandidateView {
	return ReplayCandidateView{
		ID:          c.ID,
		DateRequest: c.DateRequest,
		HTTPMethod:  c.HTTPMethod,
		Method:      c.Method,
		URL:         c.URL,
		Status:      c.Status,
		Done:        c.Done,
		RequestSize: c.RequestSize,
		SkipReason:  string(reason),
	}
}
