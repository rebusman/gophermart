-- Откат таблиц пользователей и счетов лояльности.
--
-- Порядок обратен порядку создания: balances ссылается на users внешним
-- ключом, поэтому удаляется первой.

DROP TABLE IF EXISTS balances;

DROP TABLE IF EXISTS users;
