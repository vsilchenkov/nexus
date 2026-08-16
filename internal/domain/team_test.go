package domain

import (
	"errors"
	"strings"
	"testing"
)

// newTeam — валидная команда, у которой в тестах меняется одно поле.
func newTeam(externalURL string) *Team {
	return &Team{
		Slug:        "alpha",
		Name:        "Alpha",
		CHDatabase:  "nexus_alpha",
		ExternalURL: externalURL,
	}
}

// §89.4: внешняя ссылка команды — БАЗА, к которой интерфейс приклеивает путь
// узла. Отсюда и правила: путь разрешён (внешний шлюз обычно публикует шину под
// своим префиксом), query и fragment — нет, хвостовой слеш срезается.
func TestTeamValidate_ExternalURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    string // ожидаемое значение ПОСЛЕ нормализации
		wantErr bool
	}{
		{name: "пусто — ссылка не задана", in: "", want: ""},
		{name: "https без пути", in: "https://gw.example.com", want: "https://gw.example.com"},
		{name: "http допустим", in: "http://gw.example.com", want: "http://gw.example.com"},
		{name: "порт допустим", in: "https://gw.example.com:8443", want: "https://gw.example.com:8443"},
		{
			// Основной сценарий: шлюз публикует шину под своим префиксом.
			// ValidatePublicBaseURL (§28) такой адрес отвергает — здесь нельзя.
			name: "путь разрешён",
			in:   "https://gw.example.com/nexus",
			want: "https://gw.example.com/nexus",
		},
		{
			// Иначе адрес узла получил бы «//» посередине, и глазами это не видно.
			name: "хвостовой слеш срезается",
			in:   "https://gw.example.com/nexus/",
			want: "https://gw.example.com/nexus",
		},
		{name: "несколько хвостовых слешей", in: "https://gw.example.com///", want: "https://gw.example.com"},
		{name: "пробелы по краям срезаются", in: "  https://gw.example.com  ", want: "https://gw.example.com"},

		{name: "без схемы", in: "gw.example.com/nexus", wantErr: true},
		{name: "чужая схема", in: "ftp://gw.example.com", wantErr: true},
		{name: "без хоста", in: "https:///nexus", wantErr: true},
		{name: "query запрещён", in: "https://gw.example.com/nexus?a=1", wantErr: true},
		{name: "fragment запрещён", in: "https://gw.example.com/nexus#x", wantErr: true},
		{
			name:    "длиннее потолка",
			in:      "https://gw.example.com/" + strings.Repeat("a", MaxTeamExternalURLLen),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			team := newTeam(tt.in)
			err := team.Validate()
			if tt.wantErr {
				if !errors.Is(err, ErrTeamExternalURLInvalid) {
					t.Fatalf("Validate(%q) = %v, want ErrTeamExternalURLInvalid", tt.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate(%q) = %v, want nil", tt.in, err)
			}
			// Нормализация «на месте» — контракт Validate: в репозиторий и в
			// ответ API уходит ровно проверенное значение.
			if team.ExternalURL != tt.want {
				t.Fatalf("после Validate ExternalURL = %q, want %q", team.ExternalURL, tt.want)
			}
		})
	}
}

// Ошибка внешней ссылки не должна маскировать ошибки обязательных полей:
// slug и name проверяются раньше.
func TestTeamValidate_OrderOfChecks(t *testing.T) {
	t.Parallel()

	team := &Team{Slug: "BAD SLUG", Name: "", CHDatabase: "nexus_x", ExternalURL: "ftp://x"}
	if err := team.Validate(); !errors.Is(err, ErrTeamSlugFormat) {
		t.Fatalf("Validate() = %v, want ErrTeamSlugFormat", err)
	}
}
