package domain

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"strings"
)

// §68: поддержка multipart/form-data. Тело multipart-запроса (вложения) не
// сохраняется в ClickHouse — вместо него в колонку request/response пишется
// компактный плейсхолдер: первая строка — media type ("multipart/form-data"),
// далее сводка по частям (имя поля, filename, тип, размер в байтах) БЕЗ самого
// содержимого. Истинный размер (request_size/response_size) и checksum считаются
// по полному телу отдельно и от плейсхолдера не зависят.

const (
	// multipartMaxListedParts — потолок числа частей, перечисляемых в
	// плейсхолдере. Константа (не конфиг): плейсхолдер обязан оставаться
	// маленьким при любом входе, лимит max_body_size на него не действует.
	multipartMaxListedParts = 50
	// multipartScanCap — потолок числа итераций по частям (защита от
	// патологического тела с тысячами мелких частей).
	multipartScanCap = 1000
)

// multipartPlaceholderPartPrefix — префикс строки-части в плейсхолдере. Вынесен
// в константу, потому что используется и при построении, и при детекте
// (IsMultipartLogPlaceholder) — обе стороны обязаны совпадать.
const multipartPlaceholderPartPrefix = "- part "

// IsMultipartMediaType сообщает, относится ли Content-Type к семейству
// multipart/* (form-data, mixed, related, …). Пустая строка или мусор без
// корректного media type → false. Регистр и параметры (boundary) игнорируются.
func IsMultipartMediaType(contentType string) bool {
	if contentType == "" {
		return false
	}
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return strings.HasPrefix(mt, "multipart/")
}

// MultipartLogPlaceholder строит текст, сохраняемый в ClickHouse ВМЕСТО
// multipart-тела. Первая строка — media type; далее по строке на часть с её
// метаданными (имя поля, filename, Content-Type, размер) без содержимого;
// последняя строка — сводка (число частей и полный размер тела в байтах).
//
// Функция никогда не помещает в вывод содержимое частей или значения полей.
// При ошибке разбора (нет boundary, оборванное тело) возвращается безопасный
// fallback: media type + строка-примечание. contentType должен относиться к
// multipart/* (проверьте IsMultipartMediaType) — иначе media type в первой
// строке будет тем, что вернул парсер, а тело как multipart не разберётся.
//
// Размеры частей — размер контента ПОСЛЕ декодирования Content-Transfer-Encoding
// (mime/multipart декодирует quoted-printable/base64 прозрачно); для сводки это
// приемлемо, форензику по сырым байтам провода функция не обещает.
func MultipartLogPlaceholder(contentType string, body []byte) string {
	mt, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		// Первой строкой обязан идти валидный multipart media type (на него
		// опирается IsMultipartLogPlaceholder) — сырой битый Content-Type туда
		// не годится, подставляем дефолт.
		return multipartFallback("", len(body), fmt.Sprintf("parse content-type: %v", err))
	}
	boundary := params["boundary"]
	if boundary == "" {
		return multipartFallback(mt, len(body), "no boundary in content-type")
	}

	var b strings.Builder
	b.WriteString(mt)

	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	total := 0
	for {
		if total >= multipartScanCap {
			fmt.Fprintf(&b, "\n[scan capped at %d parts, body %d bytes]", multipartScanCap, len(body))
			return b.String()
		}
		p, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			// Оборванное/битое тело: возвращаем уже собранное + примечание,
			// чтобы оператор видел, что запись неполна. Текст perr сюда НЕ
			// интерполируем: textproto вклеивает в «malformed MIME header line: …»
			// сырую строку из тела — это утечка содержимого в ClickHouse.
			fmt.Fprintf(&b, "\n[truncated after %d parts: malformed multipart body; body %d bytes]", total, len(body))
			return b.String()
		}
		total++
		if total <= multipartMaxListedParts {
			writePartLine(&b, total, p) // читает часть до конца, считая размер
		} else {
			_, _ = io.Copy(io.Discard, p) // сверхлимитную только дренируем
		}
		_ = p.Close()
	}

	if total > multipartMaxListedParts {
		fmt.Fprintf(&b, "\n[listed %d of %d parts, body %d bytes]", multipartMaxListedParts, total, len(body))
	} else {
		fmt.Fprintf(&b, "\n[%d %s, body %d bytes]", total, plural(total, "part", "parts"), len(body))
	}
	return b.String()
}

// writePartLine дописывает в builder одну строку с метаданными части. Размер
// части считается потоково через io.Copy(io.Discard, …) — содержимое в память
// целиком не читается и в вывод не попадает.
func writePartLine(b *strings.Builder, idx int, p *multipart.Part) {
	fmt.Fprintf(b, "\n%s%d: name=%q", multipartPlaceholderPartPrefix, idx, p.FormName())
	if fn := p.FileName(); fn != "" {
		fmt.Fprintf(b, "; filename=%q", fn)
	}
	if ct := p.Header.Get("Content-Type"); ct != "" {
		fmt.Fprintf(b, "; type=%s", ct)
	}
	n, _ := io.Copy(io.Discard, p)
	fmt.Fprintf(b, "; size=%d", n)
}

// multipartFallback — безопасный плейсхолдер при неразобранном теле: media type
// первой строкой (детект IsMultipartLogPlaceholder на неё опирается) + причина.
func multipartFallback(mediaType string, bodyLen int, reason string) string {
	if mediaType == "" {
		mediaType = "multipart/form-data"
	}
	return fmt.Sprintf("%s\n[multipart body not stored; %s; body %d bytes]", mediaType, reason, bodyLen)
}

// IsMultipartLogPlaceholder сообщает, является ли сохранённое в логе тело
// плейсхолдером §68 (а не настоящим телом запроса/ответа). Используется replay'ем
// для отказа: восстановить multipart-тело из плейсхолдера нельзя.
//
// Детект структурный, чтобы не путать с реальными телами: первая строка —
// корректный multipart/* media type, вторая — строка-часть ("- part …") или
// сводка ("["). Настоящее multipart-тело начинается с "--<boundary>", JSON — с
// "{"/"[" в первой строке, поэтому под критерий они не подпадают.
func IsMultipartLogPlaceholder(stored string) bool {
	nl := strings.IndexByte(stored, '\n')
	if nl < 0 {
		return false
	}
	first, rest := stored[:nl], stored[nl+1:]
	if !IsMultipartMediaType(first) {
		return false
	}
	return strings.HasPrefix(rest, multipartPlaceholderPartPrefix) || strings.HasPrefix(rest, "[")
}

// plural возвращает one при n == 1, иначе many. Мелкий помощник для сводки.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
