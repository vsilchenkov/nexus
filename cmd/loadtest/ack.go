package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
)

// §83: нагрузочная проверка шаблона ответа приёма.
//
// Смысл режима не в самом факте ответа, а в двух вопросах, на которые юнит-тесты
// не отвечают: во что обходится рендер под нагрузкой (per-mode p95 рядом с
// обычным async на том же прогоне) и остаётся ли ответ ПРАВИЛЬНЫМ при
// параллельных запросах — то есть не подмешался ли шаблон соседнего узла из
// общего кеша.

// ackTemplate — боевая форма §83: эхо максимального logId пакета.
const ackTemplate = `{"confirmedLogId": "${ body.logs[*].logId | max }"}`

// ackBatchMax — сколько записей кладём в пакет. Значения близки к боевым
// (СКУД шлёт единицы-десятки записей), а разброс проверяет ветку max по набору.
const ackBatchMax = 8

// ackRequestBody собирает тело под ackTemplate и возвращает ожидаемый ответ.
// Размер тела подгоняется под профиль нагрузки добивкой поля note: иначе
// ack-узлы получали бы систематически меньшие тела, чем остальные режимы, и
// сравнение p95 было бы нечестным.
func ackRequestBody(size int) (body []byte, wantMaxLogID int64) {
	n := 1 + rand.Intn(ackBatchMax)
	logs := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		id := int64(79000 + rand.Intn(100000))
		if id > wantMaxLogID {
			wantMaxLogID = id
		}
		logs = append(logs, map[string]any{
			"logId":   id,
			"empId":   fmt.Sprintf("%09d", rand.Intn(1000000)),
			"keyHex":  randomHex(6),
			"accessP": rand.Intn(20),
		})
	}
	payload := map[string]any{"logs": logs}
	raw, err := json.Marshal(payload)
	if err != nil {
		return []byte(`{"logs":[]}`), 0
	}
	if pad := size - len(raw) - len(`,"note":""`); pad > 0 {
		payload["note"] = strings.Repeat("a", pad)
		if padded, perr := json.Marshal(payload); perr == nil {
			raw = padded
		}
	}
	return raw, wantMaxLogID
}

// ackResponseValid — ответ обязан быть ровно эхом максимального logId.
// Ошибка здесь означает не «медленно», а «шина ответила не то»: перепутанный
// шаблон, потерянная точность числа или деградация до штатного {result,id}.
func ackResponseValid(respBody []byte, wantMaxLogID int64) bool {
	var got struct {
		ConfirmedLogID *int64 `json:"confirmedLogId"`
	}
	if err := json.Unmarshal(respBody, &got); err != nil {
		return false
	}
	return got.ConfirmedLogID != nil && *got.ConfirmedLogID == wantMaxLogID
}
