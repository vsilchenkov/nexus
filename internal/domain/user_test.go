package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// §66: DisplayName — имя с фолбэком на логин (сессии/записи до миграции 0028).
func TestSession_DisplayName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		s    Session
		want string
	}{
		{name: "name set", s: Session{Login: "vpupkin", Name: "Vasya Pupkin"}, want: "Vasya Pupkin"},
		{name: "name empty falls back to login", s: Session{Login: "vpupkin"}, want: "vpupkin"},
		{name: "both empty", s: Session{}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.s.DisplayName())
		})
	}
}

func TestUser_DisplayName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		u    User
		want string
	}{
		{name: "name set", u: User{Login: "vpupkin", Name: "Vasya Pupkin"}, want: "Vasya Pupkin"},
		{name: "name empty falls back to login", u: User{Login: "vpupkin"}, want: "vpupkin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.u.DisplayName())
		})
	}
}
