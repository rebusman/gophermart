package migrations

import "embed"

// FS содержит SQL-файлы каталога миграций, встроенные в бинарник.
//go:embed *.sql
var FS embed.FS
