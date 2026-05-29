package domain

import (
	"regexp"
	"time"
)

// HeaderCatalogEntry — запись справочника HTTP-заголовков (§24 ТЗ). Каталог
// общий для инсталляции; combobox формы узла подсказывает имена отсюда.
// UsageCount вычисляется on-read (COUNT узлов с этим именем в forward_headers),
// в БД не хранится.
type HeaderCatalogEntry struct {
	ID          string
	Name        string
	Description string
	UsageCount  int
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// headerNamePattern — RFC 7230 token: латиница, цифры и допустимые спецсимволы,
// без пробелов. Двойные кавычки позволяют включить backtick в класс напрямую.
var headerNamePattern = regexp.MustCompile("^[a-zA-Z0-9!#$%&'*+.^_`|~-]+$")

// Validate проверяет имя по RFC 7230 и длины. Та же проверка выполняется
// backend'ом при POST /api/headers, чтобы нельзя было обойти UI (§24).
func (e *HeaderCatalogEntry) Validate() error {
	if l := len(e.Name); l < 1 || l > 100 {
		return ErrHeaderNameLength
	}
	if !headerNamePattern.MatchString(e.Name) {
		return ErrHeaderNameFormat
	}
	if len(e.Description) > 500 {
		return ErrHeaderDescriptionLength
	}
	return nil
}
