package auth_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"
)

// signHS256 собирает JWT с заданными утверждениями и подписывает его
// алгоритмом HS256.
//
// Помощник нужен там, где токен должен быть корректно подписан, но содержать
// утверждения, которые издатель никогда не выпустит: проверить их иначе
// невозможно.
func signHS256(t *testing.T, secret string, claims map[string]any) string {
	t.Helper()

	header := encodeSegment(t, map[string]any{"alg": "HS256", "typ": "JWT"})
	payload := encodeSegment(t, claims)
	signingInput := header + "." + payload

	mac := hmac.New(sha256.New, []byte(secret))
	if _, err := mac.Write([]byte(signingInput)); err != nil {
		t.Fatalf("вычисление подписи: %v", err)
	}

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// encodeSegment сериализует часть токена в JSON и кодирует её base64url без
// выравнивания.
func encodeSegment(t *testing.T, value map[string]any) string {
	t.Helper()

	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("сериализация части токена: %v", err)
	}

	return base64.RawURLEncoding.EncodeToString(encoded)
}
