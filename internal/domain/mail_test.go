package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMailSettings_Resolve_Defaults(t *testing.T) {
	t.Parallel()

	got := MailSettings{}.Resolve()

	assert.False(t, got.Enabled, "отправка по умолчанию выключена (fail-closed, §88.2)")
	assert.Equal(t, MailDefaultPort, got.Port)
	assert.Equal(t, MailEncryptionSTARTTLS, got.Encryption)
	assert.Equal(t, MailAuthPlain, got.AuthType)
	assert.Equal(t, MailDefaultFromName, got.FromName)
	assert.Equal(t, MailDefaultTimeoutSec, got.TimeoutSec)
	assert.Equal(t, PasswordResetTTLDefaultMin, got.PasswordResetTTLMin)
	assert.False(t, got.PasswordResetEnabled)
}

func TestMailSettings_Resolve_OverridesAndTrim(t *testing.T) {
	t.Parallel()

	got := MailSettings{
		Enabled:              new(true),
		Host:                 new("  smtp.example.com  "),
		Port:                 new(465),
		Encryption:           new(MailEncryptionTLS),
		AuthType:             new(MailAuthLogin),
		Username:             new("noreply@example.com"),
		Password:             new("s3cret"),
		FromAddress:          new(" nexus@example.com "),
		FromName:             new("Шина"),
		HELOHost:             new(" nexus.example.com "),
		TimeoutSec:           new(42),
		SkipTLSVerify:        new(true),
		PasswordResetEnabled: new(true),
		PasswordResetTTLMin:  new(15),
	}.Resolve()

	assert.True(t, got.Enabled)
	assert.Equal(t, "smtp.example.com", got.Host, "хост обрезается по краям")
	assert.Equal(t, 465, got.Port)
	assert.Equal(t, MailEncryptionTLS, got.Encryption)
	assert.Equal(t, MailAuthLogin, got.AuthType)
	assert.Equal(t, "nexus@example.com", got.FromAddress, "адрес отправителя обрезается")
	assert.Equal(t, "nexus.example.com", got.HELOHost)
	assert.Equal(t, "Шина", got.FromName)
	assert.Equal(t, 42, got.TimeoutSec)
	assert.True(t, got.SkipTLSVerify)
	assert.True(t, got.PasswordResetEnabled)
	assert.Equal(t, 15, got.PasswordResetTTLMin)
}

// Пустая строка в поле с непустым дефолтом означает «вернуть дефолт», а не
// «сохранить пустоту»: иначе очищенное в форме поле дало бы письмо без имени
// отправителя и соединение с пустым режимом шифрования.
func TestMailSettings_Resolve_EmptyStringFallsBackToDefault(t *testing.T) {
	t.Parallel()

	got := MailSettings{
		Encryption: new(""),
		AuthType:   new(""),
		FromName:   new(""),
	}.Resolve()

	assert.Equal(t, MailEncryptionSTARTTLS, got.Encryption)
	assert.Equal(t, MailAuthPlain, got.AuthType)
	assert.Equal(t, MailDefaultFromName, got.FromName)
}

func TestValidateMailSettings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      MailSettings
		wantErr error
	}{
		{"пустой патч", MailSettings{}, nil},
		{"пустой хост допустим", MailSettings{Host: new("")}, nil},
		{"обычный хост", MailSettings{Host: new("smtp.example.com")}, nil},
		{"хост со схемой", MailSettings{Host: new("smtp://smtp.example.com")}, ErrMailHostInvalid},
		{"хост с портом", MailSettings{Host: new("smtp.example.com:587")}, ErrMailHostInvalid},
		{"хост с пробелом", MailSettings{Host: new("smtp example com")}, ErrMailHostInvalid},
		{"хост со слешем", MailSettings{Host: new("smtp.example.com/mail")}, ErrMailHostInvalid},

		{"порт нижняя граница", MailSettings{Port: new(1)}, nil},
		{"порт верхняя граница", MailSettings{Port: new(65535)}, nil},
		{"порт 0", MailSettings{Port: new(0)}, ErrMailPortInvalid},
		{"порт 65536", MailSettings{Port: new(65536)}, ErrMailPortInvalid},

		{"шифрование none", MailSettings{Encryption: new(MailEncryptionNone)}, nil},
		{"шифрование starttls", MailSettings{Encryption: new(MailEncryptionSTARTTLS)}, nil},
		{"шифрование tls", MailSettings{Encryption: new(MailEncryptionTLS)}, nil},
		{"шифрование ssl", MailSettings{Encryption: new("ssl")}, ErrMailEncryptionInvalid},

		{"auth none", MailSettings{AuthType: new(MailAuthNone)}, nil},
		{"auth cram-md5", MailSettings{AuthType: new(MailAuthCRAMMD5)}, nil},
		{"auth oauth", MailSettings{AuthType: new("oauth2")}, ErrMailAuthTypeInvalid},

		{"from обычный", MailSettings{FromAddress: new("nexus@example.com")}, nil},
		{"from с именем", MailSettings{FromAddress: new("Nexus <nexus@example.com>")}, nil},
		{"from пустой", MailSettings{FromAddress: new("")}, nil},
		{"from без @", MailSettings{FromAddress: new("nexus.example.com")}, ErrMailFromInvalid},

		{"from_name обычное", MailSettings{FromName: new("Nexus")}, nil},
		{"from_name с переводом строки", MailSettings{FromName: new("Nexus\r\nBcc: evil@example.com")}, ErrMailFromNameInvalid},
		{"from_name с одним \\n", MailSettings{FromName: new("Nexus\nX")}, ErrMailFromNameInvalid},

		{"helo обычный", MailSettings{HELOHost: new("nexus.example.com")}, nil},
		{"helo пустой", MailSettings{HELOHost: new("")}, nil},
		{"helo с пробелом", MailSettings{HELOHost: new("nexus example")}, ErrMailHELOInvalid},

		{"таймаут нижняя граница", MailSettings{TimeoutSec: new(MailTimeoutMinSec)}, nil},
		{"таймаут верхняя граница", MailSettings{TimeoutSec: new(MailTimeoutMaxSec)}, nil},
		{"таймаут 0", MailSettings{TimeoutSec: new(0)}, ErrMailTimeoutInvalid},
		{"таймаут 121", MailSettings{TimeoutSec: new(121)}, ErrMailTimeoutInvalid},

		{"ttl нижняя граница", MailSettings{PasswordResetTTLMin: new(PasswordResetTTLMinMin)}, nil},
		{"ttl верхняя граница", MailSettings{PasswordResetTTLMin: new(PasswordResetTTLMaxMin)}, nil},
		{"ttl 4", MailSettings{PasswordResetTTLMin: new(4)}, ErrPasswordResetTTLInvalid},
		{"ttl 1441", MailSettings{PasswordResetTTLMin: new(1441)}, ErrPasswordResetTTLInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateMailSettings(tt.in)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestValidateMailSettings_Lengths(t *testing.T) {
	t.Parallel()

	long := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = 'a'
		}
		return string(b)
	}

	require.ErrorIs(t, ValidateMailSettings(MailSettings{Username: new(long(256))}), ErrMailUsernameLength)
	assert.NoError(t, ValidateMailSettings(MailSettings{Username: new(long(255))}))

	require.ErrorIs(t, ValidateMailSettings(MailSettings{Password: new(long(256))}), ErrMailPasswordLength)
	assert.NoError(t, ValidateMailSettings(MailSettings{Password: new(long(255))}))

	require.ErrorIs(t, ValidateMailSettings(MailSettings{FromName: new(long(129))}), ErrMailFromNameInvalid)
	assert.NoError(t, ValidateMailSettings(MailSettings{FromName: new(long(128))}))
}

func TestValidateMailConsistency(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      MailSettings
		wantErr error
	}{
		{
			name: "выключенная почта не требует ничего",
			in:   MailSettings{Enabled: new(false)},
		},
		{
			name: "включённая почта с хостом и отправителем",
			in: MailSettings{
				Enabled:     new(true),
				Host:        new("smtp.example.com"),
				FromAddress: new("nexus@example.com"),
			},
		},
		{
			name:    "включённая почта без хоста",
			in:      MailSettings{Enabled: new(true), FromAddress: new("nexus@example.com")},
			wantErr: ErrMailHostInvalid,
		},
		{
			name:    "включённая почта без отправителя",
			in:      MailSettings{Enabled: new(true), Host: new("smtp.example.com")},
			wantErr: ErrMailFromInvalid,
		},
		{
			name:    "восстановление без включённой почты",
			in:      MailSettings{PasswordResetEnabled: new(true)},
			wantErr: ErrMailResetRequiresMail,
		},
		{
			name: "восстановление при включённой почте",
			in: MailSettings{
				Enabled:              new(true),
				Host:                 new("smtp.example.com"),
				FromAddress:          new("nexus@example.com"),
				PasswordResetEnabled: new(true),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateMailConsistency(tt.in)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestMailPasswordResetReady(t *testing.T) {
	t.Parallel()

	full := func() *AppSettings {
		return &AppSettings{
			General: GeneralSettings{PublicBaseURL: new("https://nexus.example.com")},
			Mail: MailSettings{
				Enabled:              new(true),
				Host:                 new("smtp.example.com"),
				FromAddress:          new("nexus@example.com"),
				PasswordResetEnabled: new(true),
			},
		}
	}

	t.Run("всё настроено", func(t *testing.T) {
		t.Parallel()
		assert.True(t, MailPasswordResetReady(full()))
	})

	t.Run("nil", func(t *testing.T) {
		t.Parallel()
		assert.False(t, MailPasswordResetReady(nil))
	})

	// Без публичного адреса ссылка в письме получится без хоста: письмо уйдёт,
	// а перейти по нему будет некуда (§88.4.5).
	t.Run("нет публичного адреса", func(t *testing.T) {
		t.Parallel()
		s := full()
		s.General.PublicBaseURL = new("")
		assert.False(t, MailPasswordResetReady(s))

		s2 := full()
		s2.General.PublicBaseURL = nil
		assert.False(t, MailPasswordResetReady(s2))
	})

	t.Run("почта выключена", func(t *testing.T) {
		t.Parallel()
		s := full()
		s.Mail.Enabled = new(false)
		assert.False(t, MailPasswordResetReady(s))
	})

	t.Run("восстановление выключено", func(t *testing.T) {
		t.Parallel()
		s := full()
		s.Mail.PasswordResetEnabled = new(false)
		assert.False(t, MailPasswordResetReady(s))
	})

	t.Run("нет хоста", func(t *testing.T) {
		t.Parallel()
		s := full()
		s.Mail.Host = new("")
		assert.False(t, MailPasswordResetReady(s))
	})

	t.Run("нет отправителя", func(t *testing.T) {
		t.Parallel()
		s := full()
		s.Mail.FromAddress = new("")
		assert.False(t, MailPasswordResetReady(s))
	})
}
