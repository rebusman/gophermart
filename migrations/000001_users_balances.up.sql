-- Пользователи и их счета лояльности.
--
-- Обе таблицы создаются одной миграцией: регистрация обязана заводить
-- пользователя и его пустой счёт в одной транзакции, поэтому счёт не может
-- появиться позже пользователя.

CREATE TABLE users (
    id              UUID PRIMARY KEY,
    login           TEXT NOT NULL,
    password_hash   TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT users_login_unique UNIQUE (login),
    CONSTRAINT users_login_not_empty CHECK (length(trim(login)) > 0)
);

CREATE TABLE balances (
    user_id            UUID PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    current            NUMERIC(18, 2) NOT NULL DEFAULT 0,
    withdrawn_total    NUMERIC(18, 2) NOT NULL DEFAULT 0,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT balances_current_nonnegative CHECK (current >= 0),
    CONSTRAINT balances_withdrawn_nonnegative CHECK (withdrawn_total >= 0)
);
