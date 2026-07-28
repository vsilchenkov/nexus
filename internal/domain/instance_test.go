package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestInstanceID_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		id      InstanceID
		wantErr bool
	}{
		{name: "пусто — нода до §70", id: "", wantErr: false},
		{name: "один символ", id: "b", wantErr: false},
		{name: "два символа (боевой пример)", id: "kz", wantErr: false},
		{name: "три символа", id: "edo", wantErr: false},
		{name: "буква и цифры", id: "pr2", wantErr: false},
		{name: "восемь символов — верхняя граница", id: "abcdefgh", wantErr: false},
		{name: "девять символов", id: "abcdefghi", wantErr: true},
		{name: "начинается с цифры", id: "2kz", wantErr: true},
		{name: "подчёркивание запрещено", id: "kz_1", wantErr: true},
		{name: "верхний регистр", id: "KZ", wantErr: true},
		{name: "дефис", id: "kz-1", wantErr: true},
		{name: "пробел", id: "kz 1", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.id.Validate()
			if tt.wantErr {
				if !errors.Is(err, ErrInstanceIDFormat) {
					t.Fatalf("id=%q: want ErrInstanceIDFormat, got %v", tt.id, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("id=%q: want nil, got %v", tt.id, err)
			}
		})
	}
}

func TestInstanceID_CHDatabase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		id         InstanceID
		slug       string
		wantPrefix string
		wantDB     string
	}{
		{name: "без идентификатора", id: "", slug: "default", wantPrefix: "nexus_", wantDB: "nexus_default"},
		{name: "нода kz", id: "kz", slug: "default", wantPrefix: "nexus_kz_", wantDB: "nexus_kz_default"},
		{name: "нода kz, произвольная команда", id: "kz", slug: "edo", wantPrefix: "nexus_kz_", wantDB: "nexus_kz_edo"},
		{name: "слаг с подчёркиванием", id: "", slug: "vika_dev", wantPrefix: "nexus_", wantDB: "nexus_vika_dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.id.CHDatabasePrefix(); got != tt.wantPrefix {
				t.Errorf("prefix: want %q, got %q", tt.wantPrefix, got)
			}
			if got := tt.id.CHDatabase(tt.slug); got != tt.wantDB {
				t.Errorf("database: want %q, got %q", tt.wantDB, got)
			}
		})
	}
}

// Совместимость (§70.10): при пустом идентификаторе имя обязано совпадать с тем,
// что строила прежняя CHDatabaseForSlug — иначе боевая нода после обновления
// пойдёт в другие таблицы.
func TestCHDatabaseForSlug_MatchesEmptyInstance(t *testing.T) {
	t.Parallel()

	for _, slug := range []string{"default", "vika", "task_geo", "a"} {
		if got, want := CHDatabaseForSlug(slug), InstanceID("").CHDatabase(slug); got != want {
			t.Errorf("slug=%q: CHDatabaseForSlug=%q, InstanceID(\"\").CHDatabase=%q", slug, got, want)
		}
		if want := "nexus_" + slug; CHDatabaseForSlug(slug) != want {
			t.Errorf("slug=%q: want %q, got %q", slug, want, CHDatabaseForSlug(slug))
		}
	}
}

func TestInstanceID_ValidateTeamSlug(t *testing.T) {
	t.Parallel()

	// Бюджет: 40 символов после "nexus_", из них суффикс ноды съедает len(id)+1.
	if got := InstanceID("").MaxTeamSlugLen(); got != 40 {
		t.Fatalf("MaxTeamSlugLen(\"\"): want 40, got %d", got)
	}
	if got := InstanceID("kz").MaxTeamSlugLen(); got != 37 {
		t.Fatalf("MaxTeamSlugLen(\"kz\"): want 37, got %d", got)
	}

	tests := []struct {
		name    string
		id      InstanceID
		slug    string
		wantErr bool
	}{
		{name: "обычный слаг помещается везде", id: "kz", slug: "edo", wantErr: false},
		{name: "граница для kz", id: "kz", slug: strings.Repeat("a", 37), wantErr: false},
		{name: "на символ длиннее границы", id: "kz", slug: strings.Repeat("a", 38), wantErr: true},
		{name: "граница без идентификатора", id: "", slug: strings.Repeat("a", 40), wantErr: false},
		{name: "длиннее границы без идентификатора", id: "", slug: strings.Repeat("a", 41), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.id.ValidateTeamSlug(tt.slug)
			if tt.wantErr {
				if !errors.Is(err, ErrTeamSlugTooLongForInstance) {
					t.Fatalf("want ErrTeamSlugTooLongForInstance, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("want nil, got %v", err)
			}
			// Слаг, прошедший бюджет, обязан давать имя БД, проходящее Team.Validate:
			// иначе проверка бюджета бессмысленна.
			team := &Team{Slug: "s", Name: "n", CHDatabase: tt.id.CHDatabase(tt.slug)}
			if err := team.Validate(); err != nil {
				t.Fatalf("ch_database %q не прошла Team.Validate: %v", team.CHDatabase, err)
			}
		})
	}
}

// §70.2: имя БД не является доказательством владения — слаг допускает '_',
// поэтому нода без идентификатора с командой "kz_edo" даёт то же имя, что нода
// "kz" с командой "edo". Тест фиксирует эту коллизию как известную: её ловит
// только маркер владения (§70.3), а не разбор имени.
func TestInstanceID_OwnsCHDatabase_NameIsNotProofOfOwnership(t *testing.T) {
	t.Parallel()

	const collided = "nexus_kz_edo"
	if got := InstanceID("kz").CHDatabase("edo"); got != collided {
		t.Fatalf("нода kz + команда edo: want %q, got %q", collided, got)
	}
	if got := InstanceID("").CHDatabase("kz_edo"); got != collided {
		t.Fatalf("нода без id + команда kz_edo: want %q, got %q", collided, got)
	}
	if !InstanceID("kz").OwnsCHDatabase(collided) {
		t.Error("нода kz должна считать nexus_kz_edo своим по имени")
	}
	if !InstanceID("").OwnsCHDatabase(collided) {
		t.Error("нода без id тоже считает nexus_kz_edo своим по имени — это и есть коллизия")
	}
	if InstanceID("kz").OwnsCHDatabase("nexus_default") {
		t.Error("nexus_default не принадлежит пространству имён ноды kz")
	}
	// Префикс без слага — не имя БД команды.
	if InstanceID("kz").OwnsCHDatabase("nexus_kz_") {
		t.Error("пустой слаг не должен считаться именем БД")
	}
}
