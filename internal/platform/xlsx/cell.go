package xlsx

import (
	"math"
	"strconv"
	"time"
)

type kind uint8

const (
	kEmpty kind = iota
	kStr
	kInt
	kFloat
	kBool
	kTime
)

// Cell — значение ячейки. Нулевое значение — пустая ячейка.
type Cell struct {
	k     kind
	s     string
	n     int64
	f     float64
	t     time.Time
	style int
}

// Empty — пустая ячейка.
func Empty() Cell { return Cell{} }

// Str — текст. Длиннее MaxCellChars обрезается (см. Writer.TruncatedCells);
// чтобы обрезка была видна в самой ячейке, урежьте значение заранее через
// Truncate с маркером.
func Str(s string) Cell { return Cell{k: kStr, s: s} }

// Int — целое число.
func Int(n int64) Cell { return Cell{k: kInt, n: n} }

// Float — число. NaN и бесконечности Excel не хранит — ячейка остаётся пустой.
func Float(f float64) Cell {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return Cell{}
	}
	return Cell{k: kFloat, f: f}
}

// Bool — логическое значение (ИСТИНА/ЛОЖЬ).
func Bool(b bool) Cell {
	c := Cell{k: kBool}
	if b {
		c.n = 1
	}
	return c
}

// DateTime — дата и время по часам t в её собственной зоне. Excel часовых
// поясов не знает, поэтому вызывающий заранее переводит время в нужную зону
// (t.In(loc)). Нулевое время и даты до 1900 года, которых Excel не
// представляет, — пустая ячейка.
func DateTime(t time.Time) Cell { return timeCell(t, styleDateTime) }

// Date — только дата, по тем же правилам, что DateTime.
func Date(t time.Time) Cell { return timeCell(t, styleDate) }

func timeCell(t time.Time, style int) Cell {
	if t.IsZero() || t.Year() < 1900 {
		return Cell{}
	}
	return Cell{k: kTime, t: t, style: style}
}

// writeCell пишет разметку ячейки; true — текст обрезан по MaxCellChars.
func writeCell(o *out, ref string, c Cell) bool {
	switch c.k {
	case kStr:
		o.str(`<c r="` + ref + `"` + styleAttr(c.style) + ` t="inlineStr"><is><t xml:space="preserve">`)
		cut := writeText(o, c.s, MaxCellChars)
		o.str(`</t></is></c>`)
		return cut
	case kInt:
		o.str(`<c r="` + ref + `"><v>` + strconv.FormatInt(c.n, 10) + `</v></c>`)
	case kFloat:
		o.str(`<c r="` + ref + `"><v>` + strconv.FormatFloat(c.f, 'g', -1, 64) + `</v></c>`)
	case kBool:
		o.str(`<c r="` + ref + `" t="b"><v>` + strconv.FormatInt(c.n, 10) + `</v></c>`)
	case kTime:
		o.str(`<c r="` + ref + `"` + styleAttr(c.style) + `><v>` +
			strconv.FormatFloat(serial(c.t), 'f', -1, 64) + `</v></c>`)
	}
	return false
}

func styleAttr(s int) string {
	if s == styleDefault {
		return ""
	}
	return ` s="` + strconv.Itoa(s) + `"`
}

// excelEpochMs — 1899-12-30 00:00 UTC в Unix-миллисекундах: день 0 серийных
// дат Excel (система 1900 с её «29 февраля 1900» отсчитывает от этого дня).
const excelEpochMs = -2209161600000

// serial — серийная дата Excel по часам t в её зоне, с точностью до мс.
func serial(t time.Time) float64 {
	wall := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
	return float64(wall.UnixMilli()-excelEpochMs) / 86_400_000
}
