package domain

import (
	"regexp"
	"time"
)

// RequestFieldCatalogEntry — запись справочника «полей запроса» (§41): имена
// HTTP-заголовков / query-параметров, откуда берётся креда для динамической
// авторизации (исходящей token/basic_from_request и входящей token/basic).
// Каталог общий для инсталляции; combobox формы узла подсказывает имена отсюда.
// UsageCount вычисляется on-read (COUNT узлов с этим именем в auth_dynamic_field
// или incoming_auth_dynamic_field), в БД не хранится.
type RequestFieldCatalogEntry struct {
	ID          string
	Name        string
	Description string
	UsageCount  int
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// requestFieldNamePattern — то же ограничение, что у nodes.auth_dynamic_field
// (имя HTTP-заголовка / query-параметра): латиница на старте, далее буквы,
// цифры, дефис и подчёркивание. Гарантирует, что выбранное из каталога имя
// пройдёт валидацию узла.
var requestFieldNamePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

// Validate проверяет имя и длины. Та же проверка выполняется backend'ом при
// POST /api/request-fields, чтобы нельзя было обойти UI (§41).
func (e *RequestFieldCatalogEntry) Validate() error {
	if l := len(e.Name); l < 1 || l > 64 {
		return ErrRequestFieldNameLength
	}
	if !requestFieldNamePattern.MatchString(e.Name) {
		return ErrRequestFieldNameFormat
	}
	if len(e.Description) > 500 {
		return ErrRequestFieldDescriptionLength
	}
	return nil
}
