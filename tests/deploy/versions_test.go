package deploy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readRepoFile читает файл репозитория по пути относительно корня.
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
	require.NoError(t, err)
	return string(raw)
}

// firstSubmatch — первая группа re в s; пустая строка, если совпадения нет.
func firstSubmatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[1]
}

// TestGoVersion_SameInGoModDockerfilesAndCI — версия Go одна на go.mod, базовые
// образы сборки и CI. Образ в Dockerfile закреплён точной версией: у golang-
// образов GOTOOLCHAIN=local, и образ старее `go` в go.mod роняет сборку на
// сервере (`go.mod requires go >= …`), а плавающий тег Docker берёт из кеша,
// не сверяясь с реестром (DEPLOYMENT §5.4, §13).
func TestGoVersion_SameInGoModDockerfilesAndCI(t *testing.T) {
	t.Parallel()
	want := firstSubmatch(regexp.MustCompile(`(?m)^go (\S+)$`), readRepoFile(t, "go.mod"))
	require.NotEmpty(t, want, "go directive not found in go.mod")

	ci := firstSubmatch(regexp.MustCompile(`(?m)^  GO_VERSION: "([^"]+)"`), readRepoFile(t, ".gitlab-ci.yml"))
	assert.Equal(t, want, ci, ".gitlab-ci.yml GO_VERSION")

	dockerfiles, err := filepath.Glob(filepath.Join(repoRoot, "deploy", "docker", "*.Dockerfile"))
	require.NoError(t, err)
	fromGolang := regexp.MustCompile(`(?m)^FROM golang:(\S+?)-alpine\b`)
	checked := 0
	for _, path := range dockerfiles {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		got := firstSubmatch(fromGolang, string(raw))
		if got == "" {
			continue // образ без Go-сборки (nginx, kafka)
		}
		checked++
		assert.Equal(t, want, got, "%s: FROM golang", filepath.Base(path))
	}
	// receiver, sender, web, loadtest — защита от пустого прогона.
	assert.GreaterOrEqual(t, checked, 4, "Go Dockerfiles not found")
}

// TestPostgresImage_PinnedEverywhere — PostgreSQL одной точной версии во всех
// compose-файлах и в integration-тестах (ТЗ §116.4). Плавающий тег
// `postgres:16-alpine` Docker Hub двигает на каждый минорный выпуск, и сервер,
// стек ПК и раннер CI оказывались на разных версиях. Integration-тесты
// гоняются на минимальной поддерживаемой версии (§71) — она же боевая.
func TestPostgresImage_PinnedEverywhere(t *testing.T) {
	t.Parallel()
	exact := regexp.MustCompile(`^postgres:\d+\.\d+-alpine$`)
	want := firstSubmatch(regexp.MustCompile(`(?m)^const defaultPostgresImage = "([^"]+)"`),
		readRepoFile(t, "tests/integration/node_repo_test.go"))
	require.Regexp(t, exact, want, "defaultPostgresImage must be an exact minor version")

	checked := 0
	for _, path := range composePaths(t) {
		for name, svc := range loadCompose(t, path).Services {
			img, _ := svc["image"].(string)
			if !strings.HasPrefix(img, "postgres:") {
				continue
			}
			checked++
			assert.Equal(t, want, img, "%s: service %s", filepath.Base(path), name)
		}
	}
	// корневой, deploy/docker-compose.yml, deploy/docker-compose.deps.yml
	assert.GreaterOrEqual(t, checked, 3, "postgres services not found")
}
