package pg

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openSource открывает файловый источник миграций на временном каталоге с
// перечисленными файлами. Подключение к БД не требуется: максимальная локальная
// версия считается по источнику (§74.3).
func openSource(t *testing.T, files ...string) source.Driver {
	t.Helper()
	dir := t.TempDir()
	for _, name := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("SELECT 1;"), 0o600))
	}
	src, err := source.Open("file://" + filepath.ToSlash(dir))
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })
	return src
}

func TestMaxLocalVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files []string
		want  uint
	}{
		{
			name:  "пустой каталог — версий нет",
			files: nil,
			want:  0,
		},
		{
			name:  "одна миграция",
			files: []string{"0001_init.up.sql", "0001_init.down.sql"},
			want:  1,
		},
		{
			name: "несколько миграций — берётся последняя",
			files: []string{
				"0001_init.up.sql", "0001_init.down.sql",
				"0002_nodes.up.sql", "0002_nodes.down.sql",
				"0003_users.up.sql", "0003_users.down.sql",
			},
			want: 3,
		},
		{
			name: "дыра в нумерации не обрывает обход",
			files: []string{
				"0001_init.up.sql", "0001_init.down.sql",
				"0007_late.up.sql", "0007_late.down.sql",
			},
			want: 7,
		},
		{
			name:  "версия только с down-файлом тоже считается",
			files: []string{"0001_init.up.sql", "0001_init.down.sql", "0002_drop_only.down.sql"},
			want:  2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := maxLocalVersion(openSource(t, tt.files...))
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
