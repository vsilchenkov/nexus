package bootstrap

import (
	"strings"
	"testing"
)

// claimedBy пишется в VARCHAR(64) (миграция 0029) — длинное имя хоста не должно
// валить заявку идентификатора: подпись диагностическая, обрезка безопасна.
func TestClaimedBy_FitsColumn(t *testing.T) {
	t.Parallel()

	got := claimedBy("Web")
	if got == "" {
		t.Fatal("claimedBy вернул пустую строку")
	}
	if !strings.HasPrefix(got, "Web@") {
		t.Errorf("want prefix %q, got %q", "Web@", got)
	}
	if len(got) > 64 {
		t.Errorf("длина %d > 64: значение не влезет в claimed_by", len(got))
	}

	long := claimedBy(strings.Repeat("s", 200))
	if len(long) != 64 {
		t.Errorf("длинный сервис: want ровно 64 после обрезки, got %d", len(long))
	}
}
