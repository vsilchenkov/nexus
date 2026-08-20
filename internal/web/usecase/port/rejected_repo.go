package port

import (
	"context"
	"time"

	"nexus/internal/domain"
)

// RejectedFilter — условия выборки групп отказов (§94.6).
//
// Пустые поля означают «без ограничения», кроме TeamSlugs: см. комментарий
// к полю — там пустой список и отсутствие ограничения различаются.
type RejectedFilter struct {
	// From/To — окно по last_seen: группа попадает в выдачу, если в неё
	// приходили отказы внутри окна. Нулевое время — граница не задана.
	From time.Time
	To   time.Time
	// Reasons — фильтр по причинам (OR внутри списка).
	Reasons []domain.RejectReason
	// TeamSlugs — слоги команд, которые вызывающему разрешено видеть.
	// nil = ограничения нет (администратор). Пустой НЕ-nil список означал бы
	// «не видно ничего», поэтому usecase обязан передавать либо nil, либо
	// непустой список — репозиторий трактует их буквально.
	TeamSlugs []string
	// UnknownTeamOnly — показывать только группы, чей слог не соответствует ни
	// одной существующей команде (диагностика чужих обращений; администратор).
	UnknownTeamOnly bool
	// Query — подстрока для поиска по пути узла, слогу, IP и PTR-имени клиента.
	Query string
	// IncludeResolved — включать группы с отметкой «разобрано».
	IncludeResolved bool
	// Limit/Offset — страница выдачи. Limit <= 0 означает «без предела»
	// (используется счётчиком и выгрузкой, не списком).
	Limit  int
	Offset int
}

// RejectedSummary — счётчики за период для строки на рабочем столе и бейджа
// раздела (§94.6).
type RejectedSummary struct {
	// Count — суммарное число отказов, Groups — число групп, Clients — число
	// РАЗНЫХ адресов клиентов (адрес, попавший в две группы, считается один раз).
	Count   int64
	Groups  int64
	Clients int64
	// Unresolved — группы без отметки «разобрано»: именно это число светится
	// бейджем, иначе он никогда не гаснет.
	Unresolved int64
}

// RejectedRepo — чтение и обслуживание журнала отказов (§94).
//
// Писателя здесь нет намеренно: пишет Receiver своим адаптером, и общий
// интерфейс на запись+чтение связал бы два сервиса одним контрактом ради
// экономии на объявлении.
type RejectedRepo interface {
	// ListRejected возвращает страницу групп, свежие сверху (по last_seen).
	ListRejected(ctx context.Context, f RejectedFilter) ([]*domain.RejectedGroup, error)
	// CountRejected — сколько групп попадает под фильтр (для «показано N из M»).
	CountRejected(ctx context.Context, f RejectedFilter) (int64, error)
	// SummaryRejected — агрегаты за период по тому же фильтру.
	SummaryRejected(ctx context.Context, f RejectedFilter) (RejectedSummary, error)
	// GetRejected возвращает группу по id. Нет записи → ErrRejectedGroupNotFound.
	GetRejected(ctx context.Context, id string) (*domain.RejectedGroup, error)
	// ListRejectedClients — клиенты группы, активные сверху.
	ListRejectedClients(ctx context.Context, groupID string, limit int) ([]domain.RejectedClient, error)
	// ListRejectedSamples — сохранённые запросы группы, свежие сверху.
	ListRejectedSamples(ctx context.Context, groupID string, limit int) ([]domain.RejectedSample, error)
	// ResolveRejected ставит отметку «разобрано». Нет записи →
	// ErrRejectedGroupNotFound.
	ResolveRejected(ctx context.Context, id, by string, at time.Time) error
	// DeleteRejected удаляет группу вместе с клиентами и сэмплами (каскад).
	DeleteRejected(ctx context.Context, id string) error
	// DeleteRejectedOlderThan удаляет группы, в которые не приходило отказов
	// с cutoff, и возвращает их число. Граница по last_seen: пока в группу
	// падают новые отказы, она живая (§94.5).
	DeleteRejectedOlderThan(ctx context.Context, cutoff time.Time) (int, error)
	// PurgeRejected удаляет журнал целиком — режим «сбор выключен» (срок 0).
	PurgeRejected(ctx context.Context) (int, error)
}
