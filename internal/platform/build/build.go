package build

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kardianos/service"
)

// NewOption создаёт Option из встроенного versioninfo.json + projectName.
func NewOption(versionInfoData []byte, projectName string) (*Option, error) {
	var vi VersionInfo
	if err := json.Unmarshal(versionInfoData, &vi); err != nil {
		return nil, fmt.Errorf("parse versioninfo.json: %w", err)
	}
	return &Option{
		VersionInfo: vi,
		Version:     vi.StringFileInfo.ProductVersion,
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
