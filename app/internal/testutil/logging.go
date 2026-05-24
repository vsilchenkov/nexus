package testutil

import (
	"log/slog"
	"sync"
)

// TestLogger реализует интерфейс logging.Logger для тестов
type TestLogger struct {
	mu            sync.RWMutex
	DebugMessages []string
	WarnMessages  []string
	InfoMessages  []string
	ErrorMessages []string
}

func (l *TestLogger) Debug(msg string, attrs ...slog.Attr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.DebugMessages = append(l.DebugMessages, msg)
	// fmt.Printf("DEBUG: %s\n", msg)
}

func (l *TestLogger) Info(msg string, attrs ...slog.Attr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.InfoMessages = append(l.InfoMessages, msg)
	// fmt.Printf("INFO: %s\n", msg)
}

func (l *TestLogger) Warn(msg string, attrs ...slog.Attr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.WarnMessages = append(l.WarnMessages, msg)
	// fmt.Printf("WARN: %s\n", msg)
}

func (l *TestLogger) Error(msg string, attrs ...slog.Attr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ErrorMessages = append(l.ErrorMessages, msg)
	// fmt.Printf("ERROR: %s\n", msg)
}

func (l *TestLogger) ErrorWithOp(msg string, err error, op string, attrs ...slog.Attr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ErrorMessages = append(l.ErrorMessages, msg)
	// fmt.Printf("ERROR: %s, OP: %s, ERR: %v\n", msg, op, err)
}

func (l *TestLogger) Err(err error) slog.Attr {
	return slog.Any("error", err)
}

func (l *TestLogger) Op(value string) slog.Attr {
	return slog.Attr{
		Key:   "op",
		Value: slog.StringValue(value),
	}
}

func (l *TestLogger) Str(key, value string) slog.Attr {
	return slog.Attr{
		Key:   key,
		Value: slog.StringValue(value),
	}
}

func (l *TestLogger) Int(key string, value int) slog.Attr {
	return slog.Attr{
		Key:   key,
		Value: slog.IntValue(value),
	}
}

func (l *TestLogger) Float64(key string, value float64) slog.Attr {
	return slog.Attr{
		Key:   key,
		Value: slog.Float64Value(value),
	}
}

func (l *TestLogger) Any(key string, value any) slog.Attr {
	return slog.Attr{
		Key:   key,
		Value: slog.AnyValue(value),
	}
}
