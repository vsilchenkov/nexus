package domain

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// UserPreference — одна запись персональных предпочтений пользователя (§71):
// настройка UI, привязанная к пользователю и, опционально, к команде.
//
// TeamID пустой означает глобальный преф (в БД team_id IS NULL) — он действует
// во всех командах; непустой перекрывает глобальный в рамках своей команды.
// Двухуровневый резолв «команда → глобальный → системный дефолт» делает клиент.
//
// Value хранится и отдаётся как есть: сервер семантику значения не знает
// намеренно (см. Validate) — таблица generic, и добавление нового префа не
// должно требовать правки бэкенда.
type UserPreference struct {
	UserID    string
	TeamID    string
	Key       string
	Value     json.RawMessage
	UpdatedAt time.Time
}

// PreferenceKeyOverviewPeriod — дефолтный период рабочего стола (§44.B/§71).
// Значение — сериализованный Period фронта: {"kind":"preset","range":"7d"}.
// Контракт значения держит клиент (web-ui/src/lib/period.ts), не сервер.
const PreferenceKeyOverviewPeriod = "overview.period"

// PreferenceKeyNodePeriod — дефолтный период вкладок узла «Обзор» и «Очередь»
// (§92). Значение той же формы, что у PreferenceKeyOverviewPeriod. Ключ один на
// обе вкладки: горизонт наблюдения — свойство узла, а не вкладки.
const PreferenceKeyNodePeriod = "node.period"

// PreferenceKeyKafkaPeriod — дефолтный период монитора Kafka (§92). Отдельный
// от узлового: у брокера свой горизонт наблюдения.
const PreferenceKeyKafkaPeriod = "kafka.period"

// PreferenceKeyNodeMetricsViewPrefix — префикс ключа вида вкладки «Метрики»
// узла (§84.3). Полный ключ собирает PreferenceKeyNodeMetricsView.
const PreferenceKeyNodeMetricsViewPrefix = "node.metrics.view."

// PreferenceKeyNodeMetricsView — ключ вида (период + шаг) для КОНКРЕТНОГО узла
// (§84.3): "node.metrics.view.<id узла без дефисов>".
//
// Одна строка префа на узел, а не карта в одном значении. Карта упирается в
// maxPreferenceValueBytes (4 КиБ) примерно на сороковом узле и требует
// клиентского вытеснения — механизма, который молча теряет настройки
// пользователя. Отдельные строки этой проблемы не имеют вовсе.
//
// Дефисы снимаются не для красоты: формат ключа (preferenceKeyPattern и
// зеркальный CHECK миграции 0031) их не допускает. UUID без дефисов —
// 32 символа нижнего hex, и с префиксом выходит 50 при потолке
// maxPreferenceKeyLen = 64. Запас закреплён тестом: удлинение префикса однажды
// упрётся в 400, и узнать об этом надо здесь, а не на бою.
//
// Значение — {"range":"24h","step":"1h"}; контракт держит клиент, сервер
// значения префов не интерпретирует (§71.3).
func PreferenceKeyNodeMetricsView(nodeID string) string {
	return PreferenceKeyNodeMetricsViewPrefix + strings.ReplaceAll(nodeID, "-", "")
}

const (
	// maxPreferenceKeyLen — совпадает с VARCHAR(64) в миграции 0031.
	maxPreferenceKeyLen = 64
	// maxPreferenceValueBytes — совпадает с CHECK user_preferences_value_size.
	maxPreferenceValueBytes = 4096
)

// preferenceKeyPattern — namespace'ы через точку: "overview.period",
// "logs.filters". Нижний регистр и подчёркивания, без ведущей цифры, без
// пустых сегментов. Зеркалит CHECK user_preferences_key_format (миграция 0031).
var preferenceKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$`)

// Validate проверяет инварианты префа: формат и длину ключа, валидность и
// размер значения. Семантику значения домен не проверяет намеренно (§71.3):
// это generic-хранилище, и знание про конкретные ключи здесь превратило бы
// каждый новый преф в правку доменного слоя.
func (p *UserPreference) Validate() error {
	if p.Key == "" || len(p.Key) > maxPreferenceKeyLen || !preferenceKeyPattern.MatchString(p.Key) {
		return ErrPreferenceKeyInvalid
	}
	if len(p.Value) == 0 || len(p.Value) > maxPreferenceValueBytes || !json.Valid(p.Value) {
		return ErrPreferenceValueInvalid
	}
	// JSON null — «преф есть, но значения нет»: отсутствие префа выражается
	// отсутствием строки, а не null'ом (зеркало CHECK value_not_null).
	if bytes.Equal(bytes.TrimSpace(p.Value), []byte("null")) {
		return ErrPreferenceValueInvalid
	}
	return nil
}

// IsGlobal — преф без привязки к команде (действует во всех её командах).
func (p *UserPreference) IsGlobal() bool { return p.TeamID == "" }
