package usecase

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"

	"nexus/internal/domain"
)

// VerifyWebhookSignature проверяет HMAC-SHA256 подпись входящего webhook'а
// (§16 ТЗ, IncomingAuthType=webhook_signature).
//
// Алгоритм:
//
//  1. Из header'а с именем node.WebhookSignatureHeader берётся значение.
//  2. Если оно начинается с node.WebhookSignaturePrefix — префикс отрезается.
//  3. Остаток рассматривается как hex-строка, декодируется в байты.
//  4. Вычисляется HMAC-SHA256(body, secret).
//  5. Сравнение в постоянное время (subtle.ConstantTimeCompare).
//
// Любая ошибка форматирования / decode / mismatch — domain.ErrUnauthorized;
// отсутствие header'а — domain.ErrAuthHeaderMissing. Это даёт handler'у
// единый маппинг 401 без раскрытия деталей наружу.
//
// secret — расшифрованное значение node.IncomingAuthCredentials; body —
// raw bytes тела запроса (не модифицированные).
func VerifyWebhookSignature(node *domain.Node, h http.Header, body []byte) error {
	header := node.WebhookSignatureHeader
	if header == "" {
		// Defence-in-depth: domain.Validate должен был это отсечь, но если
		// в БД попало пустое значение — лучше упасть в 401, не в 500.
		return domain.ErrAuthHeaderMissing
	}
	got := h.Get(header)
	if got == "" {
		return domain.ErrAuthHeaderMissing
	}
	if prefix := node.WebhookSignaturePrefix; prefix != "" {
		if !strings.HasPrefix(got, prefix) {
			return domain.ErrAuthHeaderMalformed
		}
		got = got[len(prefix):]
	}
	sig, err := hex.DecodeString(got)
	if err != nil {
		return domain.ErrAuthHeaderMalformed
	}

	mac := hmac.New(sha256.New, []byte(node.IncomingAuthCredentials))
	_, _ = mac.Write(body)
	expected := mac.Sum(nil)

	if subtle.ConstantTimeCompare(sig, expected) != 1 {
		return domain.ErrUnauthorized
	}
	return nil
}

// buildWebhookSignatureValue строит значение, которое клиент ДОЛЖЕН предъявить в
// заголовке node.WebhookSignatureHeader: node.WebhookSignaturePrefix + hex(HMAC-
// SHA256(body, secret)). Обратная операция к VerifyWebhookSignature — та же
// hmac.New(sha256.New, secret), поэтому подписи гарантированно совпадают.
// Используется dry-run для автоподстановки ожидаемой входящей подписи (§55.6).
func buildWebhookSignatureValue(node *domain.Node, body []byte) string {
	mac := hmac.New(sha256.New, []byte(node.IncomingAuthCredentials))
	_, _ = mac.Write(body)
	return node.WebhookSignaturePrefix + hex.EncodeToString(mac.Sum(nil))
}
