package usecase

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// stubPeerInstanceRepo — port.PeerInstanceRepo для unit-тестов (§73).
type stubPeerInstanceRepo struct {
	mu sync.Mutex

	items     []*domain.PeerInstance
	listErr   error
	getErr    error
	createErr error
	updateErr error
	deleteErr error
	saveErr   error

	created []*domain.PeerInstance
	updated []*domain.PeerInstance
	deleted []string
	saved   map[string]port.PeerInstanceProbe
}

func (r *stubPeerInstanceRepo) ListPeerInstances(context.Context) ([]*domain.PeerInstance, error) {
	return r.items, r.listErr
}

func (r *stubPeerInstanceRepo) GetPeerInstance(_ context.Context, id string) (*domain.PeerInstance, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	for _, p := range r.items {
		if p.ID == id {
			clone := *p
			return &clone, nil
		}
	}
	return nil, domain.ErrPeerInstanceNotFound
}

func (r *stubPeerInstanceRepo) CreatePeerInstance(_ context.Context, p *domain.PeerInstance) error {
	if r.createErr != nil {
		return r.createErr
	}
	p.ID = "generated-id"
	r.created = append(r.created, p)
	return nil
}

func (r *stubPeerInstanceRepo) UpdatePeerInstance(_ context.Context, p *domain.PeerInstance) error {
	if r.updateErr != nil {
		return r.updateErr
	}
	r.updated = append(r.updated, p)
	return nil
}

func (r *stubPeerInstanceRepo) DeletePeerInstance(_ context.Context, id string) error {
	if r.deleteErr != nil {
		return r.deleteErr
	}
	r.deleted = append(r.deleted, id)
	return nil
}

func (r *stubPeerInstanceRepo) SavePeerInstanceProbe(_ context.Context, id string, res port.PeerInstanceProbe) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.saved == nil {
		r.saved = map[string]port.PeerInstanceProbe{}
	}
	r.saved[id] = res
	return nil
}

// stubProber — port.InstanceProber: отдаёт заранее заданный исход на адрес.
type stubProber struct {
	mu       sync.Mutex
	byURL    map[string]port.InstanceProbeResult
	fallback port.InstanceProbeResult
	calls    []string
	panicOn  string
}

func (p *stubProber) Probe(_ context.Context, baseURL string) port.InstanceProbeResult {
	p.mu.Lock()
	p.calls = append(p.calls, baseURL)
	p.mu.Unlock()
	if baseURL == p.panicOn {
		panic("probe exploded")
	}
	if res, ok := p.byURL[baseURL]; ok {
		return res
	}
	return p.fallback
}

func newPeerInstanceUC(repo port.PeerInstanceRepo, prober port.InstanceProber) (*PeerInstanceUsecase, *stubAuditRepo) {
	auditRepo := &stubAuditRepo{}
	uc := NewPeerInstanceUsecase(repo, prober, NewAuditUsecase(auditRepo, logging.NewNoop()), logging.NewNoop())
	return uc, auditRepo
}

func TestPeerInstanceCreate(t *testing.T) {
	t.Parallel()

	t.Run("normalizes address and writes audit", func(t *testing.T) {
		t.Parallel()
		repo := &stubPeerInstanceRepo{}
		uc, audit := newPeerInstanceUC(repo, &stubProber{})

		p := &domain.PeerInstance{Title: "  Казахстан ", BaseURL: "HTTPS://Nexus-KZ.example.ru/"}
		require.NoError(t, uc.Create(context.Background(), Actor{UserLogin: "admin"}, p))

		require.Len(t, repo.created, 1)
		assert.Equal(t, "https://nexus-kz.example.ru", repo.created[0].BaseURL)
		assert.Equal(t, "Казахстан", repo.created[0].Title)
		assert.Equal(t, "admin", repo.created[0].CreatedBy)
		assert.Equal(t, "admin", repo.created[0].UpdatedBy)

		require.Len(t, audit.entries, 1)
		assert.Equal(t, domain.ActionPeerInstanceCreate, audit.entries[0].Action)
		assert.Equal(t, "https://nexus-kz.example.ru", audit.entries[0].Details["base_url"])
	})

	t.Run("rejects invalid address before touching repo", func(t *testing.T) {
		t.Parallel()
		repo := &stubPeerInstanceRepo{}
		uc, audit := newPeerInstanceUC(repo, &stubProber{})

		err := uc.Create(context.Background(), Actor{}, &domain.PeerInstance{Title: "x", BaseURL: "node.example.ru"})
		require.ErrorIs(t, err, domain.ErrPeerInstanceURLInvalid)
		assert.Empty(t, repo.created)
		assert.Empty(t, audit.entries)
	})

	t.Run("propagates duplicate address", func(t *testing.T) {
		t.Parallel()
		repo := &stubPeerInstanceRepo{createErr: domain.ErrPeerInstanceAlreadyExists}
		uc, audit := newPeerInstanceUC(repo, &stubProber{})

		err := uc.Create(context.Background(), Actor{}, &domain.PeerInstance{Title: "x", BaseURL: "https://a.ru"})
		require.ErrorIs(t, err, domain.ErrPeerInstanceAlreadyExists)
		assert.Empty(t, audit.entries, "аудит не пишется, если создать не удалось")
	})
}

func TestPeerInstanceUpdate(t *testing.T) {
	t.Parallel()

	repo := &stubPeerInstanceRepo{items: []*domain.PeerInstance{
		{ID: "id-1", Title: "Старое", BaseURL: "https://old.example.ru"},
	}}
	uc, audit := newPeerInstanceUC(repo, &stubProber{})

	got, err := uc.Update(context.Background(), Actor{UserLogin: "admin"}, "id-1", "Новое", "https://New.example.ru/", "коммент")
	require.NoError(t, err)
	assert.Equal(t, "https://new.example.ru", got.BaseURL)
	assert.Equal(t, "admin", got.UpdatedBy)

	require.Len(t, audit.entries, 1)
	assert.Equal(t, domain.ActionPeerInstanceUpdate, audit.entries[0].Action)
	assert.Equal(t, true, audit.entries[0].Details["url_changed"])
}

func TestPeerInstanceUpdateMissing(t *testing.T) {
	t.Parallel()

	repo := &stubPeerInstanceRepo{}
	uc, _ := newPeerInstanceUC(repo, &stubProber{})

	_, err := uc.Update(context.Background(), Actor{}, "nope", "t", "https://a.ru", "")
	require.ErrorIs(t, err, domain.ErrPeerInstanceNotFound)
	assert.Empty(t, repo.updated)
}

func TestPeerInstanceDelete(t *testing.T) {
	t.Parallel()

	repo := &stubPeerInstanceRepo{items: []*domain.PeerInstance{
		{ID: "id-1", Title: "Казахстан", BaseURL: "https://kz.example.ru"},
	}}
	uc, audit := newPeerInstanceUC(repo, &stubProber{})

	require.NoError(t, uc.Delete(context.Background(), Actor{UserLogin: "admin"}, "id-1"))
	assert.Equal(t, []string{"id-1"}, repo.deleted)
	require.Len(t, audit.entries, 1)
	assert.Equal(t, domain.ActionPeerInstanceDelete, audit.entries[0].Action)
	// В журнале должен остаться адрес удалённой записи — иначе по журналу не
	// понять, какой именно инстанс отключили.
	assert.Equal(t, "https://kz.example.ru", audit.entries[0].Details["base_url"])
}

func TestPeerInstanceProbeValidatesAddress(t *testing.T) {
	t.Parallel()

	prober := &stubProber{fallback: port.InstanceProbeResult{Status: domain.PeerInstanceActive, Version: "1.2.3"}}
	uc, _ := newPeerInstanceUC(&stubPeerInstanceRepo{}, prober)

	t.Run("normalizes before probing", func(t *testing.T) {
		res, err := uc.Probe(context.Background(), "https://Node.example.RU/")
		require.NoError(t, err)
		assert.Equal(t, domain.PeerInstanceActive, res.Status)
		assert.Equal(t, []string{"https://node.example.ru"}, prober.calls)
	})

	t.Run("rejects address with credentials without hitting network", func(t *testing.T) {
		before := len(prober.calls)
		_, err := uc.Probe(context.Background(), "https://user:pass@node.example.ru")
		require.ErrorIs(t, err, domain.ErrPeerInstanceURLInvalid)
		assert.Len(t, prober.calls, before, "проба не должна уходить в сеть по невалидному адресу")
	})
}

func TestPeerInstanceCheckAll(t *testing.T) {
	t.Parallel()

	repo := &stubPeerInstanceRepo{items: []*domain.PeerInstance{
		{ID: "id-1", Title: "A", BaseURL: "https://a.example.ru"},
		{ID: "id-2", Title: "B", BaseURL: "https://b.example.ru"},
	}}
	prober := &stubProber{byURL: map[string]port.InstanceProbeResult{
		"https://a.example.ru": {Status: domain.PeerInstanceActive, Version: "1.20.2", InstanceID: "kz", LatencyMS: new(42)},
		"https://b.example.ru": {Status: domain.PeerInstanceUnreachable, Error: "timeout"},
	}}
	uc, _ := newPeerInstanceUC(repo, prober)

	got, err := uc.CheckAll(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 2)

	assert.Equal(t, domain.PeerInstanceActive, got[0].LastStatus)
	assert.Equal(t, "1.20.2", got[0].LastVersion)
	assert.Equal(t, "kz", got[0].LastInstanceID)
	require.NotNil(t, got[0].LastLatencyMS)
	assert.Equal(t, 42, *got[0].LastLatencyMS)
	require.NotNil(t, got[0].LastCheckedAt)

	// Мёртвый сосед не ломает опрос остальных и получает свой статус.
	assert.Equal(t, domain.PeerInstanceUnreachable, got[1].LastStatus)
	assert.Equal(t, "timeout", got[1].LastError)
	assert.Nil(t, got[1].LastLatencyMS)

	// Результат каждой пробы сохраняется в БД.
	require.Len(t, repo.saved, 2)
	assert.Equal(t, domain.PeerInstanceActive, repo.saved["id-1"].Status)
	assert.Equal(t, "timeout", repo.saved["id-2"].Error)
}

func TestPeerInstanceCheckAllEmpty(t *testing.T) {
	t.Parallel()

	prober := &stubProber{}
	uc, _ := newPeerInstanceUC(&stubPeerInstanceRepo{}, prober)

	got, err := uc.CheckAll(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Empty(t, prober.calls)
}

// Паника внутри пробы не должна ронять процесс: горутина опроса ставит
// safego.Recover. Тест красный, если recover убрать.
func TestPeerInstanceCheckAllSurvivesPanic(t *testing.T) {
	t.Parallel()

	repo := &stubPeerInstanceRepo{items: []*domain.PeerInstance{
		{ID: "id-1", Title: "A", BaseURL: "https://boom.example.ru"},
		{ID: "id-2", Title: "B", BaseURL: "https://ok.example.ru"},
	}}
	prober := &stubProber{
		panicOn: "https://boom.example.ru",
		byURL: map[string]port.InstanceProbeResult{
			"https://ok.example.ru": {Status: domain.PeerInstanceActive, Version: "1.0.0"},
		},
	}
	uc, _ := newPeerInstanceUC(repo, prober)

	require.NotPanics(t, func() {
		got, err := uc.CheckAll(context.Background())
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, domain.PeerInstanceActive, got[1].LastStatus)
	})
}

// Сбой сохранения кеша не отменяет свежий статус в ответе: показать оператору
// актуальное состояние важнее, чем сохранить его копию.
func TestPeerInstanceCheckOneKeepsResultWhenSaveFails(t *testing.T) {
	t.Parallel()

	repo := &stubPeerInstanceRepo{
		items:   []*domain.PeerInstance{{ID: "id-1", Title: "A", BaseURL: "https://a.example.ru"}},
		saveErr: errors.New("db is down"),
	}
	prober := &stubProber{fallback: port.InstanceProbeResult{Status: domain.PeerInstanceActive, Version: "9.9.9"}}
	uc, _ := newPeerInstanceUC(repo, prober)

	got, err := uc.CheckOne(context.Background(), "id-1")
	require.NoError(t, err)
	assert.Equal(t, domain.PeerInstanceActive, got.LastStatus)
	assert.Equal(t, "9.9.9", got.LastVersion)
}

// Отменённый контекст (оператор ушёл со вкладки, браузер оборвал соединение) не
// должен оставлять в кеше ложное «нет ответа»: проба не состоялась, а не
// провалилась. Без гейта по ctx.Err() при следующем открытии таблица показывала
// бы unreachable для живых соседей. Тест красный на коде без гейта.
func TestPeerInstanceCheckDoesNotPersistOnCanceledContext(t *testing.T) {
	t.Parallel()

	repo := &stubPeerInstanceRepo{items: []*domain.PeerInstance{
		{ID: "id-1", Title: "A", BaseURL: "https://a.example.ru"},
	}}
	prober := &stubProber{fallback: port.InstanceProbeResult{
		Status: domain.PeerInstanceUnreachable, Error: "canceled",
	}}
	uc, _ := newPeerInstanceUC(repo, prober)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := uc.CheckAll(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Empty(t, repo.saved, "результат отменённой пробы не должен попадать в БД")
}

func TestPeerInstanceCheckOneMissing(t *testing.T) {
	t.Parallel()

	uc, _ := newPeerInstanceUC(&stubPeerInstanceRepo{}, &stubProber{})
	_, err := uc.CheckOne(context.Background(), "nope")
	require.ErrorIs(t, err, domain.ErrPeerInstanceNotFound)
}

// Отметка времени берётся из инжектируемого источника — иначе тест на
// last_checked_at был бы недетерминированным.
func TestPeerInstanceCheckUsesInjectedClock(t *testing.T) {
	t.Parallel()

	fixed := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	repo := &stubPeerInstanceRepo{items: []*domain.PeerInstance{
		{ID: "id-1", Title: "A", BaseURL: "https://a.example.ru"},
	}}
	uc, _ := newPeerInstanceUC(repo, &stubProber{fallback: port.InstanceProbeResult{Status: domain.PeerInstanceActive, Version: "1"}})
	uc.now = func() time.Time { return fixed }

	got, err := uc.CheckOne(context.Background(), "id-1")
	require.NoError(t, err)
	require.NotNil(t, got.LastCheckedAt)
	assert.Equal(t, fixed, *got.LastCheckedAt)
	assert.Equal(t, fixed, repo.saved["id-1"].CheckedAt)
}
