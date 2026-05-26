package build

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleVersionInfo = `{
  "FixedFileInfo": {
    "FileVersion": {"Major": 1, "Minor": 2, "Patch": 3, "Build": 4}
  },
  "StringFileInfo": {
    "ProductVersion": "1.2.3",
    "CompanyName": "Acme",
    "FileDescription": "Acme Service",
    "InternalName": "svc",
    "LegalCopyright": "(c) 2026 Acme",
    "OriginalFilename": "svc.exe",
    "ProductName": "Acme Service"
  },
  "IconPath": "/tmp/icon.ico"
}`

func resetLdflagsVars(t *testing.T) {
	t.Helper()
	prevV, prevC, prevD := Version, Commit, BuildDate
	t.Cleanup(func() {
		Version, Commit, BuildDate = prevV, prevC, prevD
	})
	Version, Commit, BuildDate = "", "", ""
}

func TestNewOption_FromVersionInfoJSON(t *testing.T) {
	resetLdflagsVars(t)

	opt, err := NewOption([]byte(sampleVersionInfo), "test-service")
	require.NoError(t, err)
	require.NotNil(t, opt)

	assert.Equal(t, "1.2.3", opt.Version, "version читается из StringFileInfo.ProductVersion")
	assert.Empty(t, opt.Commit)
	assert.Empty(t, opt.BuildDate)
	assert.Equal(t, "test-service", opt.ProjectName)
	assert.Equal(t, "Acme", opt.StringFileInfo.CompanyName)
	assert.Equal(t, 1, opt.FixedFileInfo.FileVersion.Major)
	assert.Equal(t, "/tmp/icon.ico", opt.IconPath)
	assert.NotEmpty(t, opt.WorkingDir, "WorkingDir всегда должен быть заполнен")
}

func TestNewOption_LdflagsOverrideVersion(t *testing.T) {
	resetLdflagsVars(t)
	Version = "9.9.9-rc1"
	Commit = "abc1234"
	BuildDate = "2026-05-26T00:00:00Z"

	opt, err := NewOption([]byte(sampleVersionInfo), "svc")
	require.NoError(t, err)
	assert.Equal(t, "9.9.9-rc1", opt.Version, "ldflags Version должен переопределять versioninfo.json")
	assert.Equal(t, "abc1234", opt.Commit)
	assert.Equal(t, "2026-05-26T00:00:00Z", opt.BuildDate)
	assert.Equal(t, "1.2.3", opt.StringFileInfo.ProductVersion,
		"VersionInfo сам по себе не модифицируется")
}

func TestNewOption_EmptyLdflagsFallsBackToJSON(t *testing.T) {
	resetLdflagsVars(t)
	Version = ""
	opt, err := NewOption([]byte(sampleVersionInfo), "svc")
	require.NoError(t, err)
	assert.Equal(t, "1.2.3", opt.Version)
}

func TestNewOption_InvalidJSON(t *testing.T) {
	resetLdflagsVars(t)
	opt, err := NewOption([]byte(`{"FixedFileInfo": broken`), "svc")
	require.Error(t, err)
	require.Nil(t, opt)
	assert.Contains(t, err.Error(), "parse versioninfo.json")
}

func TestNewOption_EmptyJSON(t *testing.T) {
	resetLdflagsVars(t)
	opt, err := NewOption([]byte(`{}`), "svc")
	require.NoError(t, err)
	require.NotNil(t, opt)
	assert.Empty(t, opt.Version, "пустой JSON → пустая версия")
	assert.Equal(t, "svc", opt.ProjectName)
}

func TestWorkingDir_ReturnsNonEmpty(t *testing.T) {
	// В тестовой среде service.Interactive() возвращает true, поэтому
	// должен вернуться cwd. Главное — путь непустой и не "." (потому что
	// в `go test` cwd всегда определён).
	wd := WorkingDir()
	assert.NotEmpty(t, wd)
}
