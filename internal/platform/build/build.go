package build

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kardianos/service"
)

// Эти переменные заполняются линкером через -ldflags при release-сборке
// (GoReleaser / make build-release). В обычной сборке остаются пустыми, и тогда
// версия читается из встроенного versioninfo.json (см. NewOption).
var (
	Version   = ""
	Commit    = ""
	BuildDate = ""
)

// NewOption создаёт Option из встроенного versioninfo.json + projectName.
// Если переменные пакета Version/Commit/BuildDate заданы через ldflags
// (release-сборка), они переопределяют значения из versioninfo.json.
func NewOption(versionInfoData []byte, projectName string) (*Option, error) {
	var vi VersionInfo
	if err := json.Unmarshal(versionInfoData, &vi); err != nil {
		return nil, fmt.Errorf("parse versioninfo.json: %w", err)
	}
	version := vi.StringFileInfo.ProductVersion
	if Version != "" {
		version = Version
	}
	return &Option{
		VersionInfo: vi,
		Version:     version,
		Commit:      Commit,
		BuildDate:   BuildDate,
		ProjectName: projectName,
		Interactive: service.Interactive(),
		WorkingDir:  WorkingDir(),
	}, nil
}

// WorkingDir возвращает рабочий каталог: cwd в интерактивном режиме,
// каталог с исполняемым файлом — в режиме службы.
func WorkingDir() string {
	if service.Interactive() {
		if cwd, err := os.Getwd(); err == nil {
			return cwd
		}
		return "."
	}
	exePath, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exePath)
}
