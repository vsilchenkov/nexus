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

// fakeMigrateDocker — docker для pg_migrate_native.sh: журналирует вызовы и
// отвечает на то, что скрипт спрашивает у источника и цели.
//   - container inspect → 1 (временного контейнера прошлой попытки нет);
//   - compose config → FAKE_COMPOSE_CONFIG (спецификация сервиса postgres);
//   - проверка тома (--entrypoint sh) → FAKE_VOLUME (содержимое каталога данных);
//   - pg_dumpall → файл FAKE_DUMPALL;
//   - список клиентов (pg_stat_activity) → FAKE_CLIENTS;
//   - счётчик записи (pg_stat_user_tables) → число из FAKE_WRITES_FILE, которое
//     после каждого чтения растёт на FAKE_WRITES_STEP (так имитируется запись);
//   - список баз → «nexus»; остальные запросы psql → «1».
const fakeMigrateDocker = `#!/bin/sh
echo "$*" >>"$FAKE_DOCKER_LOG"
case "$*" in
"container inspect"*) exit 1 ;;
"compose config"*) [ -n "${FAKE_COMPOSE_CONFIG:-}" ] && printf '%s\n' "$FAKE_COMPOSE_CONFIG"; exit 0 ;;
*"--entrypoint sh"*) [ -n "${FAKE_VOLUME:-}" ] && echo "$FAKE_VOLUME"; exit 0 ;;
*" pg_dumpall"*) cat "$FAKE_DUMPALL"; exit 0 ;;
*pg_stat_activity*) [ -n "${FAKE_CLIENTS:-}" ] && echo "$FAKE_CLIENTS"; exit 0 ;;
*pg_stat_user_tables*)
	if [ -n "${FAKE_WRITES_FILE:-}" ]; then
		n=$(cat "$FAKE_WRITES_FILE"); echo "$n"; echo $((n + ${FAKE_WRITES_STEP:-0})) >"$FAKE_WRITES_FILE"
	else
		echo 0
	fi
	exit 0 ;;
*"select datname from pg_database where datallowconn"*) echo nexus; exit 0 ;;
*psql*) echo 1; exit 0 ;;
esac
exit 0
`

const dumpComplete = "--\n-- PostgreSQL database cluster dump complete\n--\n"

// publicPortsConfig — спецификация, где 5432 опубликован на всех интерфейсах
// (корневой compose без override): host_ip не задан.
const publicPortsConfig = `name: nexus
services:
  postgres:
    image: postgres:16.16-alpine
    ports:
      - mode: ingress
        target: 5432
        published: "5432"
        protocol: tcp
    networks:
      nexus_net: null`

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
		"NEXUS_PG_SRC_UID=999", "NEXUS_PG_SRC_GID=999", "NEXUS_PG_STATS_DELAY=0",
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

// snapshotParts — файлы рядом с дампом, без которых restore/fence/verify его не примут.
var snapshotParts = []string{"dbs", "roles", "members", "settings", "conf", "counts", "schema", "writes", "post.sql"}

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
		for _, part := range snapshotParts {
			require.NoError(t, os.WriteFile(path+"."+part, []byte("nexus|42\n"), 0o600))
		}
	}
	return "cluster.sql"
}

// writesFile — счётчик записи источника для поддельного docker.
func writesFile(t *testing.T, dir, value string) string {
	t.Helper()
	path := filepath.Join(dir, "writes.counter")
	require.NoError(t, os.WriteFile(path, []byte(value+"\n"), 0o600))
	return filepath.ToSlash(path)
}

// TestPgMigrate_RestoreRefusesBeforeTouchingTarget — restore не запускает
// временный контейнер (`compose run -d`, который инициализирует том), пока
// дамп и цель не прошли проверки: оборванный дамп, дамп без снимков для
// сверки, непустой том, порт 5432 на всех интерфейсах (ревизия §116.5:
// роли всех баз кластера открылись бы для перебора паролей из сети).
func TestPgMigrate_RestoreRefusesBeforeTouchingTarget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		complete  bool
		snapshots bool
		env       []string
		wantOut   string
	}{
		{"truncated dump", false, true, nil, "оборван"},
		{"dump without snapshots", true, false, nil, "нет снимка"},
		{"non-empty target volume", true, true, []string{"FAKE_VOLUME=PG_VERSION"}, "не пуст"},
		{"port published on all interfaces", true, true, []string{"FAKE_COMPOSE_CONFIG=" + publicPortsConfig}, "на всех интерфейсах"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			dump := writeDump(t, dir, tt.complete, tt.snapshots)
			r := runMigrate(t, dir, tt.env, "restore", dump)
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
// сети и без stdin (иначе в циклах по базам контейнер съел бы ввод цикла);
// рядом с дампом — все снимки для сверки и ограждения.
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
	for _, part := range snapshotParts {
		assert.FileExists(t, filepath.Join(dir, "out", "cluster.sql."+part))
	}
}

// TestPgMigrate_DumpRefusesConnectedClients — подключённый к источнику клиент
// может писать после дампа, и эти записи молча потеряются: dump отказывает
// до pg_dumpall (ревизия §116.5).
func TestPgMigrate_DumpRefusesConnectedClients(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := runMigrate(t, dir, []string{"FAKE_CLIENTS=billing | app | 10.0.0.5 | worker"}, "dump", "out/cluster.sql")
	assert.Equal(t, 1, r.exitCode, r.out)
	assert.Contains(t, r.out, "billing | app | 10.0.0.5")
	assert.Contains(t, r.out, "остановите клиентов")
	assert.NotContains(t, r.dockerLog, "pg_dumpall")
}

// TestPgMigrate_DumpDetectsWritesDuringDump — счётчик записи вырос за время
// дампа: дамп неполный, и .writes не создаётся — без него restore такой дамп
// не примет.
func TestPgMigrate_DumpDetectsWritesDuringDump(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	full := filepath.Join(dir, "dumpall.out")
	require.NoError(t, os.WriteFile(full, []byte("CREATE ROLE nexus;\n"+dumpComplete), 0o600))

	r := runMigrate(t, dir, []string{
		"FAKE_DUMPALL=" + filepath.ToSlash(full),
		"FAKE_WRITES_FILE=" + writesFile(t, dir, "100"), "FAKE_WRITES_STEP=5",
	}, "dump", "out/cluster.sql")
	assert.Equal(t, 1, r.exitCode, r.out)
	assert.Contains(t, r.out, "во время дампа в источник писали")
	assert.NoFileExists(t, filepath.Join(dir, "out", "cluster.sql.writes"))
}

// TestPgMigrate_Fence — ограждение перед остановкой нативного сервера:
// счётчик записи тот же, что при дампе, — можно останавливать; вырос — нет.
func TestPgMigrate_Fence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		current  string
		wantCode int
		wantOut  string
	}{
		{"no writes after dump", "42", 0, "можно останавливать"},
		{"writes after dump", "43", 1, "после дампа в источник писали"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			dump := writeDump(t, dir, true, true) // .writes = «nexus|42»
			r := runMigrate(t, dir, []string{"FAKE_WRITES_FILE=" + writesFile(t, dir, tt.current)}, "fence", dump)
			assert.Equal(t, tt.wantCode, r.exitCode, r.out)
			assert.Contains(t, r.out, tt.wantOut)
		})
	}
}

func TestPgMigrate_UnknownSubcommand(t *testing.T) {
	t.Parallel()
	r := runMigrate(t, t.TempDir(), nil, "bogus")
	assert.Equal(t, 2, r.exitCode)
	assert.Contains(t, r.out, "неизвестная подкоманда")
	assert.Empty(t, r.dockerLog, "help path must not call docker")
}
