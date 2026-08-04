package domain

import "testing"

// TestTeamSlugReserved (§78.3) — слаги, занятые сегментами-методами боевого
// адреса. Команда с таким слагом сделала бы короткую форму §78.1
// (/api/v1/<slug>/<path>) неоднозначной.
func TestTeamSlugReserved(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"request":      true,
		"callback":     true,
		"requestasync": true, // каноническое requestAsync формату слага не удовлетворяет
		"Request":      true, // сравнение регистронезависимое
		" request ":    true, // на случай непротримленного ввода
		"webhook":      false,
		"default":      false,
		"requests":     false, // похожий, но не совпадающий слаг допустим
		"":             false,
	}
	for slug, want := range cases {
		if got := TeamSlugReserved(slug); got != want {
			t.Errorf("TeamSlugReserved(%q) = %v, want %v", slug, got, want)
		}
	}
}

// TestTeamValidate_AllowsReservedSlug — Validate НЕ обязан отвергать
// зарезервированный слаг: он вызывается и при переименовании уже существующей
// команды, а такие команды §78.3 ломать не должен (запрет живёт в
// TeamUsecase.Create — только для новых).
func TestTeamValidate_AllowsReservedSlug(t *testing.T) {
	t.Parallel()
	legacy := &Team{Slug: "request", Name: "Legacy", CHDatabase: "nexus_request"}
	if err := legacy.Validate(); err != nil {
		t.Fatalf("существующая команда с зарезервированным слагом обязана оставаться валидной, got %v", err)
	}
}
