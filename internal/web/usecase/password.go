package usecase

import (
	"errors"
	"fmt"
	"strings"
)

// minPasswordLen — минимальная длина пароля (без учёта обрамляющих пробелов).
const minPasswordLen = 8

// ErrPasswordPolicy — пароль не прошёл политику. Обёртка-признак поверх
// конкретного текста: HTTP-слою нужно отличить «пользователь ввёл негодное
// значение» (текст безопасно показать) от внутреннего сбоя (текст показывать
// нельзя — публичный эндпоинт восстановления §88.4.2 иначе отдал бы наружу
// ошибку хранилища).
var ErrPasswordPolicy = errors.New("password policy")

// validatePassword проверяет пароль перед хешированием (QA-2026-02 / П19).
//
// Прежняя проверка `len(pw) < 8` пропускала пароль из одних пробелов («        »):
// 8 пробелов = длина 8. Теперь запрещаем пустой/состоящий только из пробельных
// символов пароль и считаем минимальную длину по строке без обрамляющих пробелов.
// Внутренние пробелы (парольная фраза «two words») остаются допустимыми.
func validatePassword(pw string) error {
	trimmed := strings.TrimSpace(pw)
	if trimmed == "" {
		return fmt.Errorf("%w: password must not be empty or whitespace only", ErrPasswordPolicy)
	}
	if len([]rune(trimmed)) < minPasswordLen {
		return fmt.Errorf("%w: password must be at least %d characters (excluding leading/trailing spaces)",
			ErrPasswordPolicy, minPasswordLen)
	}
	return nil
}
