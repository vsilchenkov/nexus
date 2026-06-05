package usecase

import (
	"errors"
	"strings"
)

// minPasswordLen — минимальная длина пароля (без учёта обрамляющих пробелов).
const minPasswordLen = 8

// validatePassword проверяет пароль перед хешированием (QA-2026-02 / П19).
//
// Прежняя проверка `len(pw) < 8` пропускала пароль из одних пробелов («        »):
// 8 пробелов = длина 8. Теперь запрещаем пустой/состоящий только из пробельных
// символов пароль и считаем минимальную длину по строке без обрамляющих пробелов.
// Внутренние пробелы (парольная фраза «two words») остаются допустимыми.
func validatePassword(pw string) error {
	trimmed := strings.TrimSpace(pw)
	if trimmed == "" {
		return errors.New("password must not be empty or whitespace only")
	}
	if len([]rune(trimmed)) < minPasswordLen {
		return errors.New("password must be at least 8 characters (excluding leading/trailing spaces)")
	}
	return nil
}
