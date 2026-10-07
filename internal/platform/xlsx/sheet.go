package xlsx

import (
	"fmt"
	"strconv"
	"strings"
)

// MaxColumns — колонок на листе Excel.
const MaxColumns = 1 << 14

// Column — колонка листа: заголовок и ширина в символах (0 — ширина Excel по
// умолчанию). Лист, у колонок которого нет ни одного заголовка, пишется без
// строки заголовка.
type Column struct {
	Title string
	Width float64
}

// SheetOptions — оформление листа.
type SheetOptions struct {
	// FreezeHeader закрепляет строку заголовка при прокрутке.
	FreezeHeader bool
	// AutoFilter ставит кнопки фильтра на заголовок; диапазон охватывает все
	// записанные строки и фиксируется при EndSheet.
	AutoFilter bool
}

// sheetState — открытый лист.
type sheetState struct {
	idx        int
	cols       int
	row        int // номер последней записанной строки, с 1
	autoFilter bool
	letters    []string // буквы колонок по индексу — кеш colName
}

// StartSheet открывает следующий по порядку лист и пишет его заголовок.
func (x *Writer) StartSheet(cols []Column, opt SheetOptions) error {
	if x.closed || x.sheet != nil || x.next >= len(x.names) {
		return ErrState
	}
	if len(cols) > MaxColumns {
		return fmt.Errorf("xlsx: %d columns exceed the sheet limit", len(cols))
	}
	idx := x.next
	if err := x.openPart(sheetPartName(idx)); err != nil {
		return err
	}
	titled := hasTitles(cols)
	x.sheet = &sheetState{idx: idx, cols: len(cols), autoFilter: opt.AutoFilter && titled}
	x.o.str(xmlHeader + `<worksheet xmlns="` + nsMain + `" xmlns:r="` + nsRel + `">`)
	if opt.FreezeHeader && titled {
		x.o.str(`<sheetViews><sheetView workbookViewId="0">` +
			`<pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/>` +
			`<selection pane="bottomLeft"/></sheetView></sheetViews>`)
	}
	x.o.str(colsXML(cols))
	x.o.str(`<sheetData>`)
	if !titled {
		return x.o.err
	}
	header := make([]Cell, len(cols))
	for j, c := range cols {
		header[j] = Cell{k: kStr, s: c.Title, style: styleHeader}
	}
	return x.WriteRow(header...)
}

// WriteRow дописывает строку в открытый лист. Пустые ячейки (нулевой Cell) не
// пишутся вовсе — их место держит адрес следующей.
func (x *Writer) WriteRow(cells ...Cell) error {
	s := x.sheet
	if s == nil {
		return ErrState
	}
	if s.row >= MaxRows {
		return ErrTooManyRows
	}
	if len(cells) > MaxColumns {
		return fmt.Errorf("xlsx: %d cells exceed the sheet limit", len(cells))
	}
	s.row++
	r := strconv.Itoa(s.row)
	x.o.str(`<row r="` + r + `">`)
	for j, c := range cells {
		if c.k == kEmpty {
			continue
		}
		if writeCell(x.o, s.letter(j)+r, c) {
			x.truncated++
		}
	}
	x.o.str(`</row>`)
	if x.o.err != nil {
		return fmt.Errorf("xlsx: write row %d: %w", s.row, x.o.err)
	}
	return nil
}

// EndSheet закрывает лист: дописывает автофильтр (его диапазон известен только
// теперь) и выталкивает часть архива.
func (x *Writer) EndSheet() error {
	s := x.sheet
	if s == nil {
		return ErrState
	}
	x.o.str(`</sheetData>`)
	if s.autoFilter {
		last, end := strconv.Itoa(max(s.row, 1)), s.letter(s.cols-1)
		x.o.str(`<autoFilter ref="A1:` + end + last + `"/>`)
		x.filters[s.idx] = "$A$1:$" + end + "$" + last
	}
	x.o.str(`</worksheet>`)
	x.sheet = nil
	x.next++
	return x.closePart(sheetPartName(s.idx))
}

// letter — буквенное имя колонки j (с 0): A..Z, AA..
func (s *sheetState) letter(j int) string {
	for len(s.letters) <= j {
		s.letters = append(s.letters, colName(len(s.letters)))
	}
	return s.letters[j]
}

// colName — буквенное имя колонки j (с 0): 0 → A, 25 → Z, 26 → AA.
func colName(j int) string {
	var b []byte
	for n := j + 1; n > 0; n = (n - 1) / 26 {
		b = append([]byte{byte('A' + (n-1)%26)}, b...)
	}
	return string(b)
}

func hasTitles(cols []Column) bool {
	for _, c := range cols {
		if c.Title != "" {
			return true
		}
	}
	return false
}

// colsXML — ширины колонок. Элемент <cols> без единого <col> недопустим,
// поэтому без заданных ширин он не пишется вовсе.
func colsXML(cols []Column) string {
	var b strings.Builder
	for j, c := range cols {
		if c.Width <= 0 {
			continue
		}
		fmt.Fprintf(&b, `<col min="%d" max="%d" width="%s" customWidth="1"/>`,
			j+1, j+1, strconv.FormatFloat(c.Width, 'f', -1, 64))
	}
	if b.Len() == 0 {
		return ""
	}
	return "<cols>" + b.String() + "</cols>"
}
