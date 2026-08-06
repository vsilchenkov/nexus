// Package ackspec — мини-язык шаблонов ответа приёма (ТЗ §83).
//
// Шина принимает async-запрос в очередь и отвечает клиенту сразу, до доставки
// получателю. Клиентам с курсором (СКУД, callback-провайдеры) стандартного
// {"result":true,"id":…} мало: они двигают свой указатель только по
// эхо-подтверждению из собственного тела запроса. Спека узла описывает такой
// ответ декларативно, без кода под конкретную интеграцию.
//
// Грамматика шаблона:
//
//	template    = { text | "$${" | placeholder }
//	placeholder = "${" ws expr ws "}"
//	expr        = source "." path [ ws "|" ws fn ]
//	source      = "body" | "query" | "path" | "nexus"
//	path        = segment { "." segment | index }
//	index       = "[" ( int | "*" | jsonstring ) "]"
//	fn          = "max" | "min" | "first" | "last" | "count" | "default" "(" literal ")"
//
// Путь возвращает НАБОР значений: функция схлопывает его в одно, без функции
// набор размера ≠ 1 — ошибка (ReasonAmbiguous / ReasonPathNotFound).
//
// Правило типизации (только для application/json): плейсхолдер, вплотную
// окружённый кавычками, подставляется JSON-литералом ВМЕСТЕ с ними —
// `"${ body.logs[*].logId | max }"` даёт `79154`, а не `"79154"`. Иначе значение
// интерполируется в строку с экранированием. Ради этого числа проходят через
// json.Number: 79154 обязано вернуться как 79154, а не 7.9154e+04.
//
// Заголовки запроса источником не являются намеренно: там Authorization.
package ackspec

import (
	"errors"
	"fmt"
	"net/http"
)

// Version — единственная поддерживаемая версия спеки. Спека с другой версией
// отвергается валидацией: молча игнорировать неизвестные поля нельзя, ответ
// клиенту получился бы не тем, что описал оператор.
const Version = 1

// Лимиты — константы кода, а не настройки узла: они защищают горячий путь
// приёма, и возможность поднять их на узле означала бы возможность его
// замедлить. Превышение на рендере отрабатывается политикой Spec.OnError.
const (
	// MaxTemplateBytes — потолок текста шаблона. Зеркалит CHECK на колонке.
	MaxTemplateBytes = 8 << 10
	// MaxPlaceholders — сколько подстановок разрешено в одном шаблоне.
	MaxPlaceholders = 32
	// MaxPathDepth — глубина пути внутри тела.
	MaxPathDepth = 16
	// MaxSelectedValues — потолок набора, который может вернуть путь с [*].
	MaxSelectedValues = 1000
	// MaxParseBodyBytes — выше этого тело не парсится вовсе (ReasonBodyTooLarge).
	// receiver.max_body_bytes допускает 5 МиБ, разбирать столько на каждый
	// запрос ради эхо-подтверждения незачем.
	MaxParseBodyBytes = 1 << 20
	// MaxOutputBytes — потолок отрендеренного тела: защита от усиления, когда
	// шаблон возвращает клиенту кусок его же многомегабайтного запроса.
	MaxOutputBytes = 64 << 10
)

// ContentType — формат тела ответа. Белый список, а не свободная строка:
// значение уходит в заголовок Content-Type, где свободная строка означала бы
// инъекцию заголовка.
type ContentType string

const (
	ContentTypeJSON ContentType = "application/json"
	ContentTypeText ContentType = "text/plain"
)

// Valid сообщает, поддерживается ли формат.
func (c ContentType) Valid() bool {
	return c == ContentTypeJSON || c == ContentTypeText
}

// HTTPValue — готовое значение заголовка Content-Type ответа.
func (c ContentType) HTTPValue() string {
	if c == ContentTypeText {
		return "text/plain; charset=utf-8"
	}
	return "application/json; charset=utf-8"
}

// OnError — что делать, когда подстановка не удалась.
type OnError string

const (
	// OnErrorDefault — ответить прежним {"result":true,"id":…}. Запрос при этом
	// ПРИНЯТ и лежит в очереди: отказывать из-за шаблона поздно и нечестно.
	OnErrorDefault OnError = "default"
	// OnErrorError — отдать 400 и НЕ принимать запрос в очередь. Рендер идёт до
	// публикации в Kafka именно ради этой ветки: иначе клиент получил бы отказ
	// на уже принятое сообщение и повторил его — то есть шина сама породила бы
	// дубли, ради устранения которых §83 и написан.
	OnErrorError OnError = "error"
)

// Valid сообщает, поддерживается ли политика.
func (o OnError) Valid() bool {
	return o == OnErrorDefault || o == OnErrorError
}

// Spec — содержимое колонки nodes.async_ack_spec. nil-спека у узла означает
// прежний ответ шины, поэтому zero value безопасно: переживший выкат кеш узла
// без этого поля даёт ровно старое поведение.
type Spec struct {
	Version     int         `json:"version"`
	Status      int         `json:"status,omitempty"`
	ContentType ContentType `json:"content_type"`
	Body        string      `json:"body"`
	OnError     OnError     `json:"on_error"`
}

// Доменные ошибки пакета. ErrSpecInvalid и ErrTemplateSyntax — ошибки
// КОНФИГУРАЦИИ (форма узла, 400 с подсветкой поля), ErrRenderFailed — ошибка
// ВЫПОЛНЕНИЯ на живом запросе (её обрабатывает Spec.OnError).
var (
	ErrSpecInvalid    = errors.New("ackspec: invalid spec")
	ErrTemplateSyntax = errors.New("ackspec: invalid template syntax")
	ErrRenderFailed   = errors.New("ackspec: render failed")
)

// SetDefaults заполняет незаданные поля спеки. Вызывается перед Validate:
// форма узла присылает только осмысленную часть, а версия/формат/политика
// имеют единственный разумный дефолт.
func (s *Spec) SetDefaults() {
	if s == nil {
		return
	}
	if s.Version == 0 {
		s.Version = Version
	}
	if s.ContentType == "" {
		s.ContentType = ContentTypeJSON
	}
	if s.OnError == "" {
		s.OnError = OnErrorDefault
	}
}

// Validate проверяет спеку целиком, включая компиляцию шаблона и то, что после
// подстановки заглушек получается синтаксически валидный JSON. Вторая проверка
// нужна, потому что ошибку вида `{"a": ${x}` рендер обнаружит только на живом
// трафике, когда чинить её уже поздно.
//
// nil-спека валидна: это «отвечать как раньше».
func (s *Spec) Validate() error {
	if s == nil {
		return nil
	}
	if s.Version != Version {
		return fmt.Errorf("%w: unsupported version %d", ErrSpecInvalid, s.Version)
	}
	if !s.ContentType.Valid() {
		return fmt.Errorf("%w: unknown content_type %q", ErrSpecInvalid, s.ContentType)
	}
	if !s.OnError.Valid() {
		return fmt.Errorf("%w: unknown on_error %q", ErrSpecInvalid, s.OnError)
	}
	// Только 2xx: код уходит в nexus_requests_total{status} и в алерты, а
	// 4xx/5xx из успешной ветки означал бы «приняли, но отчитались об ошибке».
	if s.Status != 0 && (s.Status < 200 || s.Status > 299) {
		return fmt.Errorf("%w: status %d is not 2xx", ErrSpecInvalid, s.Status)
	}
	if s.Body == "" {
		return fmt.Errorf("%w: body template is empty", ErrSpecInvalid)
	}
	t, err := Compile(s.Body, s.ContentType)
	if err != nil {
		return err
	}
	return t.validateSkeleton()
}

// EffectiveStatus — код ответа для конкретного приёма. Ноль в спеке означает
// «как было до §83»: 202 для узла на паузе (§3.6), иначе 200. Поэтому
// включение шаблона само по себе не перекрашивает метрики.
func (s *Spec) EffectiveStatus(queued bool) int {
	if s != nil && s.Status != 0 {
		return s.Status
	}
	if queued {
		return http.StatusAccepted
	}
	return http.StatusOK
}
