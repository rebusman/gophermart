package auth_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"gophermart/internal/auth"
	"gophermart/internal/domain"
)

// Параметры выпуска токенов, используемые тестами.
const (
	testSecret = "тестовый секрет подписи"
	testTTL    = time.Hour
)

// newIssuer создаёт издателя токенов с тестовым секретом и заданным сроком
// действия.
func newIssuer(t *testing.T, ttl time.Duration) *auth.TokenIssuer {
	t.Helper()

	issuer, err := auth.NewTokenIssuer(testSecret, ttl)
	if err != nil {
		t.Fatalf("создание издателя токенов: %v", err)
	}

	return issuer
}

// decodeClaims разбирает полезную нагрузку токена без проверки подписи.
//
// Тест смотрит на состав утверждений напрямую: проверка через Parse показала
// бы только итог, а требование говорит о конкретных полях.
func decodeClaims(t *testing.T, token string) map[string]any {
	t.Helper()

	parts := strings.Split(token, ".")

	const jwtPartCount = 3
	if len(parts) != jwtPartCount {
		t.Fatalf("токен не состоит из трёх частей: %s", token)
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("разбор полезной нагрузки токена: %v", err)
	}

	claims := map[string]any{}
	if err = json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("разбор утверждений токена: %v", err)
	}

	return claims
}

func TestNewTokenIssuerRejectsEmptySecret(t *testing.T) {
	if _, err := auth.NewTokenIssuer("", testTTL); !errors.Is(err, auth.ErrEmptySecret) {
		t.Errorf("пустой секрет принят: %v", err)
	}
}

func TestIssuerKeepsTTL(t *testing.T) {
	issuer := newIssuer(t, testTTL)

	if issuer.TTL() != testTTL {
		t.Errorf("неожиданный срок действия: got %s, want %s", issuer.TTL(), testTTL)
	}
}

func TestIssuedTokenCarriesExpectedClaims(t *testing.T) {
	issuer := newIssuer(t, testTTL)
	userID := domain.NewUserID()

	issuedBefore := time.Now()

	token, err := issuer.Issue(userID)
	if err != nil {
		t.Fatalf("выпуск токена: %v", err)
	}

	claims := decodeClaims(t, token)

	subject, ok := claims["sub"].(string)
	if !ok || subject != userID.String() {
		t.Errorf("неожиданное утверждение sub: got %v, want %s", claims["sub"], userID)
	}

	issuedAt, ok := claims["iat"].(float64)
	if !ok {
		t.Fatalf("утверждение iat отсутствует или не является числом: %v", claims["iat"])
	}

	expiresAt, ok := claims["exp"].(float64)
	if !ok {
		t.Fatalf("утверждение exp отсутствует или не является числом: %v", claims["exp"])
	}

	if int64(issuedAt) < issuedBefore.Unix()-1 {
		t.Errorf("момент выпуска раньше начала теста: %v", issuedAt)
	}

	if int64(expiresAt)-int64(issuedAt) != int64(testTTL.Seconds()) {
		t.Errorf("срок действия не совпадает с TTL: exp-iat = %d, want %d",
			int64(expiresAt)-int64(issuedAt), int64(testTTL.Seconds()))
	}
}

func TestIssuedTokensDifferBetweenUsers(t *testing.T) {
	issuer := newIssuer(t, testTTL)

	first, err := issuer.Issue(domain.NewUserID())
	if err != nil {
		t.Fatalf("выпуск первого токена: %v", err)
	}

	second, err := issuer.Issue(domain.NewUserID())
	if err != nil {
		t.Fatalf("выпуск второго токена: %v", err)
	}

	if first == second {
		t.Error("токены разных пользователей совпали")
	}
}

func TestParseAcceptsValidToken(t *testing.T) {
	issuer := newIssuer(t, testTTL)
	userID := domain.NewUserID()

	token, err := issuer.Issue(userID)
	if err != nil {
		t.Fatalf("выпуск токена: %v", err)
	}

	parsed, err := issuer.Parse(token)
	if err != nil {
		t.Fatalf("разбор токена: %v", err)
	}

	if parsed != userID {
		t.Errorf("неожиданный владелец токена: got %s, want %s", parsed, userID)
	}
}

func TestParseRejectsForgedSignature(t *testing.T) {
	issuer := newIssuer(t, testTTL)

	token, err := issuer.Issue(domain.NewUserID())
	if err != nil {
		t.Fatalf("выпуск токена: %v", err)
	}

	// Подпись портится изменением последнего символа: полезная нагрузка
	// остаётся корректной, поэтому отказ вызван именно проверкой подписи.
	forged := token[:len(token)-1] + string(flipLastRune(token))

	if _, err = issuer.Parse(forged); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Errorf("токен с подделанной подписью принят: %v", err)
	}
}

func TestParseRejectsTokenSignedByAnotherSecret(t *testing.T) {
	issuer := newIssuer(t, testTTL)

	other, err := auth.NewTokenIssuer("другой секрет", testTTL)
	if err != nil {
		t.Fatalf("создание второго издателя: %v", err)
	}

	token, err := other.Issue(domain.NewUserID())
	if err != nil {
		t.Fatalf("выпуск токена: %v", err)
	}

	if _, err = issuer.Parse(token); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Errorf("токен, подписанный чужим секретом, принят: %v", err)
	}
}

func TestParseRejectsExpiredToken(t *testing.T) {
	// Отрицательный TTL помещает момент истечения в прошлое: токен рождается
	// уже просроченным, и тесту не нужно ждать.
	issuer := newIssuer(t, -time.Hour)

	token, err := issuer.Issue(domain.NewUserID())
	if err != nil {
		t.Fatalf("выпуск токена: %v", err)
	}

	if _, err = issuer.Parse(token); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Errorf("истёкший токен принят: %v", err)
	}
}

func TestParseRejectsMalformedAndForeignTokens(t *testing.T) {
	issuer := newIssuer(t, testTTL)

	tests := map[string]string{
		"пустая строка": "",
		"не токен":      "это не токен",
		"две части вместо трёх": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." +
			"eyJzdWIiOiIxIn0",
		// Токен с алгоритмом none: подпись отсутствует, и без проверки
		// допустимых алгоритмов он был бы принят как валидный.
		"алгоритм none": "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0." +
			"eyJzdWIiOiIwMTIzNDU2Ny04OWFiLWNkZWYtMDEyMy00NTY3ODlhYmNkZWYifQ.",
	}

	for name, token := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := issuer.Parse(token); !errors.Is(err, domain.ErrUnauthenticated) {
				t.Errorf("токен принят: %v", err)
			}
		})
	}
}

func TestParseRejectsTokenWithoutValidSubject(t *testing.T) {
	issuer := newIssuer(t, testTTL)

	// Токен подписан тем же секретом, но subject не является UUID: подпись
	// верна, а идентификатор владельца недостоверен.
	token := signHS256(t, testSecret, map[string]any{
		"sub": "не идентификатор",
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(testTTL).Unix(),
	})

	if _, err := issuer.Parse(token); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Errorf("токен с некорректным sub принят: %v", err)
	}
}

func TestParseRejectsTokenWithoutExpiration(t *testing.T) {
	issuer := newIssuer(t, testTTL)

	token := signHS256(t, testSecret, map[string]any{
		"sub": domain.NewUserID().String(),
		"iat": time.Now().Unix(),
	})

	if _, err := issuer.Parse(token); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Errorf("бессрочный токен принят: %v", err)
	}
}

// flipLastRune возвращает символ, отличный от последнего символа строки.
func flipLastRune(s string) rune {
	// Символы выбраны из канонического алфавита base64url для последней
	// позиции: последний символ подписи несёт лишь часть значащих битов, и
	// подмена символом с теми же старшими битами декодировалась бы в тот же
	// байт, оставляя подпись действительной.
	if s[len(s)-1] == 'A' {
		return 'E'
	}

	return 'A'
}
