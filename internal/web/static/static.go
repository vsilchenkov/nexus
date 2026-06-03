// Package static — встроенные ассеты SPA через embed.FS (§17.1 ТЗ).
//
// В Phase 3 backend здесь живёт только index.html-заглушка с
// REST API-документацией. Когда команда фронта пересоберёт SPA
// через `make build-ui`, файлы из /web-ui/dist копируются сюда
// (или меняется embed-директива на чтение оттуда напрямую).
package static

import (
	"embed"
	"io/fs"
)

//go:embed index.html assets
var rawFS embed.FS

// FS возвращает корень embed-файловой системы.
// SPA fallback на index.html делает Handler в adapter/in/http.
func FS() fs.FS {
	return rawFS
}
