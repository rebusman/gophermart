package auth_test

import (
	"errors"
	"strings"
	"testing"

	"gophermart/internal/auth"
	"gophermart/internal/domain"
)

// testCost — стоимость хеширования, используемая тестами.
//
// Взят минимум, поддерживаемый алгоритмом: тесты проверяют поведение, а не
// вычислительную стойкость, и не должны стоить секунды процессорного времени.
const testCost = auth.MinCost

// newHasher создаёт хешер с тестовой стоимостью.
func newHasher(t *testing.T) *auth.Hasher {
	t.Helper()

	hasher, err := auth.NewHasher(testCost)
	if err != nil {
		t.Fatalf("создание хешера: %v", err)
	}

	return hasher
}

func TestNewHasherRejectsCostOutOfRange(t *testing.T) {
	for _, cost := range []int{auth.MinCost - 1, auth.MaxCost + 1} {
		if _, err := auth.NewHasher(cost); !errors.Is(err, auth.ErrInvalidCost) {
			t.Errorf("стоимость %d принята: %v", cost, err)
		}
	}
}

func TestHashDiffersFromPassword(t *testing.T) {
	hasher := newHasher(t)
	const password = "correct-horse-battery-staple"

	hash, err := hasher.Hash(password)
	if err != nil {
		t.Fatalf("хеширование пароля: %v", err)
	}

	if hash == password {
		t.Error("сохранённое представление совпадает с паролем")
	}

	if strings.Contains(hash, password) {
		t.Error("сохранённое представление содержит пароль в открытом виде")
	}
}

func TestHashOfEqualPasswordsDiffers(t *testing.T) {
	hasher := newHasher(t)
	const password = "одинаковый пароль"

	first, err := hasher.Hash(password)
	if err != nil {
		t.Fatalf("первое хеширование: %v", err)
	}

	second, err := hasher.Hash(password)
	if err != nil {
		t.Fatalf("второе хеширование: %v", err)
	}

	if first == second {
		t.Error("два хеша одного пароля совпали: соль не применяется")
	}
}

func TestCompareDistinguishesPasswords(t *testing.T) {
	hasher := newHasher(t)
	const password = "верный пароль"

	hash, err := hasher.Hash(password)
	if err != nil {
		t.Fatalf("хеширование пароля: %v", err)
	}

	if err = hasher.Compare(hash, password); err != nil {
		t.Errorf("верный пароль отвергнут: %v", err)
	}

	err = hasher.Compare(hash, "неверный пароль")
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("неверный пароль принят: %v", err)
	}
}

func TestCompareRejectsMalformedHash(t *testing.T) {
	hasher := newHasher(t)

	err := hasher.Compare("это не хеш", "пароль")
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("непригодный хеш не отвергнут: %v", err)
	}
}

func TestHashRejectsPasswordLongerThanLimit(t *testing.T) {
	hasher := newHasher(t)

	long := strings.Repeat("a", auth.MaxPasswordBytes+1)

	if _, err := hasher.Hash(long); !errors.Is(err, domain.ErrPasswordTooLong) {
		t.Errorf("слишком длинный пароль принят: %v", err)
	}
}

func TestHashRejectsEmptyPassword(t *testing.T) {
	hasher := newHasher(t)

	if _, err := hasher.Hash(""); !errors.Is(err, domain.ErrEmptyPassword) {
		t.Errorf("пустой пароль принят: %v", err)
	}
}

// TestPasswordsSharingLimitPrefixAreNotEquivalent проверяет, что ограничение
// длины не даёт двум разным паролям с общим 72-байтовым началом стать
// взаимозаменяемыми.
//
// Без явной проверки bcrypt молча отбросил бы остаток, и пароль, отличающийся
// только за пределом, открывал бы чужую учётную запись.
func TestPasswordsSharingLimitPrefixAreNotEquivalent(t *testing.T) {
	hasher := newHasher(t)

	prefix := strings.Repeat("a", auth.MaxPasswordBytes)
	first := prefix + "первый хвост"
	second := prefix + "второй хвост"

	if _, err := hasher.Hash(first); !errors.Is(err, domain.ErrPasswordTooLong) {
		t.Fatalf("длинный пароль не отвергнут: %v", err)
	}

	// Пароль ровно предельной длины принимается, а его хеш не подходит ни к
	// одному из более длинных вариантов: они не дошли до хеширования вовсе.
	hash, err := hasher.Hash(prefix)
	if err != nil {
		t.Fatalf("хеширование пароля предельной длины: %v", err)
	}

	for _, candidate := range []string{first, second} {
		if err = hasher.Compare(hash, candidate); !errors.Is(err, domain.ErrInvalidCredentials) {
			t.Errorf("пароль %q признан эквивалентным своему 72-байтовому началу", candidate)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	tests := map[string]struct {
		password string
		want     error
	}{
		"пустой":           {password: "", want: domain.ErrEmptyPassword},
		"предельной длины": {password: strings.Repeat("a", auth.MaxPasswordBytes), want: nil},
		"на байт длиннее":  {password: strings.Repeat("a", auth.MaxPasswordBytes+1), want: domain.ErrPasswordTooLong},
		"многобайтные символы": {
			// Предел задан в байтах, а не в символах: кириллица занимает по
			// два байта, поэтому 37 символов уже превышают 72 байта.
			password: strings.Repeat("я", 37),
			want:     domain.ErrPasswordTooLong,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := auth.ValidatePassword(test.password)

			if test.want == nil {
				if err != nil {
					t.Errorf("пароль отвергнут: %v", err)
				}

				return
			}

			if !errors.Is(err, test.want) {
				t.Errorf("неожиданная ошибка: got %v, want %v", err, test.want)
			}
		})
	}
}

func TestCompareDummyAlwaysFails(t *testing.T) {
	hasher := newHasher(t)

	if err := hasher.CompareDummy(); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("сравнение с фиктивным хешем должно давать отказ: %v", err)
	}
}

func TestHasherKeepsCost(t *testing.T) {
	hasher := newHasher(t)

	if hasher.Cost() != testCost {
		t.Errorf("неожиданная стоимость хеширования: got %d, want %d", hasher.Cost(), testCost)
	}
}
