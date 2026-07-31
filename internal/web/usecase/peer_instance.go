package usecase

import (
	"context"
	"sync"

	"nexus/internal/domain"
	"nexus/internal/platform/clock"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/safego"
	"nexus/internal/web/usecase/port"
)

// probeConcurrency — сколько соседей опрашивается одновременно.
//
// Реестр по замыслу мал (единицы записей), поэтому потолок нужен не ради
// нагрузки, а как страховка: без него список из полусотни мёртвых адресов
// открыл бы столько же висящих на таймауте соединений разом.
const probeConcurrency = 8

// PeerInstanceUsecase — реестр соседних инстансов Nexus (§73): CRUD плюс
// проверка доступности.
//
// Проба вынесена за интерфейс port.InstanceProber, поэтому usecase не знает про
// HTTP и тестируется без поднятия сервера.
type PeerInstanceUsecase struct {
	repo   port.PeerInstanceRepo
	prober port.InstanceProber
	audit  *AuditUsecase
	// clock — источник времени (§4 CLAUDE.md): поле, а не time.Now в коде,
	// чтобы отметка last_checked_at была проверяемой в тестах.
	clock  clock.Clock
	logger logging.Logger
}

func NewPeerInstanceUsecase(
	repo port.PeerInstanceRepo,
	prober port.InstanceProber,
	audit *AuditUsecase,
	logger logging.Logger,
) *PeerInstanceUsecase {
	return &PeerInstanceUsecase{
		repo:   repo,
		prober: prober,
		audit:  audit,
		clock:  clock.System(),
		logger: logger,
	}
}

// List возвращает реестр с кешем последней пробы. Список отдаётся сразу, без
// сетевых запросов: интерфейс рисует таблицу мгновенно и лишь затем запускает
// Check.
func (u *PeerInstanceUsecase) List(ctx context.Context) ([]*domain.PeerInstance, error) {
	return u.repo.ListPeerInstances(ctx)
}

// Create добавляет инстанс в реестр. Адрес нормализуется в Validate, поэтому
// «https://Host/» и «https://host» считаются одним и тем же и второй раз не
// заводятся (ErrPeerInstanceAlreadyExists).
func (u *PeerInstanceUsecase) Create(ctx context.Context, actor Actor, p *domain.PeerInstance) error {
	if err := p.Validate(); err != nil {
		return err
	}
	p.CreatedBy = actor.UserLogin
	p.UpdatedBy = actor.UserLogin
	if err := u.repo.CreatePeerInstance(ctx, p); err != nil {
		return err
	}
	u.audit.Log(ctx, actor, domain.ActionPeerInstanceCreate, "instance", p.ID, map[string]any{
		"title":    p.Title,
		"base_url": p.BaseURL,
	})
	return nil
}

// Update меняет название, адрес и комментарий существующей записи.
func (u *PeerInstanceUsecase) Update(ctx context.Context, actor Actor, id, title, baseURL, comment string) (*domain.PeerInstance, error) {
	existing, err := u.repo.GetPeerInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	updated := *existing
	updated.Title = title
	updated.BaseURL = baseURL
	updated.Comment = comment
	if err := updated.Validate(); err != nil {
		return nil, err
	}
	updated.UpdatedBy = actor.UserLogin
	if err := u.repo.UpdatePeerInstance(ctx, &updated); err != nil {
		return nil, err
	}
	u.audit.Log(ctx, actor, domain.ActionPeerInstanceUpdate, "instance", id, map[string]any{
		"title":       updated.Title,
		"base_url":    updated.BaseURL,
		"url_changed": existing.BaseURL != updated.BaseURL,
	})
	return &updated, nil
}

// Delete удаляет инстанс из реестра. Ничего, кроме самой записи, удаление не
// затрагивает: реестр — справочник ссылок, на него никто не ссылается.
func (u *PeerInstanceUsecase) Delete(ctx context.Context, actor Actor, id string) error {
	existing, err := u.repo.GetPeerInstance(ctx, id)
	if err != nil {
		return err
	}
	if err := u.repo.DeletePeerInstance(ctx, id); err != nil {
		return err
	}
	u.audit.Log(ctx, actor, domain.ActionPeerInstanceDelete, "instance", id, map[string]any{
		"title":    existing.Title,
		"base_url": existing.BaseURL,
	})
	return nil
}

// Probe проверяет произвольный адрес БЕЗ сохранения — диалог подключения
// показывает результат до того, как запись создана.
//
// Адрес валидируется здесь же: без этого форма могла бы заставить сервер
// сходить по строке, которую он никогда бы не принял на сохранение.
func (u *PeerInstanceUsecase) Probe(ctx context.Context, baseURL string) (port.InstanceProbeResult, error) {
	normalized, err := domain.NormalizePeerBaseURL(baseURL)
	if err != nil {
		return port.InstanceProbeResult{}, err
	}
	return u.prober.Probe(ctx, normalized), nil
}

// CheckOne опрашивает один инстанс реестра и сохраняет результат.
func (u *PeerInstanceUsecase) CheckOne(ctx context.Context, id string) (*domain.PeerInstance, error) {
	p, err := u.repo.GetPeerInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	u.applyProbe(ctx, p)
	return p, nil
}

// CheckAll опрашивает весь реестр и возвращает записи со свежими статусами.
//
// Ошибка отдельного соседа не прерывает опрос: недоступность — это результат
// (статус unreachable), а не сбой операции.
func (u *PeerInstanceUsecase) CheckAll(ctx context.Context) ([]*domain.PeerInstance, error) {
	list, err := u.repo.ListPeerInstances(ctx)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return list, nil
	}

	sem := make(chan struct{}, probeConcurrency)
	var wg sync.WaitGroup
	for _, p := range list {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			// Recover объявлен после wg.Done: по LIFO он сработает первым и
			// погасит панику до того, как счётчик будет уменьшен.
			defer wg.Done()
			defer safego.Recover(u.logger, "web.peerinstance.check")
			defer func() { <-sem }()
			u.applyProbe(ctx, p)
		}()
	}
	wg.Wait()
	return list, nil
}

// applyProbe опрашивает соседа, записывает исход в БД и обновляет переданную
// запись на месте, чтобы вызывающий вернул в интерфейс уже свежие значения.
//
// Сбой сохранения не отменяет результат: показать оператору актуальный статус
// важнее, чем сохранить его кеш, — на следующем опросе запись всё равно
// перезапишется. Ошибка при этом не проглатывается молча, а уходит в журнал.
func (u *PeerInstanceUsecase) applyProbe(ctx context.Context, p *domain.PeerInstance) {
	res := u.prober.Probe(ctx, p.BaseURL)
	checkedAt := u.clock.Now().UTC()

	p.LastStatus = res.Status
	p.LastVersion = res.Version
	p.LastInstanceID = res.InstanceID
	p.LastLatencyMS = res.LatencyMS
	p.LastError = res.Error
	p.LastCheckedAt = &checkedAt

	// Отменённый контекст (оператор закрыл страницу, браузер оборвал соединение)
	// НЕ должен оставлять в кеше ложное «нет ответа»: проба не состоялась, а не
	// провалилась. Без этого гейта уход со вкладки посреди опроса записывал бы
	// всем соседям статус unreachable, и при следующем открытии таблица врала бы
	// до конца нового опроса. Ответ всё равно уже некому получить.
	if ctx.Err() != nil {
		u.logger.Debug("instance probe result dropped: request context is done",
			u.logger.Str("instance_id", p.ID),
			u.logger.Str("status", string(res.Status)))
		return
	}

	if err := u.repo.SavePeerInstanceProbe(ctx, p.ID, port.PeerInstanceProbe{
		Status:     res.Status,
		Version:    res.Version,
		InstanceID: res.InstanceID,
		LatencyMS:  res.LatencyMS,
		Error:      res.Error,
		CheckedAt:  checkedAt,
	}); err != nil {
		u.logger.Warn("failed to persist instance probe result",
			u.logger.Str("instance_id", p.ID),
			u.logger.Str("status", string(res.Status)),
			u.logger.Err(err))
	}
}
