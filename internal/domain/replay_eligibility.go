package domain

import (
	"strings"
	"time"
)

// LogBodyTruncationMarker — суффикс, который write-path дописывает к лог-копии
// тела, обрезанной по per-node `max_body_size` (§22.2, восстановлено §43.4).
//
// Константа живёт в домене, а не в пакете, который её ставит: маркер ставит
// Sender, а читают его web-слой (§85.3) и любой будущий разбор журнала. Две
// копии одной строки разъехались бы при первой же правке, и обрезанное тело
// молча поехало бы во внешнюю систему как целое.
const LogBodyTruncationMarker = "…(truncated)"

// IsTruncatedLogBody сообщает, что сохранённая в журнале копия тела короче
// исходной (§85.3).
//
// Проверяются ДВА независимых признака, и это не перестраховка:
//
//   - hasMarker — точный: write-path всегда дописывает LogBodyTruncationMarker.
//     Один он недостаточен, когда вызывающая сторона видит лишь начало тела;
//   - fullSize > storedBytes — приблизительный: у многобайтового текста,
//     обрезанного на несколько рун, длина копии С МАРКЕРОМ может ПРЕВЫСИТЬ
//     длину оригинала (маркер занимает 14 байт), и признак не срабатывает.
//
// Ошибка в безопасную сторону: лишний отказ от повтора дешевле, чем отправка
// обрезанного тела во внешнюю систему.
//
// Граница защиты: у строк, записанных до бэкфилла §42.10, `request_size`
// проставлен как длина УЖЕ усечённой копии, поэтому размерный признак там
// всегда ложен — работает только маркер.
func IsTruncatedLogBody(hasMarker bool, storedBytes, fullSize int64) bool {
	return hasMarker || fullSize > storedBytes
}

// ReplayOfParam — служебный query-параметр, которым повтор помечает
// реинжектированный запрос идентификатором оригинала (§7.4.1).
//
// Ставит его web-слой, а ищет — запрос к журналу (§85.6, отсев записей-повторов
// из массового набора). Константа общая ровно поэтому: разойдись эти две
// строки, отсев молча перестал бы работать, и перекрывающиеся окна начали бы
// повторять собственные повторы.
const ReplayOfParam = "__replay_of"

// ReplaySkipReason — почему запись журнала нельзя переотправить (§85.3).
// Пустое значение означает «пригодна».
//
// Значения уходят наружу как ключи счётчиков и i18n-строк, поэтому это
// стабильный контракт: переименование ломает подписи в интерфейсе.
type ReplaySkipReason string

const (
	// ReplaySkipNone — запись пригодна к повтору.
	ReplaySkipNone ReplaySkipReason = ""
	// ReplaySkipNotAsync — запись прошла синхронным путём (§85.4).
	ReplaySkipNotAsync ReplaySkipReason = "record_not_async"
	// ReplaySkipMultipart — в теле §68-плейсхолдер: вложения в ClickHouse не
	// хранятся вовсе, отправить сводку частей вместо тела нельзя.
	ReplaySkipMultipart ReplaySkipReason = "body_multipart"
	// ReplaySkipOriginalUnavailable — §96: оригинального конверта в очереди уже
	// нет (старше retention топика / Kafka не сконфигурирована), а журнальная
	// копия усечена. Отправить её означало бы повторить боевой инцидент 31.08:
	// приёмник получает оборванный JSON, отвечает 500, запись повторяют снова.
	ReplaySkipOriginalUnavailable ReplaySkipReason = "original_unavailable"
	// ReplaySkipBodyMissing — тело не логировалось (log_request_body=false),
	// а метод реинжекции его требует.
	ReplaySkipBodyMissing ReplaySkipReason = "body_missing"
	// ReplaySkipReplayCopy — запись сама является повтором (§85.6).
	ReplaySkipReplayCopy ReplaySkipReason = "already_replay"
)

// ReplayCandidate — проекция записи журнала, достаточная для решения о повторе
// (§85.3). Тело целиком НЕ читается: нужны только его начало (детект
// плейсхолдера §68), признак маркера усечения и обе длины.
type ReplayCandidate struct {
	ID          string
	DateRequest time.Time
	// Type — путь, которым прошла ЗАПИСЬ (не текущий режим узла): у sync-узла
	// бывают async-записи, принятые на паузе (§3.6).
	Type RootMethod
	// HTTPMethod — залогированный глагол (колонка http_method, §39).
	HTTPMethod string
	// Method — подпуть path-passthrough (колонка method, §39); пусто у обычных узлов.
	Method string
	URL    string
	Status int32
	Done   bool
	// RequestSize — истинный размер тела в БАЙТАХ до усечения (§42.10).
	RequestSize int64
	// StoredBytes — размер сохранённой копии тела в байтах.
	StoredBytes int64
	// BodyHead — начало сохранённого тела (первые сотни рун). Пусто, когда тело
	// не логировалось.
	BodyHead string
	// BodyTruncationMarker — в конце сохранённого тела найден
	// LogBodyTruncationMarker. Считается на стороне хранилища: хвост тела в
	// BodyHead не поместился бы.
	BodyTruncationMarker bool
	// IsReplayCopy — в параметрах записи есть служебный маркер `__replay_of`,
	// то есть она сама порождена повтором.
	IsReplayCopy bool
	// OriginalInQueue — §96: оригинальный конверт записи найден в очереди Kafka,
	// то есть повтор отправит ПОЛНОЕ тело, а не усечённую журнальную копию.
	// Заполняется поиском по топикам очереди (dlq → paused → async) до
	// классификации; false означает «не найден», включая упор в cap скана и
	// отсутствие Kafka — во всех этих случаях источником остаётся журнал.
	OriginalInQueue bool
}

// ReplayEffectiveMethod — HTTP-глагол, которым запись будет реинжектирована.
//
// Берётся ВХОДЯЩИЙ метод узла, а не залогированный: последний хранит ИСХОДЯЩИЙ
// глагол (§39), и при несовпадении настроек (POST-in / GET-out) реинжекция
// получала бы 405 (§34.5). Узел «Любой» (§40) валидного глагола не задаёт —
// для него берётся глагол исходного запроса; пустой входящий метод Receiver
// трактует как POST.
func ReplayEffectiveMethod(incoming HTTPMethod, loggedVerb string) string {
	method := string(incoming)
	if incoming == HTTPMethodAny {
		method = loggedVerb
	}
	if method == "" {
		method = string(HTTPMethodPOST)
	}
	return method
}

// ClassifyReplayCandidate — единственная точка решения «можно ли повторить эту
// запись» (§85.3). Её результат питает и предпросмотр, и саму отправку: две
// реализации разошлись бы, и оператор видел бы одно, а отправлялось другое.
//
// effectiveMethod — глагол реинжекции (см. ReplayEffectiveMethod).
// skipReplayCopies — исключать ли записи, порождённые прошлыми повторами (§85.6).
//
// §96: решение зависит не только от журнала, но и от того, найден ли
// оригинальный конверт в очереди (c.OriginalInQueue) — он и есть источник тела.
// Пока конверт жив, НИ ОДИН журнальный признак повтор не блокирует: и усечённая
// копия, и §68-плейсхолдер, и пустое тело у узла с выключенным log_request_body
// говорят лишь о том, чего нет в ЖУРНАЛЕ, а отправлять будем содержимое конверта.
//
// ПОРЯДОК ПРОВЕРОК ЗНАЧИМ: плейсхолдер §68 короче исходного тела, поэтому
// проверка усечения приняла бы его за обрезанное и подменила точную причину
// неточной.
func ClassifyReplayCandidate(c ReplayCandidate, effectiveMethod string, skipReplayCopies bool) ReplaySkipReason {
	if c.Type != RootMethodRequestAsync {
		return ReplaySkipNotAsync
	}
	if skipReplayCopies && c.IsReplayCopy {
		return ReplaySkipReplayCopy
	}
	if c.OriginalInQueue {
		return ReplaySkipNone
	}
	if IsMultipartLogPlaceholder(c.BodyHead) {
		return ReplaySkipMultipart
	}
	if IsTruncatedLogBody(c.BodyTruncationMarker, c.StoredBytes, c.RequestSize) {
		return ReplaySkipOriginalUnavailable
	}
	// Пустое тело у GET — норма, а не «не сохранилось»: тела у него нет by
	// design (боевой кейс legat_by, где GET-записи блокировались 422).
	if c.BodyHead == "" && !strings.EqualFold(effectiveMethod, string(HTTPMethodGET)) {
		return ReplaySkipBodyMissing
	}
	return ReplaySkipNone
}
