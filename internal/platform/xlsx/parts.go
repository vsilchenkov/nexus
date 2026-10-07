package xlsx

import (
	"fmt"
	"html"
	"strings"
)

const xmlHeader = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"

const (
	nsMain = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
	nsRel  = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	nsPkg  = "http://schemas.openxmlformats.org/package/2006/relationships"
)

// Стили книги (индексы cellXfs). Свои форматы дат — 164/165: встроенный 14
// зависит от региональных настроек Excel, а выгрузке нужен однозначный вид.
const (
	styleDefault  = 0
	styleHeader   = 1 // жирный шрифт
	styleDateTime = 2 // yyyy-mm-dd hh:mm:ss
	styleDate     = 3 // yyyy-mm-dd
)

const rootRelsXML = xmlHeader +
	`<Relationships xmlns="` + nsPkg + `">` +
	`<Relationship Id="rId1" Type="` + nsRel + `/officeDocument" Target="xl/workbook.xml"/>` +
	`</Relationships>`

// stylesXML — минимальная таблица стилей. Обе заливки (none и gray125)
// обязательны: без второй Excel считает файл повреждённым.
const stylesXML = xmlHeader +
	`<styleSheet xmlns="` + nsMain + `">` +
	`<numFmts count="2">` +
	`<numFmt numFmtId="164" formatCode="yyyy-mm-dd hh:mm:ss"/>` +
	`<numFmt numFmtId="165" formatCode="yyyy-mm-dd"/>` +
	`</numFmts>` +
	`<fonts count="2">` +
	`<font><sz val="11"/><name val="Calibri"/><family val="2"/></font>` +
	`<font><b/><sz val="11"/><name val="Calibri"/><family val="2"/></font>` +
	`</fonts>` +
	`<fills count="2"><fill><patternFill patternType="none"/></fill>` +
	`<fill><patternFill patternType="gray125"/></fill></fills>` +
	`<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>` +
	`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>` +
	`<cellXfs count="4">` +
	`<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>` +
	`<xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/>` +
	`<xf numFmtId="164" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>` +
	`<xf numFmtId="165" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>` +
	`</cellXfs>` +
	`<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles>` +
	`</styleSheet>`

func sheetPartName(i int) string { return fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1) }

func contentTypesXML(sheets int) string {
	var b strings.Builder
	b.WriteString(xmlHeader)
	b.WriteString(`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`)
	b.WriteString(`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>`)
	b.WriteString(`<Default Extension="xml" ContentType="application/xml"/>`)
	b.WriteString(`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>`)
	for i := range sheets {
		fmt.Fprintf(&b, `<Override PartName="/%s" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`,
			sheetPartName(i))
	}
	b.WriteString(`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>`)
	b.WriteString(`</Types>`)
	return b.String()
}

// workbookRelsXML — связи книги: листы rId1..rIdN, стили — rId(N+1).
func workbookRelsXML(sheets int) string {
	var b strings.Builder
	b.WriteString(xmlHeader)
	b.WriteString(`<Relationships xmlns="` + nsPkg + `">`)
	for i := range sheets {
		fmt.Fprintf(&b, `<Relationship Id="rId%d" Type="%s/worksheet" Target="worksheets/sheet%d.xml"/>`,
			i+1, nsRel, i+1)
	}
	fmt.Fprintf(&b, `<Relationship Id="rId%d" Type="%s/styles" Target="styles.xml"/>`, sheets+1, nsRel)
	b.WriteString(`</Relationships>`)
	return b.String()
}

// workbookXML — книга: список листов, активный лист и скрытые имена
// _xlnm._FilterDatabase. Без такого имени Excel показывает кнопки автофильтра,
// но при сортировке/фильтрации считает диапазон заново и может его потерять.
func workbookXML(names, filters []string, active int) string {
	var b strings.Builder
	b.WriteString(xmlHeader)
	b.WriteString(`<workbook xmlns="` + nsMain + `" xmlns:r="` + nsRel + `">`)
	fmt.Fprintf(&b, `<bookViews><workbookView activeTab="%d"/></bookViews>`, active)
	b.WriteString(`<sheets>`)
	for i, n := range names {
		fmt.Fprintf(&b, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, html.EscapeString(n), i+1, i+1)
	}
	b.WriteString(`</sheets>`)
	var defined strings.Builder
	for i, ref := range filters {
		if ref == "" {
			continue
		}
		quoted := "'" + strings.ReplaceAll(names[i], "'", "''") + "'!" + ref
		fmt.Fprintf(&defined, `<definedName name="_xlnm._FilterDatabase" localSheetId="%d" hidden="1">%s</definedName>`,
			i, html.EscapeString(quoted))
	}
	if defined.Len() > 0 {
		b.WriteString(`<definedNames>` + defined.String() + `</definedNames>`)
	}
	b.WriteString(`</workbook>`)
	return b.String()
}
