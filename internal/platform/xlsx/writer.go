// Package xlsx — потоковая запись книги Excel (.xlsx, Office Open XML) на
// стандартной библиотеке (§115.3).
//
// Строки пишутся сразу в выходной поток через archive/zip: память держит только
// буфер текущей части архива, а не книгу, поэтому выгрузка в сотни мегабайт не
// раздувает процесс. Цена потоковости — порядок: листы пишутся строго по
// очереди, каждый от начала до конца, а книга (xl/workbook.xml) — последней,
// когда известны диапазоны автофильтров и активный лист.
//
// Ячейки-строки всегда пишутся как inlineStr, элемент формулы <f> не пишется
// никогда: значение вида «=HYPERLINK(…)» остаётся текстом и не исполняется при
// открытии файла.
package xlsx

import (
	"archive/zip"
	"bufio"
	"compress/flate"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrState — вызов не по порядку: строка без открытого листа, второй лист
// поверх незакрытого, запись после Close.
var ErrState = errors.New("xlsx: invalid call order")

// ErrTooManyRows — лист упёрся в предел Excel (1 048 576 строк).
var ErrTooManyRows = errors.New("xlsx: sheet row limit exceeded")

// MaxRows — строк на листе Excel, включая заголовок.
const MaxRows = 1 << 20

// partBufSize — буфер записи части архива. Мелкие записи ячеек копятся в нём,
// прежде чем уйти в компрессор.
const partBufSize = 64 << 10

// Writer — потоковая книга. Не безопасен для конкурентного использования.
type Writer struct {
	zw        *zip.Writer
	names     []string
	filters   []string // абсолютный диапазон автофильтра листа ("" — без фильтра)
	active    int
	next      int         // индекс следующего листа
	sheet     *sheetState // открытый лист или nil
	o         *out        // запись открытой части архива
	truncated int
	closed    bool
}

// out — буферизованная запись части архива с «липкой» ошибкой: первая ошибка
// запоминается, последующие записи пропускаются, её возвращает flush. Так
// разметка ячейки пишется подряд, без проверки после каждого фрагмента.
type out struct {
	bw  *bufio.Writer
	err error
}

func (o *out) str(s string) {
	if o.err == nil {
		_, o.err = o.bw.WriteString(s)
	}
}

func (o *out) flush() error {
	if o.err == nil {
		o.err = o.bw.Flush()
	}
	return o.err
}

// NewWriter начинает книгу с листами sheetNames (в этом порядке) и сразу пишет
// части, не зависящие от данных: типы содержимого, связи и стили.
//
// Сжатие — flate.BestSpeed, зарегистрированное на этом экземпляре архива, а не
// глобально: выгрузка ограничена CPU сильнее, чем каналом, а текст логов и на
// быстром уровне сжимается в разы.
func NewWriter(w io.Writer, sheetNames ...string) (*Writer, error) {
	if err := validateSheetNames(sheetNames); err != nil {
		return nil, err
	}
	zw := zip.NewWriter(w)
	zw.RegisterCompressor(zip.Deflate, func(dst io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(dst, flate.BestSpeed)
	})
	x := &Writer{zw: zw, names: sheetNames, filters: make([]string, len(sheetNames))}
	parts := []struct{ name, body string }{
		{"[Content_Types].xml", contentTypesXML(len(sheetNames))},
		{"_rels/.rels", rootRelsXML},
		{"xl/_rels/workbook.xml.rels", workbookRelsXML(len(sheetNames))},
		{"xl/styles.xml", stylesXML},
	}
	for _, p := range parts {
		if err := x.writePart(p.name, p.body); err != nil {
			return nil, err
		}
	}
	return x, nil
}

// SetActiveSheet — лист, на котором Excel откроет книгу (по умолчанию первый).
func (x *Writer) SetActiveSheet(i int) error {
	if i < 0 || i >= len(x.names) {
		return fmt.Errorf("%w: sheet index %d out of range", ErrState, i)
	}
	x.active = i
	return nil
}

// TruncatedCells — сколько текстовых ячеек пришлось обрезать по MaxCellChars.
// Вызывающий, который обрезает значения сам (Truncate с маркером), видит здесь
// только то, что прошло мимо его обрезки.
func (x *Writer) TruncatedCells() int { return x.truncated }

// Flush выталкивает накопленное в выходной поток: буфер открытой части и буфер
// архива. Данные, ещё лежащие в окне компрессора, уйдут со следующими записями.
func (x *Writer) Flush() error {
	if x.o != nil {
		if err := x.o.flush(); err != nil {
			return fmt.Errorf("xlsx: flush part: %w", err)
		}
	}
	if err := x.zw.Flush(); err != nil {
		return fmt.Errorf("xlsx: flush archive: %w", err)
	}
	return nil
}

// Close завершает открытый лист, дописывает пустыми листы, до которых дело не
// дошло (книга со ссылкой на отсутствующую часть не открылась бы), пишет
// xl/workbook.xml и закрывает архив. Выходной поток не закрывает.
func (x *Writer) Close() error {
	if x.closed {
		return ErrState
	}
	if x.sheet != nil {
		if err := x.EndSheet(); err != nil {
			return err
		}
	}
	for x.next < len(x.names) {
		if err := x.StartSheet(nil, SheetOptions{}); err != nil {
			return err
		}
		if err := x.EndSheet(); err != nil {
			return err
		}
	}
	if err := x.writePart("xl/workbook.xml", workbookXML(x.names, x.filters, x.active)); err != nil {
		return err
	}
	x.closed = true
	if err := x.zw.Close(); err != nil {
		return fmt.Errorf("xlsx: close archive: %w", err)
	}
	return nil
}

// openPart начинает новую часть архива; предыдущая закрывается архивом сама.
func (x *Writer) openPart(name string) error {
	pw, err := x.zw.Create(name)
	if err != nil {
		return fmt.Errorf("xlsx: create %s: %w", name, err)
	}
	x.o = &out{bw: bufio.NewWriterSize(pw, partBufSize)}
	return nil
}

// closePart выталкивает буфер части. Ошибка — первая ошибка записи части.
func (x *Writer) closePart(name string) error {
	err := x.o.flush()
	x.o = nil
	if err != nil {
		return fmt.Errorf("xlsx: write %s: %w", name, err)
	}
	return nil
}

func (x *Writer) writePart(name, body string) error {
	if err := x.openPart(name); err != nil {
		return err
	}
	x.o.str(body)
	return x.closePart(name)
}

// validateSheetNames — правила имён листов Excel: 1..31 символ, без []:*?/\,
// без апострофа по краям, без повторов (регистр не различается).
func validateSheetNames(names []string) error {
	if len(names) == 0 {
		return fmt.Errorf("%w: at least one sheet is required", ErrState)
	}
	seen := make(map[string]struct{}, len(names))
	for _, n := range names {
		l := UTF16Len(n)
		if l == 0 || l > 31 || strings.ContainsAny(n, `[]:*?/\`) ||
			strings.HasPrefix(n, "'") || strings.HasSuffix(n, "'") {
			return fmt.Errorf("xlsx: invalid sheet name %q", n)
		}
		key := strings.ToLower(n)
		if _, dup := seen[key]; dup {
			return fmt.Errorf("xlsx: duplicate sheet name %q", n)
		}
		seen[key] = struct{}{}
	}
	return nil
}
