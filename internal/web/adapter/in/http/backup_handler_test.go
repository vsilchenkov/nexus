package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain/backup"
	"nexus/internal/platform/backupfile"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// fakeBackupService — usecase раздела с заранее заданными ответами.
type fakeBackupService struct {
	status     *usecase.BackupStatus
	file       *usecase.BackupFile
	err        error
	passphrase string
}

func (f *fakeBackupService) Status(context.Context) (*usecase.BackupStatus, error) {
	return f.status, f.err
}

func (f *fakeBackupService) Export(_ context.Context, _ usecase.Actor, passphrase string) (*usecase.BackupFile, error) {
	f.passphrase = passphrase
	return f.file, f.err
}

func backupEngine(svc backupService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewBackupHandler(svc, logging.NewNoop())
	r.GET("/api/backup/status", h.Status)
	r.POST("/api/backup/export", h.Export)
	return r
}

func TestBackupHandler_Export(t *testing.T) {
	t.Parallel()
	svc := &fakeBackupService{file: &usecase.BackupFile{
		Name: "nexus-backup-kz-20260929-173005.json", Data: []byte(`{"format":"nexus-config-backup"}`),
	}}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/backup/export",
		strings.NewReader(`{"passphrase":"correct horse battery"}`))
	backupEngine(svc).ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "correct horse battery", svc.passphrase)
	assert.Equal(t, `attachment; filename="nexus-backup-kz-20260929-173005.json"`, w.Header().Get("Content-Disposition"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, `{"format":"nexus-config-backup"}`, w.Body.String())
}

func TestBackupHandler_ExportErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		body     string
		err      error
		wantCode int
		wantKey  string
	}{
		{"нет пароля", `{}`, nil, http.StatusBadRequest, "backup.passphrase_required"},
		{"пароль в query не принимается", ``, nil, http.StatusBadRequest, "backup.passphrase_required"},
		{"короткий пароль", `{"passphrase":"short"}`, backupfile.ErrPassphraseTooShort, http.StatusBadRequest, "backup.passphrase_too_short"},
		{"снимок не восстановится", `{"passphrase":"correct horse battery"}`,
			fmt.Errorf("export: %w", backup.ErrInvalidSnapshot), http.StatusInternalServerError, "backup.snapshot_invalid"},
		{"прочая ошибка", `{"passphrase":"correct horse battery"}`, errors.New("pg down"), http.StatusInternalServerError, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/backup/export?passphrase=correct+horse+battery",
				strings.NewReader(tt.body))
			backupEngine(&fakeBackupService{err: tt.err}).ServeHTTP(w, req)
			require.Equal(t, tt.wantCode, w.Code)
			assert.Empty(t, w.Header().Get("Content-Disposition"), "при ошибке файл не отдаётся")
			if tt.wantKey == "" {
				return
			}
			var resp map[string]string
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, tt.wantKey, resp["code"])
			assert.NotContains(t, resp["error"], "%!", "аргумент перевода подставлен")
		})
	}
}

func TestBackupHandler_Status(t *testing.T) {
	t.Parallel()
	svc := &fakeBackupService{status: &usecase.BackupStatus{
		BackupTargetState: port.BackupTargetState{Nodes: 0, Teams: 1, Users: 1, Fresh: true},
		CHAvailable:       true, InstanceID: "kz", NexusVersion: "v1.39.0", SchemaVersion: 43,
	}}
	w := httptest.NewRecorder()
	backupEngine(svc).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/backup/status", nil))

	require.Equal(t, http.StatusOK, w.Code)
	var resp BackupStatusResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, BackupStatusResponse{
		Nodes: 0, Teams: 1, Users: 1, Clean: true, ClickHouseAvailable: true,
		InstanceID: "kz", NexusVersion: "v1.39.0", SchemaVersion: 43,
		MinPassphraseLen: backupfile.MinPassphraseLen,
	}, resp)
}
