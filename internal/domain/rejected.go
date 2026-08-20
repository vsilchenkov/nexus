package domain

import (
	"net/http"
	"time"
)

// RejectReason — причина, по которой Receiver не принял запрос (§94.2).
//
// Код, а не текст ответа: тексты меняются и переводятся, а по коду строятся
// фильтры интерфейса, метка Prometheus и ключ группы. Значения зеркалит
// CHECK-констрейнт rejected_groups_reason_enum (миграция 0040).
type RejectReason string

const (
	// RejectReasonNodeNotFound — узла с таким путём нет; сюда же §82.3
	// (async-эндпоинт на не-async узле) и pull-узел §27, у которого входящего
	// HTTP не бывает. Наружу все три неотличимы (факт существования узла не
	// раскрываем), а в журнале различаются: причину проставляет обработчик.
	RejectReasonNodeNotFound RejectReason = "node_not_found"
	// RejectReasonMethodNotAllowed — метод не входит в список разрешённых
	// узлом (§40); а также не-POST на /api/v1/callback/....
	RejectReasonMethodNotAllowed RejectReason = "method_not_allowed"
	// RejectReasonUnauthorized — входящая авторизация узла не пройдена (§41).
	RejectReasonUnauthorized RejectReason = "unauthorized"
	// RejectReasonNodeDisabled — узел выключен оператором.
	RejectReasonNodeDisabled RejectReason = "node_disabled"
	// RejectReasonURLNotAllowed — целевой адрес вне allowlist узла (§23).
	RejectReasonURLNotAllowed RejectReason = "url_not_allowed"
	// RejectReasonBodyTooLarge — тело превысило потолок sync или очереди (§43).
	RejectReasonBodyTooLarge RejectReason = "body_too_large"
	// RejectReasonRateLimited — превышен лимит запросов на узел (§9.4).
	RejectReasonRateLimited RejectReason = "rate_limited"
	// RejectReasonLoopDetected — запрос вернулся в шину сверх hop-лимита (§32).
	RejectReasonLoopDetected RejectReason = "loop_detected"
	// RejectReasonBadRequest — запрос сформирован неверно: пустой путь узла,
	// отсутствующий обязательный параметр адреса, невалидный URL, callback на
	// узле без подписи, отказ рендера ответа приёма (§83).
	RejectReasonBadRequest RejectReason = "bad_request"
	// RejectReasonOther — отказ, который не удалось отнести ни к одной из
	// причин выше. Существует, чтобы журнал не приписывал запросу чужую
	// причину: ненулевой счётчик здесь означает, что появилась ветка отказа,
	// не описанная в §94.2.
	RejectReasonOther RejectReason = "other"
)

// Valid сообщает, что причина — одно из известных значений.
func (r RejectReason) Valid() bool {
	switch r {
	case RejectReasonNodeNotFound, RejectReasonMethodNotAllowed, RejectReasonUnauthorized,
		RejectReasonNodeDisabled, RejectReasonURLNotAllowed, RejectReasonBodyTooLarge,
		RejectReasonRateLimited, RejectReasonLoopDetected, RejectReasonBadRequest,
		RejectReasonOther:
		return true
	}
	return false
}

// RejectReasonForStatus выводит причину из HTTP-статуса — резерв на случай,
// когда отказ вернул не доменный слой и причину в контекст никто не положил
// (413 из чтения тела, 429 из middleware лимита).
//
// Однозначность этого отображения держится на том, что каждый из статусов
// Receiver отдаёт ровно по одному поводу; появится второй — причину обязан
// проставить обработчик, иначе журнал начнёт врать.
func RejectReasonForStatus(status int) RejectReason {
	switch status {
	case http.StatusUnauthorized:
		return RejectReasonUnauthorized
	case http.StatusForbidden:
		return RejectReasonURLNotAllowed
	case http.StatusNotFound:
		return RejectReasonNodeNotFound
	case http.StatusMethodNotAllowed:
		return RejectReasonMethodNotAllowed
	case http.StatusRequestEntityTooLarge:
		return RejectReasonBodyTooLarge
	case http.StatusTooManyRequests:
		return RejectReasonRateLimited
	case http.StatusServiceUnavailable:
		return RejectReasonNodeDisabled
	case http.StatusLoopDetected:
		return RejectReasonLoopDetected
	case http.StatusBadRequest:
		return RejectReasonBadRequest
	}
	return RejectReasonOther
}

// Пределы журнала отказов (§94.3). Зашиты в код намеренно: это защита от
// разрастания, а не пользовательская настройка. Оператору доступен только срок
// хранения (§94.5).
const (
	// RejectedMaxClientsPerGroup — сколько разных IP хранится у одной группы.
	// При переполнении вытесняется самый давний по last_seen: интерес
	// представляют активные клиенты, а не первый попавшийся сканер.
	RejectedMaxClientsPerGroup = 50
	// RejectedMaxSamplesPerGroup — сколько последних запросов группы хранится
	// целиком (путь, заголовки, размер тела).
	RejectedMaxSamplesPerGroup = 20

	// Обрезка полей, приходящих от клиента. Значения зеркалят миграцию 0040 и
	// не дают чужому запросу раздуть строку: путь и User-Agent клиент задаёт
	// сам, а node_path вдобавок входит в уникальный ключ группы.
	RejectedTeamSlugMaxLen  = 64
	RejectedNodePathMaxLen  = 512
	RejectedRawPathMaxLen   = 1024
	RejectedUserAgentMaxLen = 256
	RejectedMethodMaxLen    = 16
)

// RejectedGroupKey — естественный ключ группы отказов: «куда и почему».
//
// TeamSlug хранится ровно в том виде, в каком пришёл в URL, и может не
// соответствовать ни одной существующей команде: при 404 узла нет, значит нет
// и надёжного способа определить команду. Пустая строка — форма адреса без
// слога (§78.1), она трактуется как команда default.
type RejectedGroupKey struct {
	TeamSlug   string
	NodePath   string
	Reason     RejectReason
	HTTPMethod string
}

// Normalize приводит ключ к виду, в котором он хранится: обрезает поля до
// пределов колонок и разворачивает пустой слог в default.
//
// Обрезка обязана произойти ДО агрегации: иначе два запроса с длинными путями,
// различающимися только хвостом, дали бы разные ключи в памяти и один и тот же
// — после усечения в БД, то есть UPSERT складывал бы их вместе, а счётчики
// экземпляра расходились бы с сохранёнными.
//
// Пустой слог — это форма адреса из одного сегмента (§78.1), которую сам
// Receiver резолвит как команду default; журнал обязан называть её так же,
// иначе один и тот же узел давал бы две разные группы в зависимости от формы
// адреса, а join с teams не находил бы команду.
func (k *RejectedGroupKey) Normalize() {
	if k.TeamSlug == "" {
		k.TeamSlug = DefaultTeamSlug
	}
	k.TeamSlug = truncateRunes(k.TeamSlug, RejectedTeamSlugMaxLen)
	k.NodePath = truncateRunes(k.NodePath, RejectedNodePathMaxLen)
	k.HTTPMethod = truncateRunes(k.HTTPMethod, RejectedMethodMaxLen)
	if !k.Reason.Valid() {
		k.Reason = RejectReasonOther
	}
}

// RejectedGroup — группа отказов: сколько раз, от скольких клиентов, когда
// впервые и в последний раз (§94.3).
type RejectedGroup struct {
	ID string
	RejectedGroupKey
	// Status — HTTP-код, которым Receiver ответил клиенту. Хранится отдельно от
	// причины: у bad_request это всегда 400, а у node_disabled — 503, но пара
	// «причина + код» нужна интерфейсу целиком, чтобы не восстанавливать код
	// по причине в каждом месте показа.
	Status    int32
	FirstSeen time.Time
	LastSeen  time.Time
	Count     int64
	// Clients — сколько разных IP известно группе (не больше
	// RejectedMaxClientsPerGroup). Денормализация ради списка: считать
	// подзапросом на каждую строку выдачи дороже, чем обновить при сбросе.
	Clients int32
	// ResolvedAt/ResolvedBy — отметка «разобрано» (§94.6). Снимается сама,
	// как только в группу приходит новый отказ.
	ResolvedAt *time.Time
	ResolvedBy string

	// TeamID/TeamName заполняются только на чтении (join по слогу). Пустые —
	// команда с таким слогом не существует: такие группы видит лишь
	// администратор (§94.6).
	TeamID   string
	TeamName string
}

// Resolved сообщает, помечена ли группа разобранной.
func (g *RejectedGroup) Resolved() bool { return g.ResolvedAt != nil }

// RejectedClient — один клиент группы: адрес, имя из PTR (§67), User-Agent и
// счётчики.
type RejectedClient struct {
	IP        string
	Host      string
	UserAgent string
	Count     int64
	FirstSeen time.Time
	LastSeen  time.Time
}

// RejectedSample — один сохранённый запрос группы.
//
// Тела здесь нет и не будет: хранится только размер. Значения query
// замаскированы, заголовки — отобранный белый список (§94.9).
type RejectedSample struct {
	At         time.Time
	ClientIP   string
	HTTPMethod string
	RawPath    string
	Status     int32
	BodyBytes  int64
	RequestID  string
	Headers    map[string]string
}

// RejectedAggregate — то, что один экземпляр Receiver накопил по группе за
// интервал сброса (§94.4).
//
// Count здесь — ПРИРОСТ, а не итог: репозиторий прибавляет его к сохранённому
// значению, поэтому несколько реплик складываются без синхронизации между
// собой.
type RejectedAggregate struct {
	Key       RejectedGroupKey
	Status    int32
	FirstSeen time.Time
	LastSeen  time.Time
	Count     int64
	Clients   []RejectedClient
	Samples   []RejectedSample
}

// truncateRunes обрезает строку до n рун (не байт): обрезка по байтам порвала
// бы UTF-8 в середине символа, и в БД поехала бы невалидная строка.
func truncateRunes(s string, n int) string {
	if n <= 0 || len(s) <= n {
		// len(s) <= n достаточно как быстрый путь: рун не больше, чем байт.
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
