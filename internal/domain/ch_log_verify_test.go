package domain

import "testing"

// requiredAsActual — полный набор обязательных колонок как «фактическая» схема.
func requiredAsActual() []CHLogColumn {
	out := make([]CHLogColumn, len(RequiredLogColumns))
	copy(out, RequiredLogColumns)
	return out
}

func TestVerifyLogTableColumns(t *testing.T) {
	t.Parallel()

	// mutate возвращает копию эталона с применённым изменением.
	mutate := func(f func([]CHLogColumn) []CHLogColumn) []CHLogColumn {
		return f(requiredAsActual())
	}
	drop := func(name string) []CHLogColumn {
		return mutate(func(cols []CHLogColumn) []CHLogColumn {
			out := cols[:0]
			for _, c := range cols {
				if c.Name != name {
					out = append(out, c)
				}
			}
			return out
		})
	}
	retype := func(name, typ string) []CHLogColumn {
		return mutate(func(cols []CHLogColumn) []CHLogColumn {
			for i := range cols {
				if cols[i].Name == name {
					cols[i].Type = typ
				}
			}
			return cols
		})
	}

	tests := []struct {
		name           string
		actual         []CHLogColumn
		wantMissing    []string
		wantMismatched []LogColumnMismatch
	}{
		{
			name:   "точное совпадение",
			actual: requiredAsActual(),
		},
		{
			name: "лишние колонки допускаются",
			actual: mutate(func(cols []CHLogColumn) []CHLogColumn {
				return append(cols, CHLogColumn{"tenant_code", "String"}, CHLogColumn{"trace_id", "UUID"})
			}),
		},
		{
			name:        "нет колонки",
			actual:      drop("node_id"),
			wantMissing: []string{"node_id"},
		},
		{
			name:        "пустая таблица — не хватает всего",
			actual:      nil,
			wantMissing: allRequiredNames(),
		},
		{
			name:           "неверный тип",
			actual:         retype("status", "String"),
			wantMismatched: []LogColumnMismatch{{Name: "status", Want: "Int32", Got: "String"}},
		},
		{
			name:   "DateTime с таймзоной эквивалентен DateTime",
			actual: retype("date_request", "DateTime('UTC')"),
		},
		{
			name:   "пробелы в параметрах типа не считаются расхождением",
			actual: retype("checksum_request", "FixedString( 32 )"),
		},
		{
			// Bool — алиас UInt8 (то же физическое представление), а LogRecord.Done
			// в Go объявлен как bool: драйвер принимает обе формы и на запись, и на
			// чтение. Проверено на живом ClickHouse.
			name:   "done Bool эквивалентен UInt8",
			actual: retype("done", "Bool"),
		},
		{
			name:           "Nullable — честное расхождение",
			actual:         retype("reason", "Nullable(String)"),
			wantMismatched: []LogColumnMismatch{{Name: "reason", Want: "String", Got: "Nullable(String)"}},
		},
		{
			name:           "LowCardinality — честное расхождение",
			actual:         retype("type", "LowCardinality(String)"),
			wantMismatched: []LogColumnMismatch{{Name: "type", Want: "String", Got: "LowCardinality(String)"}},
		},
		{
			name:           "DateTime64 не подменяет DateTime",
			actual:         retype("date_response", "DateTime64(3)"),
			wantMismatched: []LogColumnMismatch{{Name: "date_response", Want: "DateTime", Got: "DateTime64(3)"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := VerifyLogTableColumns(tc.actual)

			if len(got.Missing) != len(tc.wantMissing) {
				t.Fatalf("missing: want %v, got %v", tc.wantMissing, got.Missing)
			}
			for i, name := range tc.wantMissing {
				if got.Missing[i] != name {
					t.Fatalf("missing[%d]: want %q, got %q", i, name, got.Missing[i])
				}
			}

			if len(got.Mismatched) != len(tc.wantMismatched) {
				t.Fatalf("mismatched: want %v, got %v", tc.wantMismatched, got.Mismatched)
			}
			for i, want := range tc.wantMismatched {
				if got.Mismatched[i] != want {
					t.Fatalf("mismatched[%d]: want %+v, got %+v", i, want, got.Mismatched[i])
				}
			}

			wantOK := len(tc.wantMissing) == 0 && len(tc.wantMismatched) == 0
			if got.OK() != wantOK {
				t.Fatalf("OK(): want %v, got %v", wantOK, got.OK())
			}
		})
	}
}

func allRequiredNames() []string {
	out := make([]string, 0, len(RequiredLogColumns))
	for _, c := range RequiredLogColumns {
		out = append(out, c.Name)
	}
	return out
}
