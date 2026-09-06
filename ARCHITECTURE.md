# Архитектура накопительной системы лояльности «Гофермарт»

## Общее решение

Проект представлен как **модульный монолит на Go** с PostgreSQL как единственным источником истины и отдельным фоновым воркером для опроса внешней Accrual-системы.

Ключевой принцип: HTTP-хендлеры принимают запросы и изменяют локальное состояние, а начисления выполняются асинхронно. Это исключает зависание `POST /api/user/orders` из-за внешнего сервиса и позволяет корректно обрабатывать `429`, временные `500` и долгие расчёты.

## Общая схема

```text
                    ┌─────────────────────────┐
                    │       HTTP clients      │
                    │  Browser / API client   │
                    └────────────┬────────────┘
                                 │ HTTP / JSON
                                 ▼
┌─────────────────────────────────────────────────────────────┐
│                     Gophermart API (Go)                      │
│                                                             │
│  Middleware chain                                            │
│  ├─ Recovery                                                 │
│  ├─ Request ID / logging                                     │
│  ├─ Gzip                                                     │
│  ├─ Authentication                                           │
│  └─ Optional metrics / tracing                               │
│                                                             │
│  HTTP handlers → application services → repositories        │
│                                                             │
│  ├─ AuthService                                              │
│  ├─ OrderService                                             │
│  ├─ BalanceService                                           │
│  └─ WithdrawalService                                        │
└──────────────┬──────────────────────────────┬───────────────┘
               │                              │
               ▼                              ▼
     ┌──────────────────┐          ┌────────────────────────┐
     │   PostgreSQL     │          │ Accrual-system client  │
     │ users            │          │ GET /api/orders/{id}   │
     │ orders           │          └───────────▲────────────┘
     │ balances         │                      │
     │ withdrawals      │          ┌───────────┴────────────┐
     │ ledger           │          │ Background accrual      │
     └──────────────────┘          │ worker / poller         │
                                   └────────────────────────┘
```

### Почему именно так

- Пользователь отправляет номер заказа и сразу получает `202 Accepted`, не ожидая расчёта бонусов.
- Внешний Accrual API может вернуть `204`, `429`, `500`, а расчёт может оставаться `PROCESSING` неопределённо долго. Это зона ответственности фонового процесса.
- Баланс и история списаний должны быть согласованы, поэтому начисление и списание выполняются в коротких транзакциях PostgreSQL.
- При горизонтальном масштабировании несколько воркеров не должны обрабатывать один заказ параллельно. Для этого используется PostgreSQL-очередь на базе `SELECT ... FOR UPDATE SKIP LOCKED`.

## Слои и структура проекта

```text
gophermart/
├── cmd/
│   └── gophermart/
│       └── main.go
├── internal/
│   ├── app/
│   │   ├── app.go
│   │   └── config.go
│   ├── domain/
│   │   ├── user.go
│   │   ├── order.go
│   │   ├── balance.go
│   │   ├── withdrawal.go
│   │   ├── ledger.go
│   │   └── errors.go
│   ├── service/
│   │   ├── auth.go
│   │   ├── orders.go
│   │   ├── balance.go
│   │   └── withdrawals.go
│   ├── repository/
│   │   ├── user.go
│   │   ├── order.go
│   │   ├── balance.go
│   │   ├── withdrawal.go
│   │   └── tx.go
│   ├── storage/
│   │   └── postgres/
│   │       ├── postgres.go
│   │       ├── user_repository.go
│   │       ├── order_repository.go
│   │       ├── balance_repository.go
│   │       ├── withdrawal_repository.go
│   │       └── migrations/
│   ├── transport/
│   │   └── http/
│   │       ├── router.go
│   │       ├── handlers/
│   │       │   ├── auth.go
│   │       │   ├── orders.go
│   │       │   ├── balance.go
│   │       │   └── withdrawals.go
│   │       ├── middleware/
│   │       │   ├── auth.go
│   │       │   ├── gzip.go
│   │       │   ├── recovery.go
│   │       │   └── logging.go
│   │       └── dto/
│   │           ├── request.go
│   │           └── response.go
│   ├── auth/
│   │   ├── password.go
│   │   └── token.go
│   ├── accrual/
│   │   ├── client.go
│   │   ├── model.go
│   │   └── worker.go
│   └── validator/
│       └── luhn.go
├── migrations/
├── tests/
│   ├── integration/
│   └── testutil/
├── go.mod
├── Dockerfile
├── docker-compose.yml
└── README.md
```

### Ответственность компонентов

| Компонент | Ответственность |
|---|---|
| `handlers` | Разбор HTTP-запросов, вызов сервисов, преобразование ошибок в HTTP-коды, JSON-ответы |
| `service` | Бизнес-правила: уникальность заказа, баланс, списание, идемпотентность |
| `repository` | SQL, транзакции, блокировки, преобразование инфраструктурных ошибок |
| `accrual/client` | HTTP-клиент к системе начислений, таймауты, разбор `200/204/429/500` |
| `accrual/worker` | Выборка заказов для опроса, retry/backoff, обновление статусов и начисление |
| `auth` | Хеширование паролей, выпуск и проверка токенов |
| `validator` | Проверка номера заказа алгоритмом Луна |
| `domain` | Доменные модели, статусы и бизнес-ошибки без зависимости от HTTP и PostgreSQL |

## Модель данных

Для баллов следует использовать `NUMERIC(18,2)` в PostgreSQL и exact decimal в Go. Не следует использовать `float64` из-за ошибок двоичного представления дробных чисел.

Конкретный выбор: `github.com/shopspring/decimal` в доменном слое и `pgtype.Numeric` на границе репозитория. Домен не должен зависеть от драйвера БД, поэтому конвертация `decimal.Decimal` в `pgtype.Numeric` и обратно инкапсулируется в `storage/postgres`.

В DTO поля, которые по контракту API могут отсутствовать в JSON (например `accrual` у заказа), объявляются указателем `*decimal.Decimal`. Тег `omitempty` не работает для структур, а `decimal.Decimal` — структура: без указателя поле будет сериализоваться всегда и нарушит контракт.

### Пользователи

```sql
CREATE TABLE users (
    id              UUID PRIMARY KEY,
    login           TEXT NOT NULL,
    password_hash   TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT users_login_unique UNIQUE (login),
    CONSTRAINT users_login_not_empty CHECK (length(trim(login)) > 0)
);
```

- UUID генерируется приложением или PostgreSQL.
- Пароль хранится только в виде адаптивного хеша: `bcrypt` или `argon2id`.
- При необходимости регистронезависимой уникальности стоит хранить нормализованное значение логина.

### Заказы

```sql
CREATE TYPE order_status AS ENUM (
    'NEW',
    'PROCESSING',
    'INVALID',
    'PROCESSED'
);

CREATE TABLE orders (
    id                    UUID PRIMARY KEY,
    number                TEXT NOT NULL,
    user_id               UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    status                order_status NOT NULL DEFAULT 'NEW',
    accrual               NUMERIC(18,2),
    uploaded_at           TIMESTAMPTZ NOT NULL DEFAULT now(),

    next_check_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    processing_started_at TIMESTAMPTZ,
    attempts              INTEGER NOT NULL DEFAULT 0,
    last_error            TEXT,
    finalized_at          TIMESTAMPTZ,

    CONSTRAINT orders_number_unique UNIQUE (number),
    CONSTRAINT orders_accrual_nonnegative CHECK (
        accrual IS NULL OR accrual >= 0
    )
);

CREATE INDEX orders_user_uploaded_idx
    ON orders (user_id, uploaded_at DESC);

CREATE INDEX orders_pending_check_idx
    ON orders (next_check_at)
    WHERE status IN ('NEW', 'PROCESSING');
```

Глобальное ограничение `UNIQUE(number)` необходимо для поведения API:

- тот же пользователь повторно отправил номер — `200`;
- номер принадлежит другому пользователю — `409`;
- номер новый — создаётся заказ и возвращается `202`.

### Баланс и ledger

Для надёжной реализации полезно хранить и текущий баланс, и неизменяемую историю операций.

```sql
CREATE TABLE balances (
    user_id            UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    current            NUMERIC(18,2) NOT NULL DEFAULT 0,
    withdrawn_total    NUMERIC(18,2) NOT NULL DEFAULT 0,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT balances_current_nonnegative CHECK (current >= 0),
    CONSTRAINT balances_withdrawn_nonnegative CHECK (withdrawn_total >= 0)
);

CREATE TYPE ledger_operation AS ENUM ('ACCRUAL', 'WITHDRAWAL');

CREATE TABLE balance_ledger (
    id                 UUID PRIMARY KEY,
    user_id            UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    operation          ledger_operation NOT NULL,
    amount             NUMERIC(18,2) NOT NULL CHECK (amount > 0),
    order_id           UUID REFERENCES orders(id) ON DELETE RESTRICT,
    withdrawal_id      UUID REFERENCES withdrawals(id) ON DELETE RESTRICT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT ledger_operation_source CHECK (
           (operation = 'ACCRUAL'    AND order_id      IS NOT NULL AND withdrawal_id IS NULL)
        OR (operation = 'WITHDRAWAL' AND withdrawal_id IS NOT NULL AND order_id      IS NULL)
    )
);

-- Ровно одно начисление на заказ.
CREATE UNIQUE INDEX ledger_accrual_order_uniq
    ON balance_ledger (order_id)
    WHERE order_id IS NOT NULL;
```

Уникальность начисления задаётся именно частичным индексом, а не ограничением на колонку. Вариант `UNIQUE NULLS NOT DISTINCT (order_id)` здесь неприменим: у всех записей с `operation = 'WITHDRAWAL'` поле `order_id` равно `NULL`, а `NULLS NOT DISTINCT` считает все `NULL` равными между собой — тогда во всей таблице разрешено ровно одно списание, и второе падает с `unique_violation`. Условие `WHERE order_id IS NOT NULL` выводит записи списаний из-под ограничения.

`CHECK (ledger_operation_source)` не даёт создать запись, у которой тип операции не соответствует заполненной ссылке: начисление без заказа или списание без факта списания.

`balance_ledger` позволяет:

- объяснить любое изменение баланса;
- проводить аудит;
- предотвратить повторное начисление одного заказа;
- сверять агрегированный баланс с историей операций.

Схема выше показана в финальном виде. При пофичной нарезке миграций `balance_ledger` создаётся вместе с механикой начислений, когда таблицы `withdrawals` ещё нет, поэтому колонка `withdrawal_id`, внешний ключ на `withdrawals` и ограничение `ledger_operation_source` добавляются отдельной `ALTER`-миграцией вместе со списаниями.

### Списания

```sql
CREATE TABLE withdrawals (
    id                 UUID PRIMARY KEY,
    user_id            UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    order_number       TEXT NOT NULL,
    amount             NUMERIC(18,2) NOT NULL CHECK (amount > 0),
    processed_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT withdrawals_user_order_unique UNIQUE (user_id, order_number)
);

CREATE INDEX withdrawals_user_processed_idx
    ON withdrawals (user_id, processed_at DESC);
```

Уникальность номера списания задана в пределах пользователя, а не глобально. Причина в контракте ТЗ: для `POST /api/user/balance/withdraw` набор кодов ответа — `200`, `401`, `402`, `422`, `500`, и кода «номер уже использован другим пользователем» в нём нет. Глобальный `UNIQUE(order_number)` превращал бы такой случай в `500` на ровном месте.

В пределах одного пользователя ограничение остаётся защитой от двойной оплаты при повторе запроса: повторное списание на тот же номер не выполняется, а клиент получает `200`. Как это реализуется — см. «Списание баллов».

## Ключевые бизнес-потоки

### Регистрация и вход

`POST /api/user/register`:

1. Валидировать JSON и обязательные поля.
2. Хешировать пароль до записи в БД.
3. В одной транзакции создать пользователя и пустой баланс.
4. При конфликте `users_login_unique` вернуть `409`.
5. Выпустить access token через `Authorization` или защищённую cookie.
6. Вернуть `200`.

`POST /api/user/login`:

1. Найти пользователя по логину.
2. Сравнить пароль с хешем.
3. При отсутствии пользователя или неверном пароле вернуть `401`.
4. Выпустить новый токен.
5. Вернуть `200`.

Для учебного API достаточно JWT с секретом из окружения. Альтернатива — opaque session token в PostgreSQL: он удобнее для принудительной инвалидации сессий.

### Загрузка номера заказа

`POST /api/user/orders` получает plain-text тело с номером заказа.

Алгоритм:

1. Проверить аутентификацию.
2. Прочитать body с ограничением размера, например до 4 KiB.
3. Удалить завершающий перевод строки, если он поддерживается.
4. Проверить, что значение состоит только из цифр.
5. Проверить номер по алгоритму Луна; при ошибке вернуть `422`.
6. Вставить заказ со статусом `NEW`.
7. При конфликте `UNIQUE(number)` определить владельца:
   - тот же `user_id` — `200`;
   - другой `user_id` — `409`.
8. Для нового заказа вернуть `202`.

Проверка Луна должна быть отдельной чистой функцией:

```go
func ValidLuhn(number string) bool
```

### Асинхронное начисление

Воркер запускается внутри того же бинарника в отдельной goroutine. При нескольких репликах каждый экземпляр запускает свой воркер, а PostgreSQL предотвращает двойную обработку.

```text
каждые N секунд:
    начать короткую транзакцию
    выбрать до batchSize заказов:
        статус NEW/PROCESSING
        next_check_at <= now()
        ORDER BY next_check_at
        FOR UPDATE SKIP LOCKED
    для выбранных записей:
        увеличить attempts
        временно сдвинуть next_check_at
    commit

    для каждого заказа:
        вызвать Accrual API вне транзакции БД
        начать новую транзакцию
        применить результат
        commit
```

Внешний HTTP-вызов не должен происходить внутри транзакции БД, удерживающей блокировки строк.

### Отображение статусов Accrual API

| Accrual API | Локальный статус | `accrual` | Дальнейшая обработка |
|---|---:|---:|---|
| `200 REGISTERED` | `NEW` | `NULL` | Повторить позднее |
| `200 PROCESSING` | `PROCESSING` | `NULL` | Повторить позднее |
| `200 INVALID` | `INVALID` | `NULL` | Больше не опрашивать |
| `200 PROCESSED` | `PROCESSED` | Значение или `NULL` | Больше не опрашивать |
| `204` | `NEW` | `NULL` | Повторить позднее |
| `429` | Текущий | Без изменений | Учитывать `Retry-After`, иначе backoff |
| `500`, timeout, сеть | Текущий | Без изменений | Retry с backoff и jitter |

`PROCESSED` без `accrual` является валидным финальным состоянием: заказ завершён, но бонусы не начислены.

### Идемпотентное начисление

Если Accrual API вернул `PROCESSED` и `accrual > 0`, в одной транзакции необходимо:

1. Заблокировать заказ `FOR UPDATE`.
2. Проверить, что заказ ещё не финализирован и в ledger нет начисления по нему.
3. Обновить `orders.status = 'PROCESSED'`, `orders.accrual`, `finalized_at`.
4. Вставить `balance_ledger(operation = 'ACCRUAL')`.
5. Увеличить `balances.current`.
6. Зафиксировать транзакцию.

Защита от двойного начисления строится на проверке статуса и уникальном ограничении ledger-записи по `order_id`.

### Списание баллов

`POST /api/user/balance/withdraw` получает:

```json
{
  "order": "2377225624",
  "sum": 751.5
}
```

Операция должна быть атомарной, и порядок шагов внутри транзакции не произволен. Вставка списания идёт первой: так повтор запроса распознаётся до любых изменений баланса и не требует `SAVEPOINT` для восстановления транзакции после нарушения уникальности.

```sql
BEGIN;

-- 1. Регистрируем списание. Повтор того же номера тем же пользователем
--    не создаёт строку и не является ошибкой.
INSERT INTO withdrawals (id, user_id, order_number, amount)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, order_number) DO NOTHING
RETURNING id;
-- строка не вернулась: списание уже было выполнено ранее.
-- COMMIT без изменений, HTTP 200.

-- 2. Проверка достаточности и списание одной атомарной операцией.
UPDATE balances
SET current         = current - $4,
    withdrawn_total = withdrawn_total + $4,
    updated_at      = now()
WHERE user_id = $2
  AND current >= $4
RETURNING current;
-- строка не вернулась: недостаточно баллов.
-- ROLLBACK, вставленное на шаге 1 списание тоже откатывается, HTTP 402.

-- 3. Неизменяемая запись в историю операций.
INSERT INTO balance_ledger (id, user_id, operation, amount, withdrawal_id)
VALUES ($5, $2, 'WITHDRAWAL', $4, $1);

COMMIT;
-- HTTP 200
```

`UPDATE ... WHERE current >= $4` совмещает проверку доступных баллов и их списание в одной атомарной операции: отдельный `SELECT ... FOR UPDATE` не нужен, а конкурентные списания не могут увести баланс в минус. `CHECK (current >= 0)` остаётся страховкой на случай ошибки в коде.

Повторный запрос с тем же номером заказа от того же пользователя возвращает `200` и не списывает баллы второй раз. Отдельного кода ответа для этого случая в ТЗ нет, а `409` и `500` в контракте эндпоинта отсутствуют, поэтому выбрана идемпотентность.

Списание не следует реализовывать как `SELECT` → проверку в Go → `UPDATE`: между чтением и записью баланс может измениться.

## HTTP API

### Аутентификация

Рекомендуемый формат:

```http
Authorization: Bearer <JWT>
```

Auth middleware:

1. Извлекает bearer token.
2. Проверяет подпись, срок действия и subject.
3. Помещает `userID` в `context.Context`.
4. При отсутствии или невалидности токена возвращает `401 Unauthorized`.

Не следует использовать заголовки вроде `X-User-ID` в качестве аутентификации.

### Форматы ответов

`GET /api/user/orders`:

```json
[
  {
    "number": "2377225624",
    "status": "PROCESSED",
    "accrual": 500,
    "uploaded_at": "2026-08-30T14:35:10Z"
  },
  {
    "number": "12345678903",
    "status": "PROCESSING",
    "uploaded_at": "2026-08-30T14:30:00Z"
  }
]
```

`accrual` нужно исключать из JSON, если значение отсутствует, а не возвращать `0`.

```go
type OrderResponse struct {
    Number     string           `json:"number"`
    Status     string           `json:"status"`
    Accrual    *decimal.Decimal `json:"accrual,omitempty"`
    UploadedAt time.Time        `json:"uploaded_at"`
}
```

`GET /api/user/balance`:

```json
{
  "current": 500.5,
  "withdrawn": 42
}
```

`GET /api/user/withdrawals`:

```json
[
  {
    "order": "2377225624",
    "sum": 751,
    "processed_at": "2026-08-30T14:45:10Z"
  }
]
```

### Доменные ошибки и HTTP-коды

```go
var (
    ErrLoginTaken         = errors.New("login already taken")
    ErrInvalidCredentials = errors.New("invalid credentials")
    ErrUnauthenticated    = errors.New("unauthenticated")
    ErrOrderAlreadyOwned  = errors.New("order already uploaded by user")
    ErrOrderOwnedByOther  = errors.New("order already uploaded by another user")
    ErrInvalidOrderNumber = errors.New("invalid order number")
    ErrInsufficientFunds  = errors.New("insufficient funds")
)
```

| Событие | HTTP-код |
|---|---:|
| Некорректный JSON, пустой логин или пароль | `400` |
| Логин уже занят | `409` |
| Неверный логин или пароль | `401` |
| Нет токена или токен невалиден | `401` |
| Тот же заказ загружен тем же пользователем | `200` |
| Новый заказ принят | `202` |
| Заказ принадлежит другому пользователю | `409` |
| Неверный номер по Луну | `422` |
| Недостаточно баллов | `402` |
| Повтор списания тем же пользователем на тот же номер | `200`, без повторного списания |
| Нет заказов или списаний | `204` |
| Непредвиденная инфраструктурная ошибка | `500` |

Не следует возвращать клиентам тексты ошибок PostgreSQL, stack trace, DSN, JWT secret или содержимое ошибок Accrual API.

## Конфигурация и эксплуатация

### Конфигурация

```go
type Config struct {
    RunAddress           string
    DatabaseURI          string
    AccrualSystemAddress string

    JWTSecret            string
    AccrualPollInterval  time.Duration
    AccrualTimeout       time.Duration
    WorkerBatchSize      int
}
```

Приоритет значений:

1. CLI-флаги `-a`, `-d`, `-r`.
2. Переменные окружения `RUN_ADDRESS`, `DATABASE_URI`, `ACCRUAL_SYSTEM_ADDRESS`.
3. Дефолт можно оставить только для адреса HTTP-сервера, например `:8080`. Для БД и Accrual-системы лучше завершать процесс с явной ошибкой конфигурации.

`JWT_SECRET` не входит в контракт конфигурации из ТЗ: проверяющий запускает сервис только с `-a`, `-d` и `-r`. Поэтому отсутствие секрета не должно останавливать запуск. Секрет берётся из переменной окружения, а при её отсутствии генерируется случайно на время работы процесса с предупреждением в лог. Плата за это — перезапуск инвалидирует ранее выданные токены, и несколько реплик не понимают токены друг друга; для нескольких реплик `JWT_SECRET` задаётся явно. Захардкоженный дефолтный секрет неприемлем: он попадает в репозиторий.

Пример запуска:

```bash
./gophermart \
  -a ":8080" \
  -d "postgres://gophermart:secret@postgres:5432/gophermart?sslmode=disable" \
  -r "http://localhost:8081"
```

### Middleware

```text
Recovery
  → Request ID
  → Structured logging
  → Gzip
  → Router
      → Auth middleware для защищённых маршрутов
          → Handler
```

Рекомендуется реализовать:

- recovery от panic;
- request ID;
- structured logs: request ID, user ID, method, path, status, latency, error;
- gzip для ответов при `Accept-Encoding: gzip`;
- `Vary: Accept-Encoding`;
- ограничение размера body;
- `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout`;
- таймауты вызовов внешнего Accrual API;
- метрики запросов, latency, ошибок Accrual, backlog заказов и количества `429`.

### Retry и backoff

Для `NEW`, `PROCESSING`, `204`, сетевых ошибок и `500`:

```text
1-я неудача: 5 секунд
2-я: 10 секунд
3-я: 20 секунд
...
максимум: 5 минут
```

Нужен jitter, например ±20 %, чтобы заказы не опрашивались синхронно. Для `429` приоритет имеет заголовок `Retry-After`; при его отсутствии используйте backoff с увеличенной минимальной задержкой.

`INVALID` и `PROCESSED` в очередь повторных проверок больше не попадают.

### Восстановление после сбоев

Если приложение завершилось после захвата задач, блокировки PostgreSQL исчезнут при закрытии транзакции. Заказы с истёкшим `next_check_at` снова попадут в обработку после рестарта.

Если вводится отдельный промежуточный статус наподобие `CHECKING`, нужен watchdog, возвращающий зависшие записи в очередь. В предлагаемой схеме отдельный статус захвата необязателен: выборка координируется блокировкой БД и временем следующей проверки.

### Миграции

Используйте версионированные SQL-миграции, например `golang-migrate`:

```text
000001_users_balances.up.sql
000001_users_balances.down.sql
000002_orders.up.sql
000002_orders.down.sql
000003_balance_ledger.up.sql
000003_balance_ledger.down.sql
000004_withdrawals.up.sql
000004_withdrawals.down.sql
```

Схема не создаётся одним `init`-файлом: каждая функциональная возможность приносит собственную миграцию. Порядок продиктован внешними ключами:

```text
        users
          │
   ┌──────┴──────┐
   ▼             ▼
balances      orders
   │             │
   └──────┬──────┘
          ▼
   balance_ledger
          │
          ▼  ALTER: + withdrawal_id
     withdrawals
```

Отсюда две неочевидные привязки. `balances` создаётся вместе с `users`, потому что регистрация обязана создавать пользователя и пустой баланс в одной транзакции. Колонка `withdrawal_id` в `balance_ledger` добавляется `ALTER`-миграцией вместе со списаниями, так как на момент создания ledger таблицы `withdrawals` ещё не существует.

Уже применённая миграция не изменяется: любое изменение схемы оформляется новой миграцией с большей версией.

Миграции применяются самим сервисом при старте, до открытия HTTP-порта, потому что проверяющий запускает только бинарник, без предварительного шага `migrate up`. Провал применения останавливает запуск: работать на схеме неизвестного состояния нельзя.

Не следует создавать таблицы неявно из Go-кода при каждом старте приложения.

## Тестирование

Требование покрытия не менее 60 % стоит рассматривать как минимум. Для бизнес-логики реалистична цель 80 % и выше.

### Unit-тесты

| Модуль | Что проверять |
|---|---|
| `validator/luhn` | Валидные, невалидные, пустые, нецифровые и длинные номера |
| `auth/password` | Корректный и неверный пароль, отсутствие plaintext в хеше |
| `service/auth` | Уникальный логин, неверные credentials, выпуск токена |
| `service/orders` | Новый заказ, повтор того же пользователя, чужой заказ, Лун |
| `service/balance` | Однократное начисление, отсутствие accrual, финальные статусы |
| `service/withdrawals` | Списание ровно доступного баланса, недостаток на 0.01, нулевые и отрицательные суммы |
| `accrual/client` | Все `200`-статусы, `204`, `429`, `500`, timeout |
| HTTP handlers | Коды, заголовки, JSON, отсутствие `accrual` при `NULL` |

Для unit-тестов сервисов репозитории и Accrual client описываются небольшими интерфейсами и заменяются моками либо фейками.

### Интеграционные тесты

Полезная необязательная часть:

- PostgreSQL через Testcontainers;
- применение реальных миграций;
- проверка `UNIQUE(number)` при конкурентных запросах;
- два параллельных списания, когда баланса хватает только на одно;
- идемпотентность начисления;
- несколько воркеров без двойных начислений;
- stub HTTP-сервера Accrual API с последовательностью `PROCESSING → PROCESSED`, а также `429` и `500`.

Ключевой инвариант конкурентного списания:
Сумма успешных списаний не должна превышать исходно доступный баланс.

## Чего избегать

- Не вызывать Accrual API синхронно из `POST /api/user/orders`.
- Не хранить баллы в `float64`.
- Не рассчитывать баланс только в Go без транзакций и блокировок БД.
- Не полагаться на предварительный `SELECT` для уникальности номера: необходимо `UNIQUE(number)` в PostgreSQL.
- Не начислять баллы без идемпотентной записи, связанной с `order_id`.
- Не использовать `SKIP LOCKED` для пользовательских выборок и отчётов: он предназначен для конкурентной обработки очереди.

## Итог

Архитектура остаётся достаточно компактной для учебного проекта, но сохраняет production-полезные свойства:

- асинхронную интеграцию с ненадёжным внешним сервисом;
- корректный баланс при конкурентных списаниях;
- идемпотентные начисления;
- аудит операций через ledger;
- возможность запуска нескольких реплик;
- прозрачную тестируемость через разделение слоёв и зависимостей.
