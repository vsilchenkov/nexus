package domain

import (
	"strings"
	"time"
)

// PeerInstanceStatus — исход последней пробы соседнего инстанса (§73).
//
// Значения зеркалит CHECK-констрейнт peer_instances_last_status_enum
// (миграция 0032) и палитра статусов в интерфейсе.
type PeerInstanceStatus string

const (
	// PeerInstanceUnknown — инстанс заведён, но ещё ни разу не опрашивался.
	PeerInstanceUnknown PeerInstanceStatus = "unknown"
	// PeerInstanceActive — сосед ответил на GET /api/version, зависимости в норме.
	PeerInstanceActive PeerInstanceStatus = "active"
	// PeerInstanceDegraded — версия получена, но GET /ready сообщил о деградации
	// либо вернул 503 (упала обязательная зависимость соседа). Инстанс жив, но
	// обслуживает трафик не полностью.
	PeerInstanceDegraded PeerInstanceStatus = "degraded"
	// PeerInstanceUnreachable — до соседа не удалось достучаться: таймаут, отказ
	// в соединении, ошибка DNS/TLS. Отличается от Error тем, что HTTP-ответа не
	// было вовсе.
	PeerInstanceUnreachable PeerInstanceStatus = "unreachable"
	// PeerInstanceError — ответ получен, но это не работающий Nexus: не-2xx,
	// нераспарсиваемое тело либо пустая версия (например, по адресу стоит чужой
	// сервис или страница входа прокси).
	PeerInstanceError PeerInstanceStatus = "error"
)

// Valid сообщает, что статус — одно из известных значений.
func (s PeerInstanceStatus) Valid() bool {
	switch s {
	case PeerInstanceUnknown, PeerInstanceActive, PeerInstanceDegraded,
		PeerInstanceUnreachable, PeerInstanceError:
		return true
	}
	return false
}

// Максимальные длины полей. Зеркалят CHECK-констрейнты миграции 0032.
const (
	peerInstanceTitleMaxLen   = 64
	peerInstanceURLMaxLen     = 255
	peerInstanceCommentMaxLen = 255
)

// PeerInstance — запись реестра соседних инстансов Nexus (§73).
//
// «Инстанс» здесь — то же, что «нода» в §70: самостоятельное развёртывание шины
// со своими PostgreSQL/Redis/Kafka. Не путать с domain.Node — это узел
// маршрутизации, совсем другая сущность.
//
// Реестр нужен только чтобы видеть соседей и переходить в них по ссылке: обмена
// данными между инстансами раздел не вводит (§70.11 оставляет кластеризацию и
// общую конфигурацию вне рамок). Свой инстанс в реестре не хранится.
//
// Поля Last* — кеш последней пробы, а не источник истины о живости: судить о
// свежести нужно по LastCheckedAt рядом со статусом.
type PeerInstance struct {
	ID      string
	Title   string
	BaseURL string
	Comment string

	LastStatus PeerInstanceStatus
	// LastVersion — версия Web Service соседа. Receiver и Sender версию наружу
	// не отдают, поэтому «версия инстанса» — это всегда версия его Web.
	LastVersion string
	// LastInstanceID — код инстанса соседа (instance.id, §70.1). Пустая строка
	// валидна: у ноды без суффикса идентификатор пуст.
	LastInstanceID string
	// LastLatencyMS — время ответа в миллисекундах; nil = не измеряли.
	LastLatencyMS *int
	// LastError — короткая нормализованная причина отказа ("timeout",
	// "http 502"). Сырое тело ответа и текст ошибки транспорта сюда не попадают.
	LastError     string
	LastCheckedAt *time.Time

	CreatedBy string
	UpdatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NormalizePeerBaseURL приводит адрес инстанса к каноническому виду и проверяет
// его пригодность для серверной пробы (§73.2).
//
// Требования: абсолютный http(s)-URL, непустой хост, без userinfo, без пути,
// query и фрагмента — то есть чистый origin `scheme://host[:port]`. Путь
// запрещён потому, что проба сама достраивает `/api/version` и `/ready`: с
// базой вида `https://host/nexus/` получился бы `/nexus//api/version`.
//
// Userinfo (`https://user:pass@host`) отвергается отдельно от общего разбора:
// это учётные данные в URL, а адрес отдаётся обратно в интерфейс и попадает в
// журнал аудита — пароль утёк бы в оба места.
//
// В отличие от AbsoluteHTTPURL значение НОРМАЛИЗУЕТСЯ: обрезаются пробелы и
// хвостовой слеш, хост приводится к нижнему регистру. Здесь это безопасно —
// дальше используется именно возвращённая строка, она же уходит в БД, и на ней
// же держится уникальность адреса (индекс по lower(base_url)).
func NormalizePeerBaseURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, "/")
	if s == "" || len(s) > peerInstanceURLMaxLen {
		return "", ErrPeerInstanceURLInvalid
	}
	u, ok := AbsoluteHTTPURL(s)
	if !ok {
		return "", ErrPeerInstanceURLInvalid
	}
	if u.User != nil {
		return "", ErrPeerInstanceURLInvalid
	}
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", ErrPeerInstanceURLInvalid
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

// Validate проверяет запись и нормализует BaseURL на месте. Вызывается usecase
// до записи в БД; те же ограничения продублированы CHECK-констрейнтами
// миграции 0032 — БД остаётся последним рубежом при прямом INSERT.
func (p *PeerInstance) Validate() error {
	normalized, err := NormalizePeerBaseURL(p.BaseURL)
	if err != nil {
		return err
	}
	p.BaseURL = normalized

	p.Title = strings.TrimSpace(p.Title)
	if l := len([]rune(p.Title)); l < 1 || l > peerInstanceTitleMaxLen {
		return ErrPeerInstanceTitleLength
	}
	p.Comment = strings.TrimSpace(p.Comment)
	if len([]rune(p.Comment)) > peerInstanceCommentMaxLen {
		return ErrPeerInstanceCommentLength
	}
	if p.LastStatus == "" {
		p.LastStatus = PeerInstanceUnknown
	}
	if !p.LastStatus.Valid() {
		return ErrPeerInstanceStatusInvalid
	}
	return nil
}
