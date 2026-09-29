package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"nexus/internal/domain"
	"nexus/internal/domain/backup"
	"nexus/internal/platform/backupfile"
)

// BackupFile — выгруженный файл резервной копии.
type BackupFile struct {
	Name   string
	Data   []byte
	Header backupfile.Header
}

// Export читает всю конфигурацию и запечатывает её паролем (§110.4).
//
// Снимок собственной базы обязан проходить ту же проверку, что и при
// восстановлении: файл, который импорт отвергнет, обнаруживается сейчас, а не
// в момент аварии, когда исправить исходную базу уже нельзя.
func (u *BackupUsecase) Export(ctx context.Context, actor Actor, passphrase string) (*BackupFile, error) {
	if err := backupfile.ValidatePassphrase(passphrase); err != nil {
		return nil, err
	}
	release, err := u.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	started := u.clock.Now()
	s, err := u.snapshots.ReadSnapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("backup export: %w", err)
	}
	s.ExportedBy = actor.UserLogin
	s.SourcePublicURL = publicBaseURL(s.AppSettings)
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("backup export: configuration would not restore: %w", err)
	}
	f, err := u.seal(s, passphrase)
	if err != nil {
		return nil, err
	}
	u.audit.Log(ctx, actor, domain.ActionConfigExport, "config", "", map[string]any{
		"file":       f.Name,
		"size_bytes": len(f.Data),
		"counts":     f.Header.Counts,
	})
	u.logger.Debug("backup export: file sealed",
		u.logger.Str("file", f.Name),
		u.logger.Int("size_bytes", len(f.Data)),
		u.logger.Int("nodes", len(s.Nodes)),
		u.logger.Int("duration_ms", int(u.clock.Now().Sub(started).Milliseconds())))
	return f, nil
}

func (u *BackupUsecase) seal(s *backup.Snapshot, passphrase string) (*BackupFile, error) {
	payload, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("backup export: marshal snapshot: %w", err)
	}
	now := u.clock.Now().UTC()
	h := backupfile.Header{
		ExportedAt:       now,
		NexusVersion:     u.source.NexusVersion,
		SchemaVersion:    u.source.SchemaVersion,
		SourceInstanceID: u.source.InstanceID.String(),
		Counts:           s.Counts(),
	}
	data, err := backupfile.Seal(h, payload, passphrase, u.kdf)
	if err != nil {
		return nil, fmt.Errorf("backup export: %w", err)
	}
	return &BackupFile{
		Name:   backupFileName(u.source.InstanceID, s.SourcePublicURL, now),
		Data:   data,
		Header: h,
	}, nil
}

func publicBaseURL(s *domain.AppSettings) string {
	if s == nil || s.General.PublicBaseURL == nil {
		return ""
	}
	return *s.General.PublicBaseURL
}

// backupFileName — nexus-backup-<нода|хост>-<YYYYMMDD-HHMMSS>.json. Нода — по
// instance.id, без него — по хосту публичного адреса: файлы нескольких
// серверов в одной папке не должны путаться.
func backupFileName(id domain.InstanceID, publicURL string, at time.Time) string {
	tag := id.String()
	if tag == "" {
		tag = hostTag(publicURL)
	}
	ts := at.UTC().Format("20060102-150405")
	if tag == "" {
		return "nexus-backup-" + ts + ".json"
	}
	return "nexus-backup-" + tag + "-" + ts + ".json"
}

// hostTag — хост адреса, только [a-z0-9.-]: имя уходит в Content-Disposition.
func hostTag(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
			return r
		default:
			return -1
		}
	}, strings.ToLower(u.Hostname()))
}
