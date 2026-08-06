package ackspec_test

import (
	"strconv"
	"strings"
	"testing"

	"nexus/internal/domain/ackspec"
)

// Бенчмарки отвечают на вопрос «во что обходится ответ по шаблону на горячем
// пути приёма». Ориентир: рендер обязан быть на порядки дешевле похода в Kafka
// (единицы микросекунд), иначе включение спеки деградирует узел.

func benchBody(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"logs":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"logId":`)
		b.WriteString(strconv.Itoa(79000 + i))
		b.WriteString(`,"time":1786034092,"empId":"000004627","internalEmpId":640,` +
			`"accessPoint":9,"direction":2,"keyHex":"7577E3"}`)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func benchTemplate(b *testing.B, tmpl string) *ackspec.Template {
	b.Helper()
	t, err := ackspec.Compile(tmpl, ackspec.ContentTypeJSON)
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	return t
}

// BenchmarkRender_Sigur — боевая форма: один пакет, эхо максимального logId.
func BenchmarkRender_Sigur(b *testing.B) {
	tmpl := benchTemplate(b, `{"confirmedLogId": "${ body.logs[*].logId | max }"}`)
	ctx := &ackspec.Ctx{Body: []byte(sigurBody), ContentType: "application/json"}

	b.ReportAllocs()
	for b.Loop() {
		if _, err := tmpl.Render(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRender_Batch — пакеты по 10/100/1000 записей: стоимость линейна по
// размеру тела (его разбирает encoding/json), а не по шаблону.
func BenchmarkRender_Batch(b *testing.B) {
	tmpl := benchTemplate(b, `{"confirmedLogId": "${ body.logs[*].logId | max }"}`)
	for _, n := range []int{10, 100, 1000} {
		body := benchBody(n)
		ctx := &ackspec.Ctx{Body: body, ContentType: "application/json"}
		b.Run("records="+strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				if _, err := tmpl.Render(ctx); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkRender_NoBodyAccess — шаблон без body.* не должен платить разбором
// тела вовсе (за это отвечает Template.Uses).
func BenchmarkRender_NoBodyAccess(b *testing.B) {
	tmpl := benchTemplate(b, `{"ok": true, "requestId": "${ nexus.id }"}`)
	ctx := &ackspec.Ctx{Body: benchBody(1000), ContentType: "application/json", ID: "req-1"}

	b.ReportAllocs()
	for b.Loop() {
		if _, err := tmpl.Render(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCache_Get vs BenchmarkCompile — во сколько раз кеш дешевле разбора
// шаблона: узел приезжает из Redis строкой, и без кеша это была бы плата на
// каждый принятый запрос.
func BenchmarkCache_Get(b *testing.B) {
	c := ackspec.NewCache(8)
	const tmpl = `{"confirmedLogId": "${ body.logs[*].logId | max }"}`

	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.Get(tmpl, ackspec.ContentTypeJSON); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCompile(b *testing.B) {
	const tmpl = `{"confirmedLogId": "${ body.logs[*].logId | max }"}`

	b.ReportAllocs()
	for b.Loop() {
		if _, err := ackspec.Compile(tmpl, ackspec.ContentTypeJSON); err != nil {
			b.Fatal(err)
		}
	}
}
