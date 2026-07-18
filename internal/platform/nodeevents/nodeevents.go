// Package nodeevents — Redis pub/sub канал гарантированной инвалидации конфига
// узла (§57). Web публикует событие при изменении узла (Create/Update/Delete/
// Move/SetStatus), Receiver подписан и выселяет узел из своих кешей (L1 in-memory
// + Redis), после чего следующий запрос перечитывает свежий конфиг из PG.
//
// Событие публикуется по team_id+path (НЕ по slug): провал резолва slug на
// стороне Web не должен блокировать инвалидацию — slug резолвит Receiver.
package nodeevents

import (
	"context"
	"encoding/json"
	"fmt"

	goredis "github.com/redis/go-redis/v9"

	"nexus/internal/platform/logging"
)

// Channel — Redis pub/sub канал инвалидации конфига узла. Отдельный от
// nexus:config:reload (тот несёт только section app_settings, без per-node ключа).
const Channel = "nexus:nodes:invalidate"

// Event — какой узел изменился. OldPath заполняется при переименовании (нужно
// выселить и старый ключ). TeamID, а не slug — см. док пакета.
type Event struct {
	TeamID  string `json:"team_id"`
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"`
}

// Publisher публикует события инвалидации. nil-safe: nil-Publisher или nil-rdb —
// Publish no-op (инвалидация best-effort, TTL кеша — страховка).
type Publisher struct {
	rdb *goredis.Client
}

func NewPublisher(rdb *goredis.Client) *Publisher {
	return &Publisher{rdb: rdb}
}

// Publish отправляет событие. Ошибка возвращается, но вызывающий трактует её как
// best-effort (логирует, не рушит запрос) — как reloader.Publish.
func (p *Publisher) Publish(ctx context.Context, ev Event) error {
	if p == nil || p.rdb == nil {
		return nil
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal node event: %w", err)
	}
	if err := p.rdb.Publish(ctx, Channel, payload).Err(); err != nil {
		return fmt.Errorf("publish node event: %w", err)
	}
	return nil
}

// PublishNodeChange — удобная обёртка над Publish для usecase (без импорта типа
// Event на стороне консьюмера).
func (p *Publisher) PublishNodeChange(ctx context.Context, teamID, path, oldPath string) error {
	return p.Publish(ctx, Event{TeamID: teamID, Path: path, OldPath: oldPath})
}

// Handler обрабатывает событие инвалидации на стороне подписчика (Receiver):
// резолвит team_id→slug и выселяет узел из кешей.
type Handler func(ctx context.Context, ev Event)

// Subscriber слушает Channel и вызывает Handler на каждое событие.
type Subscriber struct {
	rdb     *goredis.Client
	logger  logging.Logger
	handler Handler
}

func NewSubscriber(rdb *goredis.Client, logger logging.Logger, handler Handler) *Subscriber {
	return &Subscriber{rdb: rdb, logger: logger, handler: handler}
}

// Run — блокирующий цикл подписки; завершается при ctx.Done. Ошибки протокола
// логируются, переподписка держится до отмены контекста.
func (s *Subscriber) Run(ctx context.Context) {
	if s == nil || s.rdb == nil || s.handler == nil {
		return
	}
	sub := s.rdb.Subscribe(ctx, Channel)
	defer sub.Close()

	ch := sub.Channel()
	s.logger.Info("node invalidation subscriber started", s.logger.Str("channel", Channel))
	for {
		select {
		case <-ctx.Done():
			s.logger.Info("node invalidation subscriber stopped")
			return
		case m, ok := <-ch:
			if !ok {
				return
			}
			s.dispatch(ctx, m.Payload)
		}
	}
}

func (s *Subscriber) dispatch(ctx context.Context, payload string) {
	var ev Event
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		s.logger.Warn("node invalidation malformed message",
			s.logger.Str("payload", payload), s.logger.Err(err))
		return
	}
	if ev.Path == "" {
		return
	}
	s.handler(ctx, ev)
}
