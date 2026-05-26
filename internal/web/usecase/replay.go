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
	"nexus/internal/web/usecase/port"
)

// RateLimiter — минимальный интерфейс, нужный replay (per-user).
//
// Чтобы не тянуть в usecase сторонний пакет ratelimit, объявляем интерфейс
// на месте — реализация (Redis) подсовывается из main (§17.2 ТЗ).
type RateLimiter interface {
	Allow(ctx context.Context, key string, limit int) (bool, error)
}

// ReplayOptions — переопределения, которые UI может прислать в диалоге §7.4.1.
type ReplayOptions struct {
	BodyOverride []byte // если nil — используем оригинальное тело лога
	SyncOverride bool   // true → переключить async на sync
	UseNodeAuth  bool   // true (по умолчанию) — берём текущий конфиг узла; false — без авторизации в replay
	CustomAuth   string // если непусто — override на конкретный Authorization-заголовок
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
	rateLimit  int // запросов/мин на пользователя (§7.4.1: 10)
	logger     logging.Logger
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
	if rateLimit <= 0 {
		rateLimit = 10
	}
	return &ReplayUsecase{
		logs:       logs,
		nodes:      nodes,
		dispatcher: dispatcher,
		rl:         rl,
		audit:      audit,
		rateLimit:  rateLimit,
		logger:     logger,
	}
}

// ErrReplayRateLimit — пользователь превысил квоту replay-запросов.
var ErrReplayRateLimit = errors.New("replay rate limit exceeded")

// ErrReplayTooOldFailure — replay недоступен для done=false старше 7 дней.
var ErrReplayTooOldFailure = errors.New("cannot replay failed request older than 7 days")

// Replay выполняет повторную отправку запроса через шину.
func (u *ReplayUsecase) Replay(
	ctx context.Context,
	actor Actor,
	logID, nodeID string,
	opts ReplayOptions,
) (*ReplayResult, error) {
	if actor.UserID != "" && u.rl != nil {
		ok, err := u.rl.Allow(ctx, "replay:"+actor.UserID, u.rateLimit)
		if err != nil {
			u.logger.Warn("replay rate limit check failed; allowing",
				u.logger.Str("user_id", actor.UserID), u.logger.Err(err))
		} else if !ok {
			return nil, ErrReplayRateLimit
		}
	}

	node, err := u.nodes.Get(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("replay get node: %w", err)
	}
	if node.Status == domain.NodeStatusDisabled {
		return nil, fmt.Errorf("replay: %w", domain.ErrNodeDisabled)
	}

	orig, err := u.logs.GetByID(ctx, node.ClickHouseTable, logID)
	if err != nil {
		return nil, fmt.Errorf("replay get original log: %w", err)
	}
	if !orig.Done && time.Since(orig.DateRequest) > 7*24*time.Hour {
		return nil, ErrReplayTooOldFailure
	}

	// Сборка нового запроса.
	method := orig.Method
	if method == "" {
		method = "POST"
	}
	body := opts.BodyOverride
	if body == nil {
		body = []byte(orig.Request)
	}

	q, err := url.ParseQuery(orig.Parameters)
	if err != nil {
		q = url.Values{}
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
		// По умолчанию — replay идёт без явных кредов в запросе. Receiver
		// при auth_type=basic/token подставит из конфига узла. Для
		// динамических auth (token_from_request) UI должен прислать
		// CustomAuth вручную (см. §7.4.1).
	}

	resp, err := u.dispatcher.Dispatch(ctx, port.DispatchRequest{
		NodePath: node.Path,
		Async:    async,
		Method:   method,
		Query:    q,
		Headers:  headers,
		Body:     body,
	})
	if err != nil {
		return nil, fmt.Errorf("dispatch replay: %w", err)
	}

	newID := uuid.NewString()
	u.audit.Log(ctx, actor, domain.ActionNodeReplay, "log", logID, map[string]any{
		"node_id":       node.ID,
		"new_log_id":    newID,
		"status_code":   resp.StatusCode,
		"sync_override": opts.SyncOverride,
		"async":         async,
	})

	return &ReplayResult{
		NewLogID:    newID,
		StatusCode:  resp.StatusCode,
		BodyPreview: previewBody(resp.Body, 512),
		Headers:     resp.Headers,
	}, nil
}

func previewBody(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}
	return strings.ToValidUTF8(string(b[:max]), "") + "…"
}
