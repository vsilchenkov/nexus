package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDocker подменяет docker в PATH скриптов: пишет в журнал каждую команду
// с увиденной COMPOSE_FILE и, как настоящий compose, падает на ЗАДАННОЙ, но
// пустой COMPOSE_FILE (compose принимает каталог за файл). Запущенным считает
// только сервис postgres — этого хватает автоопределению режима pg_dump.sh.
const fakeDocker = `#!/bin/sh
echo "COMPOSE_FILE=${COMPOSE_FILE-<unset>} $*" >>"$FAKE_DOCKER_LOG"
if [ "$1" = compose ] && [ "${COMPOSE_FILE+set}" = set ] && [ -z "$COMPOSE_FILE" ]; then
	echo "read .: is a directory" >&2
	exit 1
fi
case "$*" in
*"ps --status running --services"*) echo postgres ;;
esac
exit 0
`

// runDeployScript запускает скрипт scripts/deploy под sh с поддельным docker
// и экспортированной COMPOSE_FILE — так его вызывает rolling.sh.
func runDeployScript(t *testing.T, script string, args ...string) (out, dockerLog string) {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not found in PATH")
	}
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "docker"), []byte(fakeDocker), 0o755))
	logPath := filepath.Join(bin, "docker.log")
	envFile := filepath.Join(bin, ".env")
	require.NoError(t, os.WriteFile(envFile, []byte("PG_USER=nexus\nPG_DATABASE=nexus\n"), 0o600))

	cmd := exec.CommandContext(t.Context(), sh, append([]string{filepath.Join(repoRoot, "scripts", "deploy", script)}, args...)...)
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_DOCKER_LOG="+filepath.ToSlash(logPath),
		"NEXUS_ENV_FILE="+filepath.ToSlash(envFile),
		"COMPOSE_FILE=caller.yml",
	)
	raw, err := cmd.CombinedOutput()
	logRaw, _ := os.ReadFile(logPath) // журнала нет — docker не вызывался, проверки ниже это покажут
	require.NoError(t, err, "%s\ndocker calls:\n%s", raw, logRaw)
	return string(raw), string(logRaw)
}

// TestDeployScripts_KeepCallerComposeFile — скрипты не затирают COMPOSE_FILE
// вызывающего. rolling.sh экспортирует её (стек из двух файлов) и вызывает
// pg_dump.sh, оператор вызывает images.sh rollback из того же окружения.
// Раньше оба скрипта держали СВОЮ переменную с этим именем: присваивание
// `COMPOSE_FILE="${NEXUS_COMPOSE_FILE:-}"` обнуляло унаследованную, откат падал
// на `docker compose up`, а pg_dump.sh молча уходил в режим native.
func TestDeployScripts_KeepCallerComposeFile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		script     string
		args       []string
		wantOut    string
		wantDocker string
	}{
		{
			name:       "images.sh rollback restarts app with caller compose files",
			script:     "images.sh",
			args:       []string{"rollback", "1.0.0", "--apply"},
			wantOut:    "Готово.",
			wantDocker: "COMPOSE_FILE=caller.yml compose up -d --no-build --no-deps --force-recreate web receiver sender",
		},
		{
			name:       "pg_dump.sh detects postgres container via caller compose files",
			script:     "pg_dump.sh",
			args:       []string{"--check"},
			wantOut:    "режим:    docker",
			wantDocker: "COMPOSE_FILE=caller.yml compose ps --status running --services",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out, dockerLog := runDeployScript(t, tt.script, tt.args...)
			assert.Contains(t, out, tt.wantOut)
			assert.Contains(t, dockerLog, tt.wantDocker)
			assert.NotContains(t, dockerLog, "COMPOSE_FILE= compose", "script cleared caller's COMPOSE_FILE")
		})
	}
}

// TestImagesSh_RollbackDoesNotRecreateDependencies — откат кода (§74.5) меняет
// только образы приложения. Без --no-deps `up -d web receiver sender`
// пересоздаёт и зависимости из depends_on, если их спецификация в compose
// разошлась с контейнером (§112 добавил logging всем сервисам) — откат дал
// бы внезапный простой Kafka и PostgreSQL-контейнера. Проверено экспериментом.
// Дублирует строку из теста выше намеренно: тот пропускается без sh.
func TestImagesSh_RollbackDoesNotRecreateDependencies(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "deploy", "images.sh"))
	require.NoError(t, err)
	var restart string
	for line := range strings.Lines(string(raw)) {
		if strings.Contains(line, "restart=") && strings.Contains(line, " up -d") {
			restart = line
		}
	}
	require.NotEmpty(t, restart, "rollback restart command not found in images.sh")
	assert.Contains(t, restart, "--no-deps", "rollback must not recreate dependencies: %s", restart)
}
