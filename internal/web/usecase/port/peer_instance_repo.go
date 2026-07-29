package port

import (
	"context"
	"time"

	"nexus/internal/domain"
)

// PeerInstanceRepo — реестр соседних инстансов Nexus (§73).
//
// Хранит только конфигурацию (название, адрес, комментарий) и кеш последней
// пробы. Сама проба живёт за InstanceProber: репозиторий в сеть не ходит.
type PeerInstanceRepo interface {
	// ListPeerInstances возвращает все записи, отсортированные по title.
	// Таблица заведомо мала (единицы строк), пагинации нет.
	ListPeerInstances(ctx context.Context) ([]*domain.PeerInstance, error)
	// GetPeerInstance возвращает запись по id. Нет записи → ErrPeerInstanceNotFound.
	GetPeerInstance(ctx context.Context, id string) (*domain.PeerInstance, error)
	// CreatePeerInstance добавляет запись и заполняет ID/CreatedAt/UpdatedAt.
	// Адрес уже занят → ErrPeerInstanceAlreadyExists.
	CreatePeerInstance(ctx context.Context, p *domain.PeerInstance) error
	// UpdatePeerInstance меняет название, адрес и комментарий. Нет записи →
	// ErrPeerInstanceNotFound, адрес занят другой записью →
	// ErrPeerInstanceAlreadyExists. Кеш последней пробы не трогает — за него
	// отвечает SavePeerInstanceProbe.
	UpdatePeerInstance(ctx context.Context, p *domain.PeerInstance) error
	// DeletePeerInstance удаляет запись. Нет записи → ErrPeerInstanceNotFound.
	DeletePeerInstance(ctx context.Context, id string) error
	// SavePeerInstanceProbe записывает результат пробы. Отдельный метод, а не
	// часть Update: пробу выполняет фоновый опрос от имени интерфейса, и она не
	// должна конкурировать с правкой конфигурации за одну и ту же команду UPDATE.
	// Исчезнувшая между опросом и записью строка ошибкой не считается (гонка с
	// удалением) — метод возвращает nil.
	SavePeerInstanceProbe(ctx context.Context, id string, res PeerInstanceProbe) error
}

// PeerInstanceProbe — результат одной пробы соседа, пригодный для сохранения.
//
// Отдельный тип, а не domain.PeerInstance: проба заполняет строго подмножество
// полей, и передача целой записи провоцировала бы затирание конфигурации
// значениями, которых проба не знает.
type PeerInstanceProbe struct {
	Status domain.PeerInstanceStatus
	// Version — версия Web Service соседа ("" если получить не удалось).
	Version string
	// InstanceID — код инстанса соседа (§70.1). Пустая строка валидна.
	InstanceID string
	// LatencyMS — время ответа; nil, если ответа не было вовсе.
	LatencyMS *int
	// Error — короткая нормализованная причина отказа, без сырого тела ответа.
	Error string
	// CheckedAt — момент пробы (передаётся явно, чтобы usecase проставлял одно
	// время всей пачке и время оставалось тестируемым).
	CheckedAt time.Time
}
