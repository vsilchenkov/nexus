package deploy

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeMigrateDocker — docker для pg_migrate_native.sh: журналирует вызовы,
// отдаёт содержимое тома цели (FAKE_VOLUME) на проверку пустоты, вывод
// pg_dumpall (файл FAKE_DUMPALL), а на прочие psql — «1». Временного
// контейнера от прошлой попытки нет (container inspect → 1).
const fakeMigrateDocker = `#!/bin/sh
echo "$*" >>"$FAKE_DOCKER_LOG"
case "$*" in
"container inspect"*) exit 1 ;;
*"--entrypoint sh"*) [ -n "${FAKE_VOLUME:-}" ] && echo "$FAKE_VOLUME"; exit 0 ;;
*" pg_dumpall"*) cat "$FAKE_DUMPALL"; exit 0 ;;
*psql*) echo 1; exit 0 ;;
esac
exit 0
`

const dumpComplete = "--\n-- PostgreSQL database cluster dump complete\n--\n"

type migrateRun struct {
	out, dockerLog string
	exitCode       int
}

// runMigrate запускает pg_migrate_native.sh с поддельным docker в каталоге dir.
func runMigrate(t *testing.T, dir string, env []string, args ...string) migrateRun {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not found in PATH")
	}
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "docker"), []byte(fakeMigrateDocker), 0o755))
	logPath := filepath.Join(bin, "docker.log")
	script, err := filepath.Abs(filepath.Join(repoRoot, "scripts", "deploy", "pg_migrate_native.sh"))
	require.NoError(t, err)

	cmd := exec.CommandContext(t.Context(), sh, append([]string{script}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_DOCKER_LOG="+filepath.ToSlash(logPath),
		"NEXUS_ENV_FILE="+filepath.ToSlash(filepath.Join(dir, ".env")),
		"NEXUS_PG_SRC_UID=999", "NEXUS_PG_SRC_GID=999",
	), env...)
	raw, err := cmd.CombinedOutput()
	code := 0
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		code = exitErr.ExitCode()
	} else {
		require.NoError(t, err)
	}
	logRaw, _ := os.ReadFile(logPath) // docker мог не вызываться вовсе
	return migrateRun{out: string(raw), dockerLog: string(logRaw), exitCode: code}
}

// writeDump кладёт дамп (полный или оборванный) и, по желанию, снимки рядом.
func writeDump(t *testing.T, dir string, complete, snapshots bool) string {
	t.Helper()
	body := "CREATE ROLE nexus;\n"
	if complete {
		body += dumpComplete
	}
	path := filepath.Join(dir, "cluster.sql")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	if snapshots {
		for _, ext := range []string{"dbs", "roles", "members", "counts"} {
			require.NoError(t, os.WriteFile(path+"."+ext, []byte("template1|UTF8|C|C|postgres\n"), 0o600))
		}
	}
	return "cluster.sql"
}

// TestPgMigrate_RestoreRefusesBeforeTouchingTarget — restore не запускает
// временный контейнер (`compose run -d`, который инициализирует том), пока
// дамп и цель не прошли проверки: оборванный дамп, дамп без снимков для
// сверки, непустой том — каждый случай останавливает скрипт раньше.
func TestPgMigrate_RestoreRefusesBeforeTouchingTarget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		complete  bool
		snapshots bool
		volume    string
		wantOut   string
	}{
		{"truncated dump", false, true, "", "оборван"},
		{"dump without snapshots", true, false, "", "нет снимка"},
		{"non-empty target volume", true, true, "PG_VERSION", "не пуст"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			dump := writeDump(t, dir, tt.complete, tt.snapshots)
			r := runMigrate(t, dir, []string{"FAKE_VOLUME=" + tt.volume}, "restore", dump)
			assert.Equal(t, 1, r.exitCode, r.out)
			assert.Contains(t, r.out, tt.wantOut)
			assert.NotContains(t, r.dockerLog, "run -d", "temporary target container must not start")
		})
	}
}

// TestPgMigrate_DumpRejectsTruncatedOutput — оборванный вывод pg_dumpall не
// становится дампом: файла с итоговым именем нет, временный .part убран.
func TestPgMigrate_DumpRejectsTruncatedOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	truncated := filepath.Join(dir, "dumpall.out")
	require.NoError(t, os.WriteFile(truncated, []byte("CREATE ROLE nexus;\n"), 0o600))

	r := runMigrate(t, dir, []string{"FAKE_DUMPALL=" + filepath.ToSlash(truncated)}, "dump", "out/cluster.sql")
	assert.Equal(t, 1, r.exitCode, r.out)
	assert.Contains(t, r.out, "оборван")
	assert.NoFileExists(t, filepath.Join(dir, "out", "cluster.sql"))
	assert.NoFileExists(t, filepath.Join(dir, "out", "cluster.sql.part"))
}

// TestPgMigrate_DumpUsesNewClientOverHostSocket — клиент берётся из образа
// цели и ходит к нативному серверу через сокет хоста от UID postgres, без
// сети и без stdin (иначе в циклах по базам контейнер съел бы ввод цикла).
func TestPgMigrate_DumpUsesNewClientOverHostSocket(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	full := filepath.Join(dir, "dumpall.out")
	require.NoError(t, os.WriteFile(full, []byte("CREATE ROLE nexus;\n"+dumpComplete), 0o600))

	r := runMigrate(t, dir, []string{"FAKE_DUMPALL=" + filepath.ToSlash(full)}, "dump", "out/cluster.sql")
	require.Equal(t, 0, r.exitCode, r.out)
	var dumpCall string
	for line := range strings.Lines(r.dockerLog) {
		if strings.Contains(line, "pg_dumpall") {
			dumpCall = line
		}
	}
	require.NotEmpty(t, dumpCall, r.dockerLog)
	for _, want := range []string{"run --rm --network none --user 999:999", "/var/run/postgresql:/var/run/postgresql", "postgres:16.16-alpine pg_dumpall"} {
		assert.Contains(t, dumpCall, want)
	}
	assert.NotContains(t, r.dockerLog, "run --rm -i", "source client must not read stdin")
	assert.FileExists(t, filepath.Join(dir, "out", "cluster.sql"))
	for _, ext := range []string{"dbs", "roles", "members", "counts"} {
		assert.FileExists(t, filepath.Join(dir, "out", "cluster.sql."+ext))
	}
}

func TestPgMigrate_UnknownSubcommand(t *testing.T) {
	t.Parallel()
	r := runMigrate(t, t.TempDir(), nil, "bogus")
	assert.Equal(t, 2, r.exitCode)
	assert.Contains(t, r.out, "неизвестная подкоманда")
	assert.Empty(t, r.dockerLog, "help path must not call docker")
}
