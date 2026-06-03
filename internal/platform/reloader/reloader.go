// Package reloader — pub/sub-канал в Redis для уведомления сервисов о том,
// что dynamic-настройки (app_settings) изменились через UI и их нужно
// применить без рестарта (§8.4 / §14.5 ТЗ).
//
// Канал: "nexus:config:reload". Сообщение — JSON-объект `{"section":"sentry"}`
// либо `{"section":"clickhouse"}` (или "all" для повторного overlay'я обеих).
//
// Подписчик в каждом сервисе хранит набор cb-функций, привязанных к section.
// Publisher не знает о подписчиках; web/usecase.AppSettingsUsecase публикует
// после успешного Update.
package reloader

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	goredis "github.com/redis/go-redis/v9"

	"nexus/internal/platform/logging"
)

// Channel — Redis pub/sub канал для config-reload.
const Channel = "nexus:config:reload"

// Section — какие настройки изменились.
type Section string

const (
	SectionSentry        Section = "sentry"
	SectionClickHouse    Section = "clickhouse"
	SectionNotifications Section = "notifications"
	SectionAll           Section = "all"
)

// Message — формат тела события.
type Message struct {
	Section Section `json:"section"`
}

// Publisher отправляет события об изменении конфига.
type Publisher struct {
	rdb *goredis.Client
}

func NewPublisher(rdb *goredis.Client) *Publisher {
	return &Publisher{rdb: rdb}
}

// Publish отправляет событие о смене section. Ошибка логируется,
// но не пробрасывается — UI не должен отказывать при сбое Redis pub/sub.
func (p *Publisher) Publish(ctx context.Context, section Section) error {
	if p == nil || p.rdb == nil {
		return nil
	}
	payload, err := json.Marshal(Message{Section: section})
	if err != nil {
		return fmt.Errorf("marshal reload message: %w", err)
	}
	if err := p.rdb.Publish(ctx, Channel, payload).Err(); err != nil {
		return fmt.Errorf("publish reload: %w", err)
	}
	return nil
}

// Reloader — обработчик одной section на стороне сервиса. Принимает
// уведомление и применяет настройки (sentry.Init с новым DSN,
// пересоздание CH-клиента, и т.п.).
type Reloader func(ctx context.Context) error

// Subscriber слушает Redis pub/sub и вызывает зарегистрированные
// Reloader-функции при получении сообщения для соответствующей section.
type Subscriber struct {
	rdb       *goredis.Client
	logger    logging.Logger
	mu        sync.RWMutex
	reloaders map[Section][]Reloader
}

func NewSubscriber(rdb *goredis.Client, logger logging.Logger) *Subscriber {
	return &Subscriber{
		rdb:       rdb,
		logger:    logger,
		reloaders: map[Section][]Reloader{},
	}
}

// Register регистрирует callback на конкретную section. Callback может
// быть зарегистрирован несколько раз (множественные подписчики на
// «sentry», например).
func (s *Subscriber) Register(section Section, fn Reloader) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloaders[section] = append(s.reloaders[section], fn)
}

// Run запускает блокирующий цикл подписки. Завершается при ctx.Done.
// Логирует ошибки протокола, но переподписывается до отмены контекста.
func (s *Subscriber) Run(ctx context.Context) {
	if s == nil || s.rdb == nil {
		return
	}
	sub := s.rdb.Subscribe(ctx, Channel)
	defer sub.Close()

	ch := sub.Channel()
	s.logger.Info("reloader subscriber started",
		s.logger.Str("channel", Channel))

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("reloader subscriber stopped")
			return
		case m, ok := <-ch:
			if !ok {
				return
			}
			s.handle(ctx, m.Payload)
		}
	}
}

func (s *Subscriber) handle(ctx context.Context, payload string) {
	var msg Message
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		s.logger.Warn("reloader malformed message",
			s.logger.Str("payload", payload),
			s.logger.Err(err))
		return
	}
	sections := []Section{msg.Section}
	if msg.Section == SectionAll {
		sections = []Section{SectionSentry, SectionClickHouse, SectionNotifications}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sec := range sections {
		for _, fn := range s.reloaders[sec] {
			if err := fn(ctx); err != nil {
				s.logger.ErrorWithOp("reloader callback failed", err, "reloader.handle",
					s.logger.Str("section", string(sec)))
			}
		}
	}
	s.logger.Info("reloader applied",
		s.logger.Str("section", string(msg.Section)))
}
