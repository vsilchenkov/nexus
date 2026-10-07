package xlsx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unzipParts — части архива в порядке записи и их содержимое.
func unzipParts(t *testing.T, b []byte) ([]string, map[string]string) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	require.NoError(t, err)
	order := make([]string, 0, len(zr.File))
	parts := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)
		data, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
		order = append(order, f.Name)
		parts[f.Name] = string(data)
	}
	return order, parts
}

// wellFormed — часть разбирается XML-декодером до конца.
func wellFormed(t *testing.T, name, body string) {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(body))
	for {
		_, err := d.Token()
		if errors.Is(err, io.EOF) {
			return
		}
		require.NoError(t, err, name)
	}
}

func buildBook(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriter(&buf, "Логи", "Параметры")
	require.NoError(t, err)
	require.NoError(t, w.StartSheet([]Column{{Title: "ID", Width: 12}, {Title: "status"}, {Title: "date"}},
		SheetOptions{FreezeHeader: true, AutoFilter: true}))
	ts := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, w.WriteRow(Str("a&b"), Int(200), DateTime(ts)))
	require.NoError(t, w.WriteRow(Str("x"), Empty(), Bool(true)))
	require.NoError(t, w.EndSheet())
	require.NoError(t, w.StartSheet([]Column{{Title: "k"}, {Title: "v"}}, SheetOptions{}))
	require.NoError(t, w.WriteRow(Str("status"), Str("complete")))
	require.NoError(t, w.SetActiveSheet(1))
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func TestWriter_PartsOrderAndWellFormed(t *testing.T) {
	t.Parallel()
	order, parts := unzipParts(t, buildBook(t))

	// Фиксированные части — первыми (детекторы типа файла ищут
	// [Content_Types].xml в начале), книга — последней (ей нужны диапазоны
	// автофильтров и активный лист).
	assert.Equal(t, []string{
		"[Content_Types].xml", "_rels/.rels", "xl/_rels/workbook.xml.rels", "xl/styles.xml",
		"xl/worksheets/sheet1.xml", "xl/worksheets/sheet2.xml", "xl/workbook.xml",
	}, order)
	for name, body := range parts {
		wellFormed(t, name, body)
	}
	assert.Contains(t, parts["[Content_Types].xml"], `PartName="/xl/worksheets/sheet2.xml"`)
	assert.Contains(t, parts["xl/_rels/workbook.xml.rels"], `Id="rId3"`)
	assert.Contains(t, parts["xl/_rels/workbook.xml.rels"], `Target="styles.xml"`)
}

func TestWriter_SheetMarkup(t *testing.T) {
	t.Parallel()
	_, parts := unzipParts(t, buildBook(t))
	s1 := parts["xl/worksheets/sheet1.xml"]

	assert.Contains(t, s1, `state="frozen"`)
	assert.Contains(t, s1, `<cols><col min="1" max="1" width="12" customWidth="1"/></cols>`)
	assert.Contains(t, s1, `<c r="A1" s="1" t="inlineStr"><is><t xml:space="preserve">ID</t></is></c>`)
	assert.Contains(t, s1, `<c r="A2" t="inlineStr"><is><t xml:space="preserve">a&amp;b</t></is></c>`)
	assert.Contains(t, s1, `<c r="B2"><v>200</v></c>`)
	assert.Contains(t, s1, `<c r="C2" s="2"><v>46023.5</v></c>`)
	// Пустая ячейка не пишется: за A3 сразу C3.
	assert.Contains(t, s1, `<row r="3"><c r="A3" t="inlineStr"><is><t xml:space="preserve">x</t></is></c><c r="C3" t="b"><v>1</v></c></row>`)
	assert.Contains(t, s1, `<autoFilter ref="A1:C3"/>`)
	assert.NotContains(t, s1, "<f>")

	wb := parts["xl/workbook.xml"]
	assert.Contains(t, wb, `<workbookView activeTab="1"/>`)
	assert.Contains(t, wb, `<sheet name="Логи" sheetId="1" r:id="rId1"/>`)
	assert.Contains(t, wb, `localSheetId="0" hidden="1">&#39;Логи&#39;!$A$1:$C$3</definedName>`)
	// Второй лист без автофильтра — имени для него нет.
	assert.NotContains(t, wb, `localSheetId="1"`)
	assert.NotContains(t, parts["xl/worksheets/sheet2.xml"], "<autoFilter")
}

func TestWriter_CloseWritesUnstartedSheets(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	w, err := NewWriter(&buf, "A", "B")
	require.NoError(t, err)
	require.NoError(t, w.StartSheet([]Column{{Title: "x"}}, SheetOptions{}))
	require.NoError(t, w.Close())

	order, parts := unzipParts(t, buf.Bytes())
	assert.Contains(t, order, "xl/worksheets/sheet2.xml")
	wellFormed(t, "sheet2", parts["xl/worksheets/sheet2.xml"])
}

func TestWriter_CallOrder(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	w, err := NewWriter(&buf, "A")
	require.NoError(t, err)

	assert.ErrorIs(t, w.WriteRow(Str("x")), ErrState)
	assert.ErrorIs(t, w.EndSheet(), ErrState)
	require.NoError(t, w.StartSheet(nil, SheetOptions{}))
	assert.ErrorIs(t, w.StartSheet(nil, SheetOptions{}), ErrState)
	require.NoError(t, w.EndSheet())
	assert.ErrorIs(t, w.StartSheet(nil, SheetOptions{}), ErrState, "no sheets left")
	assert.ErrorIs(t, w.SetActiveSheet(1), ErrState)
	require.NoError(t, w.Close())
	assert.ErrorIs(t, w.Close(), ErrState)
}

func TestWriter_TooManyRows(t *testing.T) {
	t.Parallel()
	w, err := NewWriter(io.Discard, "A")
	require.NoError(t, err)
	require.NoError(t, w.StartSheet(nil, SheetOptions{}))
	w.sheet.row = MaxRows - 1
	require.NoError(t, w.WriteRow(Int(1)))
	assert.ErrorIs(t, w.WriteRow(Int(2)), ErrTooManyRows)
}

func TestNewWriter_SheetNames(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		sheets  []string
		wantErr bool
	}{
		{"ok", []string{"Логи", "Параметры"}, false},
		{"none", nil, true},
		{"empty", []string{""}, true},
		{"too long", []string{strings.Repeat("я", 32)}, true},
		{"forbidden char", []string{"a/b"}, true},
		{"apostrophe edge", []string{"'a"}, true},
		{"duplicate case-insensitive", []string{"Logs", "logs"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewWriter(io.Discard, tt.sheets...)
			assert.Equal(t, tt.wantErr, err != nil, err)
		})
	}
}

func TestColName(t *testing.T) {
	t.Parallel()
	for j, want := range map[int]string{0: "A", 25: "Z", 26: "AA", 51: "AZ", 52: "BA", 701: "ZZ", 702: "AAA", 16383: "XFD"} {
		assert.Equal(t, want, colName(j), j)
	}
}

func TestSerialAndTimeCells(t *testing.T) {
	t.Parallel()
	msk := time.FixedZone("MSK", 3*3600)
	tests := []struct {
		name string
		t    time.Time
		want float64
	}{
		{"new year", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 46023},
		{"noon", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), 46023.5},
		// Серийная дата — по часам времени в его зоне, а не по UTC.
		{"wall clock of zone", time.Date(2026, 1, 1, 3, 0, 0, 0, msk), 46023.125},
		{"millis", time.Date(2026, 1, 1, 0, 0, 0, 500_000_000, time.UTC), 46023 + 0.5/86400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.InDelta(t, tt.want, serial(tt.t), 1e-9)
		})
	}
	assert.Equal(t, kEmpty, DateTime(time.Time{}).k)
	assert.Equal(t, kEmpty, Date(time.Date(1899, 12, 31, 0, 0, 0, 0, time.UTC)).k)
	assert.Equal(t, kEmpty, Float(nanValue()).k)
	assert.Equal(t, kEmpty, Str("").k, "пустая строка не пишется ячейкой")
}

func nanValue() float64 {
	zero := 0.0
	return zero / zero
}
