package port

import (
	"context"
	"time"

	"nexus/internal/domain"
)

// OneTimeTokenRepo — хранилище одноразовых ссылок (§88.4.1).
//
// purpose входит в сигнатуру КАЖДОГО метода, кроме чистки, и это не
// многословность, а граница безопасности: без фильтра по назначению ссылку,
// выданную на слабое действие (подтверждение адреса), можно предъявить на
// сильном эндпоинте (смена пароля). Обязательный параметр забыть нельзя.
type OneTimeTokenRepo interface {
	// Create сохраняет выданную ссылку. TokenHash обязан быть уже посчитан
	// вызывающим — сам токен в репозиторий не передаётся.
	Create(ctx context.Context, t *domain.OneTimeToken) error

	// Consume атомарно гасит ссылку и возвращает её. Токен другого назначения,
	// уже погашенный или истёкший не находится — domain.ErrOneTimeTokenInvalid.
	// Гонка двух одновременных подтверждений разрешается на уровне СУБД.
	Consume(ctx context.Context, purpose domain.TokenPurpose, tokenHash string, at time.Time) (*domain.OneTimeToken, error)

	// Peek проверяет ссылку, НЕ расходуя её (§88.4.6): почтовые шлюзы с
	// защитой от вредоносных ссылок сами открывают адреса из писем, и
	// гашение на проверке убивало бы ссылку раньше адресата.
	Peek(ctx context.Context, purpose domain.TokenPurpose, tokenHash string, at time.Time) (bool, error)

	// CountActive — сколько действующих ссылок этого назначения у пользователя.
	CountActive(ctx context.Context, purpose domain.TokenPurpose, userID string, at time.Time) (int, error)

	// InvalidateByUser гасит все действующие ссылки назначения. Вызывается при
	// смене пароля: иначе старая ссылка из почты позволила бы перебить только
	// что установленный пароль.
	InvalidateByUser(ctx context.Context, purpose domain.TokenPurpose, userID string, at time.Time) (int, error)

	// DeleteExpiredBefore — ленивая чистка. Общая для всех назначений:
	// просроченная строка не принадлежит ни одному сценарию.
	DeleteExpiredBefore(ctx context.Context, before time.Time) (int, error)
}
