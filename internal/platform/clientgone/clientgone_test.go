package clientgone_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"nexus/internal/platform/clientgone"
)

func TestIs(t *testing.T) {
	t.Parallel()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()

	scanErr := fmt.Errorf("scan node: %w", context.Canceled)
	cases := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{"клиент ушёл, ошибка — обёрнутая отмена", canceled, scanErr, true},
		{"клиент ушёл, но ошибка другая", canceled, errors.New("connection refused"), false},
		{"клиент ушёл, ошибки нет", canceled, nil, false},
		{"запрос жив, отмена изнутри — остаётся ошибкой", context.Background(), scanErr, false},
		{"истёк срок запроса — не уход клиента", expired, fmt.Errorf("x: %w", context.DeadlineExceeded), false},
		{"истёк срок, ошибка — отмена", expired, scanErr, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, clientgone.Is(tc.ctx, tc.err))
		})
	}
}
