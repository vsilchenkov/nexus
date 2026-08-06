package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// §83.12: тело и проверка ответа ack-режима. Ошибка здесь сделала бы
// нагрузочный прогон бессмысленным — «зелёный» отчёт при неправильных ответах.

func TestAckRequestBody_IsValidJSONWithBatch(t *testing.T) {
	t.Parallel()

	for _, size := range []int{0, 100, 1024, 5120} {
		body, wantMax := ackRequestBody(size)

		var parsed struct {
			Logs []struct {
				LogID int64 `json:"logId"`
			} `json:"logs"`
			Note string `json:"note"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("size=%d: body is not valid JSON: %v (%s)", size, err, body)
		}
		if len(parsed.Logs) == 0 {
			t.Fatalf("size=%d: batch must not be empty", size)
		}

		var max int64
		for _, l := range parsed.Logs {
			if l.LogID > max {
				max = l.LogID
			}
		}
		if max != wantMax {
			t.Errorf("size=%d: expected max %d, batch max is %d", size, wantMax, max)
		}
	}
}

// TestAckRequestBody_PadsToProfileSize — без добивки ack-узлы получали бы
// систематически меньшие тела, и сравнение p95 с обычным async было бы нечестным.
func TestAckRequestBody_PadsToProfileSize(t *testing.T) {
	t.Parallel()

	const size = 4096
	body, _ := ackRequestBody(size)

	if len(body) < size/2 {
		t.Fatalf("body is %d bytes for a %d-byte profile — padding did not work", len(body), size)
	}
	if !strings.Contains(string(body), `"note"`) {
		t.Errorf("padding field is missing: %s", body[:min(len(body), 120)])
	}
}

func TestAckResponseValid(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		resp string
		want int64
		ok   bool
	}{
		{"exact echo", `{"confirmedLogId": 79154}`, 79154, true},
		{"other cursor", `{"confirmedLogId": 79000}`, 79154, false},
		// Деградация до штатного ответа даёт тот же 200 — под нагрузкой её
		// видно только по телу.
		{"degraded to default ack", `{"result":true,"id":"uuid"}`, 79154, false},
		{"string instead of number", `{"confirmedLogId": "79154"}`, 79154, false},
		{"empty body", ``, 79154, false},
		{"not json", `oops`, 79154, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ackResponseValid([]byte(tc.resp), tc.want); got != tc.ok {
				t.Errorf("ackResponseValid(%q, %d) = %v", tc.resp, tc.want, got)
			}
		})
	}
}
