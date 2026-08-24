package domain

import (
	"regexp"
	"time"
)

// LogMaskPattern — запись справочника маскирования логов узлов (§95): regex по
// колонкам url / method / parameters лога ClickHouse и строка замены (с
// поддержкой групп $1..$9). Sender применяет активные шаблоны в порядке
// sort_order ПЕРЕД записью лога. Справочник глобальный на инсталляцию.
type LogMaskPattern struct {
	ID          string
	Pattern     string
	Replacement string
	Description string
	Enabled     bool
	SortOrder   int
	CreatedBy   string
	UpdatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Validate проверяет длины и КОМПИЛИРУЕМОСТЬ шаблона как Go regexp (RE2). Та же
// проверка выполняется backend'ом при create/update, чтобы в БД не попал
// некомпилируемый шаблон (Sender молча пропустил бы его при сборке набора, и
// секрет утёк бы в лог) и чтобы нельзя было обойти UI (§95).
func (e *LogMaskPattern) Validate() error {
	if l := len(e.Pattern); l < 1 || l > 500 {
		return ErrLogMaskPatternLength
	}
	if _, err := regexp.Compile(e.Pattern); err != nil {
		return ErrLogMaskPatternInvalid
	}
	if len(e.Replacement) > 200 {
		return ErrLogMaskReplacementLength
	}
	if len(e.Description) > 500 {
		return ErrLogMaskDescriptionLength
	}
	if e.SortOrder < 0 {
		return ErrLogMaskSortOrderNegative
	}
	return nil
}
