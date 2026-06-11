package chlog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

// fallbackStore — пишет проваленный батч ClickHouse в NDJSON-файл
// в указанной директории (§9.4 ТЗ). Раз в interval сканирует свои файлы
// и пытается переотправить их через replayer-функцию. Successful файлы
// удаляются.
//
// Формат файла: одна JSON-запись на строку, каждая запись содержит
// table и log_record. Файл атомарно создаётся через .tmp + rename,
// чтобы не было «полу-записанного» файла при крэше процесса.
type fallbackStore struct {
	dir      string
	logger   logging.Logger
	interval time.Duration

	mu      sync.Mutex
	stopCh  chan struct{}
	stopped bool
	wg      sync.WaitGroup
}

type fallbackEntry struct {
	Table string            `json:"table"`
	Log   *domain.LogRecord `json:"log"`
}

// newFallbackStore создаёт каталог dir (если не существует) и
// возвращает store. Если dir пустой — fallback отключён (Save — no-op).
func newFallbackStore(dir string, interval time.Duration, logger logging.Logger) *fallbackStore {
	if dir == "" {
		return &fallbackStore{logger: logger}
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	_ = os.MkdirAll(dir, 0o755)
	return &fallbackStore{
		dir:      dir,
		logger:   logger,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

// Enabled — есть ли реальный каталог для file-fallback.
func (s *fallbackStore) Enabled() bool {
	return s != nil && s.dir != ""
}

// Save сохраняет батч в новый NDJSON-файл. Возвращает имя файла
// (для лога) и ошибку.
func (s *fallbackStore) Save(table string, batch []*domain.LogRecord) (string, error) {
	if !s.Enabled() {
		return "", errors.New("fallback disabled")
	}
	if len(batch) == 0 {
		return "", nil
	}
	name := fmt.Sprintf("ch-%s-%s.ndjson", time.Now().UTC().Format("20060102T150405"), uuid.NewString()[:8])
	tmp := filepath.Join(s.dir, name+".tmp")
	final := filepath.Join(s.dir, name)

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", fmt.Errorf("create tmp: %w", err)
	}
	bw := bufio.NewWriter(f)
	enc := json.NewEncoder(bw)
	for _, r := range batch {
		if err := enc.Encode(fallbackEntry{Table: table, Log: r}); err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
			return "", fmt.Errorf("encode: %w", err)
		}
	}
	if err := bw.Flush(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", fmt.Errorf("flush: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", fmt.Errorf("sync: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("close: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("rename: %w", err)
	}
	return final, nil
}

// Replayer — callback, который пытается отправить батч в ClickHouse.
// Возвращает nil при успехе, ошибку при провале (тогда файл остаётся).
type Replayer func(ctx context.Context, table string, batch []*domain.LogRecord) error

// Run запускает фоновый цикл рестора. Завершается при ctx.Done или Stop.
// Если store отключён — возвращается сразу.
func (s *fallbackStore) Run(ctx context.Context, replay Replayer) {
	if !s.Enabled() {
		return
	}
	s.wg.Add(1)
	defer s.wg.Done()

	tick := time.NewTicker(s.interval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-tick.C:
			s.tryRestoreOnce(ctx, replay)
		}
	}
}

// tryRestoreOnce — один проход по каталогу: читает все *.ndjson,
// группирует по table, пытается переотправить, удаляет успешные файлы.
func (s *fallbackStore) tryRestoreOnce(ctx context.Context, replay Replayer) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		s.logger.Warn("fallback readdir failed",
			s.logger.Str("dir", s.dir), s.logger.Err(err))
		return
	}
	// Стабильный порядок: старые сначала.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ndjson") {
			continue
		}
		fp := filepath.Join(s.dir, e.Name())
		if err := s.restoreFile(ctx, fp, replay); err != nil {
			s.logger.Warn("fallback restore failed; keeping file",
				s.logger.Str("file", fp), s.logger.Err(err))
			// Не удаляем — попробуем в следующий тик.
		}
	}
}

func (s *fallbackStore) restoreFile(ctx context.Context, path string, replay Replayer) error {
	byTable, err := readFallbackFile(path)
	if err != nil {
		return err
	}

	for table, batch := range byTable {
		if err := replay(ctx, table, batch); err != nil {
			return fmt.Errorf("replay %s: %w", table, err)
		}
	}
	// Файл закрыт в readFallbackFile; на Windows нельзя удалить открытый файл.
	if err := os.Remove(path); err != nil {
		// Батч уже в ClickHouse — если оставить файл, следующий тик вставит
		// его повторно (дубликаты). Помечаем как обработанный rename'ом:
		// суффикс .done выводит файл из выборки tryRestoreOnce (*.ndjson).
		if rerr := os.Rename(path, path+".done"); rerr != nil {
			return fmt.Errorf("remove: %w (rename fallback failed too: %v)", err, rerr)
		}
		s.logger.Warn("fallback file could not be removed; renamed to .done",
			s.logger.Str("file", path), s.logger.Err(err))
	}
	return nil
}

func readFallbackFile(path string) (map[string][]*domain.LogRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	byTable := map[string][]*domain.LogRecord{}
	dec := json.NewDecoder(f)
	for dec.More() {
		var e fallbackEntry
		if err := dec.Decode(&e); err != nil {
			return nil, fmt.Errorf("decode: %w", err)
		}
		if e.Log != nil && e.Table != "" {
			byTable[e.Table] = append(byTable[e.Table], e.Log)
		}
	}
	return byTable, nil
}

// Stop — graceful: завершает цикл рестора.
func (s *fallbackStore) Stop() {
	if !s.Enabled() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	close(s.stopCh)
	s.stopped = true
	s.wg.Wait()
}
