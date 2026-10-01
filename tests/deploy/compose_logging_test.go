// Package deploy — тесты инвариантов поставки: compose-файлов и шаблона .env.
//
// Compose-файлы не компилируются и не исполняются в unit-прогоне, поэтому
// забытый в новом сервисе ключ виден только на боевом диске — спустя месяцы.
// Здесь такие ключи проверяются разбором YAML.
package deploy

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Ожидаемый блок logging (§112.2). Строки сверяются ДО подстановки
// переменных: один набор .env-переменных и один дефолт на весь стек.
const (
	wantLogDriver  = "json-file"
	wantLogMaxSize = "${DOCKER_LOG_MAX_SIZE:-50m}"
	wantLogMaxFile = "${DOCKER_LOG_MAX_FILE:-5}"
)

// repoRoot — корень репозитория относительно каталога пакета.
const repoRoot = "../.."

// composeService — сервис compose-файла после разворачивания якорей и `<<:`.
// Псевдоним, а не новый тип: yaml.v3 разворачивает вложенные отображения в
// тип внешней карты, и у именованного типа блок logging не прошёл бы
// приведение к map[string]any — проверка молча сочла бы его отсутствующим.
type composeService = map[string]any

// composeFile — интересующая тест часть compose-файла.
type composeFile struct {
	Services map[string]composeService `yaml:"services"`
}

// composePaths возвращает все отслеживаемые compose-файлы: корневой прод
// (вариант C) и всё, что лежит в deploy/.
func composePaths(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(repoRoot, "deploy", "docker-compose*.yml"))
	require.NoError(t, err)
	paths = append(paths, filepath.Join(repoRoot, "docker-compose.yml"))
	// Защита от пустого прогона: шаблон, не нашедший файлов, дал бы зелёный тест.
	require.GreaterOrEqual(t, len(paths), 10, "compose files not found: %v", paths)
	return paths
}

func loadCompose(t *testing.T, path string) composeFile {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var f composeFile
	require.NoError(t, yaml.Unmarshal(raw, &f), path)
	return f
}

// createsContainer — сервис объявляет контейнер, а не заплатку оверлея:
// у него есть образ или сборка. Заплатки (ports, profiles, mem_limit…)
// наследуют logging из базового файла при наложении.
func createsContainer(svc composeService) bool {
	_, hasImage := svc["image"]
	_, hasBuild := svc["build"]
	return hasImage || hasBuild
}

// checkLogging возвращает описание расхождения с §112.2 или "".
func checkLogging(svc composeService) string {
	logging, ok := svc["logging"].(map[string]any)
	if !ok {
		return "no logging block"
	}
	if logging["driver"] != wantLogDriver {
		return "logging.driver is not " + wantLogDriver
	}
	opts, ok := logging["options"].(map[string]any)
	if !ok {
		return "no logging.options"
	}
	if opts["max-size"] != wantLogMaxSize {
		return "logging.options.max-size is not " + wantLogMaxSize
	}
	if opts["max-file"] != wantLogMaxFile {
		return "logging.options.max-file is not " + wantLogMaxFile
	}
	return ""
}

// TestCompose_EveryContainerHasLogRotation — §112: у каждого контейнера стека
// лог json-file ограничен по размеру. Без этого Docker пишет вывод без потолка
// (на бою nginx — 1.2 ГБ за 4 недели), и обнуляет его только пересоздание.
func TestCompose_EveryContainerHasLogRotation(t *testing.T) {
	t.Parallel()
	for _, path := range composePaths(t) {
		f := loadCompose(t, path)
		for name, svc := range f.Services {
			if !createsContainer(svc) {
				continue
			}
			if problem := checkLogging(svc); problem != "" {
				t.Errorf("%s: service %q: %s (§112.2: logging: *logging)", path, name, problem)
			}
		}
	}
}

// TestCompose_ProdStackContainersChecked — страж от пустой проверки: в
// корневом (боевом) файле правило выше обязано дойти до каждого контейнера.
// Если разбор якорей или признак «создаёт контейнер» сломается, этот тест
// покраснеет, а не позеленеет молча вместе с предыдущим.
func TestCompose_ProdStackContainersChecked(t *testing.T) {
	t.Parallel()
	f := loadCompose(t, filepath.Join(repoRoot, "docker-compose.yml"))
	var got []string
	for name, svc := range f.Services {
		if createsContainer(svc) {
			got = append(got, name)
		}
	}
	slices.Sort(got)
	want := []string{"kafka", "nginx", "postgres", "prometheus", "receiver", "redis", "sender", "web"}
	assert.Equal(t, want, got)
}

// TestEnvExample_DocumentsLogRotation — переменные §112 видны оператору в
// шаблоне .env, а не только в compose.
func TestEnvExample_DocumentsLogRotation(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(repoRoot, ".env.example"))
	require.NoError(t, err)
	for _, key := range []string{"DOCKER_LOG_MAX_SIZE=", "DOCKER_LOG_MAX_FILE="} {
		assert.True(t, strings.Contains(string(raw), "\n"+key), ".env.example lacks %s", key)
	}
}
