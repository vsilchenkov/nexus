package usecase

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
)

// sign возвращает HMAC-SHA256(body, secret) в hex'е — то, что должен прислать
// внешний провайдер вебхука. Используется в тестах как «честный» подписчик.
func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func webhookNode() *domain.Node {
	return &domain.Node{
		IncomingAuthType:        domain.IncomingAuthTypeWebhookSignature,
		IncomingAuthCredentials: "shared-secret",
		WebhookSignatureHeader:  "X-Hub-Signature-256",
		WebhookSignaturePrefix:  "sha256=",
	}
}

func TestVerifyWebhookSignature_HappyPath(t *testing.T) {
	body := []byte(`{"event":"ping","id":1}`)
	n := webhookNode()
	h := http.Header{}
	h.Set("X-Hub-Signature-256", "sha256="+sign("shared-secret", string(body)))

	require.NoError(t, VerifyWebhookSignature(n, h, body))
}

func TestVerifyWebhookSignature_MismatchedSignature(t *testing.T) {
	body := []byte(`{"event":"ping"}`)
	n := webhookNode()
	h := http.Header{}
	// Подпись от другого тела — должна не сойтись.
	h.Set("X-Hub-Signature-256", "sha256="+sign("shared-secret", "OTHER"))

	err := VerifyWebhookSignature(n, h, body)
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrUnauthorized),
		"want ErrUnauthorized, got %v", err)
}

func TestVerifyWebhookSignature_WrongSecret(t *testing.T) {
	body := []byte(`{"event":"ping"}`)
	n := webhookNode()
	h := http.Header{}
	// Подпись от другого секрета — должна не сойтись.
	h.Set("X-Hub-Signature-256", "sha256="+sign("wrong-secret", string(body)))

	err := VerifyWebhookSignature(n, h, body)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
}

func TestVerifyWebhookSignature_MissingHeader(t *testing.T) {
	n := webhookNode()
	err := VerifyWebhookSignature(n, http.Header{}, []byte(`{}`))
	assert.ErrorIs(t, err, domain.ErrAuthHeaderMissing)
}

func TestVerifyWebhookSignature_MalformedPrefix(t *testing.T) {
	n := webhookNode()
	h := http.Header{}
	// Prefix sha256= обязателен, а тут md5=
	h.Set("X-Hub-Signature-256", "md5=deadbeef")
	err := VerifyWebhookSignature(n, h, []byte(`{}`))
	assert.ErrorIs(t, err, domain.ErrAuthHeaderMalformed)
}

func TestVerifyWebhookSignature_NonHexValue(t *testing.T) {
	n := webhookNode()
	h := http.Header{}
	h.Set("X-Hub-Signature-256", "sha256=not-a-hex-string-zzz")
	err := VerifyWebhookSignature(n, h, []byte(`{}`))
	assert.ErrorIs(t, err, domain.ErrAuthHeaderMalformed)
}

func TestVerifyWebhookSignature_EmptyHeaderName(t *testing.T) {
	// Defence-in-depth: domain.Validate должен поймать пустой header,
	// но если БД-CHECK обошли — VerifyWebhookSignature не должен паниковать.
	n := webhookNode()
	n.WebhookSignatureHeader = ""
	err := VerifyWebhookSignature(n, http.Header{}, []byte(`{}`))
	assert.ErrorIs(t, err, domain.ErrAuthHeaderMissing)
}

func TestVerifyWebhookSignature_NoPrefix(t *testing.T) {
	// Узел без prefix: значение header'а целиком — hex-строка.
	n := webhookNode()
	n.WebhookSignaturePrefix = ""
	body := []byte(`{}`)
	h := http.Header{}
	h.Set("X-Hub-Signature-256", sign("shared-secret", string(body)))

	require.NoError(t, VerifyWebhookSignature(n, h, body))
}

func TestVerifyWebhookSignature_EmptyBody(t *testing.T) {
	// HMAC от пустого тела — валидный кейс (Stripe-style ping).
	n := webhookNode()
	h := http.Header{}
	h.Set("X-Hub-Signature-256", "sha256="+sign("shared-secret", ""))
	require.NoError(t, VerifyWebhookSignature(n, h, nil))
}

func TestCheckIncomingAuth_WebhookSignature_RoutesToVerify(t *testing.T) {
	// CheckIncomingAuth для webhook_signature должен делегировать в
	// VerifyWebhookSignature: happy-path возвращает nil, неправильная
	// подпись — ErrUnauthorized.
	body := []byte(`{"x":1}`)
	n := webhookNode()
	hOK := http.Header{}
	hOK.Set("X-Hub-Signature-256", "sha256="+sign("shared-secret", string(body)))
	require.NoError(t, CheckIncomingAuth(n, hOK, body))

	hBad := http.Header{}
	hBad.Set("X-Hub-Signature-256", "sha256="+sign("shared-secret", "other"))
	err := CheckIncomingAuth(n, hBad, body)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
}

func TestNode_Validate_WebhookSignatureRequiresHeaderAndSecret(t *testing.T) {
	base := func() *domain.Node {
		return &domain.Node{
			Path:                    "callbacks/github",
			RootMethod:              domain.RootMethodRequestAsync,
			URLMode:                 domain.URLModeStatic,
			TargetURL:               "https://internal.example.com/in",
			URLParamName:            "url_base",
			AuthType:                domain.AuthTypeNone,
			AuthDynamicSource:       domain.AuthDynSourceQuery,
			AuthDynamicField:        "token",
			Status:                  domain.NodeStatusEnabled,
			TimeoutMs:               5000,
			DLQTTLSeconds:           86_400, // §36: иначе Validate отвергнет 0 (узел строится без SetDefaults)
			DLQRetryDelaySeconds:    60,     // §36
			IncomingAuthType:        domain.IncomingAuthTypeWebhookSignature,
			IncomingAuthCredentials: "secret",
			WebhookSignatureHeader:  "X-Hub-Signature-256",
		}
	}

	// Полный набор — ок.
	require.NoError(t, base().Validate())

	// Без header'а — ошибка.
	n := base()
	n.WebhookSignatureHeader = ""
	assert.ErrorIs(t, n.Validate(), domain.ErrNodeWebhookSigHeaderRequired)

	// Без секрета — ошибка.
	n = base()
	n.IncomingAuthCredentials = ""
	assert.ErrorIs(t, n.Validate(), domain.ErrNodeWebhookSigSecretRequired)
}
